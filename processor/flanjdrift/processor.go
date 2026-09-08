package flanjdrift

import (
	"context"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

type driftProcessor struct {
	cfg    *Config
	logger *zap.Logger

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

	// specs holds the PARSED provider contracts, keyed by peer host, refreshed
	// in the background from src. The per-call path reads this map and nothing
	// else (speccache.go).
	specs *specCache
	// mcpSeeds tracks which of src's MCP snapshot rows have been offered to
	// the detector as its per-edge baseline (mcpbaseline.go). Same listing,
	// same ticker, same download-only-what-moved rule as specs.
	mcpSeeds mcpSeeds
	// src is where contracts come from: the co-located store, or — on a front
	// of the tiered topology, which has none — the store pod over HTTP.
	src specSource
	// held are findings produced OFF the batch path — the definition diff
	// drift.MCPDetector.Seed reports when the store's newer list displaces a
	// live baseline, which happens on the refresh loop — parked until the
	// next batch carries them downstream. This processor has no consumer of
	// its own to hand a record to (processorhelper), and a front always has
	// traffic, so the next batch is soon; findings dedup by signature, so
	// the delay costs nothing but latency.
	heldMu sync.Mutex
	held   []model.Finding
	// kick asks the refresh loop to run early, on first sight of a host with no
	// cached contract. Buffered to 1: a kick already pending is the same
	// request, and the loop floors how often it may act on one.
	kick chan struct{}
	done chan struct{}
	wg   sync.WaitGroup

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

// start resolves where provider contracts come from, fills the spec cache once
// so detection is live on the first call rather than a tick later — the same
// first refresh seeds the MCP detector's per-edge baselines from the store's
// persisted snapshots, whichever source they come from — and launches the
// background refresh. It also records the self contract in the shared store
// (title/version/docs link + the raw document).
//
// Best effort throughout: a collector with no contract source still detects MCP
// drift from the lists it observes itself and still stamps call ids, which is
// what makes front->store retries idempotent.
func (p *driftProcessor) start(_ context.Context, host component.Host) error {
	for _, ext := range host.GetExtensions() {
		prov, ok := ext.(store.Provider)
		if !ok {
			continue
		}
		p.st = prov.Store()
		// Subscribe to contract-set changes on the same extension. An upload,
		// replace or remove then reaches this cache in the time a channel send
		// takes, instead of waiting out a refresh tick — which is what makes
		// the UI's "Validating from now on" true rather than aspirational.
		//
		// This is the SINGLE-PROCESS path only: the tiered front runs this
		// processor in a different process from the store pod that owns the
		// uploads, and each pod of a shared-postgres deployment caches on its
		// own. Those converge on specRefresh, which is why it is short.
		if sub, ok := ext.(store.SpecSubscriber); ok {
			sub.OnSpecsChanged(p.kickRefresh)
		}
		break
	}

	// Where contracts come from. A co-located store wins: it is the same data
	// the store pod would serve, without the hop. A front of the tiered
	// topology has no store, so it asks the store pod instead.
	switch {
	case p.st != nil:
		p.src = storeSpecSource{st: p.st}
	case p.cfg.StorePodEndpoint != "":
		p.src = newRemoteSpecSource(p.cfg.StorePodEndpoint, p.cfg.StorePodToken)
	default:
		// No store and no endpoint: a front that was never told where the
		// store pod is. Uploaded contracts cannot reach it, and saying so once
		// at start is the difference between a silent hole and a fixable one.
		if p.logger != nil {
			p.logger.Warn("no contract source: this collector has no store and no store_pod_endpoint, " +
				"so uploaded contracts cannot reach it and REST drift detection will not run")
		}
	}
	if p.src != nil {
		p.refreshSpecs()
		p.wg.Add(1)
		go p.refreshLoop()
	}

	if p.st == nil {
		return nil
	}
	for _, si := range p.specInfos {
		if err := p.st.PutSpecInfo(si.info, si.raw); err != nil && p.logger != nil {
			p.logger.Warn("record spec info failed", zap.String("role", si.info.Role), zap.Error(err))
		}
	}
	return nil
}

// shutdown stops the refresh loop and waits for it, so a restarting collector
// never leaves a goroutine reading a store that is being closed underneath it.
func (p *driftProcessor) shutdown(_ context.Context) error {
	select {
	case <-p.done: // already closed (Shutdown may be called more than once)
	default:
		close(p.done)
	}
	p.wg.Wait()
	return nil
}

// refreshLoop reconciles the spec cache on a ticker, and early on a kick — a
// call for a host with no cached contract, or a contract-set change announced
// by the store extension. specRefreshFloor spaces those early runs so traffic
// to uncovered hosts — the common case on a big estate — cannot turn into a
// refresh per batch.
//
// A kick that arrives inside the floor is DEFERRED to the end of it, never
// dropped. Traffic kicks repeat every batch, so discarding one cost nothing; an
// announced change is a one-shot event, and swallowing it would put an upload
// back on the ticker — precisely the wait the announcement exists to remove.
// Deferred kicks coalesce: the pending timer is armed once and re-used, so a
// burst of uploads still costs one refresh.
func (p *driftProcessor) refreshLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(specRefresh)
	defer ticker.Stop()

	var (
		last     time.Time
		pending  *time.Timer
		pendingC <-chan time.Time
	)
	clearPending := func() {
		if pending != nil {
			pending.Stop()
			pending, pendingC = nil, nil
		}
	}
	defer clearPending()

	refresh := func() {
		clearPending()
		p.refreshSpecs()
		last = time.Now()
	}
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			refresh()
		case <-pendingC:
			refresh()
		case <-p.kick:
			if wait := specRefreshFloor - time.Since(last); wait > 0 {
				if pending == nil {
					pending = time.NewTimer(wait)
					pendingC = pending.C
				}
				continue
			}
			refresh()
		}
	}
}

