// Package viniferastore is the store EXTENSION: the single owner of the embedded
// SQLite database (WAL, on a PVC). It opens the one connection at Start and
// closes it at Shutdown, and exposes it to the store exporter (writer) and the UI
// extension (reader) via the store.Provider interface, discovered through
// host.GetExtensions(). No other component opens a second connection.
package viniferastore

import (
	"context"
	"fmt"

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
		WindowMaxRows:  10000,
		WindowMaxBytes: 256 << 20, // 256 MiB
	}
}

func create(_ context.Context, set extension.Settings, cfg component.Config) (extension.Extension, error) {
	return &storeExtension{cfg: cfg.(*Config), logger: set.Logger}, nil
}

// storeExtension owns the *store.Store and satisfies store.Provider.
type storeExtension struct {
	cfg    *Config
	logger *zap.Logger
	st     *store.Store
}

// Store exposes the shared store (store.Provider).
func (e *storeExtension) Store() *store.Store { return e.st }

// Start opens the single owning connection.
func (e *storeExtension) Start(_ context.Context, _ component.Host) error {
	st, err := store.Open(e.cfg.DBPath, e.cfg.WindowMaxRows, e.cfg.WindowMaxBytes)
	if err != nil {
		return fmt.Errorf("viniferastore extension: open store: %w", err)
	}
	e.st = st
	if e.logger != nil {
		e.logger.Info("vinifera store opened",
			zap.String("db_path", e.cfg.DBPath),
			zap.Int("window_max_rows", e.cfg.WindowMaxRows),
			zap.Int64("window_max_bytes", e.cfg.WindowMaxBytes),
		)
	}
	return nil
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
