package flanjdrift

import "errors"

// Config configures the drift detector. Keys are frozen in CONTRACTS §8.
//
// Everything here is OPTIONAL: the collector auto-discovers edges from observed
// traffic and never requires a pre-configured integration/target.
//
// PROVIDER contracts are NOT configured. They are uploaded in the collector UI,
// stored, and read at runtime by the spec cache (speccache.go) — `spec_path`,
// `spec_v2_path` and `peer_host` were removed from CONTRACTS §8 with that
// change. A config file could not carry fifty providers' documents, and a
// mounted file goes stale the moment the vendor publishes; the upload also
// binds each contract to exactly one host, which the singular config model
// could not express.
type Config struct {
	// IntegrationID labels findings from the self spec's sibling config and is
	// read by the UI extension. Provider findings take their integration from
	// the call the SDK stamped, not from here.
	IntegrationID string `mapstructure:"integration_id"`

	// SelfSpecPath is the org's OWN OpenAPI spec — the contract THIS org
	// publishes as a provider. When set, INBOUND (server-direction) responses are
	// validated against it, so providers see their own drift, not just their
	// dependencies'. Empty disables self validation.
	//
	// Deliberately still config in v1: it is one document per deployment, not
	// one per vendor, so it has neither the plurality problem nor the freshness
	// problem that moved provider contracts into the UI.
	SelfSpecPath string `mapstructure:"self_spec_path"`
	// SelfIntegrationID labels findings from the self spec (default "self").
	// Must differ from integration_id so self and provider findings never merge.
	SelfIntegrationID string `mapstructure:"self_integration_id"`

	// StorePodEndpoint is the tiered topology's spec channel: the base URL of
	// the store pod's intra-cluster contract endpoint. Set on a FRONT collector
	// only — a front runs drift but has no store, so without this an uploaded
	// contract can never reach it. Empty everywhere else, where the co-located
	// store is read directly.
	StorePodEndpoint string `mapstructure:"store_pod_endpoint"`
	// StorePodToken authenticates the front to that endpoint. Use
	// ${env:…} interpolation; it is never logged.
	StorePodToken string `mapstructure:"store_pod_token"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator. Nothing is required — a bare
// `flanjdrift: {}` is valid and makes the processor a pass-through until a
// contract is uploaded.
func (c *Config) Validate() error {
	if c.SelfSpecPath != "" && c.selfIntegration() == c.IntegrationID {
		return errors.New("flanjdrift: self_integration_id must differ from integration_id (self and provider findings must not merge)")
	}
	if c.StorePodToken != "" && c.StorePodEndpoint == "" {
		return errors.New("flanjdrift: store_pod_token needs store_pod_endpoint (a token with nothing to authenticate to is a misconfiguration, not a default)")
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