// refreshSpecs lists the source once and reconciles both halves of the
// contract channel against it: uploaded OpenAPI contracts into the cache, and
// observed MCP snapshots into the detector's per-edge baselines. Logs what
// moved.
func (p *driftProcessor) refreshSpecs() {
	if p.src == nil {
		return
	}
	infos, err := p.src.listSpecs()
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("contract refresh failed", zap.Error(err))
		}
		return
	}
	changed, errs := p.specs.reconcile(infos, p.src)
	seeded, seedFindings, seedErrs := p.mcpSeeds.reconcile(infos, p.src, p.mcp)
	p.holdFindings(seedFindings)
	if p.logger == nil {
		return
	}
	for _, err := range errs {
		p.logger.Warn("contract refresh failed", zap.Error(err))
	}
	for _, err := range seedErrs {
		p.logger.Warn("mcp baseline seed failed", zap.Error(err))
	}
	if len(changed) > 0 {
		docs, rawBytes := p.specs.stats()
		p.logger.Info("contracts refreshed",
			zap.Strings("hosts", changed),
			zap.Int("contracts", docs),
			zap.Int("source_bytes", rawBytes))
	}
	for _, sd := range seeded {
		p.logger.Info(mcpSeedLogMessage,
			zap.String("integration", sd.integration),
			zap.String("peer_host", sd.peerHost),
			zap.String("observed_at", sd.observedAt))
	}
	if len(seedFindings) > 0 {
		p.logger.Info("mcp baseline adopted from the store reports definition changes",
			zap.Int("findings", len(seedFindings)))
	}
}

// holdFindings parks findings produced off the batch path for the next batch.
func (p *driftProcessor) holdFindings(fs []model.Finding) {
	if len(fs) == 0 {
		return
	}
	p.heldMu.Lock()
	p.held = append(p.held, fs...)
	p.heldMu.Unlock()
}

// takeHeldFindings drains what holdFindings parked. Safe for concurrent
// processLogs calls: each finding leaves with exactly one batch.
func (p *driftProcessor) takeHeldFindings() []model.Finding {
	p.heldMu.Lock()
	defer p.heldMu.Unlock()
	fs := p.held
	p.held = nil
	return fs
}

