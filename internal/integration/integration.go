// Package integration derives the key a call, a finding and an MCP catalogue
// are filed under (CONTRACTS §2, the flanj.integration row). The collector
// derives it at ingest and never takes it from the SDK: every pod — a single
// collector, a tiered front, the store pod behind it — computes the same key
// from the same record, so a server's calls, its catalogue and its findings
// always meet on one key, and an older SDK that still sends an id lands where a
// new one does.
package integration

import "strings"

// Unknown is the key of a record the rule yields nothing for: an outbound or
// MCP record with no usable peer host, or an inbound call whose resource
// names no service.
const Unknown = "unknown-integration"

// Self is what the control-plane wire carries in place of a service-keyed
// integration (CONTRACTS §3). An inbound call is keyed locally by the name of
// the org's own service it reached; that name describes internal topology and
// never leaves the collector.
const Self = "self"

// DirectionServer is flanj.direction on an inbound call: the org is the
// provider, and the counterparty's host says nothing about which contract the
// call belongs to.
const DirectionServer = "server"

// ForHost derives a key from a host: ASCII letters lowercased, digits kept,
// every other character '-', runs of '-' collapsed, ends trimmed. It is a pure
// function of the host, so two uploads for one host address one contract row,
// and a contract bound to a host shares its key with the traffic to it. "" when
// the host yields nothing.
func ForHost(host string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, host)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// ServiceKeyed reports whether a record is keyed by the service it reached
// rather than by its peer host: an inbound HTTP call. Every MCP record (a tool
// call or a contract_snapshot) keys by host whatever its direction, so a
// server's calls and its catalogue share one key.
func ServiceKeyed(mcp bool, direction string) bool {
	return !mcp && direction == DirectionServer
}

// Derive is the one rule. mcp is true for every MCP record; direction is
// flanj.direction; peerHost the record's (normalised) flanj.peer.host;
// serviceName the resource's service.name. Never "".
func Derive(mcp bool, direction, peerHost, serviceName string) string {
	key := ""
	if ServiceKeyed(mcp, direction) {
		key = strings.TrimSpace(serviceName)
	} else {
		key = ForHost(peerHost)
	}
	if key == "" {
		return Unknown
	}
	return key
}
