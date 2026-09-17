package flanjui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// specServer stands in for a provider publishing (or not publishing) a spec.
// Handlers are keyed by path so one server can be a whole host's conventional
// paths at once, which is what the probe tests need.
type specServer struct {
	srv   *httptest.Server
	hits  []string
	paths map[string]http.HandlerFunc
}

func newSpecServer(t *testing.T) *specServer {
	t.Helper()
	s := &specServer{paths: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits = append(s.hits, r.URL.Path)
		if h, ok := s.paths[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *specServer) serve(path, body string) {
	s.paths[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func (s *specServer) url(path string) string { return s.srv.URL + path }

// host returns the 127.0.0.1:PORT the test server listens on — which is what
// the SDK would report as `peer.host` for a call to it, so it is also what the
// contract binds to.
func (s *specServer) host() string {
	return strings.TrimPrefix(s.srv.URL, "http://")
}

// fetchPreview runs step one and returns the staging token.
func fetchPreview(t *testing.T, r *testRig, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", body)
	return resp, out
}

// TestFetchedContractCarriesItsSourceURL is the point of the whole phase: the
// row records WHERE the document came from and WHEN it was read, because
// "your own published spec at <url>, fetched <when>" is a claim the provider
// reading a flagged thread can check, and "somebody here had a file" is not.
func TestFetchedContractCarriesItsSourceURL(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	resp, out := fetchPreview(t, r, map[string]any{
		"url":       spec.url("/openapi.json"),
		"peer_host": "api.acme.test",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch preview = %d: %v", resp.StatusCode, out)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatal("no staging token: nothing can be confirmed")
	}
	if out["source_url"] != spec.url("/openapi.json") {
		t.Errorf("source_url = %v, want the URL that was read", out["source_url"])
	}

	// NOTHING is written by the preview. A preview that persisted would make
	// "a human binds it" a description of the UI rather than of the system.
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("the preview persisted %d contracts; it must persist none", len(infos))
	}

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind = %d: %s", resp.StatusCode, raw)
	}

	infos, _ := r.st.ListSpecInfos()
	if len(infos) != 1 {
		t.Fatalf("stored %d contracts, want 1", len(infos))
	}
	si := infos[0]
	if si.Source != model.SpecSourceFetched {
		t.Errorf("source = %q, want %q — the provenance word is what the card, the finding and the thread all read",
			si.Source, model.SpecSourceFetched)
	}
	if si.SourceURL != spec.url("/openapi.json") {
		t.Errorf("source_url = %q, want %q", si.SourceURL, spec.url("/openapi.json"))
	}
	if si.PeerHost != "api.acme.test" {
		t.Errorf("peer_host = %q, want the host the operator named", si.PeerHost)
	}
	// LoadedAt is the FETCH time, not the confirm time — the sentence says
	// "fetched <when>" and it has to mean when the bytes were read.
	if si.LoadedAt == "" {
		t.Error("loaded_at is empty: the evidence sentence has no time to name")
	}
	if out["replaced"] != false {
		t.Errorf("a first fetch reported a replace: %v", out["replaced"])
	}
}

// TestFetchPreviewsBeforeItBinds is the suggest-and-approve guarantee, stated
// as a test: the ONLY thing that turns a fetched document into a contract is a
// second, explicit call carrying a token the server minted. There is no
// one-shot bind, and a client cannot supply the document.
func TestFetchPreviewsBeforeItBinds(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	// A request naming a URL never binds, however it is shaped.
	for _, body := range []map[string]any{
		{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"},
		{"url": spec.url("/openapi.json")},
	} {
		if resp, out := fetchPreview(t, r, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("preview = %d: %v", resp.StatusCode, out)
		}
		if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
			t.Fatalf("a URL request bound a contract without a confirm: %v", infos)
		}
	}

	// A made-up token binds nothing — the bytes live server-side against a
	// token the server minted, so a caller cannot name one into existence.
	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": "not-a-real-token"})
	if resp.StatusCode != http.StatusConflict || out["error"] != "fetch_expired" {
		t.Errorf("unknown token = %d %v, want 409 fetch_expired", resp.StatusCode, out)
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("an unknown token bound a contract: %v", infos)
	}
}

// TestAStagedFetchBindsExactlyOnce: the token is consumed. A double-submitted
// confirm — a second click, a retry — must not write the row twice or resurrect
// a document the operator has moved on from.
func TestAStagedFetchBindsExactlyOnce(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"})
	token := out["token"].(string)

	if resp, _, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": token}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first bind = %d", resp.StatusCode)
	}
	resp, out2, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": token})
	if resp.StatusCode != http.StatusConflict || out2["error"] != "fetch_expired" {
		t.Errorf("second bind = %d %v, want 409 fetch_expired — a token is spent when it is used", resp.StatusCode, out2)
	}
}

// TestFetchBindsTheBytesItRead: the document that lands in the store is the one
// the collector read from the URL, not anything the client sent. This is what
// makes `source_url` EVIDENCE rather than a label — a URL attached to bytes the
// client supplied would be checkable by nobody.
func TestFetchBindsTheBytesItRead(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"})
	token := out["token"].(string)

	// The confirm carries a document too. It must be ignored: the route reads
	// the staged bytes and nothing else.
	if resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{
		"token":    token,
		"document": "tampered: not a spec",
		"url":      "https://somewhere.else.test/openapi.json",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("bind = %d: %s", resp.StatusCode, raw)
	}
	infos, _ := r.st.ListSpecInfos()
	if infos[0].SourceURL != spec.url("/openapi.json") {
		t.Errorf("source_url = %q — a client field overwrote the URL the collector actually read", infos[0].SourceURL)
	}
	doc, _, _, _ := r.st.GetSpecDoc(infos[0].Integration)
	if string(doc) != specV1Doc(t) {
		t.Error("the stored document is not the one the collector fetched")
	}
}

// TestFetchRefusals covers every way a fetch can fail. Each one must be a
// STATED state with its own code — never a silent empty bind, and never a
// shrug. A 404 and a document over the cap send the operator to two different
// places, and "couldn't fetch" sends them to neither.
func TestFetchRefusals(t *testing.T) {
	spec := newSpecServer(t)
	spec.serve("/ok.json", specV1Doc(t))
	spec.paths["/notfound.json"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }
	spec.paths["/unauthorized.json"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }
	spec.paths["/empty.json"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	spec.paths["/html.json"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Not Found</body></html>"))
	}
	// One byte past the cap, served with a 200 — the case that would otherwise
	// be truncated into a document that parses as garbage.
	spec.paths["/huge.json"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", model.MaxContractDocBytes+1)))
	}

	for _, tc := range []struct {
		name string
		url  string
		code string
		want int
	}{
		{"no url at all", "", "invalid_url", http.StatusBadRequest},
		{"not a url", "::::", "invalid_url", http.StatusBadRequest},
		{"an unsupported scheme", "file:///etc/passwd", "invalid_url", http.StatusBadRequest},
		{"credentials in the url", "https://user:pw@api.acme.test/openapi.json", "invalid_url", http.StatusBadRequest},
		{"the cloud metadata service", "http://169.254.169.254/latest/meta-data/", "blocked_target", http.StatusForbidden},
		{"a 404", spec.url("/notfound.json"), "fetch_status", http.StatusBadGateway},
		{"a spec that is not published", spec.url("/unauthorized.json"), "fetch_status", http.StatusBadGateway},
		{"an empty 200", spec.url("/empty.json"), "empty_document", http.StatusBadGateway},
		{"an HTML error page served as 200", spec.url("/html.json"), "unparseable_document", http.StatusBadRequest},
		{"a document over the cap", spec.url("/huge.json"), "document_too_large", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.start(t)
			resp, out := fetchPreview(t, r, map[string]any{"url": tc.url, "peer_host": "api.acme.test"})
			if resp.StatusCode != tc.want || out["error"] != tc.code {
				t.Errorf("= %d %v, want %d %s", resp.StatusCode, out["error"], tc.want, tc.code)
			}
			// The sentence, always. A stated state that states nothing is not one.
			if msg, _ := out["message"].(string); strings.TrimSpace(msg) == "" {
				t.Error("the refusal carries no sentence — the operator is told a code and nothing to do")
			}
			// NOTHING is bound, on any refusal. This is the whole rule.
			if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
				t.Errorf("a failed fetch bound %d contracts", len(infos))
			}
			if _, out := fetchPreview(t, r, map[string]any{"url": tc.url}); out["token"] != nil {
				t.Error("a failed fetch staged a document")
			}
		})
	}
}

