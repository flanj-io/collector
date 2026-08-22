package viniferadrift

import (
	"context"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/vinifera-io/collector/internal/drift"
	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
	"github.com/vinifera-io/collector/internal/store"
)

type driftProcessor struct {
	cfg     *Config
	doc     *openapi3.T
	rawSpec []byte
	logger  *zap.Logger

	// The org's own contract (we-as-provider), validated against INBOUND calls.
	selfDoc     *openapi3.T
	rawSelfSpec []byte

	versionFindings []model.Finding
	emitVersionOnce sync.Once
}

// start records the loaded contracts in the shared store so the local UI can
// surface them (title/version/docs link + the raw spec documents). Best effort:
// a collector without the store extension still detects drift, and a collector
// without specs records nothing.
func (p *driftProcessor) start(_ context.Context, host component.Host) error {
	if p.doc == nil && p.selfDoc == nil {
		return nil
	}
	for _, ext := range host.GetExtensions() {
		prov, ok := ext.(store.Provider)
		if !ok {
			continue
		}
		if p.doc != nil {
			info := specInfoFor(p.doc, model.SpecRoleProvider, p.cfg.IntegrationID, p.cfg.PeerHost)
			if err := prov.Store().PutSpecInfo(info, p.rawSpec); err != nil && p.logger != nil {
				p.logger.Warn("record provider spec info failed", zap.Error(err))
			}
		}
		if p.selfDoc != nil {
			info := specInfoFor(p.selfDoc, model.SpecRoleSelf, p.cfg.selfIntegration(), "")
			if err := prov.Store().PutSpecInfo(info, p.rawSelfSpec); err != nil && p.logger != nil {
				p.logger.Warn("record self spec info failed", zap.Error(err))
			}
		}
		return nil
	}
	return nil
}

// specInfoFor extracts the displayable contract metadata from a loaded spec.
func specInfoFor(doc *openapi3.T, role, integration, peerHost string) model.SpecInfo {
	info := model.SpecInfo{
		Integration: integration,
		Role:        role,
		PeerHost:    peerHost,
		Format:      "openapi",
		LoadedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if doc.Paths != nil {
		info.Endpoints = doc.Paths.Len()
	}
	if doc.Info != nil {
		info.Title = doc.Info.Title
		info.Version = doc.Info.Version
	}
	if doc.ExternalDocs != nil {
		info.DocsURL = doc.ExternalDocs.URL
	}
	return info
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
				if p.doc == nil && p.selfDoc == nil {
					continue
				}
				call := otlpattr.CallFromRecord(lr)

				if call.Direction == "server" {
					// INBOUND: we are the provider — validate OUR responses
					// against OUR OWN published contract.
					if p.selfDoc == nil {
						continue
					}
					fs, err := drift.DetectLiveVsSpec(p.selfDoc, call)
					if err != nil {
						if p.logger != nil {
							p.logger.Debug("self live-vs-spec skipped", zap.String("route", call.Route), zap.Error(err))
						}
						continue
					}
					// Findings label with the call's integration (the org id) by
					// default; relabel to the self contract's id so self and
					// provider findings never share a signature.
					selfID := p.cfg.selfIntegration()
					for i := range fs {
						fs[i].Integration = selfID
						fs[i].Signature = fs[i].ComputeSignature()
					}
					findings = append(findings, fs...)
					continue
				}

				// OUTBOUND: we are the consumer — validate the provider's
				// responses against the contract the provider publishes.
				if p.doc == nil {
					continue
				}
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
