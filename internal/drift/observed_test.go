package drift

import (
	"fmt"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

func rejected(id, tool, args string) model.RedactedCall {
	c := mcpCall(id, tool, args, "")
	c.MCPIsError, c.MCPErrorCode, c.ResponseContentType = true, CodeInvalidParams, ""
	return c
}

// TestInputRejectionOnPreviouslyAcceptedArguments is R-B's observed_failure
// row: -32602 on arguments of a shape that previously SUCCEEDED fires
// input_rejection at BREAKING — provider-side, so flaggable.
func TestInputRejectionOnPreviouslyAcceptedArguments(t *testing.T) {
	d := NewMCPDetector()
	d.JudgeCall(mcpCall("c1", "get_balance", `{"account_id":"a1"}`, `{"amount":1}`))
	fs, _ := d.JudgeCall(rejected("c2", "get_balance", `{"account_id":"a2"}`))
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly one input_rejection", fs)
	}
	f := fs[0]
	if f.Kind != model.KindInputRejection || f.ChangeKind != "observed_failure" || f.Severity != model.SeverityBreaking ||
		f.Endpoint != "get_balance" || f.Rule != RuleArgumentsRejected || f.SourceCallID == nil || *f.SourceCallID != "c2" {
		t.Errorf("finding = %+v", f)
	}
	if !f.Flaggable() {
		t.Error("a provider-side rejection must be flaggable")
	}
}

// TestInputRejectionNeedsAPriorSuccess: a -32602 on a shape the tool never
// accepted is the caller's own problem — never a finding.
func TestInputRejectionNeedsAPriorSuccess(t *testing.T) {
	d := NewMCPDetector()
	d.JudgeCall(mcpCall("c1", "get_balance", `{"account_id":"a1"}`, `{"amount":1}`))
	if fs, _ := d.JudgeCall(rejected("c2", "get_balance", `{"accountId":"a1"}`)); len(fs) != 0 {
		t.Errorf("a never-accepted shape produced %+v", fs)
	}
	if fs, _ := d.JudgeCall(rejected("c3", "other_tool", `{"account_id":"a1"}`)); len(fs) != 0 {
		t.Errorf("a shape accepted by ANOTHER tool produced %+v", fs)
	}
	// A result with isError (a tool-level error, not a rejected request) is
	// not a rejection.
	c := mcpCall("c4", "get_balance", `{"account_id":"a1"}`, `{"error":"no funds"}`)
	c.MCPIsError = true
	if fs, _ := d.JudgeCall(c); len(fs) != 0 {
		t.Errorf("isError without -32602 produced %+v", fs)
	}
}

// TestInputRejectionThroughTheDispatcher: the rejection of a re-attributed
// dispatcher call lands on the inner tool, with via_dispatch.
func TestInputRejectionThroughTheDispatcher(t *testing.T) {
	d := NewMCPDetector()
	metaSurface(t, d)
	d.JudgeCall(searchCall(t, "s1", searchDef("get_balance", "account_id", "number")))
	d.JudgeCall(dispatchCall("c1", "get_balance", `{"account_id":"a1"}`, `{"amount":1}`))
	rej := dispatchCall("c2", "get_balance", `{"account_id":"a2"}`, "")
	rej.MCPIsError, rej.MCPErrorCode, rej.ResponseContentType = true, CodeInvalidParams, ""
	fs, _ := d.JudgeCall(rej)
	var got *model.Finding
	for i := range fs {
		if fs[i].Kind == model.KindInputRejection {
			got = &fs[i]
		}
	}
	if got == nil || got.Endpoint != "get_balance" || got.ViaDispatch != "call_tool" {
		t.Fatalf("findings = %+v, want an input_rejection on get_balance via call_tool", fs)
	}
}

