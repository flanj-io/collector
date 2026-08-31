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

// TestConfigNameBootMigration: provider_display_name migrates into the KV as a
// source `config` record keyed by the drift-target edge's registrable domain
// (spec_infos linkage); a `user` record is NEVER overwritten; a re-run with a
// changed YAML value refreshes the `config` record.
func TestConfigNameBootMigration(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.ProviderDisplayName = "Acme Payments"
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{Integration: "acme-payments", Role: "provider", PeerHost: "api.zzguava.dev"}, nil)

	r.ext.maybeMigrateNames(r.st)
	names, err := loadEdgeNames(r.st)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := names["zzguava.dev"]
	if !ok || rec.Name != "Acme Payments" || rec.Source != nameSourceConfig {
		t.Fatalf("migrated record = %+v ok=%v, want source config", rec, ok)
	}

	// A changed YAML value refreshes the config record on re-run (idempotent
	// otherwise — migrateConfigNames is the once-per-boot body).
	r.ext.cfg.ProviderDisplayName = "Acme Pay GmbH"
	r.ext.migrateConfigNames(r.st)
	names, _ = loadEdgeNames(r.st)
	if rec := names["zzguava.dev"]; rec.Name != "Acme Pay GmbH" || rec.Source != nameSourceConfig {
		t.Fatalf("refreshed record = %+v, want the new YAML value", rec)
	}

	// A user record is never touched by the migration.
	if err := putEdgeName(r.st, "zzguava.dev", "Our PSP", nameSourceUser); err != nil {
		t.Fatal(err)
	}
	r.ext.cfg.ProviderDisplayName = "Yet Another Name"
	r.ext.migrateConfigNames(r.st)
	names, _ = loadEdgeNames(r.st)
	if rec := names["zzguava.dev"]; rec.Name != "Our PSP" || rec.Source != nameSourceUser {
		t.Fatalf("user record overwritten by migration: %+v", rec)
	}
}

// TestConfigNameMigrationSkipsUnderivableLinkage: with no spec_infos row for
// the configured integration the linkage is NOT derivable — nothing is
// invented, nothing is written, and (read-time equivalence) the config tier
// resolves nowhere in the listing either.
func TestConfigNameMigrationSkipsUnderivableLinkage(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.ProviderDisplayName = "Acme Payments"
	r.start(t)
	seedOutboundEdge(r, "api.zzguava.dev")

	before := r.st.settingsSnapshot()
	r.ext.maybeMigrateNames(r.st)
	after := r.st.settingsSnapshot()
	if len(before) != len(after) {
		t.Fatalf("underivable linkage wrote settings: %v -> %v", before, after)
	}
	if row := edgeRowFor(t, r, "api.zzguava.dev"); row["name_source"] != "auto" {
		t.Errorf("read-time config tier resolved without a linkage: %v", row)
	}
}

// TestConfigNameReadTimeEquivalence: the listing is IDENTICAL whether the
// migration ran or not — the read-time fallback uses the same linkage.
func TestConfigNameReadTimeEquivalence(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.ProviderDisplayName = "Acme Payments"
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{Integration: "acme-payments", Role: "provider", PeerHost: "api.zzguava.dev"}, nil)
	seedOutboundEdge(r, "api.zzguava.dev")

	pre := edgeRowFor(t, r, "api.zzguava.dev")
	if pre["display_name"] != "Acme Payments" || pre["name_source"] != "config" {
		t.Fatalf("pre-migration row = %v", pre)
	}
	r.ext.maybeMigrateNames(r.st)
	post := edgeRowFor(t, r, "api.zzguava.dev")
	if post["display_name"] != pre["display_name"] || post["name_source"] != pre["name_source"] {
		t.Fatalf("migration changed the resolved output: %v -> %v", pre, post)
	}

	// A REDACTABLE YAML value: the read-time path applies the same redaction
	// floor the migration applies before persist, so the resolved name is
	// byte-identical before and after the migration — and the raw value never
	// renders.
	const pan = "4242424242424242"
	r2 := newRig(t)
	r2.ext.cfg.ProviderDisplayName = "Acme " + pan + " Payments"
	r2.start(t)
	_ = r2.st.PutSpecInfo(model.SpecInfo{Integration: "acme-payments", Role: "provider", PeerHost: "api.zzguava.dev"}, nil)
	seedOutboundEdge(r2, "api.zzguava.dev")

	pre2 := edgeRowFor(t, r2, "api.zzguava.dev")
	preName, _ := pre2["display_name"].(string)
	if pre2["name_source"] != "config" || strings.Contains(preName, pan) || !strings.Contains(preName, "⟦REDACTED:") {
		t.Fatalf("pre-migration read-time config name not redacted: %v", pre2)
	}
	r2.ext.maybeMigrateNames(r2.st)
	post2 := edgeRowFor(t, r2, "api.zzguava.dev")
	if post2["display_name"] != pre2["display_name"] || post2["name_source"] != pre2["name_source"] {
		t.Fatalf("redacted config name differs across the migration: %v -> %v", pre2, post2)
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
