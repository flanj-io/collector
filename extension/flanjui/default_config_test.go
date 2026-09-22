package flanjui

import (
	"os"
	"testing"

	"go.opentelemetry.io/collector/confmap"
	"go.yaml.in/yaml/v3"
)

// TestBakedDefaultConfigKnowsTheHostedControlPlaneAndNoIdentity pins the
// flanjui block the IMAGE bakes (config/config.default.yaml): it names the
// hosted control plane, so a first `docker run` opens on the Connect form
// with nothing to configure — and it carries no identity and no token, so it
// claims nobody's org and can send nothing until Connect mints a key
// (TestConfiguredButUnconnectedMakesNoOutboundRequest is the wire half).
func TestBakedDefaultConfigKnowsTheHostedControlPlaneAndNoIdentity(t *testing.T) {
	raw, err := os.ReadFile("../../config/config.default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	block, _ := doc["extensions"].(map[string]any)["flanjui"].(map[string]any)
	if block == nil {
		t.Fatal("config.default.yaml has no extensions.flanjui block")
	}
	cfg := createDefaultConfig().(*Config)
	if err := confmap.NewFromStringMap(block).Unmarshal(cfg); err != nil {
		t.Fatalf("the baked flanjui block does not load into the extension's Config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the baked flanjui block does not validate: %v", err)
	}
	if cfg.CPBaseURL != "https://app.flanj.io" {
		t.Errorf("cp_base_url = %q, want the hosted control plane", cfg.CPBaseURL)
	}
	if reservedDocHost(cpBaseHost(cfg.CPBaseURL)) {
		t.Errorf("cp_base_url %q is a documentation placeholder — Connect could never reach it", cfg.CPBaseURL)
	}
	if got := dashboardURL(cfg.CPPublicURL, cfg.CPBaseURL); got != "https://app.flanj.io/d" {
		t.Errorf("dashboard door from the baked config = %q, want https://app.flanj.io/d (no cp_public_url needed for a public host)", got)
	}
	for key, val := range map[string]string{
		"cp_deploy_token":       cfg.CPDeployToken,
		"integration_id":        cfg.IntegrationID,
		"consumer_display_name": cfg.ConsumerDisplayName,
		"provider_display_name": cfg.ProviderDisplayName,
	} {
		if val != "" {
			t.Errorf("the image bakes %s=%q — the default config must carry no identity and no token", key, val)
		}
	}
}
