package flanjredaction

import (
	"context"
	"encoding/json"
	"sort"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/otlpattr"
	"github.com/flanj-io/collector/internal/redact"
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
// "call" record and over the snapshot document of every "contract_snapshot"
// record (v0.5 — the snapshot is STORED as the edge's local spec, so the same
// defense-in-depth applies before storage). Finding records carry no free text
// and are passed through.
func (p *redactionProcessor) processLogs(_ context.Context, ld plog.Logs) (plog.Logs, error) {
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				switch otlpattr.RecordType(lr) {
				case otlpattr.RecordTypeCall:
					p.redactRecord(lr)
				case otlpattr.RecordTypeContractSnapshot:
					p.redactSnapshot(lr)
				}
			}
		}
	}
	return ld, nil
}

// redactSnapshot re-runs the floor over the observed tools/list document (the
// SDK already floor-redacted it at source; this pass is idempotent and
// add-only, like redactRecord). No field records: the snapshot is a contract
// document, not a call body.
func (p *redactionProcessor) redactSnapshot(lr plog.LogRecord) {
	attrs := lr.Attributes()
	v, ok := attrs.Get(otlpattr.AttrMCPContractSnapshot)
	if !ok {
		return
	}
	res := p.r.Redact(v.Str())
	if res.Text != v.Str() {
		attrs.PutStr(otlpattr.AttrMCPContractSnapshot, res.Text)
	}
	if len(res.Patterns) == 0 {
		return
	}
	fired := map[string]bool{}
	for _, id := range res.Patterns {
		fired[id] = true
	}
	attrs.PutBool(otlpattr.AttrRedactApplied, true)
	mergePatterns(attrs, fired)
}

// redactRecord redacts each body attribute in place and, if anything new fired,
// updates the redaction bookkeeping attributes (applied + patterns union +
// whole-value field records for the two body attrs).
func (p *redactionProcessor) redactRecord(lr plog.LogRecord) {
	attrs := lr.Attributes()
	fired := map[string]bool{}
	var newFields []model.RedactedFieldRecord
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
		// Only the two BODIES map to a field part; url/target/headers carry no
		// field records (a field path is a pointer into a body).
		if part := partForAttr(key); part != "" {
			for _, f := range res.Fields {
				newFields = append(newFields, model.RedactedFieldRecord{Part: part, RedactedField: f})
			}
		}
	}
	if len(fired) == 0 {
		return
	}
	// Something slipped past the SDK: record that the collector's floor fired.
	attrs.PutBool(otlpattr.AttrRedactApplied, true)
	mergePatterns(attrs, fired)
	if len(newFields) > 0 {
		mergeFields(attrs, newFields)
	}
}

// partForAttr maps a redactable attribute to the body part its field records belong
// to; "" for attributes that carry no fields.
func partForAttr(key string) string {
	switch key {
	case otlpattr.AttrReqBody:
		return "request"
	case otlpattr.AttrRespBody:
		return "response"
	}
	return ""
}

// mergePatterns unions the newly-fired pattern ids into the existing
// flanj.redaction.patterns JSON array attribute (add-only — the SDK's set is
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

// mergeFields unions the collector floor's whole-value field records into the
// existing flanj.redaction.fields JSON attribute. Add-only, like the patterns
// merge — and on a (part,path) collision the EXISTING entry wins: the SDK's floor
// ran first and captured the props of the truer original.
func mergeFields(attrs pcommon.Map, added []model.RedactedFieldRecord) {
	var merged []model.RedactedFieldRecord
	if v, ok := attrs.Get(otlpattr.AttrRedactFields); ok {
		_ = json.Unmarshal([]byte(v.Str()), &merged)
	}
	seen := map[[2]string]bool{}
	for _, f := range merged {
		seen[[2]string{f.Part, f.Path}] = true
	}
	for _, f := range added {
		key := [2]string{f.Part, f.Path}
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, f)
	}
	// Canonical wire order: by part ("request" < "response"), then by path.
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Part != merged[j].Part {
			return merged[i].Part < merged[j].Part
		}
		return merged[i].Path < merged[j].Path
	})
	b, err := json.Marshal(merged)
	if err != nil {
		return
	}
	attrs.PutStr(otlpattr.AttrRedactFields, string(b))
}
