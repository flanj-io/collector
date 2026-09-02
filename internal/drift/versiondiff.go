package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// DetectVersionDiff diffs spec v1 -> v2 and emits one breaking Finding per
// backward-incompatible change (oasdiff Level==ERR -> severity="breaking").
// source_call_id is null — the drift is in the documents, not in a call.
func DetectVersionDiff(pathV1, pathV2, integration string) ([]model.Finding, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	s1, err := load.NewSpecInfo(loader, load.NewSource(pathV1))
	if err != nil {
		return nil, fmt.Errorf("load spec v1: %w", err)
	}
	s2, err := load.NewSpecInfo(loader, load.NewSource(pathV2))
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
	changes := checker.CheckBackwardCompatibility(config, diffReport, sources)

	localizer := checker.NewDefaultLocalizer()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	fromV := s1.GetVersion()
	toV := s2.GetVersion()

	var findings []model.Finding
	for _, c := range changes {
		if c.GetLevel() < checker.ERR {
			continue // v0 emits only breaking (ERR) changes
		}
		endpoint := ""
		if ac, ok := c.(checker.ApiChange); ok {
			endpoint = ac.Operation + " " + ac.Path
		}
		fieldPath := versionDiffFieldPath(c.GetArgs())
		vf := model.Finding{
			SchemaVersion:   model.SchemaVersion,
			ID:              otlpattr.NewID(),
			Kind:            model.KindVersionDiff,
			Severity:        model.SeverityBreaking,
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
