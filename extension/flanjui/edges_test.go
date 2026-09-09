package flanjui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/confmap"

	"github.com/flanj-io/collector/internal/model"
)

// Locked accessors — the ticker goroutine and the test body race otherwise.
func (s *stubCP) edgesCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edgesCalls
}

func (s *stubCP) edgesBody(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edgesBodies[i]
}

// seedGraph puts a realistic mixed edge set in the store: two external hosts
// under one provider domain, one external inbound consumer, and TWO internal
// peers whose names are sentinels — a single-label service name and an RFC1918
// literal, the two shapes Classify calls internal.
func seedGraph(r *testRig) {
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	r.st.edges = append(r.st.edges,
		model.Edge{PeerHost: "api.acme.test", Direction: "client", Role: "consumer", Class: "external",
			FirstSeen: "2026-09-02T00:00:00Z", LastSeen: "2026-09-08T10:00:00Z", CallCount: 4242},
		model.Edge{PeerHost: "api-eu.acme.test:8443", Direction: "client", Role: "consumer", Class: "external",
			FirstSeen: "2026-09-01T00:00:00Z", LastSeen: "2026-09-07T10:00:00Z", CallCount: 17},
		model.Edge{PeerHost: "consumer-a.test", Direction: "server", Role: "provider", Class: "external",
			FirstSeen: "2026-09-03T00:00:00Z", LastSeen: "2026-09-08T11:00:00Z"},
		model.Edge{PeerHost: "SENTINELLEDGER", Direction: "client", Role: "consumer", Class: "internal",
			FirstSeen: "2026-09-01T00:00:00Z", LastSeen: "2026-09-08T12:00:00Z"},
		model.Edge{PeerHost: "10.42.0.7", Direction: "server", Role: "provider", Class: "internal",
			FirstSeen: "2026-09-01T00:00:00Z", LastSeen: "2026-09-08T12:00:00Z"},
	)
}

func decodeEdges(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var body struct {
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Edges
}

// TestEdgeSyncRegistersExternalDomainsOnly is the slice's central law at the
// relay layer: one tick registers the EXTERNAL edges by registrable domain,
// both directions — and the wire BYTES carry no internal peer, no peer host, no
// volume aggregate, and nothing reaches a log line.
func TestEdgeSyncRegistersExternalDomainsOnly(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedGraph(r)

	r.ext.syncEdgesOnce(context.Background())

	if r.cp.edgesCallCount() != 1 {
		t.Fatalf("edge registrations = %d, want 1", r.cp.edgesCallCount())
	}
	raw := r.cp.edgesBody(0)
	wire := string(raw)

	// The internal peers never left, in any spelling.
	for _, sentinel := range []string{"SENTINELLEDGER", "10.42.0.7"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("internal peer %q reached the wire: %s", sentinel, wire)
		}
	}
	// Nor did the external edges' HOSTS, ports or call counts — only domains.
	for _, sentinel := range []string{"api.acme.test", "api-eu", "8443", "4242"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("host/volume detail %q reached the wire: %s", sentinel, wire)
		}
	}
	for _, key := range []string{`"peer_host"`, `"class"`, `"role"`, `"call_count"`, `"drift_count"`} {
		if strings.Contains(wire, key) {
			t.Errorf("disallowed key %s reached the wire: %s", key, wire)
		}
	}

	rows := decodeEdges(t, raw)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (acme.test outbound + consumer-a.test inbound): %s", len(rows), wire)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row["registrable_domain"].(string)] = row["direction"].(string)
		for _, k := range []string{"registrable_domain", "direction", "first_seen", "last_seen"} {
			if v, ok := row[k]; !ok || v == "" {
				t.Errorf("row missing %q: %v", k, row)
			}
		}
	}
	if got["acme.test"] != "outbound" {
		t.Errorf("acme.test direction = %q, want outbound", got["acme.test"])
	}
	if got["consumer-a.test"] != "inbound" {
		t.Errorf("consumer-a.test direction = %q, want inbound", got["consumer-a.test"])
	}
	if r.cp.lastAuth != "Bearer "+r.cp.collectorKey {
		t.Errorf("registration must post with the collector key, got %q", r.cp.lastAuth)
	}
	r.assertNeverLogged(t, r.cp.collectorKey, "SENTINELLEDGER", "10.42.0.7", "acme.test")
}

// TestEdgeSyncIsIdempotentOnRepeat: a second tick sends the SAME rows — the CP
// upserts on (collector, domain, direction), so repeats are how `last_seen`
// stays fresh, never a duplicate.
func TestEdgeSyncIsIdempotentOnRepeat(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedGraph(r)

	r.ext.syncEdgesOnce(context.Background())
	r.ext.syncEdgesOnce(context.Background())
	if r.cp.edgesCallCount() != 2 {
		t.Fatalf("registrations = %d, want 2", r.cp.edgesCallCount())
	}
	if a, b := string(r.cp.edgesBody(0)), string(r.cp.edgesBody(1)); a != b {
		t.Errorf("a repeat registration changed the payload:\n%s\n%s", a, b)
	}
}

