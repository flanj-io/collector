package promote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vinifera-io/collector/internal/model"
)

// shapeSentinelFinding is a stored finding whose observed values carry sentinel
// strings — if ANY of them reaches the wire, the shape-only law is broken.
func shapeSentinelFinding(id string) model.Finding {
	return model.Finding{
		SchemaVersion:        1,
		ID:                   id,
		Kind:                 model.KindLiveVsSpec,
		Severity:             model.SeverityBreaking,
		Integration:          "acme-payments",
		Endpoint:             "POST /v1/charges",
		FieldPath:            model.Ptr("amount"),
		Location:             model.Ptr("SENTINEL_LOCATION.$.response.body.amount"),
		Expected:             "SENTINEL_EXPECTED type=integer",
		Actual:               `SENTINEL_ACTUAL type=string ("1200")`,
		Rule:                 "type-mismatch",
		SourceCallID:         model.Ptr("SENTINEL_CALL_ID"),
		DetectedAt:           "2026-08-23T10:00:01Z",
		Detail:               "SENTINEL_DETAIL Response field `amount` is a string.",
		Signature:            "acme-payments|POST /v1/charges|live-vs-spec|type-mismatch|amount",
		OccurrenceCount:      12,
		FirstSeen:            "2026-08-23T08:00:01Z",
		LastSeen:             "2026-08-23T09:14:33Z",
		SnapshotObservedAt:   "2026-08-23T07:00:00Z",
		SnapshotObservedFrom: "2026-08-22T07:00:00Z",
	}
}

// shapeAllowedKeys is the CONTRACTS §5 findings-sync allow-list — the ONLY keys
// a marshalled shape row may carry.
var shapeAllowedKeys = map[string]bool{
	"finding_id": true, "signature": true, "kind": true, "severity": true,
	"integration": true, "endpoint": true, "field_path": true, "rule": true,
	"occurrence_count": true, "first_seen": true, "last_seen": true,
	"detected_at": true, "snapshot_observed_at": true, "snapshot_observed_from": true,
}

// TestBuildFindingShapesWireBytes is the wire-bytes law: the marshalled sync
// payload never contains expected / actual / detail / doc / location / any
// body content — asserted on the BYTES, not the struct, so an accidental
// embed or rename cannot sneak an observed value out.
func TestBuildFindingShapesWireBytes(t *testing.T) {
	shapes := BuildFindingShapes([]model.Finding{shapeSentinelFinding("f_1")})
	raw, err := json.Marshal(FindingsRequest{Findings: shapes})
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)

	// No observed value survives to the wire.
	for _, sentinel := range []string{"SENTINEL_EXPECTED", "SENTINEL_ACTUAL", "SENTINEL_DETAIL", "SENTINEL_LOCATION", "SENTINEL_CALL_ID", "1200"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("wire bytes carry the observed value %q: %s", sentinel, wire)
		}
	}
	// Nor the field NAMES that would carry one.
	for _, key := range []string{`"expected"`, `"actual"`, `"detail"`, `"doc"`, `"location"`, `"source_call_id"`, `"spec_version_from"`, `"spec_version_to"`, `"schema_version"`, `"body"`} {
		if strings.Contains(wire, key) {
			t.Errorf("wire bytes carry the disallowed key %s: %s", key, wire)
		}
	}

	// And the row is EXACTLY the allow-list.
	var decoded struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Findings) != 1 {
		t.Fatalf("rows = %d, want 1", len(decoded.Findings))
	}
	row := decoded.Findings[0]
	for k := range row {
		if !shapeAllowedKeys[k] {
			t.Errorf("row carries a key outside the allow-list: %q", k)
		}
	}
	want := map[string]any{
		"finding_id": "f_1", "signature": "acme-payments|POST /v1/charges|live-vs-spec|type-mismatch|amount",
		"kind": "live-vs-spec", "severity": "breaking", "integration": "acme-payments",
		"endpoint": "POST /v1/charges", "field_path": "amount", "rule": "type-mismatch",
		"occurrence_count": float64(12), "first_seen": "2026-08-23T08:00:01Z",
		"last_seen": "2026-08-23T09:14:33Z", "detected_at": "2026-08-23T10:00:01Z",
		"snapshot_observed_at": "2026-08-23T07:00:00Z", "snapshot_observed_from": "2026-08-22T07:00:00Z",
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("row[%q] = %v, want %v", k, row[k], v)
		}
	}
}

// TestBuildFindingShapesNormalization covers the defensive paths: older
// records with empty dedup fields, no field path, and the 200-item cap.
func TestBuildFindingShapesNormalization(t *testing.T) {
	old := model.Finding{
		ID: "f_old", Kind: model.KindLiveVsSpec, Severity: "warning", Integration: "acme-payments",
		Endpoint: "GET /v1/things", Rule: "undocumented-enum", DetectedAt: "2026-08-01T00:00:00Z",
		Expected: "x", Actual: "y",
	}
	shapes := BuildFindingShapes([]model.Finding{old})
	if len(shapes) != 1 {
		t.Fatalf("shapes = %d", len(shapes))
	}
	s := shapes[0]
	if s.Signature != old.ComputeSignature() {
		t.Errorf("missing signature must be recomputed, got %q", s.Signature)
	}
	if s.FirstSeen != "2026-08-01T00:00:00Z" || s.LastSeen != "2026-08-01T00:00:00Z" {
		t.Errorf("empty first/last seen must fall back to detected_at: %q / %q", s.FirstSeen, s.LastSeen)
	}
	if s.OccurrenceCount != 1 {
		t.Errorf("occurrence count floors at 1, got %d", s.OccurrenceCount)
	}
	if s.FieldPath != "" || s.SnapshotObservedAt != "" || s.SnapshotObservedFrom != "" {
		t.Errorf("optional fields must stay empty when unset: %+v", s)
	}

	// The CP caps a request at 200 items — the builder never emits more.
	many := make([]model.Finding, FindingsSyncMaxItems+7)
	for i := range many {
		many[i] = shapeSentinelFinding(fmt.Sprintf("f_%d", i))
	}
	if got := len(BuildFindingShapes(many)); got != FindingsSyncMaxItems {
		t.Errorf("builder emitted %d rows, cap is %d", got, FindingsSyncMaxItems)
	}
}

