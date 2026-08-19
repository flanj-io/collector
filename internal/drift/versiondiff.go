package drift

import (
	"fmt"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	"github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
)

// DetectVersionDiff diffs spec v1 -> v2 and emits one breaking Finding per
// backward-incompatible change (oasdiff Level==ERR -> severity="breaking").
// Computed once at spec load; source_call_id is null.
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
	// enum is not breaking for a generic client. Vinifera treats a removed
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
