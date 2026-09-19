package flanjui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Who may OPEN a thread (CONTRACTS §5 `allowed_emails` / `allowed_domains`,
// thread-domain-gate 2026-09-14, three modes 2026-09-15). The sheet's "Open to"
// choice: specific people (exact addresses), anyone at a domain, or — as an
// explicit choice — anyone with the link, which is both keys null on the wire.
//
// The choice is REQUIRED on both thread-creating relay routes: a body that
// carries neither key is refused rather than defaulted. The CP reads both keys
// absent as "anyone", but only for collectors that predate the fields and could
// never have asked their operator; this one always can, so a silent default here
// would be the very thing this rule forbids. The CP normalizes and refuses the
// same shapes; checking here means the operator reads the refusal in the sheet,
// not as a CP round trip.

// openToMax mirrors the CP's cap on either list: a share list, not a directory.
const openToMax = 20

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

// normalizeEmail reads `Noor Haddad <noor@globex.test>` — what a mail client
// copies — as the address inside the brackets, then trims and lower-cases it.
// Brackets with nothing in them (`Dana <>`) stay part of the entry, so it is
// refused as not an address rather than dropped without a word, as the CP does.
func normalizeEmail(raw string) string {
	if open := strings.IndexByte(raw, '<'); open >= 0 {
		if end := strings.IndexByte(raw[open+1:], '>'); end >= 0 {
			if inner := strings.TrimSpace(raw[open+1 : open+1+end]); inner != "" {
				raw = inner
			}
		}
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func isBareDomain(d string) bool { return len(d) <= 253 && domainRe.MatchString(d) }

// isPlainEmail: exactly one `@`, a local part with no spaces, a bare domain after it.
func isPlainEmail(address string) bool {
	at := strings.IndexByte(address, '@')
	if at <= 0 || at != strings.LastIndexByte(address, '@') || len(address) > 254 {
		return false
	}
	local := address[:at]
	if len(local) > 64 || strings.ContainsAny(local, " \t\r\n") {
		return false
	}
	return isBareDomain(address[at+1:])
}

// listRules is what differs between the two lists: how an entry is normalized
// and judged, and the codes and sentences of each refusal.
type listRules struct {
	normalize   func(string) string
	valid       func(string) bool
	emptyCode   string
	emptyMsg    string
	invalidCode string
	invalidMsg  string
	notAListMsg string
	tooManyMsg  string
}

var domainRules = listRules{normalizeDomain, isBareDomain, "allowed_domains_empty", msgOpenToEmpty, "invalid_domain", msgOpenToInvalid, msgOpenToNotAList, msgOpenToTooMany}

var emailRules = listRules{normalizeEmail, isPlainEmail, "allowed_emails_empty", msgOpenToEmailsEmpty, "invalid_email", msgOpenToInvalidEmail, msgOpenToEmailsNotAList, msgOpenToTooManyPeople}

func keyPresent(raw json.RawMessage) bool { return len(bytes.TrimSpace(raw)) > 0 }

func isJSONList(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '['
}

// readList reads one key: absent or null → nil. Anything but a list, or more
// than openToMax entries, is bad_request. An entry that is not a string is
// refused like any other bad entry (invalid_*, as the CP does), never as "not a
// list" — that sentence would be false about a list. Entries are normalized,
// judged and de-duplicated, and a list with nothing usable left is refused.
func readList(raw json.RawMessage, r listRules) (list []string, code, msg string) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil, "", ""
	}
	var items []any
	if err := json.Unmarshal(t, &items); err != nil {
		return nil, "bad_request", r.notAListMsg
	}
	if len(items) > openToMax {
		return nil, "bad_request", r.tooManyMsg
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		entry, ok := item.(string)
		if !ok {
			return nil, r.invalidCode, r.invalidMsg
		}
		v := r.normalize(entry)
		if v == "" {
			continue
		}
		if !r.valid(v) {
			return nil, r.invalidCode, r.invalidMsg
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, r.emptyCode, r.emptyMsg
	}
	return out, "", ""
}

// openToOf reads the choice off a relay body. It returns the normalized lists
// (both nil means anyone with the link) and, on a refusal, the error code and the
// one-sentence message to answer 400 with.
func openToOf(domainsRaw, emailsRaw json.RawMessage) (domains, emails []string, code, msg string) {
	if !keyPresent(domainsRaw) && !keyPresent(emailsRaw) {
		return nil, nil, "missing_fields", msgOpenToRequired
	}
	if isJSONList(domainsRaw) && isJSONList(emailsRaw) {
		return nil, nil, "access_conflict", msgOpenToConflict
	}
	if domains, code, msg = readList(domainsRaw, domainRules); code != "" {
		return nil, nil, code, msg
	}
	if emails, code, msg = readList(emailsRaw, emailRules); code != "" {
		return nil, nil, code, msg
	}
	return domains, emails, "", ""
}
