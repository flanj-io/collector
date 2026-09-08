package flanjui

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/promote"
)

// The dashboard door (launch-week item 8). `dashboard_url` on GET /api/connect
// is the ONE link the local UI offers out to the control plane, and it is
// rendered in the OPERATOR'S BROWSER — so it must be minted from an address a
// browser can open, not from where this collector's own requests go. The
// first version read it off the promote client, which is right for the API
// client and wrong for a link: on the e2e stack cp_base_url is
// `http://cp-api:3001` (docker DNS), in a cluster it is a Service name or a
// VPC-private ingress, and a laptop resolves none of them — a dead pill.

// TestDashboardURLComposition pins the pure rule: cp_public_url is
// authoritative; without it cp_base_url is used only when its host is not
// obviously non-public; the path is composed here; userinfo / query / fragment
// never survive into a link the browser gets.
func TestDashboardURLComposition(t *testing.T) {
	cases := []struct {
		name, public, base, want string
	}{
		// cp_public_url set: authoritative, whatever the client's base is.
		{"public wins over a docker DNS base", "https://cp.flanj.io", "http://cp-api:3001", "https://cp.flanj.io/d"},
		{"public trailing slash is not doubled", "https://cp.flanj.io/", "http://cp-api:3001", "https://cp.flanj.io/d"},
		{"public may be loopback — explicit is explicit (the e2e stack's host-published port)", "http://localhost:3001", "http://cp-api:3001", "http://localhost:3001/d"},
		{"public keeps a path prefix", "https://acme.example.com/flanj/", "http://cp-api:3001", "https://acme.example.com/flanj/d"},
		{"public never carries userinfo into the link", "https://ops:secret@cp.flanj.io", "http://cp-api:3001", "https://cp.flanj.io/d"},
		{"public query and fragment are dropped", "https://cp.flanj.io/?x=1#f", "http://cp-api:3001", "https://cp.flanj.io/d"},
		{"public surrounding whitespace is tolerated", "  https://cp.flanj.io  ", "http://cp-api:3001", "https://cp.flanj.io/d"},

		// cp_public_url unset: fall back to the base only when a browser could plausibly reach it.
		{"public-looking base falls back", "", "https://cp.flanj.io", "https://cp.flanj.io/d"},
		{"public-looking base with trailing slash", "", "https://cp.flanj.io/", "https://cp.flanj.io/d"},
		{"public-looking base keeps its path prefix", "", "https://acme.example.com/flanj", "https://acme.example.com/flanj/d"},
		{"docker DNS base is no door", "", "http://cp-api:3001", ""},
		{"localhost base is no door", "", "http://localhost:3001", ""},
		{"loopback IP base is no door", "", "http://127.0.0.1:3001", ""},
		{"IPv6 loopback base is no door", "", "http://[::1]:3001", ""},
		{"private IP base is no door", "", "http://10.1.2.3:3001", ""},
		{"private 172.16/12 base is no door", "", "http://172.20.0.5", ""},
		{"private 192.168/16 base is no door", "", "http://192.168.1.10:3001", ""},
		{"IPv4-mapped private base is no door", "", "http://[::ffff:10.0.0.1]:3001", ""},
		{"link-local base is no door", "", "http://169.254.1.1", ""},
		{"k8s Service FQDN base is no door", "", "http://cp-api.flanj.svc.cluster.local:3001", ""},
		{"k8s Service short form base is no door", "", "http://cp-api.flanj.svc", ""},
		{".internal base is no door", "", "https://cp.flanj.internal", ""},
		{".local base is no door", "", "https://cp.local", ""},
		{".test placeholder (the example config) is no door", "", "https://cp.flanj.test", ""},
		{".example base is no door", "", "https://cp.example", ""},
		{"trailing dot does not launder a reserved suffix", "", "https://cp.flanj.internal.", ""},
		{"case does not launder a reserved suffix", "", "https://CP.FLANJ.INTERNAL", ""},
		{"no base at all", "", "", ""},
		{"unparseable base", "", "://nope", ""},
		{"non-http scheme", "", "ftp://cp.flanj.io", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dashboardURL(c.public, c.base); got != c.want {
				t.Fatalf("dashboardURL(%q, %q) = %q, want %q", c.public, c.base, got, c.want)
			}
		})
	}
}

// TestDashboardURLOnlyWhenConnected pins the first gate: the door appears ONLY
// once this deployment actually holds a collector key. Offering a door to a CP
// this collector has no identity at sends the operator to a signed-out page for
// no reason, and the SPA decides whether to render the link purely on this
// field's presence — so a browser-facing address alone is not enough.
func TestDashboardURLOnlyWhenConnected(t *testing.T) {
	r := newRig(t)
	r.ext.cfg.CPPublicURL = "https://cp.flanj.example.com/"
	r.start(t)

	// Disconnected: an address, but no key, so no door.
	_, out, _ := r.do(t, http.MethodGet, "/api/connect", nil)
	if _, ok := out["dashboard_url"]; ok {
		t.Fatalf("a disconnected collector must not offer a dashboard link, got %v", out["dashboard_url"])
	}

	connectRig(t, r)

	_, out, _ = r.do(t, http.MethodGet, "/api/connect", nil)
	got, _ := out["dashboard_url"].(string)
	if got == "" {
		t.Fatalf("a Connected collector with a browser-facing address must offer the dashboard link, got %v", out)
	}
	// The collector composes the path — the SPA must never have to know which
	// page the dashboard is, nor assemble it from a base URL.
	if !strings.HasSuffix(got, "/d") {
		t.Errorf("dashboard_url should point at the CP dashboard page, got %q", got)
	}
	if strings.Contains(got, "//d") {
		t.Errorf("dashboard_url has a doubled slash — trailing slash not trimmed: %q", got)
	}
}

