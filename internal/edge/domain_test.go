package edge

import "testing"

// TestRegistrableDomain covers the naming key (v1 phase 1): names key on the
// registrable domain — eTLD+1 via the public-suffix list, computed locally.
// IP literals key on the literal; a host the PSL cannot reduce (single-label,
// a bare public suffix) falls back to the normalized host, never an error.
func TestRegistrableDomain(t *testing.T) {
	cases := map[string]string{
		// Plain eTLD+1 reduction.
		"api.stripe.com":       "stripe.com",
		"stripe.com":           "stripe.com",
		"deep.sub.api.acme.io": "acme.io",
		// Multi-label public suffix (the co.uk case).
		"api.example.co.uk": "example.co.uk",
		"example.co.uk":     "example.co.uk",
		// Ports are stripped before keying.
		"api.stripe.com:443":     "stripe.com",
		"api.example.co.uk:8443": "example.co.uk",
		// IP literals key on the literal (port stripped, brackets stripped).
		"8.8.8.8":                     "8.8.8.8",
		"8.8.8.8:443":                 "8.8.8.8",
		"[2606:4700:4700::1111]":      "2606:4700:4700::1111",
		"[2606:4700:4700::1111]:8443": "2606:4700:4700::1111",
		// publicsuffix error paths fall back to the normalized host.
		"orders":    "orders", // single label
		"localhost": "localhost",
		// Case + trailing dot normalize before keying.
		"API.Stripe.COM":  "stripe.com",
		"api.stripe.com.": "stripe.com",
		// No identity in → no key out.
		"": "",
	}
	for host, want := range cases {
		if got := RegistrableDomain(host); got != want {
			t.Errorf("RegistrableDomain(%q) = %q, want %q", host, got, want)
		}
	}
}
