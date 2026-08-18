package viniferastore

import "errors"

// Config for the store extension — the single owner of the embedded SQLite
// database. Keys frozen in CONTRACTS §8.
type Config struct {
	// DBPath is the SQLite file path. It must live on a persistent volume (PVC)
	// so captured data survives a collector restart (§5 non-negotiable).
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
	if c.DBPath == "" {
		return errors.New("viniferastore extension: db_path is required (put it on a PVC)")
	}
	return nil
}
