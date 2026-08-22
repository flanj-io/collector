package viniferaui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"

	"github.com/vinifera-io/collector/internal/promote"
	"github.com/vinifera-io/collector/internal/store"
)

type uiExtension struct {
	cfg       *Config
	srvConfig confighttp.ServerConfig
	telemetry component.TelemetrySettings

	host   component.Host
	stOnce sync.Once
	st     store.Store // interface-typed: the nil check in storeOrError must never see a typed-nil pointer
	cp     *promote.Client
	server *http.Server
}

// resolveStore finds the single-owner store extension lazily. Extensions can
// start in any order, so the store may not have opened its connection when the
// UI's Start runs — but every extension has started by the time the first HTTP
// request arrives, so resolving on first use is race-free.
func (e *uiExtension) resolveStore() store.Store {
	e.stOnce.Do(func() {
		for _, ext := range e.host.GetExtensions() {
			if p, ok := ext.(store.Provider); ok {
				e.st = p.Store()
				return
			}
		}
	})
	return e.st
}

// Start wires the CP client and serves the UI on the loopback endpoint. The
// store is resolved lazily (see resolveStore).
func (e *uiExtension) Start(ctx context.Context, host component.Host) error {
	e.host = host
	if e.cfg.CPBaseURL != "" {
		e.cp = promote.NewClient(e.cfg.CPBaseURL, e.cfg.CPDeployToken, collectorVersion)
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
			e.telemetry.Logger.Error("vinifera UI server stopped: " + serveErr.Error())
		}
	}()
	e.telemetry.Logger.Info("vinifera UI serving on http://" + e.cfg.UIEndpoint)
	return nil
}

// Shutdown stops the UI server.
func (e *uiExtension) Shutdown(ctx context.Context) error {
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
