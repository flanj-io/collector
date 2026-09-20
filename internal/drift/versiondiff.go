package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
)

// DetectVersionDiffData diffs two contract DOCUMENTS rather than two paths.
//
// This is the upload path's entry point. Contracts are uploaded in the UI and
// live in the store (CONTRACTS §8 dropped `spec_v2_path`), so the only place a
// v1 -> v2 diff can come from is an upload REPLACING a bound contract — and at
// that moment both documents are bytes in hand, not files on disk.
//
// oasdiff loads through its own source abstraction, which reads paths, so the
// documents are written to temporary files and the tested path-based
// implementation runs unchanged. Deliberately not a reimplementation: the
// severity overrides below are contract-driven and must not fork.
func DetectVersionDiffData(v1, v2 []byte, integration string) ([]model.Finding, error) {
	dir, err := os.MkdirTemp("", "flanj-versiondiff-")
	if err != nil {
		return nil, fmt.Errorf("version diff: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The extension decides how oasdiff parses the document, and an uploaded
	// contract may be either JSON or YAML. A YAML parser reads JSON, so .yaml
	// is the extension that works for both.
	p1 := filepath.Join(dir, "v1.yaml")
	p2 := filepath.Join(dir, "v2.yaml")
	if err := os.WriteFile(p1, v1, 0o600); err != nil {
		return nil, fmt.Errorf("version diff: write previous: %w", err)
	}
	if err := os.WriteFile(p2, v2, 0o600); err != nil {
		return nil, fmt.Errorf("version diff: write current: %w", err)
	}
	return DetectVersionDiff(p1, p2, integration)
}

// deprecationAnnouncements are the oasdiff change ids that ANNOUNCE a
// deprecation — a surface marked deprecated, with or without a sunset date.
// They are emitted as severity="warning" findings (CONTRACTS §4) rather than
// dropped with the rest of the sub-ERR output.
//
// Why they need naming at all: oasdiff grades these INFO, because a
// deprecation breaks no caller on the day it is published. But it is the
// earliest honest warning a consumer ever gets — providers rarely break you
// overnight, they deprecate, give a window, then remove — and dropping it
// meant the collector said nothing until the removal, when the window had
// already closed. So the window is the finding, and WARNING is its tier.
//
// Only this family is promoted. The rest of oasdiff's sub-ERR output stays
// dropped: lifting every WARN and INFO would bury these under exactly the
// noise they exist to stand out from.
//
// The `*-reactivated` ids are deliberately absent. A deprecation being LIFTED
// constrains nobody and breaks nothing, and CONTRACTS §4 already fixes that
// posture for its mirror image ("Additive changes are not reported"). A
// warning that carries good news costs the tier its meaning.
//
// The sunset ids oasdiff already grades ERR — a missing, unparseable or
// too-near sunset date, a deleted one, a removal before one — are NOT here and
// are untouched. Those describe a promise already broken, not one being made,
// and they keep landing as severity="breaking" through the ERR path below.
var deprecationAnnouncements = map[string]bool{
	checker.EndpointDeprecatedId:                   true,
	checker.EndpointDeprecatedWithSunsetId:         true,
	checker.RequestParameterDeprecatedId:           true,
	checker.RequestPropertyDeprecatedId:            true,
	checker.RequestPropertyDeprecatedWithSunsetId:  true,
	checker.ResponsePropertyDeprecatedId:           true,
	checker.ResponsePropertyDeprecatedWithSunsetId: true,
}

// DetectVersionDiff diffs spec v1 -> v2 and emits one Finding per reported
// change: a backward-incompatible one as severity="breaking" (oasdiff
// Level==ERR), and a deprecation ANNOUNCEMENT as severity="warning"
// (deprecationAnnouncements above).
// source_call_id is null — the drift is in the documents, not in a call.
//
// Note what this can and cannot see: oasdiff's deprecation checks fire only
// where the `deprecated` flag CHANGED between the two documents, so this is
// the transition — "the provider just deprecated X" — and never the standing
// state. A contract that arrived already carrying `deprecated: true` produces
// nothing here. The standing state is the live-vs-spec path's job, where it is
// judged against the org's own traffic.
func DetectVersionDiff(pathV1, pathV2, integration string) ([]model.Finding, error) {
	s1, err := loadSelfContained(pathV1)
	if err != nil {
		return nil, fmt.Errorf("load spec v1: %w", err)
	}
	s2, err := loadSelfContained(pathV2)
	if err != nil {
		return nil, fmt.Errorf("load spec v2: %w", err)
	}

	diffReport, sources, err := diff.GetWithOperationsSourcesMap(diff.NewConfig(), s1, s2)
	if err != nil {
		return nil, fmt.Errorf("diff specs: %w", err)
	}

	// Severity override (contract-driven): oasdiff ships
	// `response-property-enum-value-removed` at INFO because narrowing a response
	// enum is not breaking for a generic client. Flanj treats a removed
	// response enum value as a BREAKING contract change — a value the consumer's
	// code may branch on has silently disappeared from the provider's declared
	// surface. Promoting it to ERR is what makes it a `severity=breaking` finding
	// (CONTRACTS §4: Level=ERR -> breaking) with rule id
	// `response-property-enum-value-removed`, matching the frozen contract.
	config := checker.NewConfig(
		checker.GetAllChecks(),
		checker.WithSeverityLevels(map[string]checker.Level{
			checker.ResponsePropertyEnumValueRemovedId:  checker.ERR,
			checker.ResponseMediaTypeEnumValueRemovedId: checker.ERR,
		}),
	)
	// INFO, not the default. checker.CheckBackwardCompatibility is
	// CheckBackwardCompatibilityUntilLevel(…, WARN), which drops everything
	// below WARN before a caller sees it — and oasdiff grades the whole
	// deprecation family INFO, because a deprecation breaks nobody on the day
	// it is published. Asking only down to WARN therefore returned the family
	// as an empty set, not as changes to be filtered: the old `< ERR` skip
	// below was never what discarded them. Everything sub-ERR that is not a
	// deprecation announcement is still dropped in the loop, so widening the
	// request widens nothing that is reported.
	changes := checker.CheckBackwardCompatibilityUntilLevel(config, diffReport, sources, checker.INFO)

	localizer := checker.NewDefaultLocalizer()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	fromV := s1.GetVersion()
	toV := s2.GetVersion()

	var findings []model.Finding
	for _, c := range changes {
		// ERR is breaking; a deprecation announcement is a warning; everything
		// else sub-ERR is dropped as before.
		severity := model.SeverityBreaking
		kind := model.KindVersionDiff
		if c.GetLevel() < checker.ERR {
			if !deprecationAnnouncements[c.GetId()] {
				continue
			}
			// A deprecation is its own kind, not a warning-severity version
			// diff: what it reports is that a surface is GOING AWAY, which is a
			// different question from how the document changed.
			severity = model.SeverityWarning
			kind = model.KindDeprecation
		}
		endpoint := ""
		if ac, ok := c.(checker.ApiChange); ok {
			endpoint = ac.Operation + " " + ac.Path
		}
		fieldPath := versionDiffFieldPath(c.GetArgs())
		vf := model.Finding{
			SchemaVersion:   model.SchemaVersion,
			ID:              otlpattr.NewID(),
			Kind:            kind,
			Severity:        severity,
			Integration:     integration,
			Endpoint:        endpoint,
			FieldPath:       model.Ptr(fieldPath),
			Expected:        "spec " + fromV,
			Actual:          "spec " + toV,
			Rule:            c.GetId(),
			SpecVersionFrom: model.Ptr(fromV),
			SpecVersionTo:   model.Ptr(toV),
			DetectedAt:      now,
			Detail:          c.GetUncolorizedText(localizer),
			OccurrenceCount: 1,
			FirstSeen:       now,
			LastSeen:        now,
		}
		vf.Signature = vf.ComputeSignature()
		findings = append(findings, vf)
	}
	return findings, nil
}

// loadSelfContained loads ONE contract document from a local file and lets it
// read nothing else: no `$ref` to a URL, to a file:// URI, to an absolute or
// relative path, or to the other version lying beside it.
//
// A bound contract is written by the provider, or by whoever answered a
// fetched URL, so every location in it is chosen by someone else. Following
// one would be a request to an address the document picked — outside the
// contract-fetch destination policy, which judges only the URL an operator
// approved — or a read of the collector's own disk whose content then surfaces
// in a finding's detail. Nothing legitimate needs it: a document only gets
// bound after the strict parse (LoadSpecData) has refused external references,
// so every stored contract is self-contained by construction. This is the same
// rule held a second time, so the diff stays safe whatever a future caller
// hands it.
//
// Three things enforce it, because no single one covers every route:
//   - the path must be what oasdiff treats as a plain file. It picks a loading
//     strategy from the SHAPE of the string: a URL is fetched, and
//     `<rev>:<path>` is read out of git through a reader of oasdiff's own,
//     which would replace the one below;
//   - the reader serves that one file and refuses every other location.
//     kin-openapi stops enforcing IsExternalRefsAllowed as soon as any
//     ReadFromURIFunc is installed (the reader then owns the policy), so the
//     reader is an allowlist of exactly one entry rather than a filter;
//   - IsExternalRefsAllowed stays false regardless, so removing the reader
//     falls back to kin-openapi's own refusal instead of its default reader,
//     which fetches http(s) and opens any local path.
//
// Each document gets a loader of its own: the allowlist is per document.
func loadSelfContained(path string) (*load.SpecInfo, error) {
	source := load.NewSource(path)
	if !source.IsFile() {
		return nil, fmt.Errorf("%q is not a local file", path)
	}
	root := filepath.ToSlash(path)

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		if location.Scheme != "" || location.Host != "" || location.Path != root {
			return nil, errExternalRef
		}
		return os.ReadFile(path)
	}
	return load.NewSpecInfo(loader, source)
}

// errExternalRef names the rule and not the location: the reference is the
// document author's text, and this error is logged.
var errExternalRef = errors.New("the document references another document; a contract is diffed as one self-contained document")

// maxFieldPath is the frozen per-field cap on `field_path` (CONTRACTS §5).
const maxFieldPath = 256

// versionDiffFieldPath is what makes two version-diff findings on ONE endpoint
// under ONE rule two findings rather than one.
//
// The signature is `integration|endpoint|kind|rule|field_path` (CONTRACTS §4)
// and the store's unique index dedups on it, so an empty field_path collapsed
// every change a rule found on an endpoint into a single row: the upload
// counted the changes it emitted while the store kept a fraction of them, and
// the "N breaking changes against the version it replaced" notice disagreed
// with the API and the tab beneath it (observed: notice 4, API 2, UI 0).
//
// oasdiff carries no field path of its own. What names the changed element is
// the change's ARGUMENTS — the locale-independent substitutions in its message
// template ("removed the `pending` enum value from the `status` response
// property for the response status `200`" -> ["pending", "status", "200"]), so
// they are the discriminator. They also carry the BEFORE and AFTER values on
// the "changed from X to Y" rules, which is what stops a ROLLBACK from being
// recorded as the forward change recurring: v1->v2 and v2->v1 fire the same
// rule on the same endpoint with their values swapped, so they now hold
// distinct signatures instead of one row with occurrence_count 2. (Rules that
// only fire one way — a removal, whose inverse is a non-breaking addition —
// never had that problem: the rollback emits nothing at all.)
func versionDiffFieldPath(args []any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, strings.TrimSpace(fmt.Sprint(a)))
	}
	p := strings.Join(parts, " ")
	r := []rune(p)
	if len(r) <= maxFieldPath {
		return p
	}
	// A plain truncation would let two long changes collapse back into one
	// signature, which is the bug this field exists to fix — so the tail
	// carries a digest of the WHOLE value rather than dropping it.
	sum := sha256.Sum256([]byte(p))
	digest := hex.EncodeToString(sum[:6])
	return string(r[:maxFieldPath-len(digest)-1]) + "…" + digest
}
