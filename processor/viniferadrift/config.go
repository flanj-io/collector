package viniferadrift

import "errors"

// Config configures the drift detector. Keys are frozen in CONTRACTS §8.
type Config struct {
	// IntegrationID is the integration being observed, e.g. "acme-payments".
	IntegrationID string `mapstructure:"integration_id"`
	// SpecPath is the provider OpenAPI spec (v1) validated against live traffic.
	SpecPath string `mapstructure:"spec_path"`
	// SpecV2Path optionally enables the spec v1->v2 breaking-change finding.
	SpecV2Path string `mapstructure:"spec_v2_path"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.SpecPath == "" {
		return errors.New("viniferadrift: spec_path is required")
	}
	if c.IntegrationID == "" {
		return errors.New("viniferadrift: integration_id is required")
	}
	return nil
}
