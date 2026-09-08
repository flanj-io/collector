package promote

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/edge"
	"github.com/flanj-io/collector/internal/model"
)

// edgeAllowedKeys is the CONTRACTS §5 edge-registration allow-list — the ONLY
// keys a marshalled registration row may carry.
var edgeAllowedKeys = map[string]bool{
	"registrable_domain": true, "direction": true, "first_seen": true, "last_seen": true,
}

func extEdge(host, direction, first, last string) model.Edge {
	return model.Edge{
		PeerHost:   host,
		Direction:  direction,
		Role:       edge.Role(direction),
		Class:      edge.ClassExternal,
		FirstSeen:  first,
		LastSeen:   last,
		CallCount:  4242,
		DriftCount: 7,
	}
}

// TestBuildEdgeRegistrationsWireBytes is the wire-bytes law, and it is the
// load-bearing test of this slice: an INTERNAL edge never appears in a sync
// payload, and neither does a peer host, a call count or a drift count —
// asserted on the BYTES, not the struct, so a rename or an accidental embed
// cannot sneak one out.
func TestBuildEdgeRegistrationsWireBytes(t *testing.T) {
	rows := BuildEdgeRegistrations([]model.Edge{
		extEdge("api.acme.test:443", edge.DirectionClient, "2026-09-01T08:00:00Z", "2026-09-08T09:00:00Z"),
		{
			PeerHost:  "SENTINEL-ledger",
			Direction: edge.DirectionClient,
			Role:      edge.RoleConsumer,
			Class:     edge.ClassInternal,
			FirstSeen: "2026-09-01T08:00:00Z",
			LastSeen:  "2026-09-08T09:00:00Z",
		},
		{
			PeerHost:  "10.1.2.3",
			Direction: edge.DirectionServer,
			Role:      edge.RoleProvider,
			Class:     edge.ClassInternal,
			FirstSeen: "2026-09-01T08:00:00Z",
			LastSeen:  "2026-09-08T09:00:00Z",
		},
		{
			PeerHost:  "SENTINEL-mcp-stdio",
			Direction: edge.DirectionClient,
			Role:      edge.RoleConsumer,
			Class:     edge.ClassLocalProcess,
			FirstSeen: "2026-09-01T08:00:00Z",
			LastSeen:  "2026-09-08T09:00:00Z",
		},
	})

	raw, err := json.Marshal(EdgesRequest{Edges: rows})
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)

	// 1. No internal peer, in any spelling, reaches the wire.
	for _, sentinel := range []string{"SENTINEL-ledger", "SENTINEL-mcp-stdio", "10.1.2.3"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("wire bytes carry the internal peer %q: %s", sentinel, wire)
		}
	}
	// 2. Nor the peer HOST of the external edge — only its registrable domain.
	for _, sentinel := range []string{"api.acme.test", ":443", "4242"} {
		if strings.Contains(wire, sentinel) {
			t.Errorf("wire bytes carry the host/volume detail %q: %s", sentinel, wire)
		}
	}
	// 3. Nor the field NAMES that would carry one.
	for _, key := range []string{`"peer_host"`, `"class"`, `"role"`, `"call_count"`, `"drift_count"`} {
		if strings.Contains(wire, key) {
			t.Errorf("wire bytes carry the disallowed key %s: %s", key, wire)
		}
	}

	// 4. And the row is EXACTLY the allow-list, with the reduced domain and the
	//    orientation vocabulary.
	var decoded struct {
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Edges) != 1 {
		t.Fatalf("rows = %d, want 1 (the one external edge): %s", len(decoded.Edges), wire)
	}
	for key := range decoded.Edges[0] {
		if !edgeAllowedKeys[key] {
			t.Errorf("row carries the disallowed key %q: %s", key, wire)
		}
	}
	for key := range edgeAllowedKeys {
		if _, ok := decoded.Edges[0][key]; !ok {
			t.Errorf("row is missing the required key %q: %s", key, wire)
		}
	}
	if got := decoded.Edges[0]["registrable_domain"]; got != "acme.test" {
		t.Errorf("registrable_domain = %v, want acme.test", got)
	}
	if got := decoded.Edges[0]["direction"]; got != edge.OrientationOutbound {
		t.Errorf("direction = %v, want %s", got, edge.OrientationOutbound)
	}
}

