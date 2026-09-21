package flanjredaction

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/otlpattr"
)

// TestRedactSnapshotRecord: the defense-in-depth pass also covers the stored
// MCP contract snapshot (v0.5) — a PAN that slipped past the SDK's floor
// (e.g. in a tool description) is tokenised before the snapshot reaches the
// drift loader / the store, and an already-redacted snapshot is untouched
// (idempotent, add-only).
func TestRedactSnapshotRecord(t *testing.T) {
	p := newRedactionProcessor(&Config{})

	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	leaked := `{"tools":[{"name":"pay","description":"test card 4111 1111 1111 1111","inputSchema":{"type":"object"}}]}`
	lr.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, leaked)
	lr.Attributes().PutStr(otlpattr.AttrRedactPatterns, `[]`)

	p.redactSnapshot(lr)

	v, _ := lr.Attributes().Get(otlpattr.AttrMCPContractSnapshot)
	if strings.Contains(v.Str(), "4111") || !strings.Contains(v.Str(), "⟦REDACTED:PAN⟧") {
		t.Fatalf("snapshot not redacted: %s", v.Str())
	}
	if a, _ := lr.Attributes().Get(otlpattr.AttrRedactApplied); !a.Bool() {
		t.Errorf("redaction.applied not set")
	}
	if pats, _ := lr.Attributes().Get(otlpattr.AttrRedactPatterns); !strings.Contains(pats.Str(), "PAN") {
		t.Errorf("patterns = %s, want PAN merged in", pats.Str())
	}

	// Idempotent: a second pass changes nothing.
	before := v.Str()
	p.redactSnapshot(lr)
	after, _ := lr.Attributes().Get(otlpattr.AttrMCPContractSnapshot)
	if after.Str() != before {
		t.Errorf("second pass mutated the snapshot (double-wrap?)")
	}

	// A clean snapshot passes through byte-identical, nothing fires.
	lr2 := plog.NewLogRecord()
	lr2.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
	clean := `{"tools":[{"name":"get_balance","inputSchema":{"type":"object"}}]}`
	lr2.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, clean)
	p.redactSnapshot(lr2)
	if v2, _ := lr2.Attributes().Get(otlpattr.AttrMCPContractSnapshot); v2.Str() != clean {
		t.Errorf("clean snapshot mutated: %s", v2.Str())
	}
	if _, ok := lr2.Attributes().Get(otlpattr.AttrRedactApplied); ok {
		t.Errorf("clean snapshot must not set redaction.applied")
	}
}

// TestRedactSnapshotServerCommand: the stdio launch line riding a
// contract_snapshot gets the same defense-in-depth as the snapshot beside it —
// it is STORED with the observed contract and rendered on its card. The floor
// runs per ELEMENT (the SDK's own contract), so the value stays a JSON array;
// what the floor leaves alone is byte-identical (raw non-ASCII, no HTML
// escaping), and a second pass changes nothing.
func TestRedactSnapshotServerCommand(t *testing.T) {
	p := newRedactionProcessor(&Config{})
	record := func(command string) plog.LogRecord {
		lr := plog.NewLogRecord()
		lr.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeContractSnapshot)
		lr.Attributes().PutStr(otlpattr.AttrMCPContractSnapshot, `{"tools":[{"name":"list_charges","inputSchema":{"type":"object"}}]}`)
		lr.Attributes().PutStr(otlpattr.AttrMCPServerCommand, command)
		lr.Attributes().PutStr(otlpattr.AttrRedactPatterns, `[]`)
		return lr
	}
	command := func(lr plog.LogRecord) string {
		v, _ := lr.Attributes().Get(otlpattr.AttrMCPServerCommand)
		return v.Str()
	}

	// A card number slipped past the SDK into an argument.
	lr := record(`["npx","-y","@acme/mcp@1.0.0","--note=<café & co>","--card=4111 1111 1111 1111"]`)
	p.redactSnapshot(lr)
	got := command(lr)
	if strings.Contains(got, "4111") || !strings.Contains(got, "⟦REDACTED:PAN⟧") {
		t.Fatalf("server command not redacted: %s", got)
	}
	parts, ok := otlpattr.DecodeServerCommand(got)
	if !ok || len(parts) != 5 {
		t.Fatalf("the redacted command is no longer a 5-element JSON array of strings: %s", got)
	}
	if parts[0] != "npx" || parts[2] != "@acme/mcp@1.0.0" {
		t.Errorf("clean elements moved: %q", parts)
	}
	if !strings.Contains(got, `"--note=<café & co>"`) {
		t.Errorf("an untouched element was re-escaped (want raw non-ASCII, no \\u003c): %s", got)
	}
	if a, _ := lr.Attributes().Get(otlpattr.AttrRedactApplied); !a.Bool() {
		t.Errorf("redaction.applied not set")
	}
	if pats, _ := lr.Attributes().Get(otlpattr.AttrRedactPatterns); !strings.Contains(pats.Str(), "PAN") {
		t.Errorf("patterns = %s, want PAN merged in", pats.Str())
	}
	p.redactSnapshot(lr)
	if again := command(lr); again != got {
		t.Errorf("second pass mutated the command (double-wrap?):\n  %s\n  %s", got, again)
	}

	// A clean command passes through byte-identical, and nothing fires.
	const clean = `["uvx","acme-mcp","--profile","café","…"]`
	lr2 := record(clean)
	p.redactSnapshot(lr2)
	if got := command(lr2); got != clean {
		t.Errorf("clean command mutated: %s", got)
	}
	if _, ok := lr2.Attributes().Get(otlpattr.AttrRedactApplied); ok {
		t.Errorf("a clean command must not set redaction.applied")
	}

	// Not a command at all: the snapshot decoder drops it, but it is floored
	// as text first — nothing un-floored rides the record whatever reads it.
	lr3 := record(`npx acme-mcp --card=4111 1111 1111 1111`)
	p.redactSnapshot(lr3)
	if got := command(lr3); strings.Contains(got, "4111") {
		t.Errorf("a malformed command carried a card number past the floor: %s", got)
	}
}

