package viniferaui

import "errors"

// Config for the localhost UI extension. Keys frozen in CONTRACTS §8.
type Config struct {
	// UIEndpoint is the localhost bind for the UI server. MUST be a loopback
	// address — the collector is outbound-only and nothing serves off-host.
	UIEndpoint string `mapstructure:"ui_endpoint"`
	// IntegrationID labels flags raised from this collector.
	IntegrationID string `mapstructure:"integration_id"`
	// ConsumerDisplayName is the "shared by <name>" identity on the peek screen.
	ConsumerDisplayName string `mapstructure:"consumer_display_name"`
	// ProviderDisplayName names the provider whose API is observed; sent on the
	// flag so the peek/thread names BOTH sides (CONTRACTS §8). If empty it
	// defaults to the humanized integration_id.
	ProviderDisplayName string `mapstructure:"provider_display_name"`
	// CPBaseURL is the control-plane base URL for the flag POST.
	CPBaseURL string `mapstructure:"cp_base_url"`
	// CPDeployToken is the static Bearer token (the only outbound auth).
	CPDeployToken string `mapstructure:"cp_deploy_token"`
	// FindingSync enables the periodic shape-only findings sync to the control
	// plane (POST /api/v1/findings — CONTRACTS §5/§8). ON by default (the
	// factory default is true, so an omitted key means on); `finding_sync:
	// false` disables the loop entirely. Only the SHAPE of a finding is sent —
	// id, signature, kind, severity, integration, endpoint, rule, counts,
	// timestamps; the observed values (expected / actual / detail) never leave
	// this collector. The sync runs only once a collector key exists (after
	// Connect).
	FindingSync bool `mapstructure:"finding_sync"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.UIEndpoint == "" {
		return errors.New("viniferaui: ui_endpoint is required")
	}
	if !isLoopback(c.UIEndpoint) {
		return errors.New("viniferaui: ui_endpoint must bind a loopback address (127.0.0.1/localhost/::1) — the collector is outbound-only")
	}
	return nil
}
