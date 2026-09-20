package openapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// LoadData is the parse every operator-bound document passes before it is
// stored — an upload, and the bytes a contract fetch read from a URL. It is
// what makes a bound contract ONE self-contained document: a `$ref` out of the
// document is refused, and refused BEFORE anything is read. A fetched document
// is written by whoever answered the URL, so a reference it followed would be a
// second request, to an address that document chose, which no destination
// check ever saw — or a read of this machine's disk.
//
// Every case references something that EXISTS (a listening server, a real
// file), so a refusal cannot be mistaken for a resource that was not there.

// extCanary exists only in the referenced resource.
const extCanary = "flanj-canary-5d2e8a"

// extPositions are the places a reference can sit, each with a target that is
// VALID there. Validity is the point: a followed reference then yields a
// document that loads, so "LoadData returned an error" can only mean the
// reference was refused. With one schema-shaped target everywhere, a parameter
// or a response that WAS read from disk failed validation afterwards, and the
// error this test asks for appeared for the wrong reason.
var extPositions = []struct{ name, target string }{
	{"schema property", `{"type":"string","description":"` + extCanary + `"}`},
	{"component schema", `{"type":"string","description":"` + extCanary + `"}`},
	{"parameter", `{"name":"q","in":"query","description":"` + extCanary + `","schema":{"type":"string"}}`},
	{"request body", `{"description":"` + extCanary + `","content":{"application/json":{"schema":{"type":"object"}}}}`},
	{"response", `{"description":"` + extCanary + `"}`},
	{"response header", `{"description":"` + extCanary + `","schema":{"type":"string"}}`},
	{"example", `{"value":"` + extCanary + `"}`},
	{"path item", `{"get":{"responses":{"200":{"description":"` + extCanary + `"}}}}`},
}

// permissiveLoad is the control: the same bytes through a loader that follows
// references.
func permissiveLoad(b []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromData(b)
	if err != nil {
		return nil, err
	}
	return doc, doc.Validate(loader.Context)
}

// extDoc places ref at one position of an otherwise valid document.
func extDoc(position, ref string) []byte {
	r := `{ $ref: "` + ref + `" }`
	okResponse := `{ description: ok }`
	parts := map[string]string{
		"pathItem": "", "parameter": "", "requestBody": "", "response": `"200": ` + okResponse, "component": "",
	}
	switch position {
	case "schema property":
		parts["response"] = `"200": { description: ok, content: { application/json: { schema: { type: object, properties: { status: ` + r + ` } } } } }`
	case "component schema":
		parts["component"] = "components: { schemas: { Status: " + r + " } }"
	case "parameter":
		parts["parameter"] = "parameters: [ " + r + " ]"
	case "request body":
		parts["requestBody"] = "requestBody: " + r
	case "response":
		parts["response"] = `"200": ` + r
	case "response header":
		parts["response"] = `"200": { description: ok, headers: { X-Rate: ` + r + ` } }`
	case "example":
		parts["response"] = `"200": { description: ok, content: { application/json: { examples: { a: ` + r + ` } } } }`
	case "path item":
		parts["pathItem"] = "  /v1/other: " + r
	default:
		panic("unknown position " + position)
	}
	return []byte(`
openapi: 3.0.3
info: { title: Acme, version: 1.0.0 }
paths:
  /v1/items:
    post:
      ` + parts["parameter"] + `
      ` + parts["requestBody"] + `
      responses:
        ` + parts["response"] + `
` + parts["pathItem"] + `
` + parts["component"] + `
`)
}

// TestLoadDataMakesNoRequestForAnExternalRef: wherever in the document the
// reference sits. kin-openapi resolves each kind of component through its own
// function, so one position proves nothing about the next.
func TestLoadDataMakesNoRequestForAnExternalRef(t *testing.T) {
	for _, position := range extPositions {
		t.Run(position.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(position.target))
			}))
			defer srv.Close()
			raw := extDoc(position.name, srv.URL+"/s.json")

			doc, err := LoadData(raw)
			if n := hits.Load(); n != 0 {
				t.Errorf("LoadData made %d request(s) to a host named by the document, want 0", n)
			}
			if err == nil || doc != nil {
				t.Errorf("LoadData accepted a document with an external reference (doc=%v, err=%v)", doc != nil, err)
			}

			// The control: the SAME bytes, through a loader that follows
			// references, reach the server and load. Without it a zero above
			// could be a document that failed to parse before the reference
			// was ever looked at.
			if _, err := permissiveLoad(raw); err != nil || hits.Load() == 0 {
				t.Fatalf("control: a permissive loader made %d request(s), err=%v — this case never reaches its reference", hits.Load(), err)
			}
		})
	}
}

// TestLoadDataReadsNoLocalFileForARef: a file:// URI, an absolute path, and a
// relative path that resolves — from this process's working directory, which is
// all a document loaded from bytes has — to a file that exists.
func TestLoadDataReadsNoLocalFileForARef(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i, position := range extPositions {
		secret := filepath.Join(dir, "secret-"+strconv.Itoa(i)+".json")
		if err := os.WriteFile(secret, []byte(position.target), 0o600); err != nil {
			t.Fatal(err)
		}
		refs := map[string]string{
			"file uri":      "file://" + filepath.ToSlash(secret),
			"absolute path": filepath.ToSlash(secret),
		}
		// Across volumes there is no relative path to the temp dir; the other
		// two spellings still run.
		if rel, err := filepath.Rel(cwd, secret); err == nil {
			refs["relative path"] = filepath.ToSlash(rel)
		}
		for name, ref := range refs {
			t.Run(name+"/"+position.name, func(t *testing.T) {
				raw := extDoc(position.name, ref)
				doc, err := LoadData(raw)
				if err == nil || doc != nil {
					t.Fatalf("LoadData accepted a document referencing a local file (doc=%v, err=%v)", doc != nil, err)
				}
				if strings.Contains(err.Error(), extCanary) {
					t.Errorf("the file's content is in the error: %v", err)
				}

				// The control: followed, this reference resolves to a file that
				// is read and is valid where it sits, so the document loads. The
				// error above is therefore the refusal, and not a target that
				// would have failed to parse anyway.
				if _, err := permissiveLoad(raw); err != nil {
					t.Fatalf("control: a permissive loader cannot load this case either: %v", err)
				}
			})
		}
	}
}

// TestLoadDataStillResolvesInternalRefs: the refusal is of OTHER documents. A
// document's references into itself are how real contracts are written.
func TestLoadDataStillResolvesInternalRefs(t *testing.T) {
	doc, err := LoadData([]byte(`
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
              schema: { $ref: "#/components/schemas/Item" }
components:
  schemas:
    Item:
      type: object
      properties:
        status: { $ref: "#/components/schemas/Status" }
    Status: { type: string, enum: [active, closed] }
`))
	if err != nil {
		t.Fatalf("a self-contained document with internal references: %v", err)
	}
	item := doc.Components.Schemas["Item"].Value
	if got := item.Properties["status"].Value; got == nil || len(got.Enum) != 2 {
		t.Fatalf("the internal reference did not resolve: %+v", item.Properties["status"])
	}
}
