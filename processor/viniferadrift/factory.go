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
	"os"

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

	dp := &driftProcessor{cfg: c, logger: set.Logger}

	// The spec is OPTIONAL: with no spec_path the processor is a pass-through that
	// still stamps call ids so capture + edge discovery work. When a spec IS
	// configured, load it once at construction so a bad spec fails the build fast.
	if c.SpecPath != "" {
		doc, err := drift.LoadSpecFile(c.SpecPath)
		if err != nil {
			return nil, fmt.Errorf("viniferadrift: load spec %q: %w", c.SpecPath, err)
		}
		dp.doc = doc
		// Keep the raw document too: at Start it is recorded in the shared store
		// so the local UI can link to the exact contract being validated.
		if raw, err := os.ReadFile(c.SpecPath); err == nil {
			dp.rawSpec = raw
		}

		// Compute the version-diff findings once, at load, if a v2 spec is provided.
		if c.SpecV2Path != "" {
			vf, err := drift.DetectVersionDiff(c.SpecPath, c.SpecV2Path, c.IntegrationID)
			if err != nil {
				return nil, fmt.Errorf("viniferadrift: version diff: %w", err)
			}
			dp.versionFindings = vf
		}
	}

	// The org's OWN contract (we-as-provider): validates INBOUND responses so a
	// provider sees its own drift, not just its dependencies'. Also optional.
	if c.SelfSpecPath != "" {
		doc, err := drift.LoadSpecFile(c.SelfSpecPath)
		if err != nil {
			return nil, fmt.Errorf("viniferadrift: load self spec %q: %w", c.SelfSpecPath, err)
		}
		dp.selfDoc = doc
		if raw, err := os.ReadFile(c.SelfSpecPath); err == nil {
			dp.rawSelfSpec = raw
		}
	}

	return processorhelper.NewLogs(
		ctx, set, cfg, next,
		dp.processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
		processorhelper.WithStart(dp.start),
	)
}
