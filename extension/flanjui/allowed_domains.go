package flanjui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Who may OPEN a thread (CONTRACTS §5 `allowed_domains`, thread-domain-gate,
// 2026-09-14). The sheet's "Open to" field: a list of email domains — the reader
// confirms an address at one of them before the CP shows them anything — or the
// explicit "Anyone with the link", which is JSON `null` on the wire.
//
// The field is REQUIRED on both thread-creating relay routes. An absent field is
// refused rather than defaulted: the CP reads an absent field as "anyone", but
// only for collectors that predate the field and could never have asked their
// operator; this one always can, so a silent default here would be the very
// thing the ruling forbids. The CP normalizes and refuses the same way; checking
// here means the operator reads the refusal in the sheet, not as a CP round trip.

// allowedDomainsMax mirrors the CP's cap: a share list, not a directory.
const allowedDomainsMax = 20

// domainRe is a bare domain: labels of letters, digits and hyphens joined by
// dots, at least one dot, no scheme, path, port or `@`.
var domainRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// normalizeDomain: trim, lower-case, drop a leading `@` and a trailing `.` —
// the forms people actually type into the field.
func normalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimLeft(d, "@")
	d = strings.TrimRight(d, ".")
	return d
}

// allowedDomainsOf reads the raw `allowed_domains` value off a relay body.
// Returns the normalized list (nil for "anyone with the link") and, on a
// refusal, the error code and the one-sentence message to answer 400 with.
func allowedDomainsOf(raw json.RawMessage) (domains []string, code, msg string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, "missing_fields", msgOpenToRequired
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, "", ""
	}
	var list []string
	if err := json.Unmarshal(trimmed, &list); err != nil {
		// Not a list at all: the same code the control plane answers (CONTRACTS-CP §5.4).
		return nil, "bad_request", msgOpenToNotAList
	}
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range list {
		d := normalizeDomain(entry)
		if d == "" {
			continue
		}
		if len(d) > 253 || !domainRe.MatchString(d) {
			return nil, "invalid_domain", msgOpenToInvalid
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, "allowed_domains_empty", msgOpenToEmpty
	}
	if len(out) > allowedDomainsMax {
		return nil, "invalid_domain", msgOpenToTooMany
	}
	return out, "", ""
}
