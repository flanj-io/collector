package promote

// Finding-shape sync (CONTRACTS §5, POST /api/v1/findings): the collector
// periodically posts the SHAPE of its current findings to the control plane so
// the owner's dashboard can index them and deep-link back to this local UI.
// Shape means structure only — what drifted, where, how often, when. The
// observed values (`expected`, `actual`, `detail`) and any document content
// NEVER leave the collector on this path: FindingShape is an explicit
// allow-list, BuildFindingShapes copies exactly the listed fields, and the
// wire-bytes test pins their absence. One field is free text by design — the
// note an operator attached to a resolution (FindingShape.ResolvedNote).

import (
	"context"
	"net/http"
	"unicode/utf8"

	"github.com/flanj-io/collector/internal/model"
)

// FindingsSyncMaxItems is the control plane's per-request cap (CONTRACTS §5):
// more than 200 findings in one POST is a 400.
const FindingsSyncMaxItems = 200

// Per-field length caps of the CP's sync DTO (the public CONTRACTS.md §5 caps; the
// CP enforces the same maxima). The
// CP validates the batch as a unit, so ONE oversized value would 400 the whole
// POST on every tick forever — BuildFindingShapes truncates every string field
// to its cap instead.
const (
	capFindingID   = 128
	capSignature   = 1024
	capKind        = 64
	capSeverity    = 32
	capIntegration = 256
	capEndpoint    = 256
	capFieldPath   = 256
	capRule        = 128
	capTimestamp   = 64
	// capResolvedNote sits well above the 500 characters the resolve route
	// accepts: redaction tokens can lengthen a note, and this cap is only the
	// liveness backstop every field here has.
	capResolvedNote = 2000
)

