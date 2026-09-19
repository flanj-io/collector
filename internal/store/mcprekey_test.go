package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// TestOpenRekeysMCPCatalogues: catalogues stored under an SDK-sent id move, at
// open, to the key the collector now derives from their peer host — the key
// every new snapshot and call lands on, so the server's calls and its
// catalogue meet again. The <integration>:search sibling moves with it. When
// two rows land on one key the NEWEST wins; a row already on its key stays; a
// second open changes nothing.
func TestOpenRekeysMCPCatalogues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		put := func(key, host, loadedAt, source, doc string) {
			t.Helper()
			if err := s.PutMCPCatalogue(model.SpecInfo{Integration: key, PeerHost: host, Format: model.SpecFormatMCP,
				Source: source, LoadedAt: loadedAt, Title: key}, []byte(doc)); err != nil {
				t.Fatal(err)
			}
		}
		obs, search := model.SpecSourceObserved, model.SpecSourceSearchResult
		// Two SDK ids for one host collapse; the newer document wins.
		put("acme-payments", "mcp.acme.test", "2026-09-19T08:00:00Z", obs, `{"tools":["older"]}`)
		put("acme-mcp", "mcp.acme.test", "2026-09-19T09:00:00Z", obs, `{"tools":["newer"]}`)
		put("acme-payments:search", "mcp.acme.test", "2026-09-19T08:30:00Z", search, `{"tools":["searched"]}`)
		// Already on its derived key, and newer than a stale SDK-keyed twin.
		put("mcp-globex-test", "mcp.globex.test", "2026-09-19T10:00:00Z", obs, `{"tools":["current"]}`)
		put("globex", "mcp.globex.test", "2026-09-19T07:00:00Z", obs, `{"tools":["stale"]}`)
		// A row whose SDK id is ANOTHER row's derived key, and which itself
		// belongs elsewhere: neither may be lost to the other on the way.
		put("mcp-initech-test", "mcp.hooli.test", "2026-09-19T06:00:00Z", obs, `{"tools":["hooli"]}`)
		put("initech", "mcp.initech.test", "2026-09-19T05:00:00Z", obs, `{"tools":["initech"]}`)
		// A stdio server: its peer host is the serverInfo.name.
		put("Stripe MCP (stdio)", "stripe-mcp", "2026-09-19T04:00:00Z", obs, `{"tools":["stdio"]}`)
		_ = s.Close()

		want := map[string]string{
			"mcp-acme-test":        `{"tools":["newer"]}`,
			"mcp-acme-test:search": `{"tools":["searched"]}`,
			"mcp-globex-test":      `{"tools":["current"]}`,
			"mcp-hooli-test":       `{"tools":["hooli"]}`,
			"mcp-initech-test":     `{"tools":["initech"]}`,
			"stripe-mcp":           `{"tools":["stdio"]}`,
		}
		check := func(st Store, pass string) {
			t.Helper()
			rows, err := st.ListMCPCatalogues()
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, r := range rows {
				keys = append(keys, r.Integration)
			}
			if len(rows) != len(want) {
				t.Fatalf("%s: catalogue keys = %v, want %d rows", pass, keys, len(want))
			}
			for key, doc := range want {
				raw, ok, err := st.GetMCPCatalogueDoc(key)
				if err != nil || !ok || string(raw) != doc {
					t.Errorf("%s: %s = %q (ok %v, err %v), want %q; keys %s", pass, key, raw, ok, err, doc, strings.Join(keys, ","))
				}
			}
		}
		s = b.reopen(t, 0, 0)
		check(s, "first open")
		_ = s.Close()
		check(b.reopen(t, 0, 0), fmt.Sprintf("second open (%s)", b.name))
	})
}
