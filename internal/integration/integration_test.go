package integration

import "testing"

func TestForHost(t *testing.T) {
	for host, want := range map[string]string{
		"api.acme.test":       "api-acme-test",
		"API.Acme.Test:8443":  "api-acme-test-8443",
		"mcp.acme.test":       "mcp-acme-test",
		"--weird..host--":     "weird-host",
		"stripe-mcp-stdio":    "stripe-mcp-stdio",
		"":                    "",
		"...":                 "",
		"xn--caf-dma.example": "xn-caf-dma-example",
	} {
		if got := ForHost(host); got != want {
			t.Errorf("ForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestDerive(t *testing.T) {
	for _, c := range []struct {
		name                     string
		mcp                      bool
		direction, peer, service string
		want                     string
	}{
		{"outbound HTTP keys by host", false, "client", "api.acme.test", "orders-svc", "api-acme-test"},
		{"outbound HTTP, no host", false, "client", "", "orders-svc", Unknown},
		{"inbound HTTP keys by service", false, "server", "partner.acme.test", "orders-svc", "orders-svc"},
		{"inbound HTTP, no service", false, "server", "partner.acme.test", "", Unknown},
		{"inbound HTTP, blank service", false, "server", "partner.acme.test", "  ", Unknown},
		{"MCP keys by host", true, "client", "mcp.acme.test", "orders-svc", "mcp-acme-test"},
		{"MCP keys by host even inbound", true, "server", "mcp.acme.test", "orders-svc", "mcp-acme-test"},
		{"MCP, no host", true, "client", "", "orders-svc", Unknown},
		{"no direction keys by host", false, "", "api.acme.test", "orders-svc", "api-acme-test"},
	} {
		if got := Derive(c.mcp, c.direction, c.peer, c.service); got != c.want {
			t.Errorf("%s: Derive = %q, want %q", c.name, got, c.want)
		}
	}
}
