package flanjui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/component"

	"github.com/flanj-io/collector/internal/model"
)

func contractFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func specV1Doc(t *testing.T) string { return contractFixture(t, "spec-v1.yaml") }
func specV2Doc(t *testing.T) string { return contractFixture(t, "spec-v2.yaml") }

// TestUploadStoresAContractBoundToItsHost is the happy path: an operator
// uploads a document, names the provider, and the collector records a contract
// the drift processor's refresh will pick up.
func TestUploadStoresAContractBoundToItsHost(t *testing.T) {
	r := newRig(t)
	r.start(t)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
		"filename":  "acme.yaml",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d: %s", resp.StatusCode, raw)
	}
	if out["replaced"] != false {
		t.Errorf("a first upload reported a replace: %v", out["replaced"])
	}

	infos, _ := r.st.ListSpecInfos()
	if len(infos) != 1 {
		t.Fatalf("stored %d contracts, want 1", len(infos))
	}
	si := infos[0]
	if si.PeerHost != "api.acme.test" {
		t.Errorf("peer_host = %q, want api.acme.test", si.PeerHost)
	}
	if si.Integration != "api-acme-test" {
		t.Errorf("integration = %q, want api-acme-test derived from the host", si.Integration)
	}
	if si.Source != model.SpecSourceUpload {
		t.Errorf("source = %q, want %q — the card's provenance word reads from it", si.Source, model.SpecSourceUpload)
	}
	if si.Role != model.SpecRoleProvider || si.Format != model.SpecFormatOpenAPI {
		t.Errorf("role/format = %q/%q, want provider/openapi", si.Role, si.Format)
	}
	if si.Endpoints == 0 || si.Version == "" {
		t.Errorf("metadata not extracted: %+v", si)
	}
	if si.LoadedAt == "" {
		t.Error("loaded_at empty — it is the change token the drift refresh compares")
	}
}

// TestUploadNeverReachesTheControlPlane is the ruling under test. Uploaded
// contracts stay on the collector; a document that left would be the whole
// feature betrayed, so this asserts it at the wire.
func TestUploadNeverReachesTheControlPlane(t *testing.T) {
	r := newRig(t)
	r.start(t)
	before := r.cp.requestCount()

	for _, path := range []string{"/api/contracts/preview", "/api/contracts/upload"} {
		resp, _, raw := r.do(t, http.MethodPost, path, map[string]string{
			"peer_host": "api.acme.test",
			"document":  specV1Doc(t),
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, resp.StatusCode, raw)
		}
	}
	if got := r.cp.requestCount(); got != before {
		t.Errorf("uploading made %d control-plane request(s) — uploaded contracts must never leave the collector", got-before)
	}
}

// TestUploadRefusesAnUnparseableDocumentAndPersistsNothing is the hard
// requirement. The drift refresh parses whatever is in the store, so a bad
// document reaching a row costs that host detection at a point the operator can
// no longer see the error.
func TestUploadRefusesAnUnparseableDocumentAndPersistsNothing(t *testing.T) {
	r := newRig(t)
	r.start(t)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  "this is not an OpenAPI document at all",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, raw)
	}
	if out["error"] != "unparseable_document" {
		t.Errorf("error = %v, want unparseable_document", out["error"])
	}
	msg, _ := out["message"].(string)
	if !strings.HasPrefix(msg, "Couldn't read that as an OpenAPI document.") {
		t.Errorf("message = %q, want the deck's string", msg)
	}
	if len(msg) <= len("Couldn't read that as an OpenAPI document. ") {
		t.Error("the parser's own reason was dropped — the operator needs to know WHICH line is wrong")
	}

	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("an unparseable document was persisted: %+v", infos)
	}
}

// TestUploadRequiresAHostBinding: binding is mandatory. An unbound contract
// validates nothing forever while its card claims otherwise — exactly how the
// removed optional `peer_host` failed, silently, on every install that left it
// unset.
func TestUploadRequiresAHostBinding(t *testing.T) {
	r := newRig(t)
	r.start(t)

	for _, host := range []string{"", "   ", "https://", "..."} {
		resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
			"peer_host": host,
			"document":  specV1Doc(t),
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("host %q: status = %d, want 400", host, resp.StatusCode)
		}
		if out["error"] != "invalid_host" {
			t.Errorf("host %q: error = %v, want invalid_host", host, out["error"])
		}
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("an unbound contract was persisted: %+v", infos)
	}
}

