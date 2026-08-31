package flanjui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// startBare serves the UI WITHOUT the rig's seeded finding/call — the
// zero-traffic collector.
func (r *testRig) startBare(t *testing.T) {
	t.Helper()
	r.ui = httptest.NewServer(r.ext.routes())
	t.Cleanup(r.ui.Close)
}

// TestDirectorySyncPullThenConditional: the first tick pulls the full table
// (the §5.14 ENVELOPE, stored raw with one blind put, ETag remembered); the
// next tick sends If-None-Match and a 304 is a no-op. Asserted at the wire.
func TestDirectorySyncPullThenConditional(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	const entries = `{"zzguava.dev":{"name":"Guava Billing","tier":"curated"}}`
	r.cp.mu.Lock()
	r.cp.directoryEntries, r.cp.directoryETag = entries, `"v1"`
	r.cp.mu.Unlock()
	// The KV stores the RAW envelope body, byte-for-byte (decision in
	// parseDirectoryTable) — never an unwrapped entries map.
	wantStored := directoryEnvelopeBody(entries)

	r.ext.syncDirectoryOnce(context.Background())
	if got, ok, _ := r.st.GetSetting(settingDirectoryTable); !ok || got != wantStored {
		t.Fatalf("directory.table = %q, want the raw envelope body %q", got, wantStored)
	}
	if got, _, _ := r.st.GetSetting(settingDirectoryETag); got != `"v1"` {
		t.Fatalf("directory.etag = %q, want %q", got, `"v1"`)
	}

	r.ext.syncDirectoryOnce(context.Background())
	if r.cp.directoryCallCount() != 2 {
		t.Fatalf("directory calls = %d, want 2", r.cp.directoryCallCount())
	}
	if r.cp.directoryINM(0) != "" {
		t.Errorf("first pull sent If-None-Match %q, want none", r.cp.directoryINM(0))
	}
	if r.cp.directoryINM(1) != `"v1"` {
		t.Errorf("second pull sent If-None-Match %q, want the stored ETag", r.cp.directoryINM(1))
	}
	if got, _, _ := r.st.GetSetting(settingDirectoryTable); got != wantStored {
		t.Errorf("304 must be a no-op; table changed to %q", got)
	}
	r.assertNeverLogged(t, r.cp.collectorKey)
}

// TestDirectorySyncSkipsSilently: no collector key → no call; no CP client →
// no call. The local UI never depends on the pull.
func TestDirectorySyncSkipsSilently(t *testing.T) {
	r := newRig(t)
	r.start(t) // CP configured, but no key in the store
	r.ext.syncDirectoryOnce(context.Background())
	if r.cp.directoryCallCount() != 0 {
		t.Errorf("no-key tick pulled %d times", r.cp.directoryCallCount())
	}

	r2 := newRig(t) // no CP client at all
	connectKeyOnly(t, r2)
	r2.ext.syncDirectoryOnce(context.Background())
	if r2.cp.directoryCallCount() != 0 {
		t.Errorf("no-CP tick pulled %d times", r2.cp.directoryCallCount())
	}
}

// TestDirectorySeedResolvesOffline: a fresh collector — no CP configured, no
// collector key, nothing pulled — resolves a baked-seed entry on the first
// listing. The seed renders offline, pre-Connect.
func TestDirectorySeedResolvesOffline(t *testing.T) {
	r := newRig(t)
	r.startBare(t) // note: no CP client is wired at all
	seedOutboundEdge(r, "api.stripe.com")

	if row := edgeRowFor(t, r, "api.stripe.com"); row["display_name"] != "Stripe" || row["name_source"] != "directory" {
		t.Errorf("offline seed resolution = %v, want the baked entry", row)
	}
	if r.cp.directoryCallCount() != 0 || r.cp.findingsCallCount() != 0 {
		t.Errorf("offline resolution must make no CP calls")
	}
}

