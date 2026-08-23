package viniferastore

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/otlpattr"
	"github.com/vinifera-io/collector/internal/store"
)

func consumerCaps() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
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
	return errors.New("viniferastore exporter: no viniferastore extension found in host extensions (it is the single store owner)")
}

// consumeLogs writes each record to the store: calls become RedactedCall rows,
// findings become Finding rows (pinning their source call).
func (e *storeExporter) consumeLogs(_ context.Context, ld plog.Logs) error {
	if e.st == nil {
		return errors.New("viniferastore exporter: store not initialised")
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
					if err := e.st.InsertFinding(f); err != nil {
						return err
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
						return err
					}
				default:
					call := otlpattr.CallFromRecord(lr)
					if !validCall(call) {
						continue
					}
					if err := e.st.InsertCall(call); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// validCall guards against writing empty records (e.g. non-vinifera logs that
// reached this exporter): a real captured call always has a method and route.
func validCall(c model.RedactedCall) bool {
	return c.Method != "" && c.Route != ""
}