// TestUploadNormalisesAPastedURL: pasting the API's URL instead of its host is
// the overwhelmingly likely mistake and the host is right there. Take it rather
// than refusing — but bind to the HOST, since that is what calls carry.
func TestUploadNormalisesAPastedURL(t *testing.T) {
	for _, in := range []string{
		"https://api.acme.test/v1/charges",
		"http://api.acme.test",
		"API.ACME.TEST",
		// A scheme names its own default port, so `:443` under https is the same
		// listener under a longer name — and the spelling the SDK already drops.
		// A BARE `api.acme.test:443` is not in this list: with no scheme nothing
		// says that port is redundant, and it keeps its port
		// (TestUploadKeepsANonDefaultPortAndDropsTheSchemeDefault).
		"https://api.acme.test:443/v1/charges",
		"http://api.acme.test:80",
		"https://user:pw@api.acme.test/v1",
		"  api.acme.test  ",
	} {
		r := newRig(t)
		r.start(t)
		resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
			"peer_host": in,
			"document":  specV1Doc(t),
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: status = %d: %s", in, resp.StatusCode, raw)
		}
		infos, _ := r.st.ListSpecInfos()
		if len(infos) != 1 || infos[0].PeerHost != "api.acme.test" {
			t.Errorf("%q bound to %+v, want api.acme.test", in, infos)
		}
	}
}

// TestPreviewPersistsNothing: the confirm step exists so an operator sees the
// binding before it happens. It must not be the binding.
func TestPreviewPersistsNothing(t *testing.T) {
	r := newRig(t)
	r.start(t)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/preview", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d: %s", resp.StatusCode, raw)
	}
	if out["integration"] != "api-acme-test" || out["peer_host"] != "api.acme.test" {
		t.Errorf("preview = %v, want the binding it would produce", out)
	}
	if n, _ := out["endpoints"].(float64); n == 0 {
		t.Error("preview reports no endpoints")
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("preview persisted a contract: %+v", infos)
	}
}

// TestPreviewCorroboratesTheServersList: `servers:` corroborates a binding and
// never decides it. A mismatch is reported so the confirm step can say so, and
// the upload still goes through — proxy, gateway and staging hosts are
// legitimate and common.
func TestPreviewCorroboratesTheServersList(t *testing.T) {
	r := newRig(t)
	r.start(t)

	_, match, _ := r.do(t, http.MethodPost, "/api/contracts/preview", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	})
	servers, _ := match["servers"].([]any)
	if len(servers) == 0 {
		t.Fatal("the fixture declares no servers: — this test proves nothing")
	}
	if match["servers_match"] != true {
		t.Errorf("servers_match = %v for the host the document declares (%v)", match["servers_match"], servers)
	}

	_, mismatch, _ := r.do(t, http.MethodPost, "/api/contracts/preview", map[string]string{
		"peer_host": "api-gateway.internal.test",
		"document":  specV1Doc(t),
	})
	if mismatch["servers_match"] != false {
		t.Errorf("servers_match = %v for an unrelated host", mismatch["servers_match"])
	}

	// And it does not block.
	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api-gateway.internal.test",
		"document":  specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a servers: mismatch blocked the upload (%d): %s", resp.StatusCode, raw)
	}
}

