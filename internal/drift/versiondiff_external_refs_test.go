package drift

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// A bound contract is ONE document. It is never allowed to pull in another —
// not over the network, not from the collector's own disk. The version diff
// reads both versions of a contract a provider (or whoever answered a fetched
// URL) wrote, so a `$ref` it followed would be a request to an address the
// document chose, outside the contract-fetch destination policy, or a read of
// a local file whose content could then surface in a finding.
//
// Every test below hands the diff a document that REFERENCES something real —
// a listening server, a file that exists — so "refused" is distinguishable
// from "could not be resolved anyway".

// refCanary is content that exists only in the referenced resource. It is a
// `format`, because a changed format is an ARGUMENT of oasdiff's
// response-property-type-changed message: if the reference is followed, the
// canary lands verbatim in a finding's Detail and FieldPath.
const refCanary = "flanj-canary-3b9f1c"

const refCanarySchema = `{"type":"integer","format":"` + refCanary + `"}`

// refV1 is self-contained: `status` is a plain string.
const refV1 = `
openapi: 3.0.3
info: { title: Acme, version: 1.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  status: { type: string }
`

// refV2 is refV1 with `status` replaced by a reference to somewhere else.
func refV2(ref string) []byte {
	return []byte(`
openapi: 3.0.3
info: { title: Acme, version: 2.0.0 }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  status: { $ref: "` + ref + `" }
`)
}

// countingServer answers every request with the canary schema and counts them.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(refCanarySchema))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// canaryFile writes the canary schema to a file that exists, and returns its
// absolute path.
func canaryFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(p, []byte(refCanarySchema), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// assertRefused: the diff errored, and nothing the reference pointed at is in
// anything it returned.
func assertRefused(t *testing.T, findings []model.Finding, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("the diff followed the reference: no error, %d findings", len(findings))
	} else if strings.Contains(err.Error(), refCanary) {
		t.Errorf("the referenced content is in the error: %v", err)
	}
	b, mErr := json.Marshal(findings)
	if mErr != nil {
		t.Fatal(mErr)
	}
	if strings.Contains(string(b), refCanary) {
		t.Errorf("the referenced content is in a finding: %s", b)
	}
}

// TestVersionDiffMakesNoRequestForAnHTTPRef: zero requests, not "one request
// whose answer was discarded" — the request itself is the harm.
func TestVersionDiffMakesNoRequestForAnHTTPRef(t *testing.T) {
	for _, side := range []string{"current", "previous"} {
		t.Run(side, func(t *testing.T) {
			srv, hits := countingServer(t)
			v1, v2 := []byte(refV1), refV2(srv.URL+"/s.json")
			if side == "previous" {
				v1, v2 = v2, v1
			}
			findings, err := DetectVersionDiffData(v1, v2, "acme")
			if n := hits.Load(); n != 0 {
				t.Errorf("the diff made %d request(s) to a host named by the document, want 0", n)
			}
			assertRefused(t, findings, err)
		})
	}
}

