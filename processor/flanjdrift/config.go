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
	// IntegrationID is DEPRECATED and IGNORED (removed from CONTRACTS §8,
	// 2026-09-14): a finding's integration is the one the SDK stamped on the
	// call, and never came from here; the deployment's identity is its
	// collector NAME on the control plane. Still decodable so a config that
	// carries it boots, with a one-line warning at construction.
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
	// SelfIntegrationID is DEPRECATED and IGNORED (2026-09-14, with
	// integration_id): self-spec findings are always labelled "self", which
	// no SDK-stamped integration is, so self and provider findings never
	// merge without a knob. Decodable so an old config boots.
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
	if c.StorePodToken != "" && c.StorePodEndpoint == "" {
		return errors.New("flanjdrift: store_pod_token needs store_pod_endpoint (a token with nothing to authenticate to is a misconfiguration, not a default)")
	}
	return nil
}

// selfIntegration is the label for self-spec findings: always "self" since
// 2026-09-14 (self_integration_id is ignored). Kept as a method so the two call
// sites read the rule from one place.
func (c *Config) selfIntegration() string {
	return "self"
}

// deprecatedKeys names the removed CONTRACTS §8 keys this config still carries,
// for the one-line boot warning. Empty when it carries none.
func (c *Config) deprecatedKeys() []string {
	var keys []string
	if c.IntegrationID != "" {
		keys = append(keys, "integration_id")
	}
	if c.SelfIntegrationID != "" {
		keys = append(keys, "self_integration_id")
	}
	return keys
}
