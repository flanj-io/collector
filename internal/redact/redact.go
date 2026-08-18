// Package redact is the collector's Go implementation of the Vinifera redaction
// floor. It is a defense-in-depth reimplementation of the SDK's
// @vinifera/redaction-patterns and conforms to the golden vectors in
// contracts/redaction-vectors.json.
//
// Three invariants (governed by the vectors, tested in redact_test.go):
//
//  1. Add-only: redaction only ever ADDS ⟦REDACTED:…⟧ tokens; it never
//     removes an existing one (poisoned-spec safety).
//  2. Idempotent: Redact(Redact(x)) == Redact(x); an existing
//     ⟦REDACTED:…⟧ token is inert to re-scanning and is never double-wrapped.
//  3. Redact before store/emit: callers must run this before a body is written
//     to disk or attached to any OTLP record.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Token delimiters are U+27E6 / U+27E7 (⟦ ⟧) — regex-stable, JSON-safe.
const (
	tokenOpen  = "⟦" // ⟦
	tokenClose = "⟧" // ⟧
)

// tokenRe matches an already-emitted redaction token so re-scans skip it
// (idempotency + never-double-wrap).
var tokenRe = regexp.MustCompile(`\x{27e6}REDACTED:[A-Z0-9]+\x{27e7}`)

// Pattern ids (stable — surfaced in vinifera.redaction.patterns).
const (
	PAN   = "PAN"
	EMAIL = "EMAIL"
	IBAN  = "IBAN"
	SSN   = "SSN"
	PHONE = "PHONE"
	CVV   = "CVV"
	TOKEN = "TOKEN"
	IP    = "IP"
)

func token(id string) string { return tokenOpen + "REDACTED:" + id + tokenClose }

// Result is the outcome of a single Redact call.
type Result struct {
	// Text is the redacted string.
	Text string
	// Patterns is the sorted, de-duplicated list of pattern ids that fired on
	// THIS call (empty when nothing new was redacted — e.g. already-redacted
	// input). It is the delta, not the running union.
	Patterns []string
}

// Redactor applies the mandatory floor. The zero value is not usable; use New.
type Redactor struct {
	// enableIP toggles the optional IPv4/IPv6 rule (off by default: no vector
	// exercises it and it over-redacts identifiers).
	enableIP bool
}

// Option configures a Redactor.
type Option func(*Redactor)

// WithIP enables the optional IP rule.
func WithIP() Option { return func(r *Redactor) { r.enableIP = true } }

// New builds a Redactor with the mandatory floor enabled.
func New(opts ...Option) *Redactor {
	r := &Redactor{}
	for _, o := range opts {
		o(r)
	}
	return r
}

// rule is one ordered redaction step. It operates only on the non-token
// segments of the input (token segments are protected for idempotency).
type rule struct {
	id  string
	fn  func(string) (string, bool) // returns (out, fired)
}

// Redact applies the floor to s, protecting any pre-existing tokens.
func (r *Redactor) Redact(s string) Result {
	fired := map[string]bool{}
	out := r.mapNonTokenSegments(s, func(seg string) string {
		for _, ru := range r.rules() {
			res, hit := ru.fn(seg)
			if hit {
				fired[ru.id] = true
			}
			seg = res
		}
		return seg
	})
	ids := make([]string, 0, len(fired))
	for id := range fired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return Result{Text: out, Patterns: ids}
}

// mapNonTokenSegments splits s on existing ⟦REDACTED:…⟧ tokens and applies fn
// only to the segments between them. Tokens pass through untouched — this is
// what makes re-scanning inert and guarantees we never double-wrap.
func (r *Redactor) mapNonTokenSegments(s string, fn func(string) string) string {
	locs := tokenRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return fn(s)
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		b.WriteString(fn(s[prev:loc[0]])) // redact the gap before the token
		b.WriteString(s[loc[0]:loc[1]])   // copy the token verbatim
		prev = loc[1]
	}
	b.WriteString(fn(s[prev:]))
	return b.String()
}

// --- pattern definitions ---------------------------------------------------

