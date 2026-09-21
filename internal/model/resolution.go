package model

// Resolution is an operator's "I have dealt with this" mark on one finding.
//
// It is state ABOUT a finding row, never part of the finding: it lives in its
// own store columns and is deliberately absent from Finding, whose JSON is what
// a flag sends across the org boundary to a counterparty. A free-text note that
// rode the finding document would reach another organization with every flag.
// The one place a resolution does go is the finding sync to this deployment's
// own dashboard (promote.FindingShape) — when, and the note — and only while it
// still covers the finding.
//
// Resolving never deletes anything. The row stays in the store, leaves the
// active counts, and comes back by itself when the trouble does — see Resolution.Covers.
type Resolution struct {
	// ResolvedAt is when the operator pressed Resolve (RFC 3339, UTC).
	ResolvedAt string `json:"resolved_at"`
	// EvidenceVersion is the finding's EvidenceVersion at that moment. Empty for
	// an occurrence-counted kind.
	EvidenceVersion string `json:"evidence_version,omitempty"`
	// OccurrenceCount is how many occurrences the resolution covers: the count
	// the operator had in front of them, never more than the store held.
	OccurrenceCount int `json:"occurrence_count"`
	// Note is the operator's optional free text, already passed through the
	// redaction floor. It reaches this deployment's own dashboard with the
	// finding sync, and nothing else: never a flag, a thread or a log line.
	Note string `json:"note,omitempty"`
}

// Resolvable reports whether a finding can be resolved at all. stale_client is
// the one kind that cannot: it is an Overview notice about this deployment's
// own client, with no row and no control anywhere.
func (f Finding) Resolvable() bool {
	return f.Kind != KindStaleClient
}

// EvidenceVersion names the EVIDENCE a finding currently stands on, for the
// kinds whose evidence is a document or an announcement rather than a call.
// Empty for every other kind, whose evidence is traffic and whose recurrence is
// counted instead.
//
// The signature cannot do this job. It is integration|endpoint|kind|rule|
// field_path — stable across successive changes to the same field — so a second
// change lands on the row the first one created. Whatever was resolved on that
// row would cover the new change too, sight unseen, unless the resolution is
// bound to something that moves when the evidence does. This is that thing, and
// the store refreshes the row's document when it moves (refreshedFindingDoc), or
// the value read here could never advance.
//
//   - definition_change: the AFTER snapshot's content hash.
//   - version-diff, and a deprecation found by diffing two contract versions:
//     the version the change arrived in.
//   - a deprecation found in live traffic: the announcement itself, "deprecated"
//     or "deprecated, sunset <date>". This arm is evidenced by calls, but its
//     occurrence count rises with every call that uses the deprecated surface —
//     for the whole deprecation window, by design — so binding a resolution to
//     the count would undo it seconds after it was made. What can actually
//     change under an operator who has read the notice is the notice: a sunset
//     date appearing, or moving. The removal itself is a different finding, with
//     a different signature, that no resolution of this one touches.
func (f Finding) EvidenceVersion() string {
	switch f.Kind {
	case KindDefinitionChange, KindVersionDiff:
		if f.SpecVersionTo != nil {
			return *f.SpecVersionTo
		}
		return ""
	case KindDeprecation:
		if f.SpecVersionTo != nil && *f.SpecVersionTo != "" {
			return *f.SpecVersionTo
		}
		return f.Actual
	}
	return ""
}

// Covers reports whether a stored resolution still covers this finding. It is
// the whole safety property of resolving — a resolution can never hide NEW
// trouble — and it is judged every time the finding is read, so nothing has to
// be written, and nothing can be missed, at the moment the trouble returns.
//
//   - Evidence-versioned kinds: covered while the evidence is the evidence that
//     was resolved. A new version re-surfaces the row.
//   - Occurrence-counted kinds: covered while nothing has happened since.
//     "Resolved" means "this stopped"; one more occurrence and it is open again,
//     under the same finding id, so a thread started from it stays attached.
//
// The count, not a timestamp, is what "since" is measured in. last_seen is
// stamped by whichever pod detected the drift and resolved_at by the pod serving
// the UI; two clocks a few seconds apart would hide exactly one recurrence. The
// count is advanced atomically inside the store and is immune to a re-delivered
// record.
//
// A finding that cannot be resolved is never covered, and neither is anything by
// a zero Resolution.
func (r Resolution) Covers(f Finding) bool {
	if r.ResolvedAt == "" || !f.Resolvable() {
		return false
	}
	if ev := f.EvidenceVersion(); ev != "" {
		return r.EvidenceVersion == ev
	}
	if r.EvidenceVersion != "" {
		// Resolved as an evidence-versioned finding and read back as one without
		// evidence: not the same thing any more.
		return false
	}
	return f.OccurrenceCount <= r.OccurrenceCount
}
