package flanjui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/confmap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/promote"
)

// Locked accessors — the ticker goroutine and the test body race on the stub's
// counters otherwise.
func (s *stubCP) findingsCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findingsCalls
}

func (s *stubCP) findingsBody(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findingsBodies[i]
}

// syncSentinelFinding carries sentinel observed values: if any of them reaches
// the wire, the shape-only law is broken.
func syncSentinelFinding(id, field string) model.Finding {
	return model.Finding{
		SchemaVersion: 1, ID: id, Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: "acme-payments", Endpoint: "POST /v1/charges", FieldPath: model.Ptr(field),
		Rule: "type-mismatch", Expected: "SENTINEL_EXPECTED integer", Actual: "SENTINEL_ACTUAL string",
		Detail: "SENTINEL_DETAIL prose", DetectedAt: "2026-08-23T10:00:01Z",
		Signature:       "acme-payments|POST /v1/charges|live-vs-spec|type-mismatch|" + field,
		OccurrenceCount: 3, FirstSeen: "2026-08-23T08:00:00Z", LastSeen: "2026-08-23T09:00:00Z",
	}
}

// connectKeyOnly puts ONLY the collector key into the store — the sync needs a
// key, and deliberately NOT a confirmed contact (this is the collector's own
// telemetry, same posture as §5.5a reads).
func connectKeyOnly(t *testing.T, r *testRig) {
	t.Helper()
	if err := r.st.PutSetting(settingCollectorKey, r.cp.collectorKey); err != nil {
		t.Fatal(err)
	}
}

// TestFindingSyncPostsShapeOnly: one tick posts the allow-listed shape with the
// collector key — and the wire BYTES carry no expected / actual / detail / doc,
// no key in any log line, no finding content in any log line.
func TestFindingSyncPostsShapeOnly(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	_ = r.st.InsertFinding(syncSentinelFinding("fnd_sync_1", "amount"))

	r.ext.syncFindingsOnce(context.Background())

	if r.cp.findingsCallCount() != 1 {
		t.Fatalf("findings posts = %d, want 1", r.cp.findingsCallCount())
	}
	raw := r.cp.findingsBody(0)
	wire := string(raw)
	for _, sentinel := range []string{"SENTINEL_EXPECTED", "SENTINEL_ACTUAL", "SENTINEL_DETAIL"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("observed value %q reached the wire: %s", sentinel, wire)
		}
	}
	for _, key := range []string{`"expected"`, `"actual"`, `"detail"`, `"doc"`, `"location"`, `"source_call_id"`} {
		if strings.Contains(wire, key) {
			t.Errorf("disallowed key %s reached the wire: %s", key, wire)
		}
	}
	var body struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	// The rig's start() seeds fnd_1 too — both findings ride one batch.
	if len(body.Findings) != 2 {
		t.Fatalf("rows = %d, want 2 (fnd_1 + fnd_sync_1)", len(body.Findings))
	}
	seen := map[string]bool{}
	for _, row := range body.Findings {
		seen[row["finding_id"].(string)] = true
		if row["signature"] == "" || row["kind"] == "" || row["severity"] == "" || row["endpoint"] == "" || row["rule"] == "" {
			t.Errorf("row missing required shape fields: %v", row)
		}
	}
	if !seen["fnd_1"] || !seen["fnd_sync_1"] {
		t.Errorf("rows = %v, want fnd_1 + fnd_sync_1", seen)
	}
	if r.cp.lastAuth != "Bearer "+r.cp.collectorKey {
		t.Errorf("sync must post with the collector key, got %q", r.cp.lastAuth)
	}
	r.assertNeverLogged(t, r.cp.collectorKey, "SENTINEL_EXPECTED", "SENTINEL_ACTUAL", "SENTINEL_DETAIL", "fnd_sync_1")
}

// TestFindingSyncSkipsStaleClient: stale_client is a local client-freshness
// notice, not drift (D10) — it never reaches the wire body; every other kind
// still syncs (the rig's live-vs-spec fnd_1 rides the same batch).
func TestFindingSyncSkipsStaleClient(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	stale := syncSentinelFinding("fnd_stale_1", "args")
	stale.Kind = model.KindStaleClient
	stale.Rule = "tool-not-listed"
	stale.Signature = "acme-payments|charges.create|stale_client|tool-not-listed|args"
	_ = r.st.InsertFinding(stale)

	r.ext.syncFindingsOnce(context.Background())

	if r.cp.findingsCallCount() != 1 {
		t.Fatalf("findings posts = %d, want 1", r.cp.findingsCallCount())
	}
	raw := r.cp.findingsBody(0)
	wire := string(raw)
	for _, sentinel := range []string{"fnd_stale_1", "stale_client"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("stale_client reached the wire via %q: %s", sentinel, wire)
		}
	}
	var body struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Findings) != 1 {
		t.Fatalf("rows = %d, want 1 (fnd_1 only)", len(body.Findings))
	}
	row := body.Findings[0]
	if row["finding_id"] != "fnd_1" || row["kind"] != model.KindLiveVsSpec {
		t.Errorf("live-vs-spec finding must still sync, got %v", row)
	}
}

