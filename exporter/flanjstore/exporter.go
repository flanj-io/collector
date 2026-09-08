package flanjstore

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/store"
)

// consumerCaps declares MutatesData: consumeLogs stamps `flanj.call.id` onto
// an unstamped call record (EnsureCallID) so the id lives on the QUEUED record
// and survives a retry. This exporter is the terminal, sole consumer of its
// pipeline, so the fanout clones nothing for it.
func consumerCaps() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

type storeExporter struct {
	logger *zap.Logger
	st     store.Store
}

// start locates the single-owner store extension via host.GetExtensions().
func (e *storeExporter) start(_ context.Context, host component.Host) error {
	for _, ext := range host.GetExtensions() {
		if p, ok := ext.(store.Provider); ok {
			e.st = p.Store()
			return nil
		}
	}
	return errors.New("flanjstore exporter: no flanjstore extension found in host extensions (it is the single store owner)")
}

// consumeLogs writes each record to the store: calls become RedactedCall rows,
// findings become Finding rows (pinning their source call).
//
// What is dropped and what is retried (CLAUDE.md "Durability"):
//
//   - a record this function cannot hand to the store at all — a finding or
//     spec_info that does not decode, a finding with no id (the occurrence
//     ledger cannot track it, so a retry would count it again) — is dropped on
//     its own, logged, and the rest of the batch goes on;
//   - a record the STORE refuses (store.ErrRejected: a constraint or encoding
//     violation, deterministic on the record) is dropped on its own too,
//     logged with its id, and the batch goes on — but the batch ends
//     PERMANENT, so the retry sender drops what is left after this one
//     attempt instead of re-delivering a record that would be refused again;
//   - every other store error is TRANSIENT, and only the records that have not
//     been applied yet — this one and everything after it — go back for retry
//     (consumererror.NewLogs). The already-applied head is not re-delivered,
//     so a 15-minute outage no longer re-executes it on every attempt. The
//     store is idempotent either way; this is about work, not correctness.
func (e *storeExporter) consumeLogs(_ context.Context, ld plog.Logs) error {
	if e.st == nil {
		return errors.New("flanjstore exporter: store not initialised")
	}
	// The FIRST store rejection in this batch, if any. Held rather than
	// returned on the spot so the records AFTER a poison one still get their
	// write — before partial retry they were lost with the batch.
	var rejected error
	idx := 0 // flat record index across all resource/scope groups
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k, idx = k+1, idx+1 {
				kind, id, err := e.writeOne(recs.At(k))
				if err == nil {
					continue
				}
				if errors.Is(err, store.ErrRejected) {
					// Logged here: exporterhelper's own "Rejecting data" line
					// names no record.
					e.logger.Error("store rejected record; dropping it (not retryable)",
						zap.String("record", kind), zap.String("id", id), zap.Error(err))
					if rejected == nil {
						rejected = err
					}
					continue
				}
				return consumererror.NewLogs(err, remainingFrom(ld, idx))
			}
		}
	}
	if rejected != nil {
		// Nothing left to retry — every other record in the batch was applied.
		// Permanent so the retry sender stops here; the request is accounted as
		// dropped whole, which over-counts (only the named record was refused),
		// and the precise line is the one logged above.
		return consumererror.NewPermanent(rejected)
	}
	return nil
}

// writeOne applies ONE record to the store. It returns the record's kind and
// id — for the caller's log line and nothing else — and the store's error, or
// nil for a record dropped here (undecodable, id-less, or not a flanj call at
// all): those are unfixable by a retry and cost the rest of the batch nothing.
func (e *storeExporter) writeOne(lr plog.LogRecord) (string, string, error) {
	switch otlpattr.RecordType(lr) {
	case otlpattr.RecordTypeFinding:
		f, err := otlpattr.FindingFromRecord(lr)
		if err != nil {
			e.logger.Warn("drop malformed finding record", zap.Error(err))
			return "finding", "", nil
		}
		if f.ID == "" {
			e.logger.Warn("drop finding record without id: the occurrence ledger cannot make it idempotent",
				zap.String("kind", f.Kind), zap.String("signature", f.Signature))
			return "finding", "", nil
		}
		return "finding", f.ID, e.st.InsertFinding(f)
	case otlpattr.RecordTypeSpecInfo:
		// Contract metadata from a front collector (tiered topology);
		// PutSpecInfo is an upsert, so the single-pod double write (direct at
		// Start + this record) is harmless.
		info, raw, err := otlpattr.SpecInfoFromRecord(lr)
		if err != nil {
			e.logger.Warn("drop malformed spec_info record", zap.Error(err))
			return "spec_info", "", nil
		}
		return "spec_info", info.Integration, e.st.PutSpecInfo(info, raw)
	default:
		// Stamp the id onto the RECORD before decoding it. The SDK never emits
		// flanj.call.id and only the drift processor stamps it, so a call
		// reaching a store pod's :4318 with no front (pipeline
		// [flanjredaction] only) has none; minting it inside CallFromRecord
		// gave every retry attempt a NEW id, `ON CONFLICT (id) DO NOTHING`
		// never fired, and a retried batch duplicated the row and inflated the
		// edge's call_count. On the queued record the id survives the retry —
		// including into the partial batch remainingFrom copies, which is taken
		// from THIS pdata, after the stamp (consumerCaps: MutatesData).
		otlpattr.EnsureCallID(lr)
		call := otlpattr.CallFromRecord(lr)
		if !validCall(call) {
			return "call", call.ID, nil
		}
		return "call", call.ID, e.st.InsertCall(call)
	}
}

// remainingFrom copies the records at flat index >= from into a fresh
// plog.Logs, keeping the resource/scope grouping each record came from (a
// front's batch carries its resource attributes, and the store reads none of
// them — but a retried request that flattened them would no longer be the
// request that was received). Resource and scope groups that end up empty are
// not created at all.
//
// This is what makes the retry PARTIAL: exporterhelper's logsRequest.OnError
// swaps the in-flight request for the logs carried by a consumererror.Logs, so
// the next attempt sends only these. Copies, not references: the original
// pdata goes back to the pool when the request is done with.
func remainingFrom(ld plog.Logs, from int) plog.Logs {
	out := plog.NewLogs()
	idx := 0
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		rl := rls.At(i)
		sls := rl.ScopeLogs()
		var destRL plog.ResourceLogs
		haveRL := false
		for j := 0; j < sls.Len(); j++ {
			sl := sls.At(j)
			recs := sl.LogRecords()
			var destSL plog.ScopeLogs
			haveSL := false
			for k := 0; k < recs.Len(); k, idx = k+1, idx+1 {
				if idx < from {
					continue
				}
				if !haveRL {
					destRL = out.ResourceLogs().AppendEmpty()
					rl.Resource().CopyTo(destRL.Resource())
					destRL.SetSchemaUrl(rl.SchemaUrl())
					haveRL = true
				}
				if !haveSL {
					destSL = destRL.ScopeLogs().AppendEmpty()
					sl.Scope().CopyTo(destSL.Scope())
					destSL.SetSchemaUrl(sl.SchemaUrl())
					haveSL = true
				}
				recs.At(k).CopyTo(destSL.LogRecords().AppendEmpty())
			}
		}
	}
	return out
}

// validCall guards against writing empty records (e.g. non-flanj logs that
// reached this exporter): a real captured call always has a method and route.
func validCall(c model.RedactedCall) bool {
	return c.Method != "" && c.Route != ""
}
