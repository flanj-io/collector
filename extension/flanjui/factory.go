// Package flanjui is the localhost UI extension: it serves the embedded Vue
// SPA (embed.FS) plus a small read API (GET /api/calls|findings|health) and the
// flag action (POST /api/flag), which promotes a redacted call + finding to the
// control plane (POST cp_base_url/api/v1/flags, Bearer cp_deploy_token) and, on
// success, marks the call promoted (unpin) in the store.
//
// It binds a LOOPBACK address only. The collector is outbound-only; nothing here
// is reachable off-host.
package flanjui

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/confignet"
	"go.opentelemetry.io/collector/extension"
)

var typeStr = component.MustNewType("flanjui")

// collectorVersion travels as X-Flanj-Collector-Version on the flag POST and
// as `collector_version` on GET /api/health. Docker builds stamp it via the
// VERSION build arg (-ldflags -X, "dev" when unset); this value is only what
// non-Docker builds (go test, go run) see.
var collectorVersion = "v0.0.0"

// NewFactory returns the UI extension factory.
func NewFactory() extension.Factory {
	return extension.NewFactory(
		typeStr,
		createDefaultConfig,
		create,
		component.StabilityLevelBeta,
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		UIEndpoint: "127.0.0.1:5335",
		// Shape-only findings sync is on unless finding_sync: false (CONTRACTS §8).
		FindingSync: true,
		// The directory display-name refresh is on unless display_name_sync:
		// false (CONTRACTS §8) — its own switch since the 2026-08-31 ruling, so
		// an omitted key still means on and behaviour is unchanged.
		DisplayNameSync: true,
	}
}

func create(_ context.Context, set extension.Settings, cfg component.Config) (extension.Extension, error) {
	c := cfg.(*Config)
	sc := confighttp.NewDefaultServerConfig()
	sc.NetAddr = confignet.AddrConfig{Endpoint: c.UIEndpoint, Transport: confignet.TransportTypeTCP}
	return &uiExtension{
		cfg:       c,
		srvConfig: sc,
		telemetry: set.TelemetrySettings,
	}, nil
}