// TestFetchStatusRefusalNamesTheStatus: 404 and 401 mean different things to an
// operator — a wrong address versus a provider who does not publish — and the
// sentence has to let them tell which they hit.
func TestFetchStatusRefusalNamesTheStatus(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.paths["/x.json"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/x.json"), "peer_host": "api.acme.test"})
	msg, _ := out["message"].(string)
	if !strings.Contains(msg, "403") {
		t.Errorf("message = %q, want the status in it", msg)
	}
}

// TestFetchRefusesAMetadataRedirect: checking only the typed URL is how a
// target check gets defeated. Every hop is re-checked.
func TestFetchRefusesAMetadataRedirect(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.paths["/redirect"] = func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}

	resp, out := fetchPreview(t, r, map[string]any{"url": spec.url("/redirect"), "peer_host": "api.acme.test"})
	if resp.StatusCode != http.StatusForbidden || out["error"] != "blocked_target" {
		t.Errorf("= %d %v, want 403 blocked_target", resp.StatusCode, out)
	}
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Error("a redirected fetch bound a contract")
	}
}

// TestFetchRecordsWhereItLanded: a redirect that moves the document must be
// recorded as the final URL, because that is the address the evidence sentence
// will claim — and an operator approving a claim has to approve the true one.
func TestFetchRecordsWhereItLanded(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/v2/openapi.json", specV1Doc(t))
	spec.paths["/openapi.json"] = func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/v2/openapi.json", http.StatusMovedPermanently)
	}

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"})
	if out["source_url"] != spec.url("/v2/openapi.json") {
		t.Errorf("source_url = %v, want where the document actually came from", out["source_url"])
	}
	if out["requested_url"] != spec.url("/openapi.json") {
		t.Errorf("requested_url = %v — the operator must see that a redirect moved it", out["requested_url"])
	}
}

