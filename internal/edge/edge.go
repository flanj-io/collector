// Package edge holds the edge-classification heuristic and role derivation shared
// across the collector. An "edge" is a (peer_host, direction) pair discovered from
// observed traffic — no target list is configured.
//
// Classification MUST match the identical heuristic used by the SDK (CONTRACTS §2):
// a host is INTERNAL if it is RFC1918 / loopback / link-local / ULA, or a name
// ending .svc.cluster.local / .internal / .local, or a single-label hostname.
// Everything else is EXTERNAL. Only external edges are surfaced; the PAN/PII
// redaction floor still applies to everything captured regardless of class.
package edge

import (
	"net"
	"strings"
)

// Edge classes (CONTRACTS §2 flanj.edge.class).
//
// A THIRD value exists on the wire and in the store — `local-process`, the
// SDK's word for a server spawned as a child process (MCP over stdio). It is
// listed here so the vocabulary is complete, NOT because this package
// classifies it: the SDK stamps it and the collector carries it through.
//
// It never appears on GET /api/edges, which filters to ClassExternal
// (store.go ListEdges). That is deliberate — a child process is not a network
// edge and does not belong on the integration graph — but the consequence is
// worth stating, because it went unstated and cost a real confusion: a stdio
// MCP server appears on the Contracts tab, on Overview and in Traffic, and
// nowhere on Edges. The Contracts tab and the Edges roll call therefore count
// MCP servers from DIFFERENT sources, and must not both count from edges.
const (
	ClassExternal = "external"
	ClassInternal = "internal"
	// ClassLocalProcess is stamped by the SDK and never derived here.
	ClassLocalProcess = "local-process"
)

// Directions (CONTRACTS §2 flanj.direction).
const (
	DirectionClient = "client" // egress — this org is the CONSUMER on the edge
	DirectionServer = "server" // ingress — this org is the PROVIDER on the edge
)

// Roles derived from direction.
const (
	RoleConsumer = "consumer" // direction=client → outbound edge org→peer
	RoleProvider = "provider" // direction=server → inbound edge peer→org
)

// Direction-derived edge orientation.
const (
	OrientationOutbound = "outbound" // you → provider (org is consumer)
	OrientationInbound  = "inbound"  // consumer → you (org is provider)
)

// Role returns the org's role on an edge given the observed call direction.
// client (egress) → consumer; server (ingress) → provider.
func Role(direction string) string {
	if direction == DirectionServer {
		return RoleProvider
	}
	return RoleConsumer
}

// Orientation returns the edge orientation for a direction.
func Orientation(direction string) string {
	if direction == DirectionServer {
		return OrientationInbound
	}
	return OrientationOutbound
}

// Classify returns ClassInternal or ClassExternal for a peer host (which may
// carry a :port). The heuristic is intentionally identical to the SDK's so a
// missing flanj.edge.class can be reconstructed deterministically.
func Classify(host string) string {
	h := normalizeHost(host)
	if h == "" {
		// No usable peer identity — treat as internal so it is never surfaced
		// or body-captured by mistake (safe default).
		return ClassInternal
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return ClassInternal
		}
		return ClassExternal
	}
	// Name-based rules.
	lower := strings.ToLower(h)
	if lower == "localhost" {
		return ClassInternal
	}
	for _, suffix := range []string{".svc.cluster.local", ".internal", ".local"} {
		if strings.HasSuffix(lower, suffix) {
			return ClassInternal
		}
	}
	if !strings.Contains(lower, ".") {
		// Single-label hostname (no dot) — internal same-team by convention.
		return ClassInternal
	}
	return ClassExternal
}

// IsExternal reports whether a class string is the external class.
func IsExternal(class string) bool { return class == ClassExternal }

// normalizeHost strips a :port and surrounding IPv6 brackets, returning the bare
// host. It tolerates bracketed IPv6 with or without a port.
func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	// Bracketed IPv6, optionally with a port: [::1] or [::1]:443.
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end >= 0 {
			return h[1:end]
		}
	}
	// host:port — only split when there's exactly one colon (IPv4/name);
	// bare IPv6 has multiple colons and no brackets, so leave it intact.
	if strings.Count(h, ":") == 1 {
		if host, _, err := net.SplitHostPort(h); err == nil {
			return host
		}
	}
	return h
}
