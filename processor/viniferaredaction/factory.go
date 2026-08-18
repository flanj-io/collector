// Package viniferaredaction is the collector's defense-in-depth redaction
// processor. It re-applies the Vinifera redaction floor (internal/redact) to the
// free-text attributes of every "call" log record BEFORE they reach the store
// exporter. Because the floor is idempotent and add-only, re-scanning the SDK's
// already-redacted output never double-wraps a ⟦REDACTED:…⟧ token; it only
// catches anything the SDK missed. This is a safety net, not the primary control.
package viniferaredaction

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
)

// typeStr is the component type used in the collector config.
var typeStr = component.MustNewType("viniferaredaction")

// NewFactory returns the processor factory (discovered by ocb-generated code).
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		createDefaultConfig,
		processor.WithLogs(createLogsProcessor, component.StabilityLevelBeta),
	)
}

func createDefaultConfig() component.Config {
	return &Config{EnableIP: false}
}

func createLogsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	next consumer.Logs,
) (processor.Logs, error) {
	rp := newRedactionProcessor(cfg.(*Config))
	return processorhelper.NewLogs(
		ctx, set, cfg, next,
		rp.processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
	)
}