// truncateToCap bounds s to at most max bytes, cut on a rune boundary. The CP
// counts UTF-16 code units and a string's UTF-16 length never exceeds its
// UTF-8 byte length, so the byte bound always satisfies the DTO cap.
func truncateToCap(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// FindingShape is one row of the POST /api/v1/findings request — the
// shape-only allow-list (CONTRACTS §5). Deliberately NOT model.Finding:
// expected / actual / detail / location carry observed values and stay local,
// and a new model field stays local too until it is deliberately added here.
type FindingShape struct {
	FindingID            string `json:"finding_id"`
	Signature            string `json:"signature"`
	Kind                 string `json:"kind"`
	Severity             string `json:"severity"`
	Integration          string `json:"integration"`
	Endpoint             string `json:"endpoint"`
	FieldPath            string `json:"field_path,omitempty"`
	Rule                 string `json:"rule"`
	OccurrenceCount      int    `json:"occurrence_count"`
	FirstSeen            string `json:"first_seen"`
	LastSeen             string `json:"last_seen"`
	DetectedAt           string `json:"detected_at"`
	SnapshotObservedAt   string `json:"snapshot_observed_at,omitempty"`
	SnapshotObservedFrom string `json:"snapshot_observed_from,omitempty"`
	// ResolvedAt / ResolvedNote: an operator's resolution of this finding — when,
	// and the optional note — sent only while that resolution still covers the
	// finding (model.Resolution.Covers) and absent otherwise. The dashboard
	// therefore mirrors this collector's answer on every tick, and a finding
	// that came back is open there one tick later with no logic of its own.
	//
	// The note is the ONE field on this path a person typed, which makes it the
	// one exception to "shape only". It was passed through the redaction floor
	// before it was stored (the UI extension's resolve handler), the editor says
	// beside the field that it is sent, and the control plane scans it again
	// before storing it. Nothing else about the resolution crosses.
	ResolvedAt   string `json:"resolved_at,omitempty"`
	ResolvedNote string `json:"resolved_note,omitempty"`
}

// FindingsRequest is the POST /api/v1/findings body. An empty list is a valid
// no-op on the CP, but callers normally skip the POST entirely.
type FindingsRequest struct {
	Findings []FindingShape `json:"findings"`
}

// FindingsResponse is the 200 answer: how many rows the CP received and how
// many it stored (upsert by signature — a repeat post updates in place).
type FindingsResponse struct {
	Received int `json:"received"`
	Stored   int `json:"stored"`
}

// BuildFindingShapes maps stored findings onto the wire allow-list. It COPIES
// exactly the listed fields — never the finding document. Defensive
// normalization for records written by older collectors: occurrence count
// floors at 1 (a stored finding is at least one observation), first/last seen
// fall back to detected_at when empty, and a missing signature is recomputed
// with the CONTRACTS §4 convention. Every string field is truncated to its CP
// DTO cap (liveness: one oversized value must not 400 the whole batch on every
// tick). At most FindingsSyncMaxItems rows are returned.
//
// inbound names the findings whose source call was inbound
// (store.Store.InboundFindingIDs): they are keyed locally by the service the
// call reached, a name that never leaves the collector (CONTRACTS §3), so they
// cross as integration "self" with the signature recomputed on it.
//
// resolutions are the stored resolutions by finding id
// (store.Store.FindingResolutions). Whether one still covers its finding is
// judged HERE, against the finding exactly as it is about to be sent: a lapsed
// resolution must never cross as a live one.
func BuildFindingShapes(findings []model.Finding, inbound map[string]bool, resolutions map[string]model.Resolution) []FindingShape {
	out := make([]FindingShape, 0, len(findings))
	for _, f := range findings {
		if len(out) == FindingsSyncMaxItems {
			break
		}
		resolvedAt, resolvedNote := "", ""
		if r, ok := resolutions[f.ID]; ok && r.Covers(f) {
			resolvedAt, resolvedNote = r.ResolvedAt, r.Note
		}
		if inbound[f.ID] {
			f = wireSelf(f)
		}
		sig := f.Signature
		if sig == "" {
			sig = f.ComputeSignature()
		}
		firstSeen := f.FirstSeen
		if firstSeen == "" {
			firstSeen = f.DetectedAt
		}
		lastSeen := f.LastSeen
		if lastSeen == "" {
			lastSeen = firstSeen
		}
		occ := f.OccurrenceCount
		if occ < 1 {
			occ = 1
		}
		field := ""
		if f.FieldPath != nil {
			field = *f.FieldPath
		}
		out = append(out, FindingShape{
			FindingID:            truncateToCap(f.ID, capFindingID),
			Signature:            truncateToCap(sig, capSignature),
			Kind:                 truncateToCap(f.Kind, capKind),
			Severity:             truncateToCap(f.Severity, capSeverity),
			Integration:          truncateToCap(f.Integration, capIntegration),
			Endpoint:             truncateToCap(f.Endpoint, capEndpoint),
			FieldPath:            truncateToCap(field, capFieldPath),
			Rule:                 truncateToCap(f.Rule, capRule),
			OccurrenceCount:      occ,
			FirstSeen:            truncateToCap(firstSeen, capTimestamp),
			LastSeen:             truncateToCap(lastSeen, capTimestamp),
			DetectedAt:           truncateToCap(f.DetectedAt, capTimestamp),
			SnapshotObservedAt:   truncateToCap(f.SnapshotObservedAt, capTimestamp),
			SnapshotObservedFrom: truncateToCap(f.SnapshotObservedFrom, capTimestamp),
			ResolvedAt:           truncateToCap(resolvedAt, capTimestamp),
			ResolvedNote:         truncateToCap(resolvedNote, capResolvedNote),
		})
	}
	return out
}

// PostFindings sends one shape-only sync batch: POST /api/v1/findings, Bearer
// collector key, the usual version headers. Single attempt, no retry — the
// caller's next tick is the retry. 200 { received, stored }.
func (c *Client) PostFindings(ctx context.Context, req FindingsRequest) (FindingsResponse, int, error) {
	var out FindingsResponse
	status, err := c.do(ctx, http.MethodPost, "/api/v1/findings", c.bearer(), req, &out, http.StatusOK)
	return out, status, err
}