// kickRefresh asks for an early refresh without ever blocking the pipeline or
// the announcer that called it. A kick already queued is the same request, so a
// full buffer is success.
func (p *driftProcessor) kickRefresh() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// specInfoFor extracts the displayable contract metadata from a loaded spec.
func specInfoFor(doc *openapi3.T, role, integration, peerHost string) model.SpecInfo {
	info := model.SpecInfo{
		Integration: integration,
		Role:        role,
		PeerHost:    peerHost,
		Format:      model.SpecFormatOpenAPI,
		// Explicit, not the store's column default: the default is also
		// "config", which is exactly how an unset Source on the observed MCP
		// path went unnoticed. Every writer names its own provenance.
		Source:   model.SpecSourceConfig,
		LoadedAt: time.Now().UTC().Format(time.RFC3339),
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
				//
				// EVERY branch below ends in a verdict stamp (otlpattr.StampValidated):
				// what this processor did with the call, or the first gate that
				// stopped it. The stamp is the fact the UI's contract chip reads
				// — it used to INFER "checked" from the contract list (a document
				// bound to the host, bound before the call), and that inference
				// said CONFORMING over calls that went through here while the
				// upload was still on its way to this cache: seconds on a single
				// pod, ten on a tiered front, forever on a front whose
				// store_pod_token is wrong. Only this process can say whether it
				// validated a call, so it says so on every one.
				//
				// MCP tools/call records take the MCP detection path (validated
				// against the observed snapshot), NEVER the OpenAPI one.
				if otlpattr.Transport(lr) == otlpattr.TransportMCP {
					if p.mcp == nil {
						otlpattr.StampValidated(lr, model.NotValidated(model.NotValidatedNoContract))
						continue
					}
					call := otlpattr.CallFromRecord(lr)
					// No baseline for this edge yet: on a tiered front the store pod
					// may well hold the tools/list a sibling front observed, so ask
					// for an early refresh — the same first-sight kick the REST path
					// gives an uncovered host. Never blocks; floored.
					if !p.mcp.HasBaseline(call.PeerHost, call.Direction) {
						p.kickRefresh()
					}
					fs, verdict := p.mcp.JudgeCall(call)
					findings = append(findings, fs...)
					otlpattr.StampValidated(lr, verdict)
					continue
				}
				// Nothing to validate against → pass-through (capture + edge
				// discovery only). The cache fills in the background, so this
				// is a per-batch check, not a fixed one.
				//
				// Ask for a refresh on the way past. This branch IS the fresh
				// install — nothing uploaded yet — and it is the one case where
				// waiting out a full tick would be felt: the operator uploads
				// their first contract and watches nothing happen. The kick
				// never blocks and the loop floors how often it may act.
				if p.selfDoc == nil && p.specs.empty() {
					p.kickRefresh()
					otlpattr.StampValidated(lr, model.NotValidated(model.NotValidatedNoContract))
					continue
				}
				call := otlpattr.CallFromRecord(lr)

				if call.Direction == "server" {
					// INBOUND: we are the provider — validate OUR responses
					// against OUR OWN published contract.
					if p.selfDoc == nil {
						otlpattr.StampValidated(lr, model.NotValidated(model.NotValidatedNoContract))
						continue
					}
					fs, verdict, jerr := drift.JudgeLiveVsSpec(p.selfDoc, call)
					if verdict.Verdict == model.ValidatedNot && p.logger != nil {
						p.logger.Debug("self live-vs-spec skipped", zap.String("route", call.Route), zap.String("reason", verdict.Reason), zap.NamedError("cause", jerr))
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
					otlpattr.StampValidated(lr, verdict)
					continue
				}

				// OUTBOUND: we are the consumer — validate the provider's
				// responses against the contract the provider publishes.
				//
				// Contracts are bound to exactly one host at upload, so the
				// host IS the lookup. A call to a host with no contract is not
				// an error and not a finding: it was captured, not validated,
				// and the UI says exactly that — off the stamp. Ask for an early
				// refresh in case the contract was uploaded moments ago.
				doc, ok := p.specs.lookup(call.PeerHost)
				if !ok {
					p.kickRefresh()
					otlpattr.StampValidated(lr, model.NotValidated(model.NotValidatedNoContract))
					continue
				}
				// A route miss, a response the document does not describe, or a
				// body that will not decode is logged, not fatal — v0's
				// deterministic finding is the response-schema mismatch — and
				// the verdict names it, so the call never reads as clean.
				fs, verdict, jerr := drift.JudgeLiveVsSpec(doc, call)
				if verdict.Verdict == model.ValidatedNot && p.logger != nil {
					p.logger.Debug("live-vs-spec skipped", zap.String("route", call.Route), zap.String("reason", verdict.Reason), zap.NamedError("cause", jerr))
				}
				findings = append(findings, fs...)
				otlpattr.StampValidated(lr, verdict)
			}
		}
	}

	// MCP snapshots observed in THIS batch ride along un-rate-limited: each is
	// already at most one per observed tools/list, and the store's PutSpecInfo
	// is an idempotent upsert. Copy-append: dueSpecInfos returns the shared
	// p.specInfos slice, which must never be appended into.
	due := p.dueSpecInfos(time.Now())
	specs := make([]specInfoRecord, 0, len(due)+len(mcpSpecs))
	specs = append(specs, due...)
	specs = append(specs, mcpSpecs...)
	// Findings the refresh loop produced since the last batch ride this one.
	findings = append(findings, p.takeHeldFindings()...)
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
	sl.Scope().SetName("flanjdrift")
	for _, f := range findings {
		lr := sl.LogRecords().AppendEmpty()
		_ = otlpattr.FindingToRecord(lr, f)
	}
	for _, si := range specs {
		lr := sl.LogRecords().AppendEmpty()
		_ = otlpattr.SpecInfoToRecord(lr, si.info, si.raw)
	}
}
