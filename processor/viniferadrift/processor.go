package viniferadrift

import (
	"context"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/vinifera-io/collector/internal/drift"
	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
)

type driftProcessor struct {
	cfg    *Config
	doc    *openapi3.T
	logger *zap.Logger

	versionFindings []model.Finding
	emitVersionOnce sync.Once
}

// processLogs validates each call record against the spec and appends a Finding
// record per violation. The precomputed version-diff findings are injected once,
// on the first batch that flows through.
func (p *driftProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	var findings []model.Finding

	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			n := recs.Len() // snapshot: we append finding records below
			for k := 0; k < n; k++ {
				lr := recs.At(k)
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeCall {
					continue
				}
				otlpattr.EnsureCallID(lr) // stamp the shared id before reconstruct
				// No spec loaded → pass-through (capture + edge discovery only).
				if p.doc == nil {
					continue
				}
				call := otlpattr.CallFromRecord(lr)
				// Spec-matched-by-host: when PeerHost is configured, only validate
				// calls on that edge against this spec.
				if p.cfg.PeerHost != "" && call.PeerHost != p.cfg.PeerHost {
					continue
				}
				fs, err := drift.DetectLiveVsSpec(p.doc, call)
				if err != nil {
					// A route miss or reconstruction error is logged, not fatal —
					// v0's deterministic finding is the response-schema mismatch.
					if p.logger != nil {
						p.logger.Debug("live-vs-spec skipped", zap.String("route", call.Route), zap.Error(err))
					}
					continue
				}
				findings = append(findings, fs...)
			}
		}
	}

	p.emitVersionOnce.Do(func() {
		findings = append(findings, p.versionFindings...)
	})

	if len(findings) > 0 {
		appendFindings(ld, findings)
	}
	return ld, nil
}

// appendFindings writes each Finding as its own log record under a fresh
// ResourceLogs/ScopeLogs so it never collides with the in-flight call records.
func appendFindings(ld plog.Logs, findings []model.Finding) {
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	sl.Scope().SetName("viniferadrift")
	for _, f := range findings {
		lr := sl.LogRecords().AppendEmpty()
		_ = otlpattr.FindingToRecord(lr, f)
	}
}
