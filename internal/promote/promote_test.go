package promote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/flanj-io/collector/internal/model"
)

func contractsDir() string { return filepath.Join("..", "..", "contracts") }

func loadJSON[T any](t *testing.T, name string) T {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractsDir(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return v
}

// flagSchema compiles cp-flag-request.schema.json with its two $ref'd schemas
// registered offline so nothing hits the network.
func flagSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	for _, name := range []string{"redacted-call.schema.json", "finding.schema.json", "cp-flag-request.schema.json"} {
		f, err := os.Open(filepath.Join(contractsDir(), name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		id := doc.(map[string]any)["$id"].(string)
		if err := c.AddResource(id, doc); err != nil {
			t.Fatalf("add resource %s: %v", name, err)
		}
	}
	sch, err := c.Compile("https://flanj.io/contracts/v1/cp-flag-request.schema.json")
	if err != nil {
		t.Fatalf("compile flag schema: %v", err)
	}
	return sch
}

func validate(t *testing.T, sch *jsonschema.Schema, raw []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("flag body does not conform to cp-flag-request.schema.json:\n%v", err)
	}
}

// TestFlagBody_ConformsToSchema is the acceptance test: the flag body the
// collector POSTs to the CP validates against the frozen schema, and the request
// carries the Bearer token + version headers, against a stub CP server.
func TestFlagBody_ConformsToSchema(t *testing.T) {
	call := loadJSON[model.RedactedCall](t, "sample-redacted-call.json")
	finding := loadJSON[model.Finding](t, "sample-finding.json")

	req := Build(Input{
		ConsumerDisplayName: "Acme Consumer Ltd",
		Message:             "", // exercise the derived default
		Call:                &call,
		Finding:             finding,
	})

	// Validate the marshaled body up-front against the frozen schema.
	sch := flagSchema(t)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validate(t, sch, body)

	// Stub CP: capture the request, assert auth + headers + schema, reply 201.
	var gotAuth, gotColVer, gotSchemaVer string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/flags" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotColVer = r.Header.Get("X-Flanj-Collector-Version")
		gotSchemaVer = r.Header.Get("X-Flanj-Schema-Version")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"thread_id":"0191-t","thread_public_id":"pub","thread_url":"https://cp.test/t/pub#k=abc","peek_url":"https://cp.test/t/pub#k=abc","magic_token":"abc","state":"open","status":"created"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "deploy_tok_123", "v0.0.0-test").WithCollectorKey("ckey_abc")
	resp, code, err := client.Post(context.Background(), req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if code != http.StatusCreated {
		t.Errorf("status = %d, want 201", code)
	}
	if resp.Status != "created" || resp.ThreadID == "" || resp.ThreadURL == "" || resp.ThreadPublicID != "pub" || resp.State != "open" {
		t.Errorf("unexpected flag response: %+v", resp)
	}
	if gotAuth != "Bearer ckey_abc" {
		t.Errorf("Authorization = %q, want Bearer <collector key> (never the deploy token once Connected)", gotAuth)
	}
	if gotColVer != "v0.0.0-test" {
		t.Errorf("collector version header = %q", gotColVer)
	}
	if gotSchemaVer != "1" {
		t.Errorf("schema version header = %q, want 1", gotSchemaVer)
	}
	// The body the CP actually received must also conform.
	validate(t, sch, gotBody)
}

// TestFlagBody_MCPOutputMismatchConforms (v0.5 Step C): a flag body built from
// an MCP tools/call (transport/mcp_* fields, client-generated correlation id in
// its own slot, no status code) and an output_mismatch finding conforms to the
// frozen contract — the flaggable MCP kinds are part of the promoted surface.
func TestFlagBody_MCPOutputMismatchConforms(t *testing.T) {
	callID := "01920000-0000-7000-8000-000000000001"
	call := model.RedactedCall{
		SchemaVersion: 1, ID: callID, CapturedAt: "2026-08-24T10:00:00.000Z",
		Integration: "acme-payments", Direction: "client", PeerHost: "mcp.acme.test", EdgeClass: "external",
		Method: "tools/call", URL: "mcp://mcp.acme.test/create_refund", Route: "/create_refund",
		RequestBody: `{"amount":1200,"card_number":"⟦REDACTED:PAN⟧","currency":"usd"}`, RequestContentType: "application/json",
		ResponseBody: `{"refund":{"id":"re_71","amount":"1200","status":"succeeded"}}`, ResponseContentType: "application/json",
		Correlation: model.Correlation{ClientRequestID: "4"},
		Redaction:   model.Redaction{Applied: true, Patterns: []string{"PAN"}},
		Transport:   "mcp", MCPToolName: "create_refund",
		MCPServerName: "acme-payments-mcp", MCPServerVersion: "3.2.0", MCPProtocolVersion: "2025-06-18",
	}
	finding := model.Finding{
		SchemaVersion: 1, ID: "01920000-0000-7000-8000-000000000002",
		Kind: model.KindOutputMismatch, Severity: model.SeverityBreaking,
		Integration: "acme-payments", Endpoint: "create_refund",
		FieldPath: model.Ptr("refund.amount"), Location: model.Ptr("$.response.structuredContent.refund.amount"),
		Expected: "type=integer", Actual: `type=string ("1200")`, Rule: "type-mismatch",
		SourceCallID: &callID, DetectedAt: "2026-08-24T10:00:01.000Z",
		Signature: "acme-payments|create_refund|output_mismatch|type-mismatch|refund.amount", OccurrenceCount: 12,
	}
	req := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Call: &call, Finding: finding})
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validate(t, flagSchema(t), body)

	// definition_change (the other flaggable v0.5 kind) still conforms while a
	// representative call happens to be attached. The CALL-LESS shape it
	// normally takes is covered by TestFlagBody_CallLessDefinitionChange.
	finding.Kind = model.KindDefinitionChange
	finding.SourceCallID = nil
	finding.SpecVersionFrom, finding.SpecVersionTo = model.Ptr("sha256:aaaaaaaaaaaa"), model.Ptr("sha256:bbbbbbbbbbbb")
	req = Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Call: &call, Finding: finding})
	if body, err = json.Marshal(req); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validate(t, flagSchema(t), body)
}