// TestReplaceKeepsThePreviousAndDiffsIt: replacing a bound contract is the only
// moment two versions of one contract exist, so it is where the version diff
// lives now that `spec_v2_path` is gone. The finding it produces is call-less
// by construction.
func TestReplaceKeepsThePreviousAndDiffsIt(t *testing.T) {
	r := newRig(t)
	r.start(t)

	if resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": specV1Doc(t),
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first upload = %d: %s", resp.StatusCode, raw)
	}
	before, _ := r.st.ListFindings(1000)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": specV2Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace = %d: %s", resp.StatusCode, raw)
	}
	if out["replaced"] != true {
		t.Fatalf("replacing a bound contract reported replaced=%v", out["replaced"])
	}
	if out["replaced_version"] == "" || out["replaced_version"] == nil {
		t.Error("replaced_version empty — the card reads \"replaced v1.0.0\" from it")
	}
	n, _ := out["breaking_changes"].(float64)
	if n == 0 {
		t.Fatal("replacing v1 with v2 produced no breaking findings — the version diff did not run")
	}

	after, _ := r.st.ListFindings(1000)
	if len(after) <= len(before) {
		t.Fatalf("findings did not grow: %d -> %d", len(before), len(after))
	}
	var sawVersionDiff bool
	for _, f := range after {
		if f.Kind == model.KindVersionDiff {
			sawVersionDiff = true
			if f.SourceCallID != nil {
				t.Errorf("version-diff finding has a source call — it is call-less by construction")
			}
		}
	}
	if !sawVersionDiff {
		t.Error("no version-diff finding was stored")
	}

	// One row, the new version live, the old one kept.
	infos, _ := r.st.ListSpecInfos()
	if len(infos) != 1 {
		t.Fatalf("replace created %d rows, want 1", len(infos))
	}
	if infos[0].PrevVersion == "" {
		t.Error("prev_version empty after a replace")
	}
}

// TestUploadWillNotClobberAConfigContract: integration ids are derived from the
// host, so an upload can land on an id a CONFIG contract already holds — in
// practice the org's own self contract. Silently replacing it with a vendor's
// document would be a very bad way to find that out.
func TestUploadWillNotClobberAConfigContract(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "api-acme-test", Role: model.SpecRoleSelf, Format: model.SpecFormatOpenAPI,
		Source: model.SpecSourceConfig, LoadedAt: "t0",
	}, []byte("self-doc"))

	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": specV1Doc(t),
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if out["error"] != "integration_conflict" {
		t.Errorf("error = %v, want integration_conflict", out["error"])
	}

	infos, _ := r.st.ListSpecInfos()
	if len(infos) != 1 || infos[0].Role != model.SpecRoleSelf {
		t.Errorf("the config contract was overwritten: %+v", infos)
	}
}

// TestRemoveUploadedContract: Remove ships WITH upload, because a contract bound
// to the wrong host with no undo is worse than no contract.
func TestRemoveUploadedContract(t *testing.T) {
	r := newRig(t)
	r.start(t)
	if resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": specV1Doc(t),
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d: %s", resp.StatusCode, raw)
	}

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/remove", map[string]string{
		"integration": "api-acme-test",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove = %d: %s", resp.StatusCode, raw)
	}
	if out["removed"] != true {
		t.Errorf("removed = %v, want true", out["removed"])
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Errorf("contract survived removal: %+v", infos)
	}
}

// TestRemoveRefusesAConfigContract: deleting a config-loaded row would just be
// undone at the next start. Saying so beats a button that appears to work.
func TestRemoveRefusesAConfigContract(t *testing.T) {
	r := newRig(t)
	r.start(t)
	_ = r.st.PutSpecInfo(model.SpecInfo{
		Integration: "self", Role: model.SpecRoleSelf, Format: model.SpecFormatOpenAPI,
		Source: model.SpecSourceConfig, LoadedAt: "t0",
	}, []byte("self-doc"))

	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/remove", map[string]string{
		"integration": "self",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if out["error"] != "not_removable" {
		t.Errorf("error = %v, want not_removable", out["error"])
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 1 {
		t.Errorf("the config contract was removed anyway: %+v", infos)
	}
}

// TestContractRoutesAreGuarded: the upload routes mutate local state and accept
// a document, so they answer to the same guard as every other mutating route —
// POST only, the UI header, JSON, and no foreign Origin.
func TestContractRoutesAreGuarded(t *testing.T) {
	r := newRig(t)
	r.start(t)
	body := map[string]string{"peer_host": "api.acme.test", "document": specV1Doc(t)}

	for _, path := range []string{"/api/contracts/preview", "/api/contracts/upload", "/api/contracts/remove"} {
		if resp, _, _ := r.do(t, http.MethodGet, path, nil); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", path, resp.StatusCode)
		}
		if resp, out, _ := r.do(t, http.MethodPost, path, body, func(req *http.Request) {
			req.Header.Del("X-Flanj-UI")
		}); resp.StatusCode != http.StatusForbidden || out["error"] != "ui_header_required" {
			t.Errorf("%s without the UI header = %d %v", path, resp.StatusCode, out["error"])
		}
		if resp, out, _ := r.do(t, http.MethodPost, path, body, func(req *http.Request) {
			req.Header.Set("Content-Type", "text/plain")
		}); resp.StatusCode != http.StatusUnsupportedMediaType || out["error"] != "json_required" {
			t.Errorf("%s without JSON = %d %v", path, resp.StatusCode, out["error"])
		}
		if resp, out, _ := r.do(t, http.MethodPost, path, body, func(req *http.Request) {
			req.Header.Set("Origin", "https://evil.test")
		}); resp.StatusCode != http.StatusForbidden || out["error"] != "forbidden_origin" {
			t.Errorf("%s from a foreign origin = %d %v", path, resp.StatusCode, out["error"])
		}
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Errorf("a refused request still persisted a contract: %+v", infos)
	}
}

