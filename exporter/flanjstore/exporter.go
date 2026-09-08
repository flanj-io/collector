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
// What is dropped and what is retried (CLAUDE.md "Durability"): a record this
// function cannot hand to the store — a finding or spec_info that does not
// decode, a finding with no id (the occurrence ledger cannot track it, so a
// retry would count it again) — is dropped on its own, logged, and the rest of
// the batch goes on. A record the STORE refuses (store.ErrRejected — a
// constraint or encoding violation, deterministic on the record) fails the
// batch PERMANENTLY, so the retry sender drops it after this one attempt
// (writeErr). Every other store error is returned as-is and retried.
func (e *storeExporter) consumeLogs(_ context.Context, ld plog.Logs) error {
	if e.st == nil {
		return errors.New("flanjstore exporter: store not initialised")
	}
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				switch otlpattr.RecordType(lr) {
				case otlpattr.RecordTypeFinding:
					f, err := otlpattr.FindingFromRecord(lr)
					if err != nil {
						e.logger.Warn("drop malformed finding record", zap.Error(err))
						continue
					}
					if f.ID == "" {
						e.logger.Warn("drop finding record without id: the occurrence ledger cannot make it idempotent",
							zap.String("kind", f.Kind), zap.String("signature", f.Signature))
						continue
					}
					if err := e.st.InsertFinding(f); err != nil {
						return e.writeErr("finding", f.ID, err)
					}
				case otlpattr.RecordTypeSpecInfo:
					// Contract metadata from a front collector (tiered topology);
					// PutSpecInfo is an upsert, so the single-pod double write
					// (direct at Start + this record) is harmless.
					info, raw, err := otlpattr.SpecInfoFromRecord(lr)
					if err != nil {
						e.logger.Warn("drop malformed spec_info record", zap.Error(err))
						continue
					}
					if err := e.st.PutSpecInfo(info, raw); err != nil {
						return e.writeErr("spec_info", info.Integration, err)
					}
				default:
					// Stamp the id onto the RECORD before decoding it. The SDK
					// never emits flanj.call.id and only the drift processor
					// stamps it, so a call reaching a store pod's :4318 with no
					// front (pipeline [flanjredaction] only) has none; minting
					// it inside CallFromRecord gave every retry attempt a NEW
					// id, `ON CONFLICT (id) DO NOTHING` never fired, and a
					// retried batch duplicated the row and inflated the edge's
					// call_count. On the queued record the id survives the
					// retry (consumerCaps: MutatesData).
					otlpattr.EnsureCallID(lr)
					call := otlpattr.CallFromRecord(lr)
					if !validCall(call) {
						continue
					}
					if err := e.st.InsertCall(call); err != nil {
						return e.writeErr("call", call.ID, err)
					}
				}
			}
		}
	}
	return nil
}

// writeErr classifies a failed store write for the retry sender. A rejection
// (store.ErrRejected: sqlite SQLITE_CONSTRAINT, postgres SQLSTATE class 23/22
// — the same record would be refused on every attempt) is returned PERMANENT,
// so exporterhelper drops the batch after this one attempt; it is logged here
// with the record's identity, since the helper's own "Dropping data" line has
// none. What landed before the rejected record stays (the writes are
// idempotent); what came after it in the batch is lost with it — a partial
// retry (consumererror.NewLogs) is a follow-up. Everything else (connection,
// lock, timeout) is returned as-is and retried for max_elapsed_time.
func (e *storeExporter) writeErr(record, id string, err error) error {
	if errors.Is(err, store.ErrRejected) {
		e.logger.Error("store rejected record; dropping its batch (not retryable)",
			zap.String("record", record), zap.String("id", id), zap.Error(err))
		return consumererror.NewPermanent(err)
	}
	return err
}

// validCall guards against writing empty records (e.g. non-flanj logs that
// reached this exporter): a real captured call always has a method and route.
func validCall(c model.RedactedCall) bool {
	return c.Method != "" && c.Route != ""
}
