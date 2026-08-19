package viniferadrift

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

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator. Nothing is required — a bare
// `viniferadrift: {}` is valid and makes the processor a pass-through.
func (c *Config) Validate() error {
	return nil
}