// TestUploadCapsDocumentSize: an 8 MiB ceiling, matching what the front<-store
// channel will carry, so a document that uploads is a document that reaches the
// fronts.
func TestUploadCapsDocumentSize(t *testing.T) {
	r := newRig(t)
	r.start(t)

	huge := specV1Doc(t) + "\n#" + strings.Repeat("x", maxDocBytes)
	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test", "document": huge,
	})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if out["error"] != "document_too_large" {
		t.Errorf("error = %v, want document_too_large", out["error"])
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Errorf("an oversized document was persisted: %+v", infos)
	}
}

// TestIntegrationForHostIsStableAndSafe: the id is derived, never asked for, so
// two uploads for one host must always address one row, and no host may produce
// an id that is empty or collides through punctuation alone.
func TestIntegrationForHostIsStableAndSafe(t *testing.T) {
	cases := map[string]string{
		"api.acme.test":      "api-acme-test",
		"api.globex.test":    "api-globex-test",
		"a.b.c.d.example.io": "a-b-c-d-example-io",
		"localhost":          "localhost",
		"10.0.0.7":           "10-0-0-7",
	}
	for host, want := range cases {
		if got := integrationForHost(host); got != want {
			t.Errorf("integrationForHost(%q) = %q, want %q", host, got, want)
		}
		if integrationForHost(host) != integrationForHost(host) {
			t.Errorf("integrationForHost(%q) is not stable", host)
		}
	}
	if got := integrationForHost("api.acme.test"); got == integrationForHost("api.globex.test") {
		t.Error("two different hosts derived the same id")
	}
}

