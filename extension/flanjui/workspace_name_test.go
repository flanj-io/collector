package flanjui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/promote"
)

// The collector no longer names its organization (CONTRACTS §5, 2026-09-19).
// A workspace has ONE display name, owned by the control plane and chosen by
// the contact on the confirmation page; the collector only reads it back from
// `me`, keeps a copy in the store, and shows or sends that copy.

func expireMe(r *testRig) {
	r.ext.me.mu.Lock()
	r.ext.me.at = time.Time{}
	r.ext.me.mu.Unlock()
}

func setWorkspace(r *testRig, name string) {
	r.cp.mu.Lock()
	r.cp.workspaceName = &name
	r.cp.mu.Unlock()
}

// Connect needs no org name any more, and never sends one to the control
// plane — not even when an older UI still posts the field.
func TestConnectSendsNoOrgName(t *testing.T) {
	r := newRig(t)
	r.start(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{
		"collector_name": "prod-eu",
		"contact_email":  "ops@acme.test",
	})
	if resp.StatusCode != 202 {
		t.Fatalf("Connect without an org name must be accepted: %d %s", resp.StatusCode, raw)
	}
	if _, has := r.cp.lastRegisterBody["consumer_display_name"]; has {
		t.Fatalf("register must not carry consumer_display_name: %v", r.cp.lastRegisterBody)
	}

	// An older UI still sends the field: accepted, and ignored.
	resp, out, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{
		"consumer_display_name": "Old Org Name",
		"collector_name":        "prod-eu",
		"contact_email":         "ops@acme.test",
	})
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		t.Fatalf("Connect from an older UI: %d %s", resp.StatusCode, raw)
	}
	if _, has := r.cp.lastRegisterBody["consumer_display_name"]; has {
		t.Fatalf("register must not relay an older UI's org name: %v", r.cp.lastRegisterBody)
	}
	if strings.Contains(string(raw), "Old Org Name") {
		t.Fatalf("the ignored org name must not come back in the view: %s", raw)
	}
	// Nor does the contact's display name default to it any more.
	if out["contact_display_name"] != nil {
		t.Fatalf("contact_display_name must not be seeded from the org name: %v", out["contact_display_name"])
	}
}

// The `me` poll caches workspace_display_name in the store settings KV; GET
// /api/connect serves the copy, follows a rename, and keeps serving it while
// the control plane is unreachable and after a restart.
func TestWorkspaceDisplayNameCached(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectRig(t, r)

	// Before any contact has named the workspace: null, never a guess — the
	// configured consumer_display_name ("Cfg Consumer" in the rig) included.
	expireMe(r)
	_, out, raw := r.do(t, http.MethodGet, "/api/connect", nil)
	if v, has := out["workspace_display_name"]; !has || v != nil {
		t.Fatalf("workspace_display_name before naming = %v (present %v), want null: %s", v, has, raw)
	}
	if _, has := out["consumer_display_name"]; has {
		t.Fatalf("GET /api/connect must not carry consumer_display_name: %s", raw)
	}
	if strings.Contains(string(raw), "Cfg Consumer") || strings.Contains(string(raw), "Acme Consumer Ltd") {
		t.Fatalf("no configured or deprecated org name may reach the Connect view: %s", raw)
	}

	// The contact names the workspace on the confirmation page.
	setWorkspace(r, "Acme Ltd")
	expireMe(r)
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["workspace_display_name"] != "Acme Ltd" {
		t.Fatalf("workspace_display_name after naming = %v", out["workspace_display_name"])
	}
	if v, ok, _ := r.st.GetSetting("connect.workspace_display_name"); !ok || v != "Acme Ltd" {
		t.Fatalf("stored connect.workspace_display_name = %q (%v)", v, ok)
	}

	// A rename on the dashboard shows up on the next poll.
	setWorkspace(r, "Acme Group")
	expireMe(r)
	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["workspace_display_name"] != "Acme Group" {
		t.Fatalf("workspace_display_name after a rename = %v", out["workspace_display_name"])
	}

	// The control plane fails: the copy stays.
	r.cp.mu.Lock()
	r.cp.meStatus = 500
	r.cp.mu.Unlock()
	expireMe(r)
	_, out, raw = r.do(t, http.MethodGet, "/api/connect", nil)
	if out["workspace_display_name"] != "Acme Group" {
		t.Fatalf("workspace_display_name while the CP errors = %v: %s", out["workspace_display_name"], raw)
	}

	// A restart: a new extension on the same store, with a control plane that
	// is not there at all.
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	ext2 := &uiExtension{
		cfg:       r.ext.cfg,
		telemetry: component.TelemetrySettings{Logger: zap.NewNop()},
		st:        r.st,
	}
	ext2.cp = promote.NewClient(goneURL, "", "v-test")
	ui2 := httptest.NewServer(ext2.routes())
	t.Cleanup(ui2.Close)
	resp, err := http.Get(ui2.URL + "/api/connect")
	if err != nil {
		t.Fatal(err)
	}
	var out2 map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out2)
	resp.Body.Close()
	if out2["workspace_display_name"] != "Acme Group" {
		t.Fatalf("after a restart with the CP unreachable: workspace_display_name = %v (%v)", out2["workspace_display_name"], out2)
	}
}

// Zero-traffic honesty: /api/health names no organization from config.
func TestHealthNamesNoOrgFromConfig(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_, out, raw := r.do(t, http.MethodGet, "/api/health", nil)
	if _, has := out["consumer_display_name"]; has || strings.Contains(string(raw), "Cfg Consumer") {
		t.Fatalf("/api/health must not name the organization from config: %s", raw)
	}
}

// The flag body carries the cached workspace name when known, else no
// consumer_display_name at all — never the config value, never the name an
// older register sent.
func TestFlagCarriesWorkspaceName(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectRig(t, r)
	r.cp.mu.Lock()
	r.cp.contactStatus = "confirmed"
	r.cp.mu.Unlock()
	expireMe(r)
	r.do(t, http.MethodGet, "/api/connect", nil)

	resp, _, raw := r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{"acme-payments.test"}})
	if resp.StatusCode != 201 {
		t.Fatalf("flag: %d %s", resp.StatusCode, raw)
	}
	if v, has := r.cp.lastFlagBody["consumer_display_name"]; has {
		t.Fatalf("flag with no known workspace name must omit consumer_display_name, got %v", v)
	}

	setWorkspace(r, "Acme Ltd")
	expireMe(r)
	r.do(t, http.MethodGet, "/api/connect", nil)
	resp, _, raw = r.do(t, http.MethodPost, "/api/flag", map[string]any{"finding_id": "fnd_1", "allowed_domains": []string{"acme-payments.test"}})
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		t.Fatalf("re-flag: %d %s", resp.StatusCode, raw)
	}
	if got := r.cp.lastFlagBody["consumer_display_name"]; got != "Acme Ltd" {
		t.Fatalf("flag consumer_display_name = %v, want the cached workspace name", got)
	}
}