// TestFlagBody_CallLessDefinitionChange (qfix2-2026-08-26, ux-design-v2 §2.7.5):
// a definition_change is CALL-LESS by nature — its evidence is the provider's
// own two published tools/list snapshots, carried by the Finding. The body must
// OMIT `call` entirely rather than carry an empty or invented call record, and
// it must still name the provider from the finding's own integration.
//
// The vendored schema makes `call` conditional on kind (required for every kind
// EXCEPT definition_change), so this body is validated against it below — that
// assertion is what keeps a re-vendor from silently regressing the relaxation.
func TestFlagBody_CallLessDefinitionChange(t *testing.T) {
	finding := loadJSON[model.Finding](t, "sample-finding.json")
	finding.Kind = model.KindDefinitionChange
	finding.Rule = model.RuleDescriptionChanged
	finding.SourceCallID = nil
	finding.Integration = "acme-tools"
	finding.SpecVersionFrom, finding.SpecVersionTo = model.Ptr("sha256:aaaaaaaaaaaa"), model.Ptr("sha256:bbbbbbbbbbbb")

	req := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Finding: finding})
	if req.Call != nil {
		t.Fatalf("call-less flag carries a call: %+v", req.Call)
	}
	if req.ProviderDisplayName != "Acme Tools" {
		t.Errorf("provider display name = %q, want Acme Tools (humanized from the finding)", req.ProviderDisplayName)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["call"]; has {
		t.Errorf("call-less flag body still has a `call` key: %s", body)
	}
	if _, has := m["finding"]; !has {
		t.Errorf("call-less flag body must still carry the finding: %s", body)
	}
	// The guard: a re-vendor that restores `call` to the unconditional `required`
	// list fails here, not in production.
	validate(t, flagSchema(t), body)
}

