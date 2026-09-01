// Package flanjdrift is the collector's technical-adherence drift detector. It
// validates each captured "call" record against the provider's OpenAPI spec
// (live-vs-spec, kin-openapi) and, at load time, diffs spec v1->v2 for breaking
// changes (version-diff, oasdiff). Each violation is emitted as a Finding log
// record (flanj.record.type=finding) flowing to the store exporter.
//
// Provider contracts are UPLOADED in the UI and read from the store at runtime
// (speccache.go), never from config. The org's OWN contract stays config.
//
// Technical adherence ONLY — fields/types/shapes/enums. Never business/economic
// correctness (pricing, quantities, business rules), which would produce a false-positive storm.
package flanjdrift

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"github.com/flanj-io/collector/internal/drift"
	"github.com/flanj-io/collector/internal/model"
)

var typeStr = component.MustNewType("flanjdrift")

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

	// The MCP detector is ALWAYS on (no config): MCP contracts are
	// self-delivering — an observed tools/list snapshot is the local spec.
	dp := &driftProcessor{
		cfg:    c,
		logger: set.Logger,
		mcp:    drift.NewMCPDetector(),
		specs:  newSpecCache(),
		kick:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}

	// PROVIDER contracts are not loaded here — they arrive from the store at
	// Start and on every refresh tick. With none uploaded the processor is a
	// pass-through that still stamps call ids, so capture and edge discovery
	// work exactly as before.

	// The org's OWN contract (we-as-provider): validates INBOUND responses so a
	// provider sees its own drift, not just its dependencies'. Optional, and
	// still config — one document per deployment, loaded once, fails fast.
	if c.SelfSpecPath != "" {
		doc, err := drift.LoadSpecFile(c.SelfSpecPath)
		if err != nil {
			return nil, fmt.Errorf("flanjdrift: load self spec %q: %w", c.SelfSpecPath, err)
		}
		dp.selfDoc = doc
		if raw, err := os.ReadFile(c.SelfSpecPath); err == nil {
			dp.rawSelfSpec = raw
		}
	}

	// Precompute the contract metadata records once (stable loaded_at): used
	// for the direct store write at Start AND emitted into the pipeline for a
	// store pod behind an otlphttp hop.
	//
	// Only the SELF contract now. A front no longer announces provider
	// contracts upward — it READS them from the store pod, which is where the
	// upload landed and which is therefore already their source of truth.
	if dp.selfDoc != nil {
		dp.specInfos = append(dp.specInfos, specInfoRecord{
			info: specInfoFor(dp.selfDoc, model.SpecRoleSelf, c.selfIntegration(), ""),
			raw:  dp.rawSelfSpec,
		})
	}

	return processorhelper.NewLogs(
		ctx, set, cfg, next,
		dp.processLogs,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
		processorhelper.WithStart(dp.start),
		processorhelper.WithShutdown(dp.shutdown),
	)
}