// callRecord is a call record carrying the given route and target, the shape
// any OTLP sender can deliver.
func callRecord(route, target string) plog.LogRecord {
	lr := plog.NewLogRecord()
	lr.Attributes().PutStr(otlpattr.AttrRecordType, otlpattr.RecordTypeCall)
	lr.Attributes().PutStr(otlpattr.AttrRoute, route)
	lr.Attributes().PutStr(otlpattr.AttrTarget, target)
	return lr
}

func strAttr(lr plog.LogRecord, key string) string {
	v, _ := lr.Attributes().Get(key)
	return v.Str()
}

// TestRedactRouteAttribute: `flanj.http.route` is stored and shown beside the
// target, so the floor must cover it for any sender — not only the SDKs, which
// redact it at source. A raw email or card number carried as a path segment is
// tokenised, the fired patterns reach flanj.redaction.patterns (canonical
// order) and flanj.redaction.applied flips, and route gets no field records.
func TestRedactRouteAttribute(t *testing.T) {
	p := newRedactionProcessor(&Config{})
	lr := callRecord("/v1/users/jane.doe@example.com/cards/4111111111111111", "/v1/users/x")
	lr.Attributes().PutStr(otlpattr.AttrRedactPatterns, `["PAN"]`)

	p.redactRecord(lr)

	route := strAttr(lr, otlpattr.AttrRoute)
	if strings.Contains(route, "jane.doe") || strings.Contains(route, "4111") ||
		!strings.Contains(route, "⟦REDACTED:EMAIL⟧") || !strings.Contains(route, "⟦REDACTED:PAN⟧") {
		t.Fatalf("route not redacted: %s", route)
	}
	if a, _ := lr.Attributes().Get(otlpattr.AttrRedactApplied); !a.Bool() {
		t.Errorf("redaction.applied not set")
	}
	if got := strAttr(lr, otlpattr.AttrRedactPatterns); got != `["EMAIL","PAN"]` {
		t.Errorf("patterns = %s, want [\"EMAIL\",\"PAN\"] (canonical order, SDK's set kept)", got)
	}
	if _, ok := lr.Attributes().Get(otlpattr.AttrRedactFields); ok {
		t.Errorf("route must carry no field records")
	}

	// Idempotent: a second pass neither changes the route nor double-wraps.
	p.redactRecord(lr)
	if again := strAttr(lr, otlpattr.AttrRoute); again != route {
		t.Errorf("second pass mutated the route:\n  %s\n  %s", route, again)
	}
}

// TestRedactRouteCleanAndMCPUnchanged: routes with nothing to redact — a
// templated HTTP route and an MCP route (`/<tool.name>`) — pass through
// byte-identical and set no bookkeeping.
func TestRedactRouteCleanAndMCPUnchanged(t *testing.T) {
	p := newRedactionProcessor(&Config{})
	for _, route := range []string{"/v1/users/{id}/profile", "/create_refund", "/"} {
		lr := callRecord(route, route)
		p.redactRecord(lr)
		if got := strAttr(lr, otlpattr.AttrRoute); got != route {
			t.Errorf("route %q mutated to %q", route, got)
		}
		if _, ok := lr.Attributes().Get(otlpattr.AttrRedactApplied); ok {
			t.Errorf("route %q: a clean route must not set redaction.applied", route)
		}
	}
}

// TestRouteScanMatchesTargetScan pins how the floor treats a `cvv=123` path
// segment. That verdict depends on the query string beside it: with a query
// the segment is tokenised, without one it is left alone — in the target as
// much as in the route. So scanning `route` alone is exactly as strong as
// scanning `target` for the same string, but NOT as strong as the target of
// the same call when the sender's raw target carries a query and its raw route
// is the bare path: the target comes back tokenised and the route does not.
// `route` is deliberately not derived from the target (a sender may send a
// templated route that is not a prefix of it); the residual gap is the floor's
// own context-dependence and is reported, not fixed, here.
func TestRouteScanMatchesTargetScan(t *testing.T) {
	p := newRedactionProcessor(&Config{})

	// Same string, judged the same in either attribute.
	for _, path := range []string{"/v1/pay/cvv=123", "/v1/pay/cvv=123?x=1"} {
		lr := callRecord(path, path)
		p.redactRecord(lr)
		if r, tg := strAttr(lr, otlpattr.AttrRoute), strAttr(lr, otlpattr.AttrTarget); r != tg {
			t.Errorf("route and target of %q are judged differently: route %q, target %q", path, r, tg)
		}
	}

	// With a query the segment is tokenised — in the route too.
	lr := callRecord("/v1/pay/cvv=123?x=1", "/v1/pay/cvv=123?x=1")
	p.redactRecord(lr)
	if got := strAttr(lr, otlpattr.AttrRoute); got != "/v1/pay/cvv=⟦REDACTED:CVV⟧?x=1" {
		t.Errorf("route with a query not tokenised: %s", got)
	}

	// The residual: a bare route beside a target that has the query.
	lr = callRecord("/v1/pay/cvv=123", "/v1/pay/cvv=123?x=1")
	p.redactRecord(lr)
	t.Logf("bare route %q beside target %q (the floor's context-dependence, reported in the PR)",
		strAttr(lr, otlpattr.AttrRoute), strAttr(lr, otlpattr.AttrTarget))
}
