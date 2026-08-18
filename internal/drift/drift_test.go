package drift

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
)

func contractsDir() string { return filepath.Join("..", "..", "contracts") }

// loadGoldenCall parses contracts/golden-otlp-call.json into a RedactedCall via
// the same OTLP attribute mapping the collector uses at runtime.
func loadGoldenCall(t *testing.T) model.RedactedCall {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractsDir(), "golden-otlp-call.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	// Minimal OTLP/JSON shape we need: resourceLogs[].scopeLogs[].logRecords[].attributes[]
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					TimeUnixNano string `json:"timeUnixNano"`
					Attributes   []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
							IntValue    *string `json:"intValue"`
							BoolValue   *bool   `json:"boolValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	lr := plog.NewLogRecord()
	rec := payload.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	for _, a := range rec.Attributes {
		switch {
		case a.Value.StringValue != nil:
			lr.Attributes().PutStr(a.Key, *a.Value.StringValue)
		case a.Value.IntValue != nil:
			var n int64
			for _, c := range *a.Value.IntValue {
				n = n*10 + int64(c-'0')
			}
			lr.Attributes().PutInt(a.Key, n)
		case a.Value.BoolValue != nil:
			lr.Attributes().PutBool(a.Key, *a.Value.BoolValue)
		}
	}
	return otlpattr.CallFromRecord(lr)
}

// TestLiveVsSpec_GoldenCall is the flagship drift test: the golden call's
// response returns amount as a string where spec-v1 declares integer.
func TestLiveVsSpec_GoldenCall(t *testing.T) {
	doc, err := LoadSpecFile(filepath.Join(contractsDir(), "spec-v1.yaml"))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	call := loadGoldenCall(t)

	findings, err := DetectLiveVsSpec(doc, call)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	var amount *model.Finding
	for i := range findings {
		if findings[i].Location != nil && *findings[i].Location == "$.response.body.amount" {
			amount = &findings[i]
		}
	}
	if amount == nil {
		t.Fatalf("expected a finding on $.response.body.amount, got %d findings: %+v", len(findings), findings)
	}
	if amount.Kind != model.KindLiveVsSpec {
		t.Errorf("kind = %q, want live-vs-spec", amount.Kind)
	}
	if amount.Rule != "type-mismatch" {
		t.Errorf("rule = %q, want type-mismatch", amount.Rule)
	}
	if amount.Expected != "type=integer" {
		t.Errorf("expected = %q, want type=integer", amount.Expected)
	}
	if amount.Actual != `type=string ("1200")` {
		t.Errorf("actual = %q, want type=string (\"1200\")", amount.Actual)
	}
	if amount.Severity != model.SeverityBreaking {
		t.Errorf("severity = %q, want breaking", amount.Severity)
	}
	if amount.FieldPath == nil || *amount.FieldPath != "amount" {
		t.Errorf("field_path = %v, want amount", amount.FieldPath)
	}
	if amount.SourceCallID == nil || *amount.SourceCallID != call.ID {
		t.Errorf("source_call_id = %v, want %s", amount.SourceCallID, call.ID)
	}
	if amount.Endpoint != "POST /v1/charges" {
		t.Errorf("endpoint = %q, want POST /v1/charges", amount.Endpoint)
	}
}

// TestVersionDiff_BreakingChanges asserts the two documented breaking changes
// (amount integer->string, status enum `pending` removed).
func TestVersionDiff_BreakingChanges(t *testing.T) {
	findings, err := DetectVersionDiff(
		filepath.Join(contractsDir(), "spec-v1.yaml"),
		filepath.Join(contractsDir(), "spec-v2.yaml"),
		"acme-payments",
	)
	if err != nil {
		t.Fatalf("version diff: %v", err)
	}
	for _, f := range findings {
		if f.Kind != model.KindVersionDiff {
			t.Errorf("kind = %q, want version-diff", f.Kind)
		}
		if f.Severity != model.SeverityBreaking {
			t.Errorf("severity = %q, want breaking", f.Severity)
		}
		if f.SourceCallID != nil {
			t.Errorf("source_call_id must be null for version-diff, got %v", f.SourceCallID)
		}
		if f.SpecVersionFrom == nil || f.SpecVersionTo == nil {
			t.Errorf("spec_version_from/to must be set, got %v -> %v", f.SpecVersionFrom, f.SpecVersionTo)
		}
	}
	rules := map[string]bool{}
	for _, f := range findings {
		rules[f.Rule] = true
		t.Logf("version-diff finding: rule=%s endpoint=%q detail=%q", f.Rule, f.Endpoint, f.Detail)
	}
	if len(findings) != 2 {
		t.Fatalf("expected exactly 2 breaking findings, got %d: %+v", len(findings), rules)
	}
	if !rules["response-property-type-changed"] {
		t.Errorf("missing response-property-type-changed; got rules %v", rules)
	}
	if !rules["response-property-enum-value-removed"] {
		t.Errorf("missing response-property-enum-value-removed; got rules %v", rules)
	}
}