var (
	// PAN candidate: 13–19 digits with optional single space/dash separators.
	// Luhn is checked per match; separators are stripped before length/Luhn.
	panCandidateRe = regexp.MustCompile(`[0-9](?:[ -]?[0-9]){12,18}`)
	panSepRe       = regexp.MustCompile(`[ -]`)

	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

	// ISO-13616 IBAN: 2 letters, 2 check digits, up to 30 alnum. Guard both
	// ends so it doesn't chew neighbouring letters/digits.
	ibanRe = regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}[A-Z0-9]{10,30}\b`)

	ssnRe = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)

	// E.164: leading + and 10–15 digits.
	phoneRe = regexp.MustCompile(`\+[1-9][0-9]{9,14}\b`)

	// Bearer secret keys, stripe-style sk_/pk_ keys, and JWTs.
	bearerRe = regexp.MustCompile(`(?i)(Bearer\s+)([A-Za-z0-9._\-]{8,})`)
	apiKeyRe = regexp.MustCompile(`\b[sp]k_[A-Za-z0-9_]{6,}`)
	jwtRe    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`)

	// CVV is contextual: only the value of a cvv/cvc/cvv2 JSON key.
	cvvRe = regexp.MustCompile(`(?i)("(?:cvv2?|cvc)"\s*:\s*")([0-9]{3,4})(")`)

	ipv4Re = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\b`)
)

// rules returns the ordered floor. Order matters: TOKEN and IBAN run before PAN
// so their digit runs are consumed before the Luhn scan sees them.
func (r *Redactor) rules() []rule {
	rs := []rule{
		{id: TOKEN, fn: tokenRule},
		{id: IBAN, fn: simpleRule(ibanRe, token(IBAN))},
		{id: SSN, fn: simpleRule(ssnRe, token(SSN))},
		{id: PAN, fn: panRule},
		{id: PHONE, fn: simpleRule(phoneRe, token(PHONE))},
		{id: EMAIL, fn: simpleRule(emailRe, token(EMAIL))},
		{id: CVV, fn: groupReplaceRule(cvvRe, token(CVV))},
	}
	if r.enableIP {
		rs = append(rs, rule{id: IP, fn: simpleRule(ipv4Re, token(IP))})
	}
	return rs
}

// simpleRule replaces every full match of re with tok.
func simpleRule(re *regexp.Regexp, tok string) func(string) (string, bool) {
	return func(s string) (string, bool) {
		if !re.MatchString(s) {
			return s, false
		}
		return re.ReplaceAllString(s, tok), true
	}
}

// groupReplaceRule replaces only capture group 2, keeping groups 1 and 3
// (used for contextual keys like "cvv":"123").
func groupReplaceRule(re *regexp.Regexp, tok string) func(string) (string, bool) {
	return func(s string) (string, bool) {
		if !re.MatchString(s) {
			return s, false
		}
		return re.ReplaceAllString(s, "${1}"+tok+"${3}"), true
	}
}

// tokenRule redacts bearer tokens, sk_/pk_ keys and JWTs.
func tokenRule(s string) (string, bool) {
	fired := false
	s = jwtRe.ReplaceAllStringFunc(s, func(string) string { fired = true; return token(TOKEN) })
	s = apiKeyRe.ReplaceAllStringFunc(s, func(string) string { fired = true; return token(TOKEN) })
	s = bearerRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := bearerRe.FindStringSubmatch(m)
		fired = true
		return sub[1] + token(TOKEN)
	})
	return s, fired
}

// panRule redacts only PAN candidates that pass Luhn after stripping separators.
func panRule(s string) (string, bool) {
	fired := false
	out := panCandidateRe.ReplaceAllStringFunc(s, func(m string) string {
		digits := panSepRe.ReplaceAllString(m, "")
		if len(digits) < 13 || len(digits) > 19 || !luhn(digits) {
			return m
		}
		fired = true
		return token(PAN)
	})
	return out, fired
}

// luhn reports whether an all-digit string passes the Luhn checksum.
func luhn(digits string) bool {
	sum := 0
	dbl := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		dbl = !dbl
	}
	return sum%10 == 0
}
