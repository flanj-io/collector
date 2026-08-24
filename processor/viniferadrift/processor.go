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

	// The MCP side (v0.5 Step C): per-edge tools/list snapshots + the three MCP
	// findings. Needs NO configuration — MCP contracts are self-delivering
	// (contract_snapshot records observed in the traffic).
	mcp *drift.MCPDetector
	// st is the co-located store (nil on a front collector of the tiered
	// topology). Used to persist MCP snapshots as spec_infos directly and to
	// seed the MCP baseline across restarts; the spec_info records emitted
	// into the pipeline cover the tiered store pod either way.
	st store.Store

	versionFindings []model.Finding
	emitVersionOnce sync.Once

	// The loaded contracts as spec_info records — precomputed at construction
	// (stable loaded_at) and used both for the direct PutSpecInfo at Start
	// (single-pod topology) and for emission INTO the pipeline, so a store pod
	// behind an otlphttp hop learns what this front loaded (docs/STORE.md,
	// "Topologies"). Emitted on the first batch after Start and then at most
	// every specInfoRefresh, so a fresh store converges without a front restart.
	specInfos    []specInfoRecord
	specEmitMu   sync.Mutex
	nextSpecEmit time.Time
}

// specInfoRecord pairs a contract's metadata with its raw document.
type specInfoRecord struct {
	info model.SpecInfo
	raw  []byte
}

// specInfoRefresh bounds how often a front re-emits its spec_info records.
const specInfoRefresh = 10 * time.Minute

// start records the loaded contracts in the shared store so the local UI can
// surface them (title/version/docs link + the raw spec documents) and seeds the
// MCP detector from the persisted snapshots. Best effort: a collector without
// the store extension still detects drift (a front collector in the tiered
// topology — the spec_info records emitted into the pipeline carry the
// contracts instead, and the MCP baseline re-establishes from the next
// observed tools/list).
func (p *driftProcessor) start(_ context.Context, host component.Host) error {
	for _, ext := range host.GetExtensions() {
		prov, ok := ext.(store.Provider)
		if !ok {
			continue
		}
		p.st = prov.Store()
		break
	}
	if p.st == nil {
		return nil
	}
	for _, si := range p.specInfos {
		if err := p.st.PutSpecInfo(si.info, si.raw); err != nil && p.logger != nil {
			p.logger.Warn("record spec info failed", zap.String("role", si.info.Role), zap.Error(err))
		}
	}
	// Seed the MCP baseline from the persisted snapshots (spec_infos rows with
	// format "mcp"), so a restart diffs the next observed tools/list against
	// the last persisted one instead of silently re-baselining.
	if p.mcp != nil {
		infos, err := p.st.ListSpecInfos()
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("list spec infos for mcp seed failed", zap.Error(err))
			}
			return nil
		}
		for _, info := range infos {
			if info.Format != model.SpecFormatMCP {
				continue
			}
			raw, _, ok, err := p.st.GetSpecDoc(info.Integration)
			if err != nil || !ok {
				continue
			}
			if err := p.mcp.Seed(info, raw); err != nil && p.logger != nil {
				p.logger.Warn("seed mcp contract failed", zap.String("integration", info.Integration), zap.Error(err))
			}
		}
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
	var mcpSpecs []specInfoRecord

	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			n := recs.Len() // snapshot: we append finding records below
			for k := 0; k < n; k++ {
				lr := recs.At(k)
				// contract_snapshot (v0.5): an observed MCP tools/list — the
				// self-delivering local spec. Load it (versioned, diffed) and
				// carry its SpecInfo downstream like a loaded contract.
				if otlpattr.RecordType(lr) == otlpattr.RecordTypeContractSnapshot {
					if p.mcp == nil {
						continue
					}
					snap, err := otlpattr.ContractSnapshotFromRecord(lr)
					if err != nil {
						if p.logger != nil {
							p.logger.Warn("drop malformed contract_snapshot record", zap.Error(err))
						}
						continue
					}
					fs, info, raw, err := p.mcp.LoadSnapshot(snap)
					if err != nil {
						if p.logger != nil {
							p.logger.Warn("load mcp contract snapshot failed", zap.String("peer", snap.PeerHost), zap.Error(err))
						}
						continue
					}
					findings = append(findings, fs...)
					mcpSpecs = append(mcpSpecs, specInfoRecord{info: info, raw: raw})
					// Direct persist when a store is co-located (single-pod /
					// store pod); the emitted spec_info record covers the
					// tiered hop — the double write is a harmless upsert.
					if p.st != nil {
						if err := p.st.PutSpecInfo(info, raw); err != nil && p.logger != nil {
							p.logger.Warn("persist mcp contract snapshot failed", zap.Error(err))
						}
					}
					continue
				}
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeCall {
					continue
				}
				otlpattr.EnsureCallID(lr) // stamp the shared id before reconstruct
				// MCP tools/call records take the MCP detection path (validated
				// against the observed snapshot), NEVER the OpenAPI one.
				if otlpattr.Transport(lr) == otlpattr.TransportMCP {
					if p.mcp != nil {
						findings = append(findings, p.mcp.DetectCall(otlpattr.CallFromRecord(lr))...)
					}
					continue
				}
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

	// MCP snapshots observed in THIS batch ride along un-rate-limited: each is
	// already at most one per observed tools/list, and the store's PutSpecInfo
	// is an idempotent upsert. Copy-append: dueSpecInfos returns the shared
	// p.specInfos slice, which must never be appended into.
	due := p.dueSpecInfos(time.Now())
	specs := make([]specInfoRecord, 0, len(due)+len(mcpSpecs))
	specs = append(specs, due...)
	specs = append(specs, mcpSpecs...)
	if len(findings) > 0 || len(specs) > 0 {
		appendRecords(ld, findings, specs)
	}
	return ld, nil
}

// dueSpecInfos returns the spec_info records to emit with this batch: all of
// them on the first batch after Start and then at most once per
// specInfoRefresh; nil otherwise. Safe for concurrent processLogs calls.
func (p *driftProcessor) dueSpecInfos(now time.Time) []specInfoRecord {
	if len(p.specInfos) == 0 {
		return nil
	}
	p.specEmitMu.Lock()
	defer p.specEmitMu.Unlock()
	if now.Before(p.nextSpecEmit) {
		return nil
	}
	p.nextSpecEmit = now.Add(specInfoRefresh)
	return p.specInfos
}

// appendRecords writes each Finding and spec_info as its own log record under a
// fresh trailing ResourceLogs/ScopeLogs so they never collide with the in-flight
// call records (and calls stay ahead of findings within the batch).
func appendRecords(ld plog.Logs, findings []model.Finding, specs []specInfoRecord) {
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	sl.Scope().SetName("viniferadrift")
	for _, f := range findings {
		lr := sl.LogRecords().AppendEmpty()
		_ = otlpattr.FindingToRecord(lr, f)
	}
	for _, si := range specs {
		lr := sl.LogRecords().AppendEmpty()
		_ = otlpattr.SpecInfoToRecord(lr, si.info, si.raw)
	}
}
