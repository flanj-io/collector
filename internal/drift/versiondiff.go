package drift

import (
	"fmt"
	"os"
	"path/filepath"
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
		vf := model.Finding{
			SchemaVersion:   model.SchemaVersion,
			ID:              otlpattr.NewID(),
			Kind:            model.KindVersionDiff,
			Severity:        model.SeverityBreaking,
			Integration:     integration,
			Endpoint:        endpoint,
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