// TestVersionDiffReadsNoLocalFileForARef: every spelling of "a file on this
// machine" — a file:// URI, an absolute path, and a path relative to wherever
// the document happens to sit.
func TestVersionDiffReadsNoLocalFileForARef(t *testing.T) {
	secret := canaryFile(t)

	t.Run("file uri", func(t *testing.T) {
		findings, err := DetectVersionDiffData([]byte(refV1), refV2("file://"+filepath.ToSlash(secret)), "acme")
		assertRefused(t, findings, err)
	})

	t.Run("absolute path", func(t *testing.T) {
		findings, err := DetectVersionDiffData([]byte(refV1), refV2(filepath.ToSlash(secret)), "acme")
		assertRefused(t, findings, err)
	})

	// The path form, so the test controls the directory: secret.json sits right
	// beside the documents, which is the friendliest case a relative reference
	// can have.
	t.Run("relative path", func(t *testing.T) {
		dir := filepath.Dir(secret)
		p1, p2 := filepath.Join(dir, "v1.yaml"), filepath.Join(dir, "v2.yaml")
		if err := os.WriteFile(p1, []byte(refV1), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p2, refV2("secret.json"), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, err := DetectVersionDiff(p1, p2, "acme")
		assertRefused(t, findings, err)
	})

	// The two versions are written side by side, under names this package
	// chose. One version may not read the other through a reference either: a
	// document is diffed as what it says, not as what its neighbour says.
	t.Run("the other version", func(t *testing.T) {
		v1 := []byte(`
openapi: 3.0.3
info: { title: Acme, version: 1.0.0 }
paths: {}
components:
  schemas:
    Status: ` + refCanarySchema + `
`)
		findings, err := DetectVersionDiffData(v1, refV2("v1.yaml#/components/schemas/Status"), "acme")
		if err == nil {
			t.Errorf("one version resolved a reference into the other: no error, %d findings", len(findings))
		}
	})
}

// TestVersionDiffTakesLocalFilesOnly: oasdiff decides how to load a source from
// the SHAPE of its path — a URL is fetched, `<rev>:<path>` is read out of git
// with a reader of oasdiff's own. Neither is a contract on disk, and the second
// would replace the reader that refuses references.
func TestVersionDiffTakesLocalFilesOnly(t *testing.T) {
	good := filepath.Join(contractsDir(), "spec-v1.yaml")

	t.Run("url", func(t *testing.T) {
		srv, hits := countingServer(t)
		for _, pair := range [][2]string{{srv.URL + "/v1.yaml", good}, {good, srv.URL + "/v2.yaml"}} {
			if _, err := DetectVersionDiff(pair[0], pair[1], "acme"); err == nil || !strings.Contains(err.Error(), "not a local file") {
				t.Errorf("DetectVersionDiff(%q, %q) err = %v, want a not-a-local-file refusal", pair[0], pair[1], err)
			}
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("the diff made %d request(s) for a path that is a URL, want 0", n)
		}
	})

	t.Run("git revision", func(t *testing.T) {
		_, err := DetectVersionDiff("HEAD:contracts/spec-v1.yaml", good, "acme")
		if err == nil || !strings.Contains(err.Error(), "not a local file") {
			t.Errorf("err = %v, want a not-a-local-file refusal", err)
		}
	})
}

// TestVersionDiffStillResolvesInternalRefs: refusing OTHER documents must not
// cost a document its own `#/components/...` references — nearly every real
// contract is built out of them.
func TestVersionDiffStillResolvesInternalRefs(t *testing.T) {
	doc := func(version, statusType string) []byte {
		return []byte(`
openapi: 3.0.3
info: { title: Acme, version: ` + version + ` }
paths:
  /v1/items:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Item" }
components:
  schemas:
    Item:
      type: object
      properties:
        status: { $ref: "#/components/schemas/Status" }
    Status: { type: ` + statusType + ` }
`)
	}
	findings, err := DetectVersionDiffData(doc("1.0.0", "string"), doc("2.0.0", "integer"), "acme")
	if err != nil {
		t.Fatalf("a self-contained document with internal references: %v", err)
	}
	if len(findings) != 1 || findings[0].Rule != "response-property-type-changed" {
		t.Fatalf("findings = %+v, want exactly one response-property-type-changed", findings)
	}
}

// TestStrictParseRefusesExternalRefs: LoadSpecData — and DescribeSpec, the form
// of it upload and contract fetch call before a document is stored — is the
// reason a stored contract is self-contained at all, and the version diff above
// only holds the same rule a second time. The loader's own suite
// (contract/openapi) pins every position a reference can sit in; this pins that
// the two entry points the bind paths actually use are that loader.
func TestStrictParseRefusesExternalRefs(t *testing.T) {
	srv, hits := countingServer(t)
	refs := map[string]string{
		"http":          srv.URL + "/s.json",
		"file uri":      "file://" + filepath.ToSlash(canaryFile(t)),
		"absolute path": filepath.ToSlash(canaryFile(t)),
	}
	for name, ref := range refs {
		t.Run(name, func(t *testing.T) {
			if doc, err := LoadSpecData(refV2(ref)); err == nil || doc != nil {
				t.Errorf("LoadSpecData accepted a document with an external reference (err=%v)", err)
			} else if strings.Contains(err.Error(), refCanary) {
				t.Errorf("the referenced content is in the error: %v", err)
			}
			if sum, err := DescribeSpec(refV2(ref)); err == nil {
				t.Errorf("DescribeSpec accepted a document with an external reference: %+v", sum)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the strict parse made %d request(s) to a host named by the document, want 0", n)
	}
}