// A registration is per (domain, direction): several hosts under one domain
// fold into one row spanning the widest window, and the two directions of the
// same domain stay separate rows.
func TestBuildEdgeRegistrationsFoldsByDomainAndDirection(t *testing.T) {
	rows := BuildEdgeRegistrations([]model.Edge{
		extEdge("api.acme.test", edge.DirectionClient, "2026-09-03T00:00:00Z", "2026-09-05T00:00:00Z"),
		extEdge("api-eu.acme.test", edge.DirectionClient, "2026-09-01T00:00:00Z", "2026-09-04T00:00:00Z"),
		extEdge("cdn.acme.test:8443", edge.DirectionClient, "2026-09-02T00:00:00Z", "2026-09-09T00:00:00Z"),
		extEdge("consumer-a.test", edge.DirectionServer, "2026-09-06T00:00:00Z", "2026-09-07T00:00:00Z"),
	})

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (acme.test outbound + consumer-a.test inbound): %+v", len(rows), rows)
	}
	got := rows[0]
	if got.RegistrableDomain != "acme.test" || got.Direction != edge.OrientationOutbound {
		t.Fatalf("row 0 = %+v, want acme.test outbound", got)
	}
	// The fold spans the WIDEST window across the three hosts.
	if got.FirstSeen != "2026-09-01T00:00:00Z" {
		t.Errorf("first_seen = %q, want the earliest 2026-09-01T00:00:00Z", got.FirstSeen)
	}
	if got.LastSeen != "2026-09-09T00:00:00Z" {
		t.Errorf("last_seen = %q, want the latest 2026-09-09T00:00:00Z", got.LastSeen)
	}
	if rows[1].RegistrableDomain != "consumer-a.test" || rows[1].Direction != edge.OrientationInbound {
		t.Errorf("row 1 = %+v, want consumer-a.test inbound", rows[1])
	}
}

// An IP-literal peer registers the literal (RegistrableDomain's rule), and a
// row with no usable timestamps or no resolvable domain is dropped rather than
// registered blank — a blank field would 400 the whole batch on every tick.
func TestBuildEdgeRegistrationsNormalizesAndDrops(t *testing.T) {
	rows := BuildEdgeRegistrations([]model.Edge{
		extEdge("203.0.113.9:8080", edge.DirectionClient, "", "2026-09-08T00:00:00Z"),
		extEdge("api.globex.test", edge.DirectionClient, "2026-09-08T00:00:00Z", ""),
		extEdge("", edge.DirectionClient, "2026-09-08T00:00:00Z", "2026-09-08T00:00:00Z"),
		extEdge("api.timeless.test", edge.DirectionClient, "", ""),
	})

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	// first_seen falls back to last_seen, and vice versa — never empty.
	if rows[0].RegistrableDomain != "203.0.113.9" || rows[0].FirstSeen != "2026-09-08T00:00:00Z" {
		t.Errorf("row 0 = %+v, want the IP literal with a filled first_seen", rows[0])
	}
	if rows[1].RegistrableDomain != "globex.test" || rows[1].LastSeen != "2026-09-08T00:00:00Z" {
		t.Errorf("row 1 = %+v, want globex.test with a filled last_seen", rows[1])
	}
}

// The cap is the CP's, and it takes a STABLE subset: the same rows every tick,
// not a different slice each time.
func TestBuildEdgeRegistrationsCapsAtMaxItems(t *testing.T) {
	edges := make([]model.Edge, 0, EdgesSyncMaxItems+20)
	for i := 0; i < EdgesSyncMaxItems+20; i++ {
		edges = append(edges, extEdge(
			"api.d"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))+".test",
			edge.DirectionClient, "2026-09-08T00:00:00Z", "2026-09-08T00:00:00Z"))
	}
	first := BuildEdgeRegistrations(edges)
	if len(first) != EdgesSyncMaxItems {
		t.Fatalf("rows = %d, want the cap %d", len(first), EdgesSyncMaxItems)
	}
	second := BuildEdgeRegistrations(edges)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("the capped subset is not stable at row %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// An empty input is an empty batch, never a nil that marshals to `"edges":null`.
func TestBuildEdgeRegistrationsEmpty(t *testing.T) {
	raw, err := json.Marshal(EdgesRequest{Edges: BuildEdgeRegistrations(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"edges":[]}` {
		t.Errorf("empty batch marshals as %s, want {\"edges\":[]}", got)
	}
}
