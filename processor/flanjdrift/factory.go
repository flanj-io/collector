// Package flanjdrift is the collector's technical-adherence drift detector. It
// validates each captured "call" record against the provider's OpenAPI spec
// (live-vs-spec, kin-openapi) and, at load time, diffs spec v1->v2 for breaking
// changes (version-diff, oasdiff). Each violation is emitted as a Finding log
// record (flanj.record.type=finding) flowing to the store exporter.
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
	dp := &driftProcessor{cfg: c, logger: set.Logger, mcp: drift.NewMCPDetector()}

	// The spec is OPTIONAL: with no spec_path the processor is a pass-through that
	// still stamps call ids so capture + edge discovery work. When a spec IS
	// configured, load it once at construction so a bad spec fails the build fast.
	if c.SpecPath != "" {
		doc, err := drift.LoadSpecFile(c.SpecPath)
		if err != nil {
			return nil, fmt.Errorf("flanjdrift: load spec %q: %w", c.SpecPath, err)
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
				return nil, fmt.Errorf("flanjdrift: version diff: %w", err)
			}
			dp.versionFindings = vf
		}
	}

	// The org's OWN contract (we-as-provider): validates INBOUND responses so a
	// provider sees its own drift, not just its dependencies'. Also optional.
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
	if dp.doc != nil {
		dp.specInfos = append(dp.specInfos, specInfoRecord{
			info: specInfoFor(dp.doc, model.SpecRoleProvider, c.IntegrationID, c.PeerHost),
			raw:  dp.rawSpec,
		})
	}
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
	)
}