// TestDashboardURLIsBrowserFacingNotClientFacing is the defect itself: with
// cp_public_url set, the link is minted from it and NOT from the address the
// relay's own requests go to (here the stub CP on 127.0.0.1, which stands in
// for `http://cp-api:3001`). And neither address ever reaches a log line.
func TestDashboardURLIsBrowserFacingNotClientFacing(t *testing.T) {
	r := newRig(t)
	const public = "https://cp.flanj.example.com"
	r.ext.cfg.CPPublicURL = public
	r.start(t)
	connectRig(t, r)

	_, out, _ := r.do(t, http.MethodGet, "/api/connect", nil)
	got, _ := out["dashboard_url"].(string)
	if got != public+"/d" {
		t.Fatalf("dashboard_url must be minted from cp_public_url, got %q (client base %q)", got, r.ext.cp.BaseURL)
	}
	if strings.Contains(got, r.ext.cp.BaseURL) {
		t.Fatalf("dashboard_url leaked the client-facing base into a browser link: %q", got)
	}
	r.assertNeverLogged(t, public, r.ext.cp.BaseURL)
}

// TestDashboardURLOmittedWhenBaseIsNotBrowserReachable: no cp_public_url and a
// base a browser cannot open (the stub CP is loopback — the same class as a
// docker DNS name or a k8s Service) → the field is ABSENT even though the
// collector is Connected. The SPA turns absence into the Settings button; the
// old behaviour turned it into a dead link.
func TestDashboardURLOmittedWhenBaseIsNotBrowserReachable(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectRig(t, r)

	resp, out, raw := r.do(t, http.MethodGet, "/api/connect", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/connect: %d %s", resp.StatusCode, raw)
	}
	if v, ok := out["dashboard_url"]; ok {
		t.Fatalf("a Connected collector whose only CP address is %q must offer NO door (the browser cannot open it), got %v", r.ext.cp.BaseURL, v)
	}
	if out["status"] == "disconnected" {
		t.Fatalf("precondition: the collector should hold a key, got %s", raw)
	}
}

// TestDashboardURLFallsBackToPublicLookingBase: no cp_public_url, but a base a
// browser plausibly can open → the door is minted from it, and it does not
// depend on the CP answering right now (the deployment IS Connected; a blip
// on `me` is reported as `error`, not by hiding the door).
func TestDashboardURLFallsBackToPublicLookingBase(t *testing.T) {
	r := newRig(t)
	r.start(t)
	connectRig(t, r)

	// Swap the client for one pointed at a public-looking origin whose
	// transport never touches the network.
	r.ext.cp = &promote.Client{
		BaseURL:     "https://cp.flanj.io",
		DeployToken: r.cp.deployToken,
		HTTP:        &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("no network in this test") })},
	}

	_, out, raw := r.do(t, http.MethodGet, "/api/connect", nil)
	if got, _ := out["dashboard_url"].(string); got != "https://cp.flanj.io/d" {
		t.Fatalf("a public-looking cp_base_url must still mint the door when cp_public_url is unset, got %q (%s)", got, raw)
	}
	if out["error"] != "cp_unreachable" {
		t.Errorf("the failed `me` refresh should be reported as error=cp_unreachable, got %v", out["error"])
	}
}

// TestConfigValidateCPPublicURL: a typo ships a dead link on every page load,
// so the key is validated at boot; and it is handed to a browser verbatim, so
// credentials in it are refused rather than stripped silently.
func TestConfigValidateCPPublicURL(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
	}{
		{"", true},
		{"https://cp.flanj.io", true},
		{"http://localhost:3001", true},
		{"https://acme.example.com/flanj/", true},
		{"cp.flanj.io", false},             // no scheme
		{"//cp.flanj.io", false},           // scheme-relative is not a link the collector should complete
		{"ftp://cp.flanj.io", false},       // not a browser origin
		{"https://", false},                // no host
		{"https://u:p@cp.flanj.io", false}, // credentials
		{"://nope", false},
	}
	for _, c := range cases {
		cfg := &Config{UIEndpoint: "127.0.0.1:5335", CPPublicURL: c.value}
		err := cfg.Validate()
		if c.ok && err != nil {
			t.Errorf("cp_public_url=%q should validate, got %v", c.value, err)
		}
		if !c.ok && err == nil {
			t.Errorf("cp_public_url=%q should be refused at boot", c.value)
		}
	}
}

// connectRig performs the first Connect, which persists the collector key.
func connectRig(t *testing.T, r *testRig) {
	t.Helper()
	if resp, _, raw := r.do(t, http.MethodPost, "/api/connect", map[string]string{
		"consumer_display_name": "Acme Consumer Ltd",
		"contact_email":         "ops@acme.test",
		"contact_display_name":  "Dana",
	}); resp.StatusCode != 202 && resp.StatusCode != 200 {
		t.Fatalf("connect: %d %s", resp.StatusCode, raw)
	}
}

// roundTripFunc adapts a func to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
