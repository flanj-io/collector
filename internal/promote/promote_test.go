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
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/vinifera-io/collector/internal/model"
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
	sch, err := c.Compile("https://vinifera.io/contracts/v1/cp-flag-request.schema.json")
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
		InviteeEmail:        "api-support@provider.test",
		Message:             "", // exercise the derived default
		Call:                call,
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
		gotColVer = r.Header.Get("X-Vinifera-Collector-Version")
		gotSchemaVer = r.Header.Get("X-Vinifera-Schema-Version")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"thread_id":"0191-t","peek_url":"https://cp.test/peek/abc","magic_token":"abc","status":"created"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "deploy_tok_123", "v0.0.0-test")
	resp, code, err := client.Post(context.Background(), req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if code != http.StatusCreated {
		t.Errorf("status = %d, want 201", code)
	}
	if resp.Status != "created" || resp.ThreadID == "" || resp.PeekURL == "" {
		t.Errorf("unexpected flag response: %+v", resp)
	}
	if gotAuth != "Bearer deploy_tok_123" {
		t.Errorf("Authorization = %q, want Bearer deploy_tok_123", gotAuth)
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