func respond(d *MCPDetector, from int, bodies ...string) []model.Finding {
	var out []model.Finding
	for i, b := range bodies {
		fs, _ := d.JudgeCall(mcpCall(fmt.Sprintf("v%d", from+i), "list_orders", `{}`, b))
		for _, f := range fs {
			if f.Kind == model.KindValueChange {
				out = append(out, f)
			}
		}
	}
	return out
}

func repeat(n int, body string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = body
	}
	return out
}

// TestValueFormatChange is R-B's value row: a field whose format held and then
// changed and held again is ONE value_change at WARNING.
func TestValueFormatChange(t *testing.T) {
	for _, tc := range []struct{ name, before, after, from, to, path string }{
		{"timestamp ISO -> epoch", `{"created":"2026-09-01T10:00:00Z"}`, `{"created":1767261600}`, "timestamp:iso-8601", "timestamp:epoch-seconds", "created"},
		{"ID uuid -> prefixed", `{"id":"3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e"}`, `{"id":"ord_8Kx2mQ91"}`, "id:uuid", "id:prefixed", "id"},
		{"enum casing", `{"orders":[{"status":"PENDING"}]}`, `{"orders":[{"status":"pending"}]}`, "enum:UPPER_CASE", "enum:lower_case", "orders.items.status"},
		{"units: integer cents -> decimal", `{"amount":1200}`, `{"amount":12.5}`, "number:integer", "number:decimal", "amount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewMCPDetector()
			if fs := respond(d, 0, repeat(valueStable, tc.before)...); len(fs) != 0 {
				t.Fatalf("a steady format fired: %+v", fs)
			}
			// Fewer than valueConfirm responses in the new format: not yet.
			if fs := respond(d, 10, repeat(valueConfirm-1, tc.after)...); len(fs) != 0 {
				t.Fatalf("fired before the change held: %+v", fs)
			}
			fs := respond(d, 20, tc.after)
			if len(fs) != 1 {
				t.Fatalf("findings = %+v, want exactly one", fs)
			}
			f := fs[0]
			if f.ChangeKind != "value" || f.Severity != model.SeverityWarning || f.Expected != tc.from || f.Actual != tc.to ||
				f.FieldPath == nil || *f.FieldPath != tc.path || f.Endpoint != "list_orders" {
				t.Errorf("finding = %+v", f)
			}
			// Holding the new format does not fire again.
			if fs := respond(d, 30, repeat(5, tc.after)...); len(fs) != 0 {
				t.Errorf("the new format fired again: %+v", fs)
			}
		})
	}
}

// TestValueFormatNeverFiresOnUnsettledOrFreeText: a field that never holds one
// format, free text, and a change across families never fire.
func TestValueFormatNeverFiresOnUnsettledOrFreeText(t *testing.T) {
	d := NewMCPDetector()
	var alternating []string
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			alternating = append(alternating, `{"ref":"3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e","note":"Order shipped today."}`)
		} else {
			alternating = append(alternating, `{"ref":"ord_8Kx2mQ91","note":"Customer asked for a refund."}`)
		}
	}
	if fs := respond(d, 0, alternating...); len(fs) != 0 {
		t.Errorf("an unsettled field fired: %+v", fs)
	}
	// A format held for fewer than valueStable responses was never the
	// field's format, so moving off it is not a change.
	d = NewMCPDetector()
	respond(d, 0, repeat(valueStable-1, `{"created":"2026-09-01T10:00:00Z"}`)...)
	if fs := respond(d, 10, repeat(valueConfirm+2, `{"created":1767261600}`)...); len(fs) != 0 {
		t.Errorf("a format that never settled was reported as changed: %+v", fs)
	}
	d = NewMCPDetector()
	respond(d, 0, repeat(valueStable, `{"ref":"3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e"}`)...)
	if fs := respond(d, 10, repeat(5, `{"ref":"2026-09-01T10:00:00Z"}`)...); len(fs) != 0 {
		t.Errorf("an id -> timestamp change (across families) fired: %+v", fs)
	}
}
