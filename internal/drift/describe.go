package drift

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// SpecSummary is a parsed contract described in plain values — no kin-openapi
// types cross this boundary.
//
// It exists so the upload endpoint can parse-before-persist without dealing in
// openapi3 types: the caller (extension/flanjui) is its own module and has no
// business knowing which OpenAPI library parses a document, only whether the
// document parsed and what it says.
type SpecSummary struct {
	Title     string
	Version   string
	Endpoints int
	DocsURL   string
	// ServerHosts are the hosts from the document's `servers:` list. They
	// CORROBORATE a binding and never decide it — proxy, gateway and staging
	// hosts are legitimate and common, so a mismatch warns and never blocks.
	ServerHosts []string
}

// DescribeSpec parses a contract document and describes it. The error is the
// parser's own, because "Couldn't read that as an OpenAPI document" is only
// actionable when it says which line.
func DescribeSpec(raw []byte) (SpecSummary, error) {
	doc, err := LoadSpecData(raw)
	if err != nil {
		return SpecSummary{}, err
	}
	return summarize(doc), nil
}

func summarize(doc *openapi3.T) SpecSummary {
	var s SpecSummary
	if doc.Info != nil {
		s.Title = doc.Info.Title
		s.Version = doc.Info.Version
	}
	if doc.Paths != nil {
		s.Endpoints = doc.Paths.Len()
	}
	if doc.ExternalDocs != nil {
		s.DocsURL = doc.ExternalDocs.URL
	}
	s.ServerHosts = make([]string, 0, len(doc.Servers))
	seen := map[string]bool{}
	for _, srv := range doc.Servers {
		if srv == nil {
			continue
		}
		h := hostFromServerURL(srv.URL)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		s.ServerHosts = append(s.ServerHosts, h)
	}
	return s
}

// hostFromServerURL pulls the host out of a `servers:` entry. These are often
// templated ("https://{region}.acme.test/v1") or relative ("/v1"), so anything
// that does not yield a plain host yields nothing — a corroboration line that
// guesses is worse than one that stays quiet.
func hostFromServerURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "/") {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.ContainsAny(s, "{} ") {
		return ""
	}
	return s
}
