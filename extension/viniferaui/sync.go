package viniferaui

// Finding-shape sync (CONTRACTS §5, POST /api/v1/findings): every tick the
// collector posts the SHAPE of its current findings — id, signature, kind,
// severity, integration, endpoint, rule, counts, timestamps — so the owner's
// control-plane dashboard can list them and deep-link back to this UI
// (#contracts/<finding_id>). The observed values (expected / actual / detail)
// NEVER leave the collector on this path: the payload builder is an explicit
// allow-list (internal/promote.BuildFindingShapes) with a wire-bytes test.
//
// The loop is on by default and disabled entirely with `finding_sync: false`
// (CONTRACTS §8). A tick skips silently unless the control plane is
// configured, a collector key exists in the store, and there is at least one
// finding. One attempt per tick — a failure is retried by the next tick, never
// inline. Log lines carry only status + counts: never the bearer, never any
// finding content.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/promote"
)

// findingSyncInterval is the tick period. The first tick fires immediately on
// Start so a fresh deployment syncs fast.
const findingSyncInterval = 15 * time.Second

// startFindingSync launches the sync ticker goroutine (called from Start).
// No-op when the loop is disabled or no control plane is configured.
func (e *uiExtension) startFindingSync() {
	if !e.cfg.FindingSync || e.cp == nil {
		return
	}
	// Not Start's ctx (that one ends with the Start call): the loop lives until
	// Shutdown cancels it.
	ctx, cancel := context.WithCancel(context.Background())
	e.syncCancel = cancel
	e.syncDone = make(chan struct{})
	go func() {
		defer close(e.syncDone)
		t := time.NewTicker(findingSyncInterval)
		defer t.Stop()
		for {
			e.syncFindingsOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// stopFindingSync cancels the ticker and waits for the goroutine to exit
// (called from Shutdown — no goroutine leak).
func (e *uiExtension) stopFindingSync() {
	if e.syncCancel == nil {
		return
	}
	e.syncCancel()
	<-e.syncDone
	e.syncCancel = nil
}

// syncFindingsOnce is one tick: list up to the CP's per-request cap, build the
// shape-only payload, POST it with the collector key. Every skip is silent by
// design — an unconnected or empty collector has nothing to say.
func (e *uiExtension) syncFindingsOnce(ctx context.Context) {
	if e.cp == nil {
		return
	}
	st := e.resolveStore()
	if st == nil {
		return
	}
	cs, err := loadConnect(st)
	if err != nil || cs.CollectorKey == "" {
		return
	}
	findings, err := st.ListFindings(promote.FindingsSyncMaxItems)
	if err != nil || len(findings) == 0 {
		return
	}
	// D10 syncs drift findings. stale_client is a local client-freshness notice,
	// not drift — its CP deep link would dead-end — so it never syncs; every
	// other kind (version-diff included) does.
	findings = slices.DeleteFunc(findings, func(f model.Finding) bool { return f.Kind == model.KindStaleClient })
	shapes := promote.BuildFindingShapes(findings)
	if len(shapes) == 0 {
		return
	}
	resp, status, err := e.keyedClient(cs).PostFindings(ctx, promote.FindingsRequest{Findings: shapes})
	if err != nil {
		// Status + count only — never the response body, never the bearer.
		e.telemetry.Logger.Debug(fmt.Sprintf("finding sync: post of %d findings failed with status %d — retrying next tick", len(shapes), status))
		return
	}
	e.telemetry.Logger.Debug(fmt.Sprintf("finding sync: %d findings posted, %d stored (status %d)", len(shapes), resp.Stored, status))
}