// TestDirectoryPullMergesOverSeed: a pulled entry WINS over the baked seed for
// the same domain, while a seed-only domain still resolves — merge, never
// replace. The table arrives through the real pull (wire), not a KV fixture.
func TestDirectoryPullMergesOverSeed(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectKeyOnly(t, r)
	seedOutboundEdge(r, "api.stripe.com") // seed says "Stripe"; the pull disagrees
	seedOutboundEdge(r, "api.adyen.com")  // only the seed knows this one
	r.cp.mu.Lock()
	r.cp.directoryEntries, r.cp.directoryETag = `{"stripe.com":{"name":"Stripe, Inc.","tier":"curated"}}`, `"v2"`
	r.cp.mu.Unlock()

	r.ext.syncDirectoryOnce(context.Background())

	if row := edgeRowFor(t, r, "api.stripe.com"); row["display_name"] != "Stripe, Inc." || row["name_source"] != "directory" {
		t.Errorf("pulled entry must win over the baked seed: %v", row)
	}
	if row := edgeRowFor(t, r, "api.adyen.com"); row["display_name"] != "Adyen" || row["name_source"] != "directory" {
		t.Errorf("seed-only entry must survive the merge: %v", row)
	}
}

// TestContractNameTier: the contract UPLOADED for a domain names its edges, in
// the provider's own words (`info.title`). This is the tier that replaced
// `config` when `spec_path` was removed — the legacy YAML name could not say
// WHICH edge it meant, and an upload says both at once.
func TestContractNameTier(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "api-zzguava-dev", Role: model.SpecRoleProvider,
		Format: model.SpecFormatOpenAPI, PeerHost: "api.zzguava.dev",
		Title: "Guava Billing API", Source: model.SpecSourceUpload,
	}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")

	row := edgeRowFor(t, r, "api.zzguava.dev")
	if row["display_name"] != "Guava Billing API" || row["name_source"] != "contract" {
		t.Fatalf("row = %v, want the contract's title at source `contract`", row)
	}
}

// TestContractNameIsDomainWide: contract BINDING is host-level (subdomains
// routinely run different APIs) but NAMING is domain-level (a name describes
// the organisation). One upload therefore names every edge under the domain,
// which is what makes a fifty-provider estate legible after fifty uploads.
func TestContractNameIsDomainWide(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "api-zzguava-dev", Role: model.SpecRoleProvider,
		Format: model.SpecFormatOpenAPI, PeerHost: "api.zzguava.dev",
		Title: "Guava Billing API", Source: model.SpecSourceUpload,
	}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")
	seedOutboundEdge(r, "api-eu.zzguava.dev")

	sibling := edgeRowFor(t, r, "api-eu.zzguava.dev")
	if sibling["display_name"] != "Guava Billing API" || sibling["name_source"] != "contract" {
		t.Errorf("sibling host under the same domain = %v, want the same name", sibling)
	}
}

// TestUserNameBeatsContractName: a rename is the more specific act, so it wins.
func TestUserNameBeatsContractName(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "api-zzguava-dev", Role: model.SpecRoleProvider,
		Format: model.SpecFormatOpenAPI, PeerHost: "api.zzguava.dev",
		Title: "Guava Billing API", Source: model.SpecSourceUpload,
	}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")
	if err := putEdgeName(r.st, "zzguava.dev", "Guava (ours)", nameSourceUser); err != nil {
		t.Fatal(err)
	}

	row := edgeRowFor(t, r, "api.zzguava.dev")
	if row["display_name"] != "Guava (ours)" || row["name_source"] != "user" {
		t.Errorf("row = %v, want the operator's own name to win", row)
	}
}