// TestDefaultMessage_DescriptionAsksRatherThanAccuses (qfix2-2026-08-26,
// ux-design-v2 §2.7.4): the flag sheet labels its textarea "Message
// (optional)", so a user who clears the prefilled question sends an EMPTY
// message — and the default written here is what the provider actually reads.
// For a DESCRIPTION change that default must stay the question. "Contract
// drift on <tool>" would file a wording change as a defect claim, which is the
// mute risk the sheet's guard line exists to prevent.
func TestDefaultMessage_DescriptionAsksRatherThanAccuses(t *testing.T) {
	finding := loadJSON[model.Finding](t, "sample-finding.json")
	finding.Kind = model.KindDefinitionChange
	finding.Rule = model.RuleDescriptionChanged
	finding.Endpoint = "create_refund"
	finding.SourceCallID = nil
	finding.SnapshotObservedAt = "2026-08-19T14:02:00.000Z"
	finding.Detail = "Definition change (DESCRIPTION): description-changed on `create_refund` at description — tools/list observed T1 → T2."

	req := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Message: "", Finding: finding})
	want := "Your tools/list description for create_refund changed on Aug 19. The schema didn't change, but the wording did, and our agent picks tools from that text. Can you confirm the new wording is intended and stable?"
	if req.Message != want {
		t.Errorf("default message =\n  %q\nwant\n  %q", req.Message, want)
	}
	if strings.Contains(req.Message, "Contract drift") {
		t.Errorf("a DESCRIPTION flag must never file a defect claim: %q", req.Message)
	}

	// A whitespace-only message is the same case (Build trims).
	if got := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Message: "   ", Finding: finding}).Message; got != want {
		t.Errorf("whitespace-only message = %q, want the prefill", got)
	}
	// A user's own message is never replaced.
	if got := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Message: "Was this intended?", Finding: finding}).Message; got != "Was this intended?" {
		t.Errorf("typed message = %q, want it untouched", got)
	}
	// No observed-at (older collector / absent field): the clause is dropped,
	// never rendered as a broken date.
	noDate := finding
	noDate.SnapshotObservedAt = ""
	if got := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Finding: noDate}).Message; !strings.HasPrefix(got, "Your tools/list description for create_refund changed. The schema") {
		t.Errorf("message without an observed-at = %q", got)
	}
	// Every other class still states the drift plainly — this branch is
	// DESCRIPTION-only.
	breaking := finding
	breaking.Rule = "type-narrowed"
	if got := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Finding: breaking}).Message; !strings.HasPrefix(got, "Contract drift on create_refund.") {
		t.Errorf("non-description default message = %q", got)
	}
}

// TestHumanizeIntegration proves the shared humanize rule: split on -/_/space,
// Title Case each word.
func TestHumanizeIntegration(t *testing.T) {
	cases := map[string]string{
		"acme-payments":     "Acme Payments",
		"acme_payments":     "Acme Payments",
		"acme payments":     "Acme Payments",
		"stripe":            "Stripe",
		"ACME-PAYMENTS":     "Acme Payments",
		"nilos-fx_gateway":  "Nilos Fx Gateway",
		"":                  "",
		"  acme--payments ": "Acme Payments",
	}
	for in, want := range cases {
		if got := HumanizeIntegration(in); got != want {
			t.Errorf("HumanizeIntegration(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuild_ProviderDisplayName proves the flag body carries provider_display_name:
// the explicit value when given, else the humanized integration id.
func TestBuild_ProviderDisplayName(t *testing.T) {
	call := loadJSON[model.RedactedCall](t, "sample-redacted-call.json") // integration=acme-payments
	finding := loadJSON[model.Finding](t, "sample-finding.json")

	explicit := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", ProviderDisplayName: "Acme Payments Inc", Call: &call, Finding: finding})
	if explicit.ProviderDisplayName != "Acme Payments Inc" {
		t.Errorf("explicit provider name = %q, want Acme Payments Inc", explicit.ProviderDisplayName)
	}

	defaulted := Build(Input{ConsumerDisplayName: "Acme Consumer Ltd", Call: &call, Finding: finding})
	if defaulted.ProviderDisplayName != "Acme Payments" {
		t.Errorf("defaulted provider name = %q, want Acme Payments (humanized integration)", defaulted.ProviderDisplayName)
	}

	// The defaulted body must still conform to the frozen schema.
	sch := flagSchema(t)
	body, err := json.Marshal(defaulted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validate(t, sch, body)
}

// TestBuild_IdempotencyKeyFromFinding proves re-flagging the same finding yields
// the same idempotency key (CP returns the existing thread).
func TestBuild_IdempotencyKeyFromFinding(t *testing.T) {
	finding := loadJSON[model.Finding](t, "sample-finding.json")
	a := Build(Input{Finding: finding})
	b := Build(Input{Finding: finding})
	if a.IdempotencyKey != b.IdempotencyKey {
		t.Errorf("idempotency key not stable: %q vs %q", a.IdempotencyKey, b.IdempotencyKey)
	}
	if a.IdempotencyKey != "flag_"+finding.ID {
		t.Errorf("idempotency key = %q, want flag_%s", a.IdempotencyKey, finding.ID)
	}
}

// TestBuild_RedactsMessage proves the free-text message is DLP-scanned before it
// can ride out on a flag (§5).
func TestBuild_RedactsMessage(t *testing.T) {
	finding := loadJSON[model.Finding](t, "sample-finding.json")
	req := Build(Input{
		Message: "please check card 4111 1111 1111 1111 on the failing charge",
		Finding: finding,
	})
	if want := "⟦REDACTED:PAN⟧"; !bytes.Contains([]byte(req.Message), []byte(want)) {
		t.Errorf("message not redacted: %q", req.Message)
	}
	if bytes.Contains([]byte(req.Message), []byte("4111")) {
		t.Errorf("raw PAN leaked into flag message: %q", req.Message)
	}
}
