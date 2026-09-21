package flanjui

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/redact"
)

// Resolving a finding: the one "I have dealt with this" mark this collector has.
//
// Finding rows never age out — retention evicts calls only — so without it a
// finding that was fixed, or that described an integration long since changed,
// sat in the active view forever. Resolving takes a row out of every count and
// moves it to the Resolved band. It deletes nothing: the row, its evidence and
// any thread started from it stay exactly where they were, and Reopen puts it
// back.
//
// It replaces the earlier "acknowledge", which did the same thing for
// informational definition changes only. There is deliberately one mechanism,
// for every kind a row is drawn for (model.Finding.Resolvable).
//
// A RESOLUTION CAN NEVER HIDE NEW TROUBLE. That is the property everything here
// is arranged around, and it lives in one place — model.Resolution.Covers,
// judged each time the finding is read:
//
//   - a finding evidenced by a document or an announcement (definition_change,
//     version-diff, deprecation) is bound to that evidence's version, and the
//     store refreshes the row when the version moves, so the next change to the
//     same field comes back open instead of arriving already resolved;
//   - a finding evidenced by traffic is bound to its occurrence count, so one
//     more occurrence reopens it — same row, same id, same thread.
//
// What the binding is made of is decided on the SERVER, from the stored finding.
// The one thing the browser contributes is the occurrence count it had on screen
// (seen_occurrence_count), and it can only ever lower the bound: an occurrence
// that landed between the last poll and the press is one the operator never saw,
// and must not be covered by a resolution of the ones they did.
//
// What is BOUND stays here; what was DECIDED is shared with the dashboard. The
// finding sync carries when a finding was resolved and the operator's note, for
// as long as the resolution still covers it (promote.FindingShape) — so the
// dashboard stops counting what the collector stopped counting — and never the
// evidence version or the count behind it. The note is free text typed by a
// person looking at payloads, so it passes the redaction floor on the way IN,
// before it is stored: every later reader — the sync, and the agent read
// surface, whose next hop can be a model provider — only ever sees the floored
// text. The editor says beside the field that the note is sent. It never rides
// a flag, a thread or a log line.

// maxResolveNoteRunes bounds the note. A longer one is REFUSED, never cut: a
// note that silently lost its second half is worse than one the operator is
// asked to shorten.
const maxResolveNoteRunes = 500

// resolveRequestBody is the optional POST body of /api/findings/{id}/resolve.
type resolveRequestBody struct {
	// Note is the operator's optional free text.
	Note string `json:"note"`
	// SeenOccurrenceCount is the occurrence count the row showed when Resolve was
	// pressed. Optional; it can only lower what the resolution covers.
	SeenOccurrenceCount int `json:"seen_occurrence_count"`
}

// resolutionFor builds the resolution of f as it is stored right now. seen is
// the browser's count, or 0 when it sent none.
func resolutionFor(f model.Finding, note string, seen int, now time.Time) model.Resolution {
	covered := f.OccurrenceCount
	if seen > 0 && seen < covered {
		covered = seen
	}
	return model.Resolution{
		ResolvedAt:      now.UTC().Format(time.RFC3339),
		EvidenceVersion: f.EvidenceVersion(),
		OccurrenceCount: covered,
		Note:            note,
	}
}

// handleFindingResolve / handleFindingReopen are POST /api/findings/{id}/resolve
// and /reopen. LOCAL mutations: guarded like every mutating route EXCEPT the
// control-plane check (guardLocalMutating) — resolving works on a collector that
// was never Connected, and the request itself sends nothing anywhere.
func (e *uiExtension) handleFindingResolve(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	finding, ok, err := st.GetFinding(r.PathValue("id"))
	if err != nil {
		e.storeErr(w, "get finding", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	if !finding.Resolvable() {
		writeErr(w, http.StatusForbidden, "not_resolvable", msgNotResolvable)
		return
	}
	var body resolveRequestBody
	if !readOptionalJSONBody(w, r, maxSmallBodyBytes, &body, "request_too_large", msgRequestTooLarge) {
		return // the refusal is written — 413 or 400.
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > maxResolveNoteRunes {
		writeErr(w, http.StatusBadRequest, "note_too_long", msgResolveNoteTooLong)
		return
	}
	if note != "" {
		note = strings.TrimSpace(redact.New().Redact(note).Text)
	}
	res := resolutionFor(finding, note, body.SeenOccurrenceCount, time.Now())
	if found, err := st.ResolveFinding(finding.ID, res); err != nil {
		e.storeErr(w, "resolve finding", err)
		return
	} else if !found {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	// Whether it is resolved is answered by the same rule every reader uses, not
	// assumed: a press that raced a new occurrence answers resolved:false, and the
	// row the operator sees next is still open, which is the truth.
	writeJSON(w, http.StatusOK, map[string]any{
		"finding_id":  finding.ID,
		"resolved":    res.Covers(finding),
		"resolved_at": res.ResolvedAt,
	})
}

func (e *uiExtension) handleFindingReopen(w http.ResponseWriter, r *http.Request) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	found, err := st.ReopenFinding(r.PathValue("id"))
	if err != nil {
		e.storeErr(w, "reopen finding", err)
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"finding_id": r.PathValue("id"), "resolved": false})
}