// TestFindingSyncRepeatPosts: a second tick posts the SAME signatures again —
// the CP upserts by (collector, signature), so repeats are how counters and
// last_seen stay fresh, never an error.
func TestFindingSyncRepeatPosts(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)

	r.ext.syncFindingsOnce(context.Background())
	r.ext.syncFindingsOnce(context.Background())
	if r.cp.findingsCallCount() != 2 {
		t.Fatalf("posts = %d, want 2", r.cp.findingsCallCount())
	}
	sig := func(raw []byte) []string {
		var b struct {
			Findings []map[string]any `json:"findings"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(b.Findings))
		for _, row := range b.Findings {
			out = append(out, row["signature"].(string))
		}
		return out
	}
	a, b := sig(r.cp.findingsBody(0)), sig(r.cp.findingsBody(1))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("batches differ in size: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("repeat post changed a signature: %q vs %q", a[i], b[i])
		}
	}
}

// TestFindingSyncConfigOff: with BOTH legs off the loop never starts (since
// 2026-08-31 `finding_sync: false` alone only silences the findings POST — see
// TestSyncSwitchesGateLegsIndependently); a missing CP client never starts it
// either.
func TestFindingSyncConfigOff(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	r.ext.cfg.FindingSync, r.ext.cfg.DirectorySync, r.ext.cfg.EdgeSync = false, false, false

	r.ext.startFindingSync()
	if r.ext.syncCancel != nil || r.ext.syncDone != nil {
		t.Fatal("every switch off must not start the ticker")
	}
	time.Sleep(30 * time.Millisecond)
	if r.cp.findingsCallCount() != 0 {
		t.Errorf("config-off posted %d times", r.cp.findingsCallCount())
	}

	// No CP configured: on but nowhere to post — the loop never starts.
	r2 := newRig(t)
	r2.ext.cfg.FindingSync = true
	r2.ext.startFindingSync()
	if r2.ext.syncCancel != nil {
		t.Error("no CP client — the ticker must not start")
	}
}

// TestFindingSyncSkipsWithoutKeyOrFindings: no collector key → nothing posts;
// key but zero findings → nothing posts. Both silent.
func TestFindingSyncSkipsWithoutKeyOrFindings(t *testing.T) {
	// findings exist, no key
	r := newRig(t)
	r.start(t)
	r.ext.syncFindingsOnce(context.Background())
	if r.cp.findingsCallCount() != 0 {
		t.Errorf("no-key tick posted %d times", r.cp.findingsCallCount())
	}

	// key exists, zero findings
	r2 := newRig(t)
	r2.ext.cp = promote.NewClient(r2.cp.srv.URL, r2.cp.deployToken, "v-test")
	connectKeyOnly(t, r2)
	r2.ext.syncFindingsOnce(context.Background())
	if r2.cp.findingsCallCount() != 0 {
		t.Errorf("zero-findings tick posted %d times", r2.cp.findingsCallCount())
	}
}

// TestFindingSyncTickerLifecycle: Start fires the first tick immediately;
// Shutdown's stop joins the goroutine and no tick fires after it.
func TestFindingSyncTickerLifecycle(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	r.ext.cfg.FindingSync = true

	r.ext.startFindingSync()
	if r.ext.syncDone == nil {
		t.Fatal("ticker did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.cp.findingsCallCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first tick never fired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	done := r.ext.syncDone
	r.ext.stopFindingSync()
	select {
	case <-done:
	default:
		t.Fatal("stopFindingSync returned before the goroutine exited")
	}
	after := r.cp.findingsCallCount()
	time.Sleep(30 * time.Millisecond)
	if r.cp.findingsCallCount() != after {
		t.Error("a tick fired after stopFindingSync")
	}
	// Stopping twice is safe (Shutdown may run after a failed Start).
	r.ext.stopFindingSync()
	r.assertNeverLogged(t, r.cp.collectorKey)
}

// TestFindingSyncDefaultOn pins the factory default: an omitted finding_sync
// key means ON (CONTRACTS §8).
func TestFindingSyncDefaultOn(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	if !cfg.FindingSync {
		t.Fatal("finding_sync must default to true")
	}
}

// --- the two switches (owner ruling 2026-08-31) -----------------------------
//
// `finding_sync` and `directory_sync` gate the two legs of the ONE ticker
// independently: an egress (findings POST) and a pure fetch (directory GET)
// with two different privacy stories must not share a switch.

// syncLegCounts is what actually reached the stub control plane on one run of
// the REAL ticker — the wire, not internal state.
type syncLegCounts struct {
	findings  int
	directory int
	edges     int
	started   bool
}

// runSyncLoop starts the REAL ticker with the three switches as given, lets it
// settle, stops it, and reports what reached the stub CP. `started` says
// whether a ticker was created at all.
func runSyncLoop(t *testing.T, findingSync, displayNameSync, edgeSync bool) syncLegCounts {
	t.Helper()
	r := newRig(t)
	r.start(t) // seeds one finding, so the findings leg has something to post
	connectKeyOnly(t, r)
	seedGraph(r) // ...and one external edge, so the registration leg does too
	r.cp.mu.Lock()
	r.cp.directoryEntries, r.cp.directoryETag = `{"zzguava.dev":{"name":"Guava Billing","tier":"curated"}}`, `"v1"`
	r.cp.mu.Unlock()
	r.ext.cfg.FindingSync, r.ext.cfg.DirectorySync, r.ext.cfg.EdgeSync = findingSync, displayNameSync, edgeSync

	r.ext.startFindingSync()
	started := r.ext.syncDone != nil
	if started {
		// Wait for every ENABLED leg to reach the wire (bounded, no sleeps as
		// synchronisation).
		deadline := time.Now().Add(2 * time.Second)
		for (findingSync && r.cp.findingsCallCount() == 0) ||
			(displayNameSync && r.cp.directoryCallCount() == 0) ||
			(edgeSync && r.cp.edgesCallCount() == 0) {
			if time.Now().After(deadline) {
				t.Fatalf("enabled leg never reached the wire (finding_sync=%v directory_sync=%v edge_sync=%v; findings=%d directory=%d edges=%d)",
					findingSync, displayNameSync, edgeSync,
					r.cp.findingsCallCount(), r.cp.directoryCallCount(), r.cp.edgesCallCount())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	// Settle: give a DISABLED leg every chance to betray itself before we read
	// the counters (the first pass runs every leg back to back).
	time.Sleep(50 * time.Millisecond)
	if started {
		r.ext.stopFindingSync()
	}
	r.assertNeverLogged(t, r.cp.collectorKey)
	return syncLegCounts{r.cp.findingsCallCount(), r.cp.directoryCallCount(), r.cp.edgesCallCount(), started}
}

// TestSyncSwitchesGateLegsIndependently pins every combination at the wire:
// each switch turns its own leg on/off and NONE touches another. The
// `edge_sync: false` row is the acceptance criterion in miniature — findings
// still sync while registration sends nothing.
func TestSyncSwitchesGateLegsIndependently(t *testing.T) {
	cases := []struct {
		name                                   string
		findingSync, displayName, edgeSync     bool
		wantFindings, wantDirectory, wantEdges bool
		wantTicker                             bool
	}{
		{"all on — every leg fires", true, true, true, true, true, true, true},
		{"finding_sync off — the directory pull and registration still run, no findings POST", false, true, true, false, true, true, true},
		{"directory_sync off — findings and registration still run, no directory GET", true, false, true, true, false, true, true},
		{"edge_sync off — findings STILL sync, no edges POST at all", true, true, false, true, true, false, true},
		{"only edge_sync on — registration alone reaches the wire", false, false, true, false, false, true, true},
		{"all off — no ticker, no wire traffic at all", false, false, false, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runSyncLoop(t, tc.findingSync, tc.displayName, tc.edgeSync)
			if got.started != tc.wantTicker {
				t.Errorf("ticker started = %v, want %v", got.started, tc.wantTicker)
			}
			if any := got.findings > 0; any != tc.wantFindings {
				t.Errorf("POST /api/v1/findings reached the wire %d time(s); want any=%v", got.findings, tc.wantFindings)
			}
			if any := got.directory > 0; any != tc.wantDirectory {
				t.Errorf("GET /api/v1/directory reached the wire %d time(s); want any=%v", got.directory, tc.wantDirectory)
			}
			if any := got.edges > 0; any != tc.wantEdges {
				t.Errorf("POST /api/v1/edges/sync reached the wire %d time(s); want any=%v", got.edges, tc.wantEdges)
			}
		})
	}
}

// TestDirectorySyncDefaultOn pins the factory default: an omitted
// directory_sync key means ON (CONTRACTS §8), so splitting the switch off
// finding_sync changed nobody's behaviour.
func TestDirectorySyncDefaultOn(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	if !cfg.DirectorySync {
		t.Fatal("directory_sync must default to true")
	}
	// And it survives a config file that never mentions the key: unmarshalling
	// a flanjui block WITHOUT directory_sync must leave the default alone.
	cfg2 := createDefaultConfig().(*Config)
	if err := confmap.NewFromStringMap(map[string]any{
		"ui_endpoint":    "127.0.0.1:5335",
		"integration_id": "acme-payments",
		"finding_sync":   false,
	}).Unmarshal(cfg2); err != nil {
		t.Fatal(err)
	}
	if cfg2.FindingSync {
		t.Error("finding_sync: false must be read from the config")
	}
	if !cfg2.DirectorySync {
		t.Fatal("an absent directory_sync key must stay true — finding_sync: false must not disable name refreshes")
	}
}
