package edge

import "testing"

// TestClassify covers the heuristic that MUST be identical across the SDK and
// collector (CONTRACTS §2): RFC1918 / loopback / link-local / ULA / cluster or
// .internal / .local names / single-label hosts are internal; everything else
// external.
func TestClassify(t *testing.T) {
	cases := map[string]string{
		// External (public destinations).
		"api.acme.test":        ClassExternal,
		"partner.acme.test":    ClassExternal,
		"api.stripe.com:443":   ClassExternal,
		"8.8.8.8":              ClassExternal,
		"[2606:4700:4700::1111]": ClassExternal,
		// Internal — RFC1918.
		"10.0.0.5":       ClassInternal,
		"10.0.0.5:8080":  ClassInternal,
		"172.16.3.4":     ClassInternal,
		"192.168.1.10":   ClassInternal,
		// Internal — loopback / link-local / ULA / unspecified.
		"127.0.0.1":     ClassInternal,
		"[::1]:5335":    ClassInternal,
		"169.254.10.2":  ClassInternal,
		"fc00::1":       ClassInternal,
		// Internal — names.
		"localhost":                   ClassInternal,
		"payments.svc.cluster.local":  ClassInternal,
		"billing.internal":            ClassInternal,
		"printer.local":               ClassInternal,
		"orders":                      ClassInternal, // single label, no dot
		"":                            ClassInternal, // no identity → safe default
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