// TestBuildFindingShapesTruncatesToCaps is the liveness law: an oversized
// string field is truncated to the CP DTO cap on the wire (rune-safe), so one
// bad value cannot 400 the whole batch on every tick — the batch still posts.
func TestBuildFindingShapesTruncatesToCaps(t *testing.T) {
	oversized := shapeSentinelFinding("f_big")
	oversized.FieldPath = model.Ptr(strings.Repeat("a", 300) + "é") // 301 runes, 303 bytes
	oversized.Signature = strings.Repeat("s", 2000)

	shapes := BuildFindingShapes([]model.Finding{oversized, shapeSentinelFinding("f_ok")})
	if len(shapes) != 2 {
		t.Fatalf("shapes = %d, want 2", len(shapes))
	}
	if got := len(shapes[0].FieldPath); got != capFieldPath {
		t.Errorf("field_path = %d bytes, want the %d cap", got, capFieldPath)
	}
	if shapes[0].FieldPath != strings.Repeat("a", capFieldPath) {
		t.Errorf("field_path must be a clean prefix cut, got %q…", shapes[0].FieldPath[:16])
	}
	if got := len(shapes[0].Signature); got != capSignature {
		t.Errorf("signature = %d bytes, want the %d cap", got, capSignature)
	}
	// A multi-byte rune straddling the cut is dropped whole — never split.
	multi := shapeSentinelFinding("f_multi")
	multi.FieldPath = model.Ptr(strings.Repeat("a", capFieldPath-1) + "é") // é starts at byte 255, ends past the cap
	if got := BuildFindingShapes([]model.Finding{multi})[0].FieldPath; got != strings.Repeat("a", capFieldPath-1) {
		t.Errorf("rune-straddling cut produced %d bytes ending %q", len(got), got[len(got)-1:])
	}

	// The batch with the truncated row still posts and decodes as valid JSON.
	s := &stubCP{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&s.body); err != nil {
			t.Errorf("wire body is not valid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"received":2,"stored":2}`))
	}))
	t.Cleanup(s.srv.Close)
	out, status, err := NewClient(s.srv.URL, "deploy_secret", "v-test").WithCollectorKey("ckey_secret").
		PostFindings(context.Background(), FindingsRequest{Findings: shapes})
	if err != nil || status != 200 || out.Stored != 2 {
		t.Fatalf("truncated batch must still post: %v (%d) %+v", err, status, out)
	}
	rows, ok := s.body["findings"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("body findings = %v", s.body["findings"])
	}
	wireField, _ := rows[0].(map[string]any)["field_path"].(string)
	if len(wireField) != capFieldPath {
		t.Errorf("wire field_path = %d bytes, want the %d cap", len(wireField), capFieldPath)
	}
}

// TestPostFindings proves the client call: path, method, collector-key bearer,
// version headers, envelope decode, typed CPError — and the bearer never in an
// error string.
func TestPostFindings(t *testing.T) {
	var gotVersion, gotSchema string
	s := &stubCP{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.path = r.Method, r.URL.Path
		s.auth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("X-Vinifera-Collector-Version")
		gotSchema = r.Header.Get("X-Vinifera-Schema-Version")
		_ = json.NewDecoder(r.Body).Decode(&s.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"received":2,"stored":1}`))
	}))
	t.Cleanup(s.srv.Close)

	shapes := BuildFindingShapes([]model.Finding{shapeSentinelFinding("f_1"), shapeSentinelFinding("f_2")})
	out, status, err := NewClient(s.srv.URL, "deploy_secret", "v-test").WithCollectorKey("ckey_secret").
		PostFindings(context.Background(), FindingsRequest{Findings: shapes})
	if err != nil || status != 200 {
		t.Fatalf("PostFindings: %v (%d)", err, status)
	}
	if s.method != "POST" || s.path != "/api/v1/findings" {
		t.Errorf("%s %s", s.method, s.path)
	}
	if s.auth != "Bearer ckey_secret" {
		t.Errorf("findings sync must use the collector key, got %q", s.auth)
	}
	if gotVersion != "v-test" || gotSchema == "" {
		t.Errorf("version headers missing: collector=%q schema=%q", gotVersion, gotSchema)
	}
	if out.Received != 2 || out.Stored != 1 {
		t.Errorf("envelope = %+v", out)
	}
	if rows, ok := s.body["findings"].([]any); !ok || len(rows) != 2 {
		t.Errorf("body findings = %v", s.body["findings"])
	}

	// A CP refusal is typed; the bearer never reaches the error string.
	s400 := newStubCP(t, 400, `{"error":"too_many_items","message":"at most 200 findings per request"}`)
	_, _, err = NewClient(s400.srv.URL, "deploy_secret", "v").WithCollectorKey("ckey_secret").
		PostFindings(context.Background(), FindingsRequest{})
	ce := AsCPError(err)
	if ce == nil || ce.Status != 400 || ce.Code != "too_many_items" {
		t.Fatalf("400 not typed: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("bearer leaked into error: %v", err)
	}
}