// TestProbeNeverAutoBinds is the guardrail this route exists inside. A probe
// offers; a human binds. Nothing below may ever write to the store.
func TestProbeNeverAutoBinds(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))
	// The probe only touches a host with traffic, so give it one.
	host := spec.host()
	seedEdge(t, r, host)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/probe", map[string]any{"peer_host": host})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe = %d: %s", resp.StatusCode, raw)
	}
	candidates, _ := out["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("found %d candidates, want the one document that is there: %s", len(candidates), raw)
	}

	// THE assertion. A hit, parsed, described — and not bound.
	if infos, _ := r.st.ListSpecInfos(); len(infos) != 0 {
		t.Fatalf("the probe bound %d contracts. It must bind none, ever: a wrong contract renders DRIFTED to a stranger on their real provider", len(infos))
	}

	// What it found is enough to decide on, and taking the offer goes back
	// through the same fetch confirm a typed URL does.
	c := candidates[0].(map[string]any)
	if c["url"] != spec.url("/openapi.json") {
		t.Errorf("candidate url = %v", c["url"])
	}
	if c["endpoints"] == nil || c["endpoints"].(float64) == 0 {
		t.Error("the offer does not say what is in the document")
	}
	if _, ok := c["servers_match"]; !ok {
		t.Error("the offer omits the servers corroboration — the one signal that catches a wrong document")
	}
}

