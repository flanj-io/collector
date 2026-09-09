package flanjui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"

	"github.com/flanj-io/collector/internal/promote"
	"github.com/flanj-io/collector/internal/store"
)

type uiExtension struct {
	cfg       *Config
	srvConfig confighttp.ServerConfig
	telemetry component.TelemetrySettings

	host   component.Host
	stMu   sync.Mutex
	st     store.Store     // interface-typed: the nil check in storeOrError must never see a typed-nil pointer
	cp     *promote.Client // deploy-token client; per-request copies carry the collector key (keyedClient)
	me     meCache
	server *http.Server

	// Finding-shape sync ticker (sync.go): started in Start, stopped (and
	// waited for — no goroutine leak) in Shutdown.
	syncCancel context.CancelFunc
	syncDone   chan struct{}

	// The agent-facing MCP read surface (mcp.go), built on first request.
	mcpState
}

// resolveStore finds the single-owner store extension lazily. Extensions can
// start in any order, so the store may not have opened its connection when the
// UI's Start runs — every extension has started by the time the first HTTP
// request arrives. The finding-sync ticker can fire BEFORE that moment, so an
// unresolved lookup is retried on the next call, never latched: caching a nil
// here would blind every later request.
func (e *uiExtension) resolveStore() store.Store {
	e.stMu.Lock()
	defer e.stMu.Unlock()
	if e.st != nil || e.host == nil {
		return e.st
	}
	for _, ext := range e.host.GetExtensions() {
		if p, ok := ext.(store.Provider); ok {
			e.st = p.Store()
			break
		}
	}
	return e.st
}

// announceSpecChange tells the in-process contract cache that an upload,
// replace or remove landed, so the drift processor refreshes now instead of at
// its next tick. Without it the UI's ratified promise ("Validating from now
// on") is false for up to a full refresh interval, and calls in that window are
// scored against the superseded document.
//
// Not latched, unlike resolveStore: this runs only on the two mutating contract
// routes, so re-scanning the extensions costs nothing worth caching. A host
// with no store extension (or one predating the interface) is a legitimate
// no-op — the refresh ticker still converges.
func (e *uiExtension) announceSpecChange() {
	if e.host == nil {
		return
	}
	for _, ext := range e.host.GetExtensions() {
		if p, ok := ext.(store.SpecPublisher); ok {
			p.NotifySpecsChanged()
			return
		}
	}
}

// servesFronts reports whether the store extension beside this one hands its
// contracts to FRONT collectors (store.ContractServer — i.e. `spec_endpoint` is
// configured).
//
// The Contracts card asks so it can be honest about the document cap, which
// belongs to that hop alone: a co-located drift processor reads the same rows
// in-process with no cap at all, so an over-cap document is bound and
// validating on a single pod and a card calling it "too large to serve" there
// would warn about something that works.
//
// Not latched, like announceSpecChange and unlike resolveStore: this scans a
// handful of extensions on a poll that already reads the store, and a host with
// no store extension — or one predating the interface — answers false, which is
// the pre-tiered default and the safe one.
func (e *uiExtension) servesFronts() bool {
	if e.host == nil {
		return false
	}
	for _, ext := range e.host.GetExtensions() {
		if cs, ok := ext.(store.ContractServer); ok {
			return cs.ServesContracts()
		}
	}
	return false
}

// Start wires the CP client and serves the UI on the loopback endpoint. The
// store is resolved lazily (see resolveStore).
func (e *uiExtension) Start(ctx context.Context, host component.Host) error {
	e.host = host
	if e.cfg.CPBaseURL != "" {
		e.cp = promote.NewClient(e.cfg.CPBaseURL, e.cfg.CPDeployToken, collectorVersion)
		// Said at boot rather than only at the first Connect, because a headless
		// operator never presses Connect and would otherwise learn this from a
		// UI they cannot open. The VALUE is deliberately not logged — the key
		// name is enough to act on, and config values stay out of the log.
		if reservedDocHost(cpBaseHost(e.cfg.CPBaseURL)) {
			e.telemetry.Logger.Warn("flanj: cp_base_url is a name reserved for documentation and can never resolve to a control plane — Connect will fail until it names a real one")
		}
	}

	handler := e.routes()
	srv, err := e.srvConfig.ToServer(ctx, host.GetExtensions(), e.telemetry, handler)
	if err != nil {
		return err
	}
	e.server = srv

	ln, err := e.srvConfig.ToListener(ctx)
	if err != nil {
		return err
	}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			e.telemetry.Logger.Error("flanj UI server stopped: " + serveErr.Error())
		}
	}()
	e.telemetry.Logger.Info("flanj UI serving on http://" + e.cfg.UIEndpoint)
	e.startFindingSync()
	return nil
}

// Shutdown stops the finding-sync ticker and the UI server.
func (e *uiExtension) Shutdown(ctx context.Context) error {
	e.stopFindingSync()
	if e.server == nil {
		return nil
	}
	return e.server.Shutdown(ctx)
}

var _ extensionShim = (*uiExtension)(nil)

// extensionShim documents the component.Component surface the extension satisfies
// (Start + Shutdown); extension.Extension is an alias of component.Component.
type extensionShim interface {
	Start(context.Context, component.Host) error
	Shutdown(context.Context) error
}

// isLoopback reports whether endpoint binds a loopback host. Enforced by config
// validation so the outbound-only collector never exposes the UI off-host.
func isLoopback(endpoint string) bool {
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
