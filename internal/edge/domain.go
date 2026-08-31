package edge

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// RegistrableDomain returns the naming key for a peer host (v1 phase 1 — edge
// naming): the registrable domain (eTLD+1) computed LOCALLY via the public
// suffix list. IP literals key on the literal. A host the PSL cannot reduce
// (single-label names, a bare public suffix) falls back to the normalized
// host — the key must always exist for a host that has one.
//
// The input may carry a :port or IPv6 brackets, exactly like Classify's input;
// both are stripped first. The result is lowercase with no trailing dot.
func RegistrableDomain(host string) string {
	h := strings.ToLower(normalizeHost(host))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		return h
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return d
}
