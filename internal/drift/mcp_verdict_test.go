package drift

import (
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The per-call VERDICT the processor stamps on an MCP call (JudgeCall). Every
// gate DetectCall applies has a name here, in DetectCall's own order, and the
// two answers must agree with each other: a call that produced an
// output_mismatch is `drifted`, a call the output check ran clean over is
// `clean`, and a call the check never reached says which gate stopped it —
// never "clean", which is the false green this stamp exists to retire.

func loadedGolden(t *testing.T) *MCPDetector {
	t.Helper()
	d := NewMCPDetector()
	if _, _, _, err := d.LoadSnapshot(goldenSnapshot(t)); err != nil {
		t.Fatalf("load golden snapshot: %v", err)
	}
	return d
}

func wantNotValidated(t *testing.T, label string, v model.Validation, reason string) {
	t.Helper()
	if v.Verdict != model.ValidatedNot || v.Reason != reason {
		t.Errorf("%s: verdict = %+v, want not-validated / %s", label, v, reason)
	}
}

func TestJudgeCall_NoSnapshotIsNotValidated(t *testing.T) {
	d := NewMCPDetector()
	fs, v := d.JudgeCall(goldenMCPCall(t))
	if len(fs) != 0 {
		t.Fatalf("no-snapshot findings = %+v, want none", fs)
	}
	wantNotValidated(t, "no snapshot", v, model.NotValidatedNoContract)
}

func TestJudgeCall_DriftedAndClean(t *testing.T) {
	d := loadedGolden(t)

	// The golden call: refund.amount comes back as a string. One
	// output_mismatch, and the verdict says so.
	fs, v := d.JudgeCall(goldenMCPCall(t))
	if len(fs) != 1 || fs[0].Kind != model.KindOutputMismatch {
		t.Fatalf("golden findings = %+v, want exactly one output_mismatch", fs)
	}
	if v.Verdict != model.ValidatedDrifted || v.Reason != "" {
		t.Errorf("golden verdict = %+v, want drifted", v)
	}

	// The same tool answering on-contract: no finding, and CLEAN — the check
	// ran and found nothing, which is a different fact from "nothing checked".
	clean := mcpCall("c_clean", "create_refund", `{"amount":1200,"currency":"usd"}`,
		`{"refund":{"id":"re_71","amount":1200,"status":"succeeded"}}`)
	fs, v = d.JudgeCall(clean)
	if len(fs) != 0 {
		t.Fatalf("clean call findings = %+v, want none", fs)
	}
	if v.Verdict != model.ValidatedClean || v.Reason != "" {
		t.Errorf("clean verdict = %+v, want clean", v)
	}
}

// TestJudgeCall_StaleArgumentsLeaveTheVerdictClean: stale_client is about the
// CONSUMER's arguments. The output check still runs and its verdict stands on
// its own — the store's `drifted` gate excludes stale_client for the same
// reason, and the two must agree.
func TestJudgeCall_StaleArgumentsLeaveTheVerdictClean(t *testing.T) {
	d := loadedGolden(t)
	// get_balance requires account_id; send none. The result conforms.
	call := mcpCall("c_stale", "get_balance", `{}`, `{"amount":1200,"currency":"usd"}`)
	fs, v := d.JudgeCall(call)
	if len(fs) != 1 || fs[0].Kind != model.KindStaleClient {
		t.Fatalf("findings = %+v, want exactly one stale_client", fs)
	}
	if v.Verdict != model.ValidatedClean {
		t.Errorf("verdict = %+v, want clean — a stale argument is not the provider's drift", v)
	}
}

// TestJudgeCall_GatesNameTheirReason walks every branch that stops the output
// check, in DetectCall's order, and asserts the reason each one stamps.
func TestJudgeCall_GatesNameTheirReason(t *testing.T) {
	d := loadedGolden(t)
	okArgs := `{"account_id":"a1"}`
	okResult := `{"amount":1200,"currency":"usd"}`

	t.Run("tool not listed", func(t *testing.T) {
		fs, v := d.JudgeCall(mcpCall("c1", "nope", okArgs, okResult))
		if len(fs) != 1 || fs[0].Kind != model.KindStaleClient || fs[0].Rule != RuleToolNotListed {
			t.Fatalf("findings = %+v, want the tool-not-listed stale_client", fs)
		}
		wantNotValidated(t, "unlisted tool", v, model.NotValidatedToolNotListed)
	})
	t.Run("input_required is mid-flight", func(t *testing.T) {
		c := mcpCall("c2", "get_balance", `{}`, `{}`)
		c.MCPResultType = model.MCPResultTypeInputRequired
		fs, v := d.JudgeCall(c)
		if len(fs) != 0 {
			t.Fatalf("input_required produced findings: %+v", fs)
		}
		wantNotValidated(t, "input_required", v, model.NotValidatedInputRequired)
	})
	t.Run("no outputSchema", func(t *testing.T) {
		_, v := d.JudgeCall(mcpCall("c3", "list_transactions", okArgs, `{"transactions":"whatever"}`))
		wantNotValidated(t, "list_transactions", v, model.NotValidatedNoOutputContract)
	})
	t.Run("isError", func(t *testing.T) {
		c := mcpCall("c4", "get_balance", okArgs, `{"error":"boom"}`)
		c.MCPIsError = true
		fs, v := d.JudgeCall(c)
		if len(fs) != 0 {
			t.Fatalf("isError produced findings: %+v", fs)
		}
		wantNotValidated(t, "isError", v, model.NotValidatedErrorResult)
	})
	t.Run("task handle", func(t *testing.T) {
		c := mcpCall("c5", "get_balance", okArgs, "")
		c.MCPTaskID = "task_9"
		_, v := d.JudgeCall(c)
		wantNotValidated(t, "task handle", v, model.NotValidatedTaskHandle)
	})
	t.Run("empty body", func(t *testing.T) {
		_, v := d.JudgeCall(mcpCall("c6", "get_balance", okArgs, ""))
		wantNotValidated(t, "empty body", v, model.NotValidatedResultNotJSON)
	})
	t.Run("truncated body", func(t *testing.T) {
		c := mcpCall("c7", "get_balance", okArgs, `{"amount":12`)
		c.ResponseBodyTruncated = true
		_, v := d.JudgeCall(c)
		wantNotValidated(t, "truncated", v, model.NotValidatedResultNotJSON)
	})
	t.Run("text/plain content fallback", func(t *testing.T) {
		c := mcpCall("c8", "get_balance", okArgs, "balance is 1200")
		c.ResponseContentType = "text/plain"
		_, v := d.JudgeCall(c)
		wantNotValidated(t, "text/plain", v, model.NotValidatedResultNotJSON)
	})
	t.Run("application/json that does not parse", func(t *testing.T) {
		// Nothing was compared to the schema, so nothing may read as clean.
		_, v := d.JudgeCall(mcpCall("c9", "get_balance", okArgs, `{"amount":`))
		wantNotValidated(t, "unparseable JSON", v, model.NotValidatedResultNotJSON)
	})
	t.Run("the gates are ordered: isError before task handle before body", func(t *testing.T) {
		c := mcpCall("c10", "get_balance", okArgs, "")
		c.MCPIsError = true
		c.MCPTaskID = "task_9"
		_, v := d.JudgeCall(c)
		wantNotValidated(t, "isError+task+empty", v, model.NotValidatedErrorResult)
	})
}

// TestDetectCallStillReturnsTheFindings: DetectCall is JudgeCall minus the
// verdict — the existing battery keeps its oracle.
func TestDetectCallStillReturnsTheFindings(t *testing.T) {
	d := loadedGolden(t)
	if fs := d.DetectCall(goldenMCPCall(t)); len(fs) != 1 || fs[0].Kind != model.KindOutputMismatch {
		t.Fatalf("DetectCall = %+v, want the golden output_mismatch", fs)
	}
}
