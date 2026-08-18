// Package viniferadrift is the collector's technical-adherence drift detector. It
// validates each captured "call" record against the provider's OpenAPI spec
// (live-vs-spec, kin-openapi) and, at load time, diffs spec v1->v2 for breaking
// changes (version-diff, oasdiff). Each violation is emitted as a Finding log
// record (vinifera.record.type=finding) flowing to the store exporter.
//
// Technical adherence ONLY — fields/types/shapes/enums. Never business/economic
// correctness (FX/fees/spreads), which would produce a false-positive storm.
package viniferadrift

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/vinifera-io/collector/internal/drift"
)

var typeStr = component.MustNewType("viniferadrift")

// NewFactory returns the drift processor factory.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		createDefaultConfig,
		processor.WithLogs(createLogsProcessor, component.StabilityLevelBeta),
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func createLogsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	next consumer.Logs,
) (processor.Logs, error) {
	c := cfg.(*Config)

	// Load the spec once, at construction, so a bad spec fails the build fast.
	doc, err := drift.LoadSpecFile(c.SpecPath)
	if err != nil {
		return nil, fmt.Errorf("viniferadrift: load spec %q: %w", c.SpecPath, err)
	}

	dp := &driftProcessor{cfg: c, doc: doc, logger: set.Logger}

	// Compute the version-diff findings once, at load, if a v2 spec is provided.
	if c.SpecV2Path != "" {
		vf, err := drift.DetectVersionDiff(c.SpecPath, c.SpecV2Path, c.IntegrationID)
		if err != nil {
			return nil, fmt.Errorf("viniferadrift: version diff: %w", err)
		}
		dp.versionFindings = vf
	}

	return processorhelper.NewLogs(
		ctx, set, cfg, next,
		dp.processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
	)
}
