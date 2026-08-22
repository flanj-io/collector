package viniferastore

import (
	"errors"
	"fmt"
)

// Store backends. See internal/store and docs/STORE.md.
const (
	// BackendSQLite is the embedded default: one WAL file on a PVC, exactly one
	// collector pod per file.
	BackendSQLite = "sqlite"
	// BackendPostgres is the shared external database: N collector pods may
	// write to one database concurrently (the multi-pod deployment mode).
	BackendPostgres = "postgres"
)

// Config for the store extension — the single in-process owner of the store
// handle. Keys frozen in CONTRACTS §8.
type Config struct {
	// Backend selects the store implementation: "sqlite" (default) or
	// "postgres".
	Backend string `mapstructure:"backend"`
	// DSN is the postgres connection string (required iff backend=postgres).
	// Use ${env:...} interpolation to keep credentials out of the config file;
	// it is only ever logged redacted.
	DSN string `mapstructure:"dsn"`
	// DBPath is the SQLite file path (required iff backend=sqlite). It must
	// live on a persistent volume (PVC) so captured data survives a collector
	// restart. With backend=postgres it is instead the OPTIONAL one-shot
	// migration source: if the file exists at Start, its durable evidence
	// (pinned calls, findings, edges) is copied into postgres and the file is
	// renamed "<db_path>.migrated".
	DBPath string `mapstructure:"db_path"`
	// WindowMaxRows caps the rolling window row count (<=0 disables the cap).
	WindowMaxRows int `mapstructure:"window_max_rows"`
	// WindowMaxBytes caps the rolling window byte size (<=0 disables the cap).
	WindowMaxBytes int64 `mapstructure:"window_max_bytes"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	switch c.Backend {
	case "", BackendSQLite:
		if c.DBPath == "" {
			return errors.New("viniferastore extension: db_path is required (put it on a PVC)")
		}
	case BackendPostgres:
		if c.DSN == "" {
			return errors.New("viniferastore extension: dsn is required when backend=postgres")
		}
	default:
		return fmt.Errorf("viniferastore extension: unknown backend %q (want %q or %q)", c.Backend, BackendSQLite, BackendPostgres)
	}
	return nil
}
