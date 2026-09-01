package flanjstore

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

	// SpecEndpoint binds the intra-cluster CONTRACT endpoint — the tiered
	// topology's spec channel. Set it on the STORE POD; front collectors then
	// point `flanjdrift.store_pod_endpoint` at it and read the contracts an
	// operator uploaded in the UI. Empty (the default) serves nothing, which is
	// correct for every single-pod and shared-postgres deployment, where the
	// drift processor reads the co-located store directly.
	//
	// Read-only and contracts-only. The UI stays loopback (non-negotiable #5);
	// this is a sibling of the `:4318` intra-cluster ingest, not a second UI.
	SpecEndpoint string `mapstructure:"spec_endpoint"`
	// SpecToken is the shared bearer token fronts present to that endpoint. Use
	// ${env:...} interpolation; it is never logged.
	SpecToken string `mapstructure:"spec_token"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	switch c.Backend {
	case "", BackendSQLite:
		if c.DBPath == "" {
			return errors.New("flanjstore extension: db_path is required (put it on a PVC)")
		}
	case BackendPostgres:
		if c.DSN == "" {
			return errors.New("flanjstore extension: dsn is required when backend=postgres")
		}
	default:
		return fmt.Errorf("flanjstore extension: unknown backend %q (want %q or %q)", c.Backend, BackendSQLite, BackendPostgres)
	}
	if c.SpecToken != "" && c.SpecEndpoint == "" {
		return errors.New("flanjstore extension: spec_token needs spec_endpoint (a token guarding nothing is a misconfiguration, not a default)")
	}
	// The other direction is the dangerous one. This listener is the single
	// deliberate exception to outbound-only/loopback, and the shipped store
	// config reads its token from ${env:FLANJ_SPEC_TOKEN} — which expands to
	// the empty string when unset. Binding anyway would put every uploaded
	// contract behind no auth at all on the cluster interface, as the DEFAULT
	// failure of the documented config. Refuse to start instead.
	if c.SpecEndpoint != "" && c.SpecToken == "" {
		return errors.New("flanjstore extension: spec_endpoint requires spec_token (this listener is not loopback — set FLANJ_SPEC_TOKEN, or remove spec_endpoint)")
	}
	return nil
}
