package flanjui

import (
	"errors"
	"net/url"
)

// Config for the localhost UI extension. Keys frozen in CONTRACTS §8.
type Config struct {
	// UIEndpoint is the localhost bind for the UI server. MUST be a loopback
	// address — the collector is outbound-only and nothing serves off-host.
	UIEndpoint string `mapstructure:"ui_endpoint"`
	// IntegrationID labels flags raised from this collector.
	IntegrationID string `mapstructure:"integration_id"`
	// ConsumerDisplayName is the "shared by <name>" identity on the peek screen.
	ConsumerDisplayName string `mapstructure:"consumer_display_name"`
	// ProviderDisplayName is the fallback provider name sent ON A FLAG, so the
	// thread names the provider when the UI does not supply one.
	//
	// It NO LONGER names an edge. That tier needed a config→edge linkage, which
	// came from the config spec's peer_host, and provider contracts are uploaded
	// now (CONTRACTS §8, 2026-08-31) — an upload carries the host AND the
	// document's title, so the `contract` tier names edges from what the
	// operator actually did. Naming a provider on a thread needs no linkage at
	// all, which is why this key survives that removal.
	ProviderDisplayName string `mapstructure:"provider_display_name"`
	// CPBaseURL is the control-plane base URL this collector's OWN requests go
	// to (register, me, flags, threads, the syncs). It may well be an
	// in-network address — a docker service name, a k8s Service, a VPC-private
	// ingress — because only the collector has to reach it.
	CPBaseURL string `mapstructure:"cp_base_url"`
	// CPPublicURL is the control-plane origin the OPERATOR'S BROWSER can open:
	// the base of the one link the local UI offers out (the Connected pill's
	// dashboard door, `dashboard_url` on GET /api/connect). Optional. Set it
	// wherever cp_base_url is not resolvable from a laptop; with it unset the
	// door is minted from cp_base_url only when that host is not obviously
	// non-public (loopback / private IP / single-label / .local-style names)
	// and omitted otherwise — the pill then stays a Settings button, which is
	// honest, where a dead link is not (dashboardURL in connect.go). Neither
	// URL is ever logged.
	CPPublicURL string `mapstructure:"cp_public_url"`
	// CPDeployToken is the static Bearer token (the only outbound auth).
	CPDeployToken string `mapstructure:"cp_deploy_token"`
	// FindingSync enables the periodic shape-only findings sync to the control
	// plane (POST /api/v1/findings — CONTRACTS §5/§8). ON by default (the
	// factory default is true, so an omitted key means on); `finding_sync:
	// false` disables the loop entirely. Only the SHAPE of a finding is sent —
	// id, signature, kind, severity, integration, endpoint, rule, counts,
	// timestamps; the observed values (expected / actual / detail) never leave
	// this collector. The sync runs only once a collector key exists (after
	// Connect). It governs the findings POST ONLY — the directory-name refresh
	// that rides the same ticker has its own switch, DirectorySync.
	FindingSync bool `mapstructure:"finding_sync"`
	// DirectorySync enables the periodic directory display-name refresh
	// (GET /api/v1/directory — CONTRACTS §8, CONTRACTS-CP §5.14). ON by default
	// (the factory default is true, so an omitted key means on);
	// `directory_sync: false` disables the refresh only. It rides the same
	// ticker as FindingSync but is gated independently: two different egresses
	// with two different privacy stories do not share one switch (owner ruling
	// 2026-08-31). The refresh is a pure FETCH — a conditional (ETag) full-table
	// GET; this collector's edges, peer hosts and domains are NEVER sent, and
	// there is no per-miss lookup. With it off, the baked directory seed still
	// resolves names offline.
	DirectorySync bool `mapstructure:"directory_sync"`

	// prevent unkeyed literal initialization
	_ struct{}
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.UIEndpoint == "" {
		return errors.New("flanjui: ui_endpoint is required")
	}
	if !isLoopback(c.UIEndpoint) {
		return errors.New("flanjui: ui_endpoint must bind a loopback address (127.0.0.1/localhost/::1) — the collector is outbound-only")
	}
	if c.CPPublicURL != "" {
		// A typo here ships a dead link on every page load, so refuse it at
		// boot; and the value is handed to a browser verbatim, so it must not
		// carry credentials.
		u, err := url.Parse(c.CPPublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("flanjui: cp_public_url must be an absolute http(s) URL — the control-plane origin the operator's browser can reach")
		}
		if u.User != nil {
			return errors.New("flanjui: cp_public_url must not carry credentials — it is handed to the browser as a link")
		}
	}
	return nil
}
