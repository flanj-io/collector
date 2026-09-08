package flanjdrift

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// fakeStorePod mimics the store extension's contract endpoint
// (extension/flanjstore/specserver.go) so the front's half of the channel is
// testable on its own. The two ends are pinned to the same shape by the paths
// and the payload key asserted here and there.
func fakeStorePod(t *testing.T, token string, docs map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool {
		return token == "" || r.Header.Get("Authorization") == "Bearer "+token
	}
	mux.HandleFunc("GET /internal/contracts", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		infos := make([]model.SpecInfo, 0, len(docs))
		for integration, doc := range docs {
			infos = append(infos, model.SpecInfo{
				Integration: integration,
				Role:        model.SpecRoleProvider,
				Format:      model.SpecFormatOpenAPI,
				PeerHost:    "api." + integration + ".test",
				// The channel's change token, and the real store pod moves it
				// whenever the document does. Deriving it from the content
				// here gives a test that serves a DIFFERENT document the
				// re-download the real stamp would have produced — without it,
				// a cache holding a contract skips the fetch entirely and the
				// test proves nothing.
				LoadedAt: fmt.Sprintf("%x", sha256.Sum256(doc)),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"contracts": infos})
	})
	mux.HandleFunc("GET /internal/contracts/doc", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		doc, ok := docs[r.URL.Query().Get("integration")]
		if !ok {
			http.Error(w, "no such contract", http.StatusNotFound)
			return
		}
		_, _ = w.Write(doc)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRemoteSourceFillsTheCache closes the tiered loop: a contract uploaded on
// the store pod reaches a front that has no store of its own, and the front can
// then validate that host's traffic.
func TestRemoteSourceFillsTheCache(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})

	c := newSpecCache()
	if _, errs := c.refresh(newRemoteSpecSource(srv.URL, "")); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Fatal("a contract uploaded on the store pod did not reach the front")
	}
}

// TestRemoteSourcePresentsItsToken: the endpoint is on the cluster interface,
// so the front must authenticate or get nothing.
func TestRemoteSourcePresentsItsToken(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "s3cret", map[string][]byte{"acme": doc})

	if _, errs := newSpecCache().refresh(newRemoteSpecSource(srv.URL, "")); len(errs) == 0 {
		t.Error("an unauthenticated front was served contracts")
	}

	c := newSpecCache()
	if _, errs := c.refresh(newRemoteSpecSource(srv.URL, "s3cret")); len(errs) != 0 {
		t.Fatalf("authenticated refresh failed: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("authenticated front got no contract")
	}
}

// TestRemoteSourceHidesTheStorePodResponse: an error must carry the status and
// nothing else. The request that produced it carried the shared token, and a
// misconfigured endpoint could be any server at all — neither belongs in a log
// line.
func TestRemoteSourceHidesTheStorePodResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "s3cret leaked in a body", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := newRemoteSpecSource(srv.URL, "s3cret").listSpecs()
	if err == nil {
		t.Fatal("a 500 was not an error")
	}
	if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "leaked") {
		t.Errorf("error echoes the peer's body: %v", err)
	}
}

// TestRemoteSourceMissingDocIsNotAnError: a contract removed between a front's
// list and its fetch is gone, not broken. Treating it as an error would retry
// it forever.
func TestRemoteSourceMissingDocIsNotAnError(t *testing.T) {
	srv := fakeStorePod(t, "", map[string][]byte{})
	raw, err := newRemoteSpecSource(srv.URL, "").specDoc("vanished")
	if err != nil {
		t.Errorf("404 became an error: %v", err)
	}
	if raw != nil {
		t.Errorf("raw = %q, want nil", raw)
	}
}

// TestRemoteSourceNormalisesTheEndpoint: an operator writing a bare host:port
// (the shape they just wrote for `otlphttp`) or a trailing slash gets a working
// front, not a silent one.
func TestRemoteSourceNormalisesTheEndpoint(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})
	bare := strings.TrimPrefix(srv.URL, "http://")

	for _, endpoint := range []string{srv.URL, srv.URL + "/", bare, bare + "/"} {
		c := newSpecCache()
		if _, errs := c.refresh(newRemoteSpecSource(endpoint, "")); len(errs) != 0 {
			t.Errorf("endpoint %q: %v", endpoint, errs)
			continue
		}
		if _, ok := c.lookup("api.acme.test"); !ok {
			t.Errorf("endpoint %q resolved no contract", endpoint)
		}
	}
}

// TestRemoteSourceUnreachableKeepsServing: a store pod restart must not blind
// every front in the deployment. The refresh fails and the cache keeps what it
// has.
func TestRemoteSourceUnreachableKeepsServing(t *testing.T) {
	doc := specV1(t)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": doc})

	src := newRemoteSpecSource(srv.URL, "")
	c := newSpecCache()
	if _, errs := c.refresh(src); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}

	srv.Close() // the store pod goes away mid-deployment
	if _, errs := c.refresh(src); len(errs) == 0 {
		t.Error("an unreachable store pod refreshed clean")
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("a store pod restart dropped a working contract — detection must ride it out")
	}
}