// TestUploadBindsAHostWithAPort is the seam this file exists to hold. CONTRACTS
// §2 defines `flanj.peer.host` as host[:port] and names it THE EDGE KEY; the
// spec cache looks it up by exact string. Truncating at the colon meant a
// host:port edge could not be bound at all — and worse than "not at all": the UI
// locked the uploader to api.acme.test:28080, the confirm step said it was
// binding to that, and the server bound api.acme.test instead. On a stack that
// already has a contract there, that reads as a replace and overwrites a
// DIFFERENT edge's contract with this document.
func TestUploadBindsAHostWithAPort(t *testing.T) {
	const withPort = "api.acme.test:28080"

	r := newRig(t)
	r.start(t)

	// A contract already bound to the bare host — the neighbour that got
	// overwritten. Its presence is what makes the silent rewrite destructive.
	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seeding the bare-host contract = %d: %s", resp.StatusCode, raw)
	}

	// Preview must describe the binding the operator asked for, and must not
	// claim it would displace the bare host's contract.
	_, prev, praw := r.do(t, http.MethodPost, "/api/contracts/preview", map[string]string{
		"peer_host": withPort,
		"document":  specV2Doc(t),
	})
	if prev["peer_host"] != withPort {
		t.Errorf("preview bound to %v, want %s — the confirm step must not promise a binding the upload won't make: %s",
			prev["peer_host"], withPort, praw)
	}
	if prev["integration"] != "api-acme-test-28080" {
		t.Errorf("preview integration = %v, want api-acme-test-28080 derived from host:port", prev["integration"])
	}
	if prev["replaces"] != nil && prev["replaces"] != "" {
		t.Errorf("preview reports replacing %v — a different listener's contract is not this upload's to displace", prev["replaces"])
	}

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": withPort,
		"document":  specV2Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d: %s", resp.StatusCode, raw)
	}
	if out["replaced"] != false {
		t.Errorf("binding %s reported a replace — it overwrote the bare host's contract", withPort)
	}

	infos, _ := r.st.ListSpecInfos()
	if len(infos) != 2 {
		t.Fatalf("stored %d contracts, want 2 — host and host:port are different edges: %+v", len(infos), infos)
	}
	byHost := map[string]model.SpecInfo{}
	for _, si := range infos {
		byHost[si.PeerHost] = si
	}
	bound, ok := byHost[withPort]
	if !ok {
		t.Fatalf("nothing bound to %s; stored %+v", withPort, infos)
	}
	if bound.Integration != "api-acme-test-28080" {
		t.Errorf("integration = %q, want api-acme-test-28080", bound.Integration)
	}
	bare, ok := byHost["api.acme.test"]
	if !ok {
		t.Fatal("the bare host's contract is gone — the port upload overwrote a different edge")
	}
	if bare.Version == bound.Version {
		t.Errorf("both edges carry version %q — the bare host's document was replaced", bare.Version)
	}
}

// TestUploadKeepsANonDefaultPortAndDropsTheSchemeDefault: a non-default port is
// a genuinely different listener and stays; the scheme's own default is the same
// listener under a longer name and goes, because that is what the SDK emits.
func TestUploadKeepsANonDefaultPortAndDropsTheSchemeDefault(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"api.acme.test:28080", "api.acme.test:28080"},
		{"https://api.acme.test:8080/v1", "api.acme.test:8080"},
		{"api.acme.test:80", "api.acme.test:80"}, // no scheme: nothing says :80 is redundant
		{"http://api.acme.test:80", "api.acme.test"},
		{"https://api.acme.test:443", "api.acme.test"},
		// :443 is not HTTP's default — a listener on it is real, not a spelling.
		{"http://api.acme.test:443", "api.acme.test:443"},
		{"HTTPS://API.ACME.TEST:8443/v1", "api.acme.test:8443"},
		{"[::1]:8080", "[::1]:8080"},
		{"https://[::1]:443/v1", "[::1]"},
	} {
		r := newRig(t)
		r.start(t)
		resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
			"peer_host": tc.in,
			"document":  specV1Doc(t),
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: status = %d: %s", tc.in, resp.StatusCode, raw)
		}
		infos, _ := r.st.ListSpecInfos()
		if len(infos) != 1 || infos[0].PeerHost != tc.want {
			t.Errorf("%q bound to %+v, want %s", tc.in, infos, tc.want)
		}
	}
}

// TestUploadRefusesAMalformedPort: the port is half the edge key, so a port that
// is not a port would bind a contract to a string no call can ever carry — the
// silent no-op the host binding is mandatory to prevent.
func TestUploadRefusesAMalformedPort(t *testing.T) {
	r := newRig(t)
	r.start(t)

	for _, host := range []string{
		"api.acme.test:https",
		"api.acme.test:0",
		"api.acme.test:65536",
		"api.acme.test:8080x",
		"api.acme.test:-1",
		"api.acme.test:08080",
	} {
		resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
			"peer_host": host,
			"document":  specV1Doc(t),
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("host %q: status = %d, want 400: %s", host, resp.StatusCode, raw)
		}
		if out["error"] != "invalid_host" {
			t.Errorf("host %q: error = %v, want invalid_host", host, out["error"])
		}
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("a contract bound to an unreachable key was persisted: %+v", infos)
	}
}

/* ── The contract-change announcement ───────────────────────────────────── */

// recordingPublisher stands in for the store extension: in a real collector it
// is `flanjstore` that carries store.SpecPublisher, and the drift processor
// subscribes to it.
type recordingPublisher struct {
	mu sync.Mutex
	n  int
}

