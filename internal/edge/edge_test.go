package edge

import "testing"

// TestClassify covers the heuristic that MUST be identical across the SDK and
// collector (CONTRACTS §2): RFC1918 / loopback / link-local / ULA / cluster or
// .internal / .local names / single-label hosts are internal; everything else
// external.
func TestClassify(t *testing.T) {
	cases := map[string]string{
		// External (public destinations).
		"api.acme.test":          ClassExternal,
		"partner.acme.test":      ClassExternal,
		"api.stripe.com:443":     ClassExternal,
		"8.8.8.8":                ClassExternal,
		"[2606:4700:4700::1111]": ClassExternal,
		// Internal — RFC1918.
		"10.0.0.5":      ClassInternal,
		"10.0.0.5:8080": ClassInternal,
		"172.16.3.4":    ClassInternal,
		"192.168.1.10":  ClassInternal,
		// Internal — loopback / link-local / ULA / unspecified.
		"127.0.0.1":    ClassInternal,
		"[::1]:5335":   ClassInternal,
		"169.254.10.2": ClassInternal,
		"fc00::1":      ClassInternal,
		// Internal — names.
		"localhost":                  ClassInternal,
		"payments.svc.cluster.local": ClassInternal,
		"billing.internal":           ClassInternal,
		"printer.local":              ClassInternal,
		"orders":                     ClassInternal, // single label, no dot
		"":                           ClassInternal, // no identity → safe default
	}
	for host, want := range cases {
		if got := Classify(host); got != want {
			t.Errorf("Classify(%q) = %q, want %q", host, got, want)
		}
	}
}

// TestRole proves role/orientation fall out of direction.
func TestRole(t *testing.T) {
	if Role(DirectionClient) != RoleConsumer {
		t.Errorf("client → %q, want consumer", Role(DirectionClient))
	}
	if Role(DirectionServer) != RoleProvider {
		t.Errorf("server → %q, want provider", Role(DirectionServer))
	}
	if Orientation(DirectionClient) != OrientationOutbound {
		t.Errorf("client orientation = %q, want outbound", Orientation(DirectionClient))
	}
	if Orientation(DirectionServer) != OrientationInbound {
		t.Errorf("server orientation = %q, want inbound", Orientation(DirectionServer))
	}
}

// TestSplitHostPort: `flanj.peer.host` is host[:port] (CONTRACTS §2) and the
// port is half the edge key, so the split has to be right about what IS a port —
// a bare IPv6 literal is all colons and carries none.
func TestSplitHostPort(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"api.acme.test", "api.acme.test", ""},
		{"api.acme.test:8080", "api.acme.test", "8080"},
		{"api.acme.test:", "api.acme.test", ""},
		{"10.0.0.5:443", "10.0.0.5", "443"},
		{"[::1]", "[::1]", ""},
		{"[::1]:8080", "[::1]", "8080"},
		{"[fd00::1]:443", "[fd00::1]", "443"},
		{"fd00::1", "fd00::1", ""}, // bare IPv6: colons, no port
		{"::1", "::1", ""},         // ditto
		{"[::1", "[::1", ""},       // unterminated bracket: not a host:port
		{"", "", ""},
	}
	for _, c := range cases {
		host, port := SplitHostPort(c.in)
		if host != c.host || port != c.port {
			t.Errorf("SplitHostPort(%q) = %q, %q; want %q, %q", c.in, host, port, c.host, c.port)
		}
	}
}

// TestStripDefaultPort is the collector's half of a CROSS-COMPONENT rule: the
// SDK applies the identical one at capture (src/instrumentation/http-args.ts).
// One origin must produce ONE edge key however it was dialled, or a contract
// bound to `api.acme.test` never validates `api.acme.test:443` and its drifted
// responses produce no finding at all.
func TestStripDefaultPort(t *testing.T) {
	cases := []struct{ hostPort, scheme, want string }{
		// The scheme's own default is the same listener under a longer name.
		{"api.acme.test:443", "https", "api.acme.test"},
		{"api.acme.test:80", "http", "api.acme.test"},
		{"api.acme.test:443", "HTTPS", "api.acme.test"},
		{"[::1]:443", "https", "[::1]"},
		// A non-default port is a genuinely different listener and stays.
		{"api.acme.test:8080", "https", "api.acme.test:8080"},
		{"api.acme.test:28080", "https", "api.acme.test:28080"},
		{"[::1]:8443", "https", "[::1]:8443"},
		// So is a default port under the OTHER scheme.
		{"api.acme.test:443", "http", "api.acme.test:443"},
		{"api.acme.test:80", "https", "api.acme.test:80"},
		// An unknown or absent scheme changes nothing — guessing would merge a
		// live edge away. `mcp` is the MCP call path's own url scheme.
		{"api.acme.test:443", "", "api.acme.test:443"},
		{"api.acme.test:443", "mcp", "api.acme.test:443"},
		// Idempotent: an already-normalised host survives untouched.
		{"api.acme.test", "https", "api.acme.test"},
		{"api.acme.test:8080", "https", "api.acme.test:8080"},
		{"fd00::1", "https", "fd00::1"},
	}
	for _, c := range cases {
		if got := StripDefaultPort(c.hostPort, c.scheme); got != c.want {
			t.Errorf("StripDefaultPort(%q, %q) = %q, want %q", c.hostPort, c.scheme, got, c.want)
		}
		// Applying it twice must change nothing more than applying it once.
		if once := StripDefaultPort(c.hostPort, c.scheme); StripDefaultPort(once, c.scheme) != once {
			t.Errorf("StripDefaultPort is not idempotent for %q/%q", c.hostPort, c.scheme)
		}
	}
}
