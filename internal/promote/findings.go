package promote

// Finding-shape sync (CONTRACTS §5, POST /api/v1/findings): the collector
// periodically posts the SHAPE of its current findings to the control plane so
// the owner's dashboard can index them and deep-link back to this local UI.
// Shape means structure only — what drifted, where, how often, when. The
// observed values (`expected`, `actual`, `detail`) and any document content
// NEVER leave the collector on this path: FindingShape is an explicit
// allow-list, BuildFindingShapes copies exactly the listed fields, and the
// wire-bytes test pins their absence.

import (
	"context"
	"net/http"
	"unicode/utf8"

	"github.com/flanj-io/collector/internal/model"
)

// FindingsSyncMaxItems is the control plane's per-request cap (CONTRACTS §5):
// more than 200 findings in one POST is a 400.
const FindingsSyncMaxItems = 200

// Per-field length caps of the CP's sync DTO (the public CONTRACTS.md §5 caps;
// they mirror the MaxLength decorators in the CP's sync-finding.dto.ts). The
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
func BuildFindingShapes(findings []model.Finding) []FindingShape {
	out := make([]FindingShape, 0, len(findings))
	for _, f := range findings {
		if len(out) == FindingsSyncMaxItems {
			break
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