func (p *recordingPublisher) NotifySpecsChanged() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (p *recordingPublisher) Start(context.Context, component.Host) error { return nil }
func (p *recordingPublisher) Shutdown(context.Context) error              { return nil }

// extHost is a component.Host carrying a fixed extension set.
type extHost struct{ exts map[component.ID]component.Component }

func (h extHost) GetExtensions() map[component.ID]component.Component { return h.exts }

// withPublisher attaches a store-extension stand-in to the rig's host, the way
// the collector's own extension set does.
func (r *testRig) withPublisher(t *testing.T) *recordingPublisher {
	t.Helper()
	pub := &recordingPublisher{}
	r.ext.host = extHost{exts: map[component.ID]component.Component{
		component.MustNewID("flanjstore"): pub,
	}}
	return pub
}

// TestUploadAnnouncesTheContractChange: the drift processor caches parsed
// contracts and refreshes on a ticker, so a write to the store is invisible to
// detection until it hears about it. A REPLACE is the case that needs the
// announcement: the host is already covered, so the processor's own first-sight
// kick never fires, and every call until the next tick would be scored against
// the document this upload just superseded — while the UI says "Validating from
// now on" and the card shows the new version as live.
func TestUploadAnnouncesTheContractChange(t *testing.T) {
	r := newRig(t)
	pub := r.withPublisher(t)
	r.start(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first upload = %d: %s", resp.StatusCode, raw)
	}
	if pub.count() != 1 {
		t.Fatalf("announcements after a first upload = %d, want 1", pub.count())
	}

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV2Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace = %d: %s", resp.StatusCode, raw)
	}
	if out["replaced"] != true {
		t.Fatalf("second upload did not report a replace: %v", out["replaced"])
	}
	if pub.count() != 2 {
		t.Fatalf("announcements after a REPLACE = %d, want 2 — a replaced contract "+
			"is a cache HIT, so nothing else tells detection the document moved", pub.count())
	}
}

// TestRefusedUploadAnnouncesNothing: an upload that never reached the store
// changed no contract set, and announcing one would cost every subscriber a
// refresh for nothing.
func TestRefusedUploadAnnouncesNothing(t *testing.T) {
	r := newRig(t)
	pub := r.withPublisher(t)
	r.start(t)

	resp, _, _ := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  "this is not an OpenAPI document",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unparseable upload = %d, want 400", resp.StatusCode)
	}
	if pub.count() != 0 {
		t.Errorf("announcements after a refused upload = %d, want 0", pub.count())
	}
}

// TestRemoveAnnouncesTheContractChange: a removal is a cache HIT on the deleted
// document, so without the announcement the contract keeps validating traffic
// after the operator removed it — the undo that does not undo. Removing a
// contract that was not there changed nothing and must stay quiet.
func TestRemoveAnnouncesTheContractChange(t *testing.T) {
	r := newRig(t)
	pub := r.withPublisher(t)
	r.start(t)

	if resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d: %s", resp.StatusCode, raw)
	}
	before := pub.count()

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/remove", map[string]string{
		"integration": "api-acme-test",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove = %d: %s", resp.StatusCode, raw)
	}
	if out["removed"] != true {
		t.Fatalf("remove did not report a deletion: %v", out["removed"])
	}
	if pub.count() != before+1 {
		t.Fatalf("announcements after a remove = %d, want %d", pub.count(), before+1)
	}

	after := pub.count()
	if resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/remove", map[string]string{
		"integration": "api-acme-test",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("second remove = %d: %s", resp.StatusCode, raw)
	}
	if pub.count() != after {
		t.Errorf("removing a contract that was not there announced a change (%d -> %d)", after, pub.count())
	}
}

// TestAnnouncingWithNoStoreExtensionIsANoOp: the UI must not depend on a
// subscriber existing. A tiered store pod runs no drift processor at all, and
// the handler tests run with no host — neither may turn an upload into a 500.
func TestAnnouncingWithNoStoreExtensionIsANoOp(t *testing.T) {
	r := newRig(t) // no host, no publisher
	r.start(t)

	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV1Doc(t),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload with no store extension = %d: %s", resp.StatusCode, raw)
	}
}
