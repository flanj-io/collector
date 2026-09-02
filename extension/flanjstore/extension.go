// Package flanjstore is the store EXTENSION: the single in-process owner of
// the store handle. It opens the configured backend at Start — embedded SQLite
// (WAL, on a PVC; one pod per file) by default, or a shared postgres database
// (N pods may share it) — closes it at Shutdown, and exposes it to the store
// exporter (writer) and the UI extension (reader) via the store.Provider
// interface, discovered through host.GetExtensions(). No other component opens
// a second handle.
package flanjstore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/store"
)

var typeStr = component.MustNewType("flanjstore")

// NewFactory returns the store extension factory.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		typeStr,
		createDefaultConfig,
		create,
		component.StabilityLevelBeta,
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		Backend:        BackendSQLite,
		WindowMaxRows:  10000,
		WindowMaxBytes: 256 << 20, // 256 MiB
	}
}

func create(_ context.Context, set extension.Settings, cfg component.Config) (extension.Extension, error) {
	return &storeExtension{cfg: cfg.(*Config), logger: set.Logger}, nil
}

// storeExtension owns the store.Store handle and satisfies store.Provider.
type storeExtension struct {
	cfg    *Config
	logger *zap.Logger

	// mu guards st: extensions start in an unspecified order, so another
	// extension's background goroutine (the UI's finding-sync ticker fires
	// immediately on its Start) can call Store() while this extension's Start
	// is still writing the handle.
	mu sync.Mutex
	st store.Store

	// The intra-cluster contract endpoint (specserver.go), bound only when
	// spec_endpoint is set — the tiered topology's store pod.
	specSrv *http.Server
	specLn  net.Listener

	// specWatchMu guards specWatchers, and is deliberately NOT e.mu: a
	// subscriber's callback must never be able to reach the store handle's lock.
	specWatchMu sync.Mutex
	// specWatchers are the in-process listeners for a contract-set change
	// (store.SpecSubscriber). Registered at Start and never removed — the only
	// subscriber is a processor whose lifetime is this process's.
	specWatchers []func()
}

// OnSpecsChanged registers a listener for contract-set changes
// (store.SpecSubscriber).
func (e *storeExtension) OnSpecsChanged(fn func()) {
	if fn == nil {
		return
	}
	e.specWatchMu.Lock()
	defer e.specWatchMu.Unlock()
	e.specWatchers = append(e.specWatchers, fn)
}

// NotifySpecsChanged announces a contract upload, replace or remove
// (store.SpecPublisher), so a cache converges in the time a callback takes
// rather than at its next refresh tick.
//
// Snapshot under the lock, call outside it: a subscriber is arbitrary code, and
// holding the registry lock across it would let one listener block every future
// announcement.
func (e *storeExtension) NotifySpecsChanged() {
	e.specWatchMu.Lock()
	watchers := make([]func(), len(e.specWatchers))
	copy(watchers, e.specWatchers)
	e.specWatchMu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

// Store exposes the shared store (store.Provider). Nil until Start has opened
// the backend — a true nil interface, never a typed-nil pointer.
func (e *storeExtension) Store() store.Store {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st
}

// Start opens the single owning connection for the configured backend.
func (e *storeExtension) Start(_ context.Context, _ component.Host) error {
	var (
		st  store.Store
		err error
	)
	backend := e.cfg.Backend
	if backend == "" {
		backend = BackendSQLite
	}
	switch backend {
	case BackendSQLite:
		st, err = store.OpenSQLite(e.cfg.DBPath, e.cfg.WindowMaxRows, e.cfg.WindowMaxBytes)
	case BackendPostgres:
		st, err = store.OpenPostgres(e.cfg.DSN, e.cfg.WindowMaxRows, e.cfg.WindowMaxBytes)
		if err == nil && e.cfg.DBPath != "" {
			// One-shot upgrade path: import the legacy embedded store's durable
			// evidence, then tombstone the file. Failure aborts Start — a crash
			// loop is visible, silently starting empty is not — and the import
			// is retry-safe end-to-end.
			sum, mErr := store.MigrateFromSQLite(st, e.cfg.DBPath)
			if mErr != nil {
				_ = st.Close()
				return fmt.Errorf("flanjstore extension: migrate legacy sqlite store %s: %w", e.cfg.DBPath, mErr)
			}
			if sum.Ran && e.logger != nil {
				e.logger.Info("legacy sqlite store migrated into postgres",
					zap.String("source", e.cfg.DBPath),
					zap.Int("pinned_calls", sum.PinnedCalls),
					zap.Int("findings", sum.Findings),
					zap.Int("edges", sum.Edges),
				)
			}
		}
	default:
		// Unreachable in practice: Config.Validate rejects unknown backends.
		err = fmt.Errorf("unknown backend %q", backend)
	}
	if err != nil {
		return fmt.Errorf("flanjstore extension: open store: %w", err)
	}
	e.mu.Lock()
	e.st = st
	e.mu.Unlock()
	// The contract endpoint serves from the handle just opened, so it binds
	// after it. A bind failure aborts Start: a store pod that silently fails to
	// serve contracts leaves every front detecting nothing, with no symptom.
	if err := e.startSpecServer(); err != nil {
		_ = st.Close()
		return fmt.Errorf("flanjstore extension: bind contract endpoint %s: %w", e.cfg.SpecEndpoint, err)
	}
	if e.logger != nil {
		e.logger.Info("flanj store opened",
			zap.String("backend", backend),
			zap.String("db_path", e.cfg.DBPath),
			zap.String("dsn", redactDSN(e.cfg.DSN)),
			zap.Int("window_max_rows", e.cfg.WindowMaxRows),
			zap.Int64("window_max_bytes", e.cfg.WindowMaxBytes),
		)
	}
	return nil
}

// redactDSN strips credentials from a connection string for logging. If the
// DSN does not parse as a URL, nothing of it is logged.
func redactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "(redacted)"
	}
	u.User = nil
	if q := u.Query(); q.Has("password") {
		q.Set("password", "xxxxx")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Shutdown closes the connection.
func (e *storeExtension) Shutdown(context.Context) error {
	e.stopSpecServer()
	e.mu.Lock()
	st := e.st
	e.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.Close()
}

// compile-time assertions.
var (
	_ extension.Extension  = (*storeExtension)(nil)
	_ store.Provider       = (*storeExtension)(nil)
	_ store.SpecPublisher  = (*storeExtension)(nil)
	_ store.SpecSubscriber = (*storeExtension)(nil)
)
