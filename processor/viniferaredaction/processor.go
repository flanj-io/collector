package viniferaredaction

import (
	"context"
	"encoding/json"
	"sort"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/vinifera-io/collector/internal/otlpattr"
	"github.com/vinifera-io/collector/internal/redact"
)

type redactionProcessor struct {
	r *redact.Redactor
}

func newRedactionProcessor(cfg *Config) *redactionProcessor {
	var opts []redact.Option
	if cfg.EnableIP {
		opts = append(opts, redact.WithIP())
	}
	return &redactionProcessor{r: redact.New(opts...)}
}

// processLogs re-runs the redaction floor over the redactable attributes of every
// "call" record. Finding records carry no free text and are passed through.
func (p *redactionProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				if otlpattr.RecordType(lr) != otlpattr.RecordTypeCall {
					continue
				}
				p.redactRecord(lr)
			}
		}
	}
	return ld, nil
}

// redactRecord redacts each body attribute in place and, if anything new fired,
// updates the redaction bookkeeping attributes (applied + patterns union).
func (p *redactionProcessor) redactRecord(lr plog.LogRecord) {
	attrs := lr.Attributes()
	fired := map[string]bool{}
	for _, key := range otlpattr.BodyAttrs() {
		v, ok := attrs.Get(key)
		if !ok {
			continue
		}
		res := p.r.Redact(v.Str())
		if res.Text != v.Str() {
			attrs.PutStr(key, res.Text)
		}
		for _, id := range res.Patterns {
			fired[id] = true
		}
	}
	if len(fired) == 0 {
		return
	}
	// Something slipped past the SDK: record that the collector's floor fired.
	attrs.PutBool(otlpattr.AttrRedactApplied, true)
	mergePatterns(attrs, fired)
}

// mergePatterns unions the newly-fired pattern ids into the existing
// vinifera.redaction.patterns JSON array attribute (add-only — the SDK's set is
// preserved, the collector's additions are merged in).
func mergePatterns(attrs pcommon.Map, fired map[string]bool) {
	set := map[string]bool{}
	if v, ok := attrs.Get(otlpattr.AttrRedactPatterns); ok {
		var existing []string
		if err := json.Unmarshal([]byte(v.Str()), &existing); err == nil {
			for _, id := range existing {
				set[id] = true
			}
		}
	}
	for id := range fired {
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.Marshal(ids)
	if err != nil {
		return
	}
	attrs.PutStr(otlpattr.AttrRedactPatterns, string(b))
}
