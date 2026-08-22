package viniferadrift

import "errors"

// Config configures the drift detector. Keys are frozen in CONTRACTS §8.
//
// Everything here is OPTIONAL: the collector auto-discovers edges from observed
// traffic and never requires a pre-configured integration/target. Drift detection
// is an opt-in enhancer that only runs on edges for which a local spec is loaded.
// With no spec_path the processor is a pass-through — capture and edge discovery
// still work; only findings are suppressed.
type Config struct {
	// IntegrationID labels findings from this spec, e.g. "acme-payments".
	IntegrationID string `mapstructure:"integration_id"`
	// SpecPath is the provider OpenAPI spec (v1) validated against live traffic.
	// Empty disables live-vs-spec detection.
	SpecPath string `mapstructure:"spec_path"`
	// SpecV2Path optionally enables the spec v1->v2 breaking-change finding.
	SpecV2Path string `mapstructure:"spec_v2_path"`
	// PeerHost optionally scopes live-vs-spec detection to one discovered edge by
	// its peer host (spec-matched-by-host). Empty validates every captured call
	// against the loaded spec.
	PeerHost string `mapstructure:"peer_host"`

	// SelfSpecPath is the org's OWN OpenAPI spec — the contract THIS org
	// publishes as a provider. When set, INBOUND (server-direction) responses are
	// validated against it, so providers see their own drift, not just their
	// dependencies'. Empty disables self validation.
	SelfSpecPath string `mapstructure:"self_spec_path"`
	// SelfIntegrationID labels findings from the self spec (default "self").
	// Must differ from integration_id so self and provider findings never merge.
	SelfIntegrationID string `mapstructure:"self_integration_id"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator. Nothing is required — a bare
// `viniferadrift: {}` is valid and makes the processor a pass-through.
func (c *Config) Validate() error {
	if c.SelfSpecPath != "" && c.selfIntegration() == c.IntegrationID {
		return errors.New("viniferadrift: self_integration_id must differ from integration_id (self and provider findings must not merge)")
	}
	return nil
}

// selfIntegration returns the label for self-spec findings ("self" by default).
func (c *Config) selfIntegration() string {
	if c.SelfIntegrationID != "" {
		return c.SelfIntegrationID
	}
	return "self"
}
