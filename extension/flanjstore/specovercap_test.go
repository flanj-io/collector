package flanjstore

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/flanj-io/collector/internal/model"
)

// startObservedStorePod is startStorePod with a logger whose lines can be
// counted. The document-cap condition produces no finding and no record, so the
// log IS its observable surface — on this pod and on the front.
func startObservedStorePod(t *testing.T) (*storeExtension, string, *observer.ObservedLogs) {
	t.Helper()
	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(t.TempDir(), "store.db")
	cfg.SpecEndpoint = "127.0.0.1:0"
	cfg.SpecToken = testSpecToken

	core, logs := observer.New(zap.DebugLevel)
	ext, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := ext.(*storeExtension)
	e.logger = zap.New(core)
	if err := e.Start(context.Background(), nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e, "http://" + e.specLn.Addr().String(), logs
}

func listContracts(t *testing.T, base string) []model.SpecInfo {
	t.Helper()
	code, body := get(t, base+"/internal/contracts", testSpecToken)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", code)
	}
	var payload struct {
		Contracts []model.SpecInfo `json:"contracts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return payload.Contracts
}

// TestSpecListCarriesTheDocumentSize: the metadata a front polls names how big
// each stored document is.
//
// Without it the ONLY way either end could learn a row is past the cap was to
// attempt the transfer and read the refusal — so a front re-requested a
// document it would be refused on every ten-second tick, this pod read the
// whole oversized document into memory each time to say no, and the operator's
// Contracts card had nothing to render but a contract that looked fine.
func TestSpecListCarriesTheDocumentSize(t *testing.T) {
	e, base, _ := startObservedStorePod(t)
	small := []byte("openapi: 3.0.3\n")
	big := mcpSnapshotOfSize(t, specMaxDoc+4096)
	seedContract(t, e, "acme", "api.acme.test", model.SpecRoleProvider, model.SpecFormatOpenAPI, small)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP, big)

	sizes := map[string]int{}
	for _, si := range listContracts(t, base) {
		sizes[si.Integration] = si.DocBytes
	}
	if sizes["acme"] != len(small) {
		t.Errorf("acme doc_bytes = %d, want %d", sizes["acme"], len(small))
	}
	if sizes["acme-tools"] != len(big) {
		t.Errorf("acme-tools doc_bytes = %d, want %d", sizes["acme-tools"], len(big))
	}
	if sizes["acme-tools"] <= specMaxDoc {
		t.Errorf("the oversized row lists %d bytes, which does not read as over the %d cap",
			sizes["acme-tools"], specMaxDoc)
	}
}

// TestSpecDocRefusedWithoutReadingTheDocument: the doc route refuses from the
// metadata it already resolved, so an over-cap document is not loaded into this
// pod's heap once per front per tick just to be turned down.
func TestSpecDocRefusedWithoutReadingTheDocument(t *testing.T) {
	e, base, _ := startObservedStorePod(t)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, specMaxDoc+1))

	code, _ := get(t, base+"/internal/contracts/doc?integration=acme-tools", testSpecToken)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("doc status = %d, want 413", code)
	}
}

// TestSpecOverCapLogsTheStartNotEveryTick: the refusal is an EVENT, logged when
// it starts, and the thousand identical re-discoveries after it are not.
//
// A front lists every ten seconds and asks for each moved document, so before
// this the store pod wrote a Warn line six times a minute per oversized edge
// per front, forever — burying the one line an operator needed under copies of
// itself and reading like a storm of new events rather than one unchanged fact.
func TestSpecOverCapLogsTheStartNotEveryTick(t *testing.T) {
	e, base, logs := startObservedStorePod(t)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, specMaxDoc+4096))

	// Twelve ticks' worth of exactly what a front does.
	for i := 0; i < 12; i++ {
		listContracts(t, base)
		if code, _ := get(t, base+"/internal/contracts/doc?integration=acme-tools", testSpecToken); code != http.StatusRequestEntityTooLarge {
			t.Fatalf("tick %d: doc status = %d, want 413", i, code)
		}
	}

	raised := logs.FilterMessage(msgSpecOverCap).All()
	if len(raised) != 1 {
		t.Fatalf("logged the refusal %d times over 12 refresh ticks, want exactly 1 — "+
			"the transition is the event, the repeats are the same sentence", len(raised))
	}
	fields := raised[0].ContextMap()
	if fields["integration"] != "acme-tools" {
		t.Errorf("integration = %v, want acme-tools — the line has to name the row", fields["integration"])
	}
	if fields["peer_host"] != "mcp.acme.test" {
		t.Errorf("peer_host = %v, want mcp.acme.test", fields["peer_host"])
	}
	if got, want := fields["bytes"], int64(specMaxDoc+4096); got != want {
		t.Errorf("bytes = %v, want %v — the operator acts on the SIZE", got, want)
	}
}

// TestSpecOverCapLogsWhenItClears: the end of a standing condition is the other
// event. Without it an operator who shrinks the catalogue has no confirmation
// from this pod that the channel is open again.
func TestSpecOverCapLogsWhenItClears(t *testing.T) {
	e, base, logs := startObservedStorePod(t)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, specMaxDoc+4096))
	listContracts(t, base)
	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 1 {
		t.Fatalf("the refusal was logged %d times on the first sweep, want 1", n)
	}

	// The server's catalogue is split; the next observed snapshot fits.
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, 4096))
	listContracts(t, base)

	cleared := logs.FilterMessage(msgSpecOverCapCleared).All()
	if len(cleared) != 1 {
		t.Fatalf("logged the clear %d times, want exactly 1", len(cleared))
	}
	if cleared[0].ContextMap()["integration"] != "acme-tools" {
		t.Errorf("clear line names %v, want acme-tools", cleared[0].ContextMap()["integration"])
	}

	// And it stays quiet afterwards, on both lines.
	for i := 0; i < 5; i++ {
		listContracts(t, base)
	}
	if n := logs.FilterMessage(msgSpecOverCapCleared).Len(); n != 1 {
		t.Errorf("the clear repeated %d times over later sweeps", n)
	}
	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 1 {
		t.Errorf("the refusal came back %d times after clearing", n)
	}
}

// TestSpecOverCapLogsANewOversizedDocument: a REPLACEMENT that is also over the
// cap is a new event, not a repeat — the server published a different
// catalogue and it is still too big, which is a thing that happened.
func TestSpecOverCapLogsANewOversizedDocument(t *testing.T) {
	e, base, logs := startObservedStorePod(t)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, specMaxDoc+4096))
	listContracts(t, base)
	listContracts(t, base)
	seedContract(t, e, "acme-tools", "mcp.acme.test", model.SpecRoleProvider, model.SpecFormatMCP,
		mcpSnapshotOfSize(t, specMaxDoc+9000))
	listContracts(t, base)

	if n := logs.FilterMessage(msgSpecOverCap).Len(); n != 2 {
		t.Fatalf("logged %d refusals, want 2 (the first document, then the replacement)", n)
	}
}

// TestServesContractsTracksTheEndpoint: the UI reads this to decide whether the
// document cap applies at all. A pod with no contract endpoint has no fronts to
// refuse, so an over-cap document there is bound and validating in-process, and
// a card calling it "too large to serve" would be warning about a thing that
// works.
func TestServesContractsTracksTheEndpoint(t *testing.T) {
	e, _, _ := startObservedStorePod(t)
	if !e.ServesContracts() {
		t.Error("a store pod with spec_endpoint set says it serves no fronts")
	}

	cfg := createDefaultConfig().(*Config)
	cfg.DBPath = filepath.Join(t.TempDir(), "single.db")
	single, err := create(context.Background(), extension.Settings{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if single.(*storeExtension).ServesContracts() {
		t.Error("a single-pod store with no spec_endpoint claims to serve fronts")
	}
}
