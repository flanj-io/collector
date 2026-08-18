// Package viniferastore is the store exporter: the WRITER side of the embedded
// store. It does not own the database — it discovers the single-owner
// viniferastore extension via host.GetExtensions() and writes call + finding
// records through it. Keeping one owner (the extension) and one writer (this
// exporter) is the store's core non-negotiable (CLAUDE.md).
package viniferastore

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

var typeStr = component.MustNewType("viniferastore")

// NewFactory returns the store exporter factory.
func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		typeStr,
		createDefaultConfig,
		exporter.WithLogs(createLogsExporter, component.StabilityLevelBeta),
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func createLogsExporter(
	ctx context.Context,
	set exporter.Settings,
	cfg component.Config,
) (exporter.Logs, error) {
	e := &storeExporter{logger: set.Logger}
	return exporterhelper.NewLogs(
		ctx, set, cfg,
		e.consumeLogs,
		exporterhelper.WithStart(e.start),
		exporterhelper.WithCapabilities(consumerCaps()),
	)
}
