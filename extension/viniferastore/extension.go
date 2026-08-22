// Package viniferastore is the store EXTENSION: the single in-process owner of
// the store handle. It opens the configured backend at Start — embedded SQLite
// (WAL, on a PVC; one pod per file) by default, or a shared postgres database
// (N pods may share it) — closes it at Shutdown, and exposes it to the store
// exporter (writer) and the UI extension (reader) via the store.Provider
// interface, discovered through host.GetExtensions(). No other component opens
// a second handle.
package viniferastore

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"

	"github.com/vinifera-io/collector/internal/store"
)

var typeStr = component.MustNewType("viniferastore")

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
	st     store.Store
}

// Store exposes the shared store (store.Provider).
func (e *storeExtension) Store() store.Store { return e.st }

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
				return fmt.Errorf("viniferastore extension: migrate legacy sqlite store %s: %w", e.cfg.DBPath, mErr)
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
		return fmt.Errorf("viniferastore extension: open store: %w", err)
	}
	e.st = st
	if e.logger != nil {
		e.logger.Info("vinifera store opened",
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
	if e.st == nil {
		return nil
	}
	return e.st.Close()
}

// compile-time assertions.
var (
	_ extension.Extension = (*storeExtension)(nil)
	_ store.Provider      = (*storeExtension)(nil)
)