// TestSelfContractNamesNothing: the contract WE publish describes our own API,
// not a provider's. Letting it name an outbound edge would put the org's own
// name on somebody else's row.
func TestSelfContractNamesNothing(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "self", Role: model.SpecRoleSelf, Format: model.SpecFormatOpenAPI,
		PeerHost: "api.zzguava.dev", Title: "Our Public API", Source: model.SpecSourceConfig,
	}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")

	row := edgeRowFor(t, r, "api.zzguava.dev")
	if row["name_source"] == "contract" {
		t.Errorf("a SELF contract named an outbound edge: %v", row)
	}
}

// TestContractNamePassesTheRedactionFloor: an uploaded document is
// operator-supplied text like a typed rename, so its title goes through the same
// floor before it can render. A title the floor consumes entirely names nothing.
func TestContractNamePassesTheRedactionFloor(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "api-zzguava-dev", Role: model.SpecRoleProvider,
		Format: model.SpecFormatOpenAPI, PeerHost: "api.zzguava.dev",
		Title: "4111111111111111", Source: model.SpecSourceUpload,
	}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")

	row := edgeRowFor(t, r, "api.zzguava.dev")
	name, _ := row["display_name"].(string)
	if strings.Contains(name, "4111111111111111") {
		t.Errorf("a card number in info.title rendered as an edge name: %v", row)
	}
}


// TestHealthZeroTrafficHonesty: a fresh zero-traffic collector fabricates
// nothing — /api/health omits `integration` and `provider_display_name`
// entirely (no replacement values). They appear once an external outbound edge
// (or a finding) exists. `consumer_display_name` — the installer's own name —
// renders regardless.
func TestHealthZeroTrafficHonesty(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.ProviderDisplayName = "Acme Payments"
	r.startBare(t)

	resp, out, raw := r.do(t, http.MethodGet, "/api/health", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("health: %d %s", resp.StatusCode, raw)
	}
	for _, k := range []string{"integration", "provider_display_name"} {
		if _, present := out[k]; present {
			t.Errorf("zero-traffic health carries %q: %s", k, raw)
		}
	}
	if out["consumer_display_name"] != "Cfg Consumer" {
		t.Errorf("consumer_display_name (the installer's own name) must still render: %v", out["consumer_display_name"])
	}

	// One external OUTBOUND edge → both fields emit.
	seedOutboundEdge(r, "api.zzguava.dev")
	_, out, _ = r.do(t, http.MethodGet, "/api/health", nil)
	if out["integration"] != "acme-payments" || out["provider_display_name"] != "Acme Payments" {
		t.Errorf("post-traffic health = %v", out)
	}

	// An INBOUND-only or internal edge is not a provider observation.
	r2 := newRig(t)
	r2.ext.cfg.ProviderDisplayName = "Acme Payments"
	r2.startBare(t)
	r2.st.mu.Lock()
	r2.st.edges = append(r2.st.edges,
		model.Edge{PeerHost: "in.zzcaller.dev", Direction: "server", Class: "external"},
		model.Edge{PeerHost: "10.0.0.5", Direction: "client", Class: "internal"})
	r2.st.mu.Unlock()
	_, out, _ = r2.do(t, http.MethodGet, "/api/health", nil)
	if _, present := out["integration"]; present {
		t.Errorf("inbound/internal edges alone must not emit the provider fields: %v", out)
	}

	// A finding (with no edges) also counts as an observation.
	r3 := newRig(t)
	r3.ext.cfg.ProviderDisplayName = "Acme Payments"
	r3.startBare(t)
	_ = r3.st.InsertFinding(model.Finding{SchemaVersion: 1, ID: "fnd_z", Kind: model.KindLiveVsSpec, Severity: model.SeverityBreaking,
		Integration: "acme-payments", Endpoint: "POST /v1/charges", Expected: "integer", Actual: "string", Rule: "type", DetectedAt: "2026-08-30T10:00:01Z"})
	_, out, _ = r3.do(t, http.MethodGet, "/api/health", nil)
	if out["integration"] != "acme-payments" || out["provider_display_name"] != "Acme Payments" {
		t.Errorf("a finding must unlock the provider fields: %v", out)
	}
}
