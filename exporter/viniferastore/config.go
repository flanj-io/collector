package viniferastore

// Config for the store exporter. The exporter does not open the database — the
// viniferastore EXTENSION is the single owner. The exporter discovers that
// extension through host.GetExtensions() at Start, so there is nothing to tune
// here in v0.
type Config struct {
	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error { return nil }