// TestEdgeSyncSkipsWithoutKeyOrEdges: an UN-CONNECTED collector registers
// nothing (no collector key → no POST, silently), and a Connected one that has
// discovered nothing external stays silent too — including when its only edges
// are internal, which is the case that must never produce an empty-batch POST
// naming the deployment.
func TestEdgeSyncSkipsWithoutKeyOrEdges(t *testing.T) {
	// Edges exist, no key: nothing leaves a collector that never Connected.
	r := newRig(t)
	r.start(t)
	seedGraph(r)
	r.ext.syncEdgesOnce(context.Background())
	if r.cp.edgesCallCount() != 0 {
		t.Errorf("un-Connected tick registered %d time(s)", r.cp.edgesCallCount())
	}

	// Key exists, zero edges.
	r2 := newRig(t)
	r2.start(t)
	connectKeyOnly(t, r2)
	r2.ext.syncEdgesOnce(context.Background())
	if r2.cp.edgesCallCount() != 0 {
		t.Errorf("zero-edge tick registered %d time(s)", r2.cp.edgesCallCount())
	}

	// Key exists, INTERNAL edges only: still nothing on the wire at all.
	r3 := newRig(t)
	r3.start(t)
	connectKeyOnly(t, r3)
	r3.st.mu.Lock()
	r3.st.edges = append(r3.st.edges, model.Edge{
		PeerHost: "SENTINELLEDGER", Direction: "client", Role: "consumer", Class: "internal",
		FirstSeen: "2026-09-01T00:00:00Z", LastSeen: "2026-09-08T12:00:00Z"})
	r3.st.mu.Unlock()
	r3.ext.syncEdgesOnce(context.Background())
	if r3.cp.edgesCallCount() != 0 {
		t.Errorf("internal-only tick registered %d time(s) — an internal edge must produce no request at all", r3.cp.edgesCallCount())
	}
}

// TestEdgeSyncDefaultOn pins the factory default: an omitted `edge_sync` key
// means ON (CONTRACTS §8), and setting one of the sibling switches never
// touches it.
func TestEdgeSyncDefaultOn(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	if !cfg.EdgeSync {
		t.Fatal("edge_sync must default to true")
	}
	cfg2 := createDefaultConfig().(*Config)
	if err := confmap.NewFromStringMap(map[string]any{
		"ui_endpoint":    "127.0.0.1:5335",
		"integration_id": "acme-payments",
		"finding_sync":   false,
	}).Unmarshal(cfg2); err != nil {
		t.Fatal(err)
	}
	if !cfg2.EdgeSync {
		t.Fatal("an absent edge_sync key must stay true — finding_sync: false must not disable registration")
	}

	// And the key is really read when it IS present.
	cfg3 := createDefaultConfig().(*Config)
	if err := confmap.NewFromStringMap(map[string]any{
		"ui_endpoint": "127.0.0.1:5335",
		"edge_sync":   false,
	}).Unmarshal(cfg3); err != nil {
		t.Fatal(err)
	}
	if cfg3.EdgeSync {
		t.Error("edge_sync: false must be read from the config")
	}
	if !cfg3.FindingSync || !cfg3.DirectorySync {
		t.Error("edge_sync: false must not disable the findings sync or the directory pull")
	}
}

// TestConnectCarriesEdgeSyncOnBothVerbs: the disclosure the Connect panel renders
// is only honest if it reflects THIS deployment's switch, so `/api/connect` must
// report it — and on BOTH verbs. The SPA replaces its whole connect state from
// the POST response, so a GET-only field would blank the disclosure at the exact
// moment the operator pressed Connect, until the next background poll put it back.
func TestConnectCarriesEdgeSyncOnBothVerbs(t *testing.T) {
	for _, on := range []bool{true, false} {
		r := newRig(t)
		r.start(t)
		r.ext.cfg.EdgeSync = on

		_, get, _ := r.do(t, "GET", "/api/connect", nil)
		if got, ok := get["edge_sync"].(bool); !ok || got != on {
			t.Errorf("GET /api/connect edge_sync = %v (present=%v), want %v", get["edge_sync"], ok, on)
		}

		_, post, _ := r.do(t, "POST", "/api/connect", map[string]any{
			"consumer_display_name": "CustomerX",
			"contact_email":         "ops@customerx.example",
		})
		if got, ok := post["edge_sync"].(bool); !ok || got != on {
			t.Errorf("POST /api/connect edge_sync = %v (present=%v), want %v — the panel loses the disclosure on Connect", post["edge_sync"], ok, on)
		}
	}
}
