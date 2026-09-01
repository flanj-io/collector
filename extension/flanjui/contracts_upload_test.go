package flanjui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		"api.acme.test:443",
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