// TestRemoteSourceRefusesADocumentPastTheCap: a body over the cap is an ERROR
// naming the size, never the first maxSpecBytes bytes of it.
//
// io.LimitReader truncates silently, so this used to return a fragment with a
// nil error and the fragment went straight to the parser. What the front then
// logged was a PARSE failure — for a document that parses perfectly well when
// it is all there — and detection on that edge stopped for as long as the
// document stayed big, with nothing anywhere naming the size. Same
// wrong-diagnosis class as the upload path (#40) and the localhost API's
// request bodies (#48); this was the last one, on the outbound side.
func TestRemoteSourceRefusesADocumentPastTheCap(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxSpecBytes+1)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": oversized})

	raw, err := newRemoteSpecSource(srv.URL, "").specDoc("acme")
	if err == nil {
		t.Fatalf("an oversized document read clean as %d bytes — the front parses that fragment "+
			"and reports a PARSE error for a SIZE problem", len(raw))
	}
	if raw != nil {
		t.Errorf("raw = %d bytes, want none: a truncated document must never reach a parser", len(raw))
	}
	// The refusal has to say what an operator can act on: which contract, and
	// that the fault is its size.
	if !strings.Contains(err.Error(), "acme") {
		t.Errorf("the error does not name the contract: %v", err)
	}
	if !strings.Contains(err.Error(), "larger than") || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("the error does not name the cap: %v", err)
	}
}

// TestRemoteSourceAcceptsADocumentAtTheCap: the cap is inclusive. The check
// above reads one byte past it precisely so a document of exactly maxSpecBytes
// still crosses whole — an off-by-one here would cost a legitimate contract its
// detection, which is the failure this whole change exists to stop.
func TestRemoteSourceAcceptsADocumentAtTheCap(t *testing.T) {
	atCap := bytes.Repeat([]byte("x"), maxSpecBytes)
	srv := fakeStorePod(t, "", map[string][]byte{"acme": atCap})

	raw, err := newRemoteSpecSource(srv.URL, "").specDoc("acme")
	if err != nil {
		t.Fatalf("a document exactly at the cap was refused: %v", err)
	}
	if len(raw) != maxSpecBytes {
		t.Errorf("read %d bytes, want the whole %d-byte document", len(raw), maxSpecBytes)
	}
}

// TestRemoteSourceOversizedKeepsThePreviousDocument pins the RULING, which is
// the one reconcile already applies to every per-document failure: report it,
// skip it, and keep validating against whatever is already cached for that
// edge. A contract that outgrows the cap must cost the front the UPDATE, never
// the detection it already had — the same reasoning as
// TestRemoteSourceUnreachableKeepsServing.
//
// Two pods rather than one mutated map: the front re-downloads only when the
// row's loaded_at moves, so the second pod's document has to arrive under its
// own change token, exactly as a replaced document would.
func TestRemoteSourceOversizedKeepsThePreviousDocument(t *testing.T) {
	small := fakeStorePod(t, "", map[string][]byte{"acme": specV1(t)})
	c := newSpecCache()
	if _, errs := c.refresh(newRemoteSpecSource(small.URL, "")); len(errs) != 0 {
		t.Fatalf("refresh: %v", errs)
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Fatal("the first refresh cached nothing")
	}

	// The provider's contract grows past the cap.
	big := fakeStorePod(t, "", map[string][]byte{"acme": bytes.Repeat([]byte("x"), maxSpecBytes+1)})
	_, errs := c.refresh(newRemoteSpecSource(big.URL, ""))
	if len(errs) == 0 {
		t.Fatal("an oversized document refreshed clean")
	}
	// And it is reported as a SIZE problem. Truncating produced an error here
	// too — from the PARSER, over a fragment — which is how a store pod serving
	// a document the channel cannot carry read as a malformed contract.
	if !strings.Contains(errs[0].Error(), "larger than") {
		t.Errorf("the refresh blames something other than the size: %v", errs[0])
	}
	if _, ok := c.lookup("api.acme.test"); !ok {
		t.Error("an oversized document dropped the contract the front was already validating against")
	}
}

// TestRemoteSourceRefusesAnOversizedList: the same reader serves the metadata
// route, and a truncated JSON list fails as "unexpected end of JSON input" —
// a parse verdict for a size problem, on the request a front makes every ten
// seconds. One reader, one ruling.
func TestRemoteSourceRefusesAnOversizedList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxSpecBytes+1))
	}))
	t.Cleanup(srv.Close)

	_, err := newRemoteSpecSource(srv.URL, "").listSpecs()
	if err == nil {
		t.Fatal("an oversized contract list read clean")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("the error does not name the size: %v", err)
	}
}

// TestRemoteSourceReadsTheStorePodsOwnRefusal is the pairing that actually
// ships: a current front against a current store pod. The pod refuses the
// oversized document itself (413, extension/flanjstore), so no truncated body
// is ever transmitted — and the front must still log the SIZE, not a bare
// status the operator has to go decode against the other pod's logs.
func TestRemoteSourceReadsTheStorePodsOwnRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "contract document larger than the cap", http.StatusRequestEntityTooLarge)
	}))
	t.Cleanup(srv.Close)

	raw, err := newRemoteSpecSource(srv.URL, "").specDoc("acme-tools")
	if err == nil {
		t.Fatal("a 413 from the store pod was not an error")
	}
	if raw != nil {
		t.Errorf("raw = %d bytes, want none", len(raw))
	}
	if !strings.Contains(err.Error(), "larger than") || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("the error does not name the cap: %v", err)
	}
	if !strings.Contains(err.Error(), "acme-tools") {
		t.Errorf("the error does not name the contract: %v", err)
	}
}