// TestProbeReportsAMissAsAResult: most providers publish at none of these
// paths. An empty answer with no account of what was asked is indistinguishable
// from a broken control.
func TestProbeReportsAMissAsAResult(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t) // serves nothing
	host := spec.host()
	seedEdge(t, r, host)

	resp, out, raw := r.do(t, http.MethodPost, "/api/contracts/probe", map[string]any{"peer_host": host})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe = %d: %s", resp.StatusCode, raw)
	}
	if c, _ := out["candidates"].([]any); len(c) != 0 {
		t.Errorf("candidates = %v, want none", c)
	}
	tried, _ := out["tried"].([]any)
	if len(tried) != len(conventionalSpecPaths) {
		t.Errorf("tried %d paths, want all %d named — 'we looked' with no list is not checkable",
			len(tried), len(conventionalSpecPaths))
	}
	// And it really did ask, exactly those paths and no others.
	if len(spec.hits) != len(conventionalSpecPaths) {
		t.Errorf("made %d requests for %d paths: %v", len(spec.hits), len(conventionalSpecPaths), spec.hits)
	}
}

// TestProbeOnlyTouchesAHostWeAlreadyCall is what keeps the probe from being a
// URL fetcher under another name. It can reach nothing this deployment is not
// already talking to — so it opens no destination, and a typo'd domain never
// receives four requests from a stranger.
func TestProbeOnlyTouchesAHostWeAlreadyCall(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))
	// Deliberately NO edge seeded for this host.

	resp, out, _ := r.do(t, http.MethodPost, "/api/contracts/probe", map[string]any{"peer_host": spec.host()})
	if resp.StatusCode != http.StatusNotFound || out["error"] != "unknown_host" {
		t.Errorf("= %d %v, want 404 unknown_host", resp.StatusCode, out)
	}
	if len(spec.hits) != 0 {
		t.Errorf("the probe made %d requests to a host with no traffic: %v", len(spec.hits), spec.hits)
	}
}

// TestFetchAndProbeAreGuardedLikeUpload: the same browser-facing rules, because
// a route that fetches a user-typed URL from inside the customer's network is
// exactly the one a foreign page must not be able to drive.
func TestFetchAndProbeAreGuardedLikeUpload(t *testing.T) {
	r := newRig(t)
	r.start(t)
	for _, path := range []string{"/api/contracts/fetch", "/api/contracts/probe"} {
		t.Run(path, func(t *testing.T) {
			resp, _, _ := r.do(t, http.MethodGet, path, nil)
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("GET = %d, want 405", resp.StatusCode)
			}
			resp, out, _ := r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Del("X-Flanj-UI") })
			if resp.StatusCode != http.StatusForbidden || out["error"] != "ui_header_required" {
				t.Errorf("no UI header = %d %v", resp.StatusCode, out)
			}
			resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Set("Origin", "https://evil.example") })
			if resp.StatusCode != http.StatusForbidden || out["error"] != "forbidden_origin" {
				t.Errorf("foreign origin = %d %v", resp.StatusCode, out)
			}
			resp, out, _ = r.do(t, http.MethodPost, path, map[string]string{}, func(q *http.Request) { q.Header.Set("Content-Type", "text/plain") })
			if resp.StatusCode != http.StatusUnsupportedMediaType || out["error"] != "json_required" {
				t.Errorf("text/plain = %d %v", resp.StatusCode, out)
			}
		})
	}
}

// TestAFetchedContractIsRemovableAndReplaceable: a human bound it here, so a
// human can unbind it here. Missing this answered the CONFIG sentence —
// "remove it there" — for a contract that has no file anywhere.
func TestAFetchedContractIsRemovableAndReplaceable(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"})
	if resp, _, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": out["token"]}); resp.StatusCode != http.StatusOK {
		t.Fatal("bind failed")
	}

	// An upload may replace it — the two operator-bound sources are
	// interchangeable, and a refusal here would strand the contract.
	resp, _, raw := r.do(t, http.MethodPost, "/api/contracts/upload", map[string]string{
		"peer_host": "api.acme.test",
		"document":  specV2Doc(t),
		"filename":  "acme.yaml",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload over a fetched contract = %d: %s", resp.StatusCode, raw)
	}

	// Re-fetch, then remove.
	_, out = fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json"), "peer_host": "api.acme.test"})
	if resp, _, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": out["token"]}); resp.StatusCode != http.StatusOK {
		t.Fatal("re-bind failed")
	}
	resp, out2, raw := r.do(t, http.MethodPost, "/api/contracts/remove", map[string]string{"integration": "api-acme-test"})
	if resp.StatusCode != http.StatusOK || out2["removed"] != true {
		t.Fatalf("remove a fetched contract = %d %s", resp.StatusCode, raw)
	}
}

// TestFetchTargetError pins the narrowness of the target guard directly. It
// refuses the cloud metadata pivot and NOTHING ELSE that a self-hosted install
// legitimately needs: an internal provider's spec lives on an internal host,
// and refusing RFC1918 would refuse the case this product is built for.
func TestFetchTargetError(t *testing.T) {
	for _, blocked := range []string{
		"169.254.169.254", "169.254.169.254:80", "[fe80::1]", "fd00:ec2::254",
		"metadata.google.internal", "metadata",
	} {
		if fetchTargetError(blocked) == nil {
			t.Errorf("%s was allowed; it is a metadata endpoint, never a published spec", blocked)
		}
	}
	for _, allowed := range []string{
		"api.acme.test", "api.acme.test:8443",
		"10.0.0.5", "192.168.1.10:8080", "172.16.4.4", // internal providers are first-class here
		"127.0.0.1:9000", "localhost:3000", // a developer's own machine
	} {
		if err := fetchTargetError(allowed); err != nil {
			t.Errorf("%s was refused (%v); refusing internal hosts refuses the self-hosted case", allowed, err)
		}
	}
}

// TestFetchTakesTheHostFromTheURLWhenNoneIsGiven — the probe's candidates and a
// hurried paste both arrive this way, and an unbound contract validates nothing
// forever while its card claims otherwise.
func TestFetchTakesTheHostFromTheURLWhenNoneIsGiven(t *testing.T) {
	r := newRig(t)
	r.start(t)
	spec := newSpecServer(t)
	spec.serve("/openapi.json", specV1Doc(t))

	_, out := fetchPreview(t, r, map[string]any{"url": spec.url("/openapi.json")})
	preview, _ := out["preview"].(map[string]any)
	if preview["peer_host"] != spec.host() {
		t.Errorf("peer_host = %v, want the URL's own host %q", preview["peer_host"], spec.host())
	}
	if resp, _, _ := r.do(t, http.MethodPost, "/api/contracts/fetch", map[string]any{"token": out["token"]}); resp.StatusCode != http.StatusOK {
		t.Fatal("bind failed")
	}
	infos, _ := r.st.ListSpecInfos()
	if infos[0].PeerHost != spec.host() {
		t.Errorf("bound to %q, want %q", infos[0].PeerHost, spec.host())
	}
}

// seedEdge records a discovered edge for a host — the probe's precondition, and
// the same shape seedGraph uses.
func seedEdge(t *testing.T, r *testRig, host string) {
	t.Helper()
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	r.st.edges = append(r.st.edges, model.Edge{
		PeerHost: host, Direction: "client", Role: "consumer", Class: model.EdgeClassExternal,
		FirstSeen: "2026-09-17T09:00:00Z", LastSeen: "2026-09-17T10:00:00Z", CallCount: 7,
	})
}
