package flanjui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flanj-io/collector/internal/model"
	"github.com/flanj-io/collector/internal/store"
)

// Local finding acknowledgements (qfix-2026-08-25): a collector-local "reviewed
// it" mark for INFORMATIONAL findings only — definition_change with class
// DESCRIPTION (rule description-changed) or NON-BREAKING (severity info).
// Acknowledging is LOCAL ONLY: nothing is ever sent to the control plane, and
// it is never a path to flagging — the relay's 403 not_flaggable for
// stale_client stays exactly as it is. BREAKING findings are not ackable in
// this slice, and stale_client stays an Overview notice with no control at all.
//
// Storage is the settings KV, keyed by finding SIGNATURE (not id), so the mark
// survives a store reset and a re-detected finding keeps its id-independent
// identity.
//
// THE SIGNATURE ALONE IS NOT A SUFFICIENT KEY (qfix2-2026-08-26, ux-design-v2
// §2.8). Finding.ComputeSignature() is integration|endpoint|kind|rule|field_path
// — STABLE across successive definition changes. A SECOND description change on
// the same tool and field produces the IDENTICAL signature, so under a
// signature-only key it would arrive silently pre-acknowledged, and a breaking
// change could sit unseen behind an old acknowledgement. (The previous comment
// here claimed "a genuinely NEW change (new signature) arrives un-acked" — that
// assumption is FALSE for definition_change and is what this fixes.)
//
// So an ack also binds to the EVIDENCE VERSION it acknowledges:
//
//   - definition_change (BREAKING / NON-BREAKING / DESCRIPTION): the after-
//     snapshot content hash (Finding.SpecVersionTo — the same "AFTER (snapshot
//     …)" hash the row renders). When the provider changes the field again the
//     after-hash changes, the ack no longer matches, and the finding returns
//     UN-acknowledged in its own colour.
//   - occurrence-counted kinds (type-mismatch / output_mismatch): no evidence
//     version (empty) — the key stays the signature alone, because recurrence
//     there is expected and is surfaced as text, not as a re-alarm.
//
// Matching is EQUALITY of evidence versions (record vs finding), which makes
// the migration fail safe: a legacy record written before this change carries
// no evidence_version, so against a real definition_change (which always
// carries an after-hash) it does NOT match and the finding re-surfaces
// un-acknowledged rather than staying silently acked.
//
// THE KEY IS ONLY HALF THE FIX, and the other half lives in internal/store.
// InsertFinding dedups on signature, so a second change to the same field lands
// on the row the FIRST change created. While that row's doc stayed frozen,
// f.SpecVersionTo here could never advance and this comparison could never stop
// matching — the key would be inert and the finding still silently pre-acked.
// The store therefore REFRESHES a definition_change doc in place when its
// after-hash moves (store.refreshedFindingDoc), keeping the finding id. If that
// ever regresses, this key goes quiet with it: internal/store's
// TestDefinitionChangeRefreshesEvidence is the oracle for that half.

const (
	// settingAckPrefix + <signature> → ackRecord JSON.
	settingAckPrefix = "finding.ack."
	// settingAcksIndex → JSON array of acked signatures, and the last
	// read-modify-write left in this package: the index is re-read immediately
	// before each write and verified after, up to indexWriteAttempts times, and
	// a residual two-writer race can still drop an entry — the per-signature
	// record itself is never lost. See indexWriteAttempts for why the threads
	// list's per-thread-key answer does not transfer here.
	settingAcksIndex = "findings.acks"
)

// ackRecord is what the collector remembers about one acknowledged signature.
// It never leaves this collector.
type ackRecord struct {
	Signature string `json:"signature"`
	Rule      string `json:"rule"`
	AckedAt   string `json:"acked_at"`
	// EvidenceVersion is the content hash of the evidence this ack covers — the
	// AFTER snapshot hash for definition_change, empty for occurrence-counted
	// kinds (and on legacy records, which therefore no longer match a
	// definition_change). See the package comment above.
	EvidenceVersion string `json:"evidence_version,omitempty"`
	// Reason / Note / ActorPersonID are accepted and persisted when the caller
	// supplies them (ux-design-v2 §2.8 wire shape). The reason-set UI, the note
	// field and the person model land in the NEXT slice — nothing renders them
	// yet, and nothing here invents an actor.
	Reason        string `json:"reason,omitempty"`
	Note          string `json:"note,omitempty"`
	ActorPersonID string `json:"actor_person_id,omitempty"`
}

// ackEvidenceVersion is the evidence hash an ack on this finding binds to:
// the AFTER snapshot hash for a definition_change, empty for every other kind
// (occurrence-counted findings key on the signature alone).
func ackEvidenceVersion(f model.Finding) string {
	if f.Kind != model.KindDefinitionChange || f.SpecVersionTo == nil {
		return ""
	}
	return *f.SpecVersionTo
}

// ackMatches reports whether a stored record still acknowledges this finding.
// A definition_change whose after-hash has moved on (the provider changed the
// same field again) no longer matches, so it renders un-acknowledged.
func ackMatches(f model.Finding, rec ackRecord) bool {
	return rec.EvidenceVersion == ackEvidenceVersion(f)
}

// ackable reports whether a finding may be acknowledged: only informational
// definition changes — DESCRIPTION (rule) or NON-BREAKING (severity info).
func ackable(f model.Finding) bool {
	if f.Kind != model.KindDefinitionChange {
		return false
	}
	return f.Rule == model.RuleDescriptionChanged || f.Severity == model.SeverityInfo
}

// findingSignature is the KV key material: the stored signature, computed as a
// fallback for records that predate the signature column.
func findingSignature(f model.Finding) string {
	if f.Signature != "" {
		return f.Signature
	}
	return f.ComputeSignature()
}

func loadAckIndex(st store.Store) ([]string, error) {
	raw, ok, err := st.GetSetting(settingAcksIndex)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var sigs []string
	if err := json.Unmarshal([]byte(raw), &sigs); err != nil {
		return nil, nil // a corrupt index is treated as empty; records stay reachable by signature
	}
	return sigs, nil
}

// errAckIndexRace: the index write was overwritten by a concurrent writer on
// every attempt; the ack record itself is persisted, only the index entry is
// missing.
var errAckIndexRace = errors.New("findings.acks: concurrent writer won every attempt; record saved, index entry missing")

// indexWriteAttempts bounds the read-modify-write retry on findings.acks.
//
// findings.acks is a single JSON array behind a plain settings KV (no
// compare-and-swap), so adding or removing a signature is a read-modify-write:
// the index is re-read IMMEDIATELY before each write and the write is verified
// by a re-read afterwards, up to this many times. RESIDUAL RACE: two writers
// that both read, both write and both verify inside each other's window can
// still drop an entry — without CAS in the KV this cannot be made airtight from
// here. (The threads list retired its own array for exactly this reason and now
// uses a per-thread single-key pointer; the ack index has no equivalent reverse
// key to hang off, so the retry stays.)
const indexWriteAttempts = 3

// containsID reports whether ids holds id.
func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// mutateAckIndex adds or removes one signature in findings.acks with the
// re-read-before-write + verify retry described on indexWriteAttempts.
func mutateAckIndex(st store.Store, sig string, add bool) error {
	var lastErr error
	for attempt := 0; attempt < indexWriteAttempts; attempt++ {
		sigs, err := loadAckIndex(st) // fresh read right before the write
		if err != nil {
			return err
		}
		if containsID(sigs, sig) == add {
			return nil
		}
		next := make([]string, 0, len(sigs)+1)
		for _, s := range sigs {
			if s != sig {
				next = append(next, s)
			}
		}
		if add {
			next = append(next, sig)
		}
		nb, _ := json.Marshal(next)
		if err := st.PutSetting(settingAcksIndex, string(nb)); err != nil {
			return err
		}
		after, err := loadAckIndex(st)
		if err != nil {
			return err
		}
		if containsID(after, sig) == add {
			return nil
		}
		lastErr = errAckIndexRace
	}
	return lastErr
}

// loadAckSet returns every acknowledged signature with its record. The index
// says WHICH signatures were acknowledged; the RECORD says which evidence
// version was acknowledged, so callers must still run ackMatches. A missing or
// corrupt record therefore yields an empty evidence version — which keeps a
// definition_change un-acknowledged (fail safe) and leaves occurrence-counted
// kinds acked with no timestamp, exactly as before.
func loadAckSet(st store.Store) (map[string]ackRecord, error) {
	sigs, err := loadAckIndex(st)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ackRecord, len(sigs))
	for _, sig := range sigs {
		rec := ackRecord{Signature: sig}
		if raw, ok, err := st.GetSetting(settingAckPrefix + sig); err == nil && ok && raw != "" {
			_ = json.Unmarshal([]byte(raw), &rec)
		}
		out[sig] = rec
	}
	return out, nil
}

// ackRequestBody is the optional POST body of /api/findings/{id}/ack. Every
// field is optional and LOCAL-ONLY; the evidence version is never taken from
// the client — it is derived server-side from the finding being acknowledged.
type ackRequestBody struct {
	// Reason is the wire code for why (ux-design-v2 §2.2: "no_change" |
	// "we_adapt"). Stored opaquely — the contract file is authoritative for the
	// value set and the reason-set UI is the NEXT slice's work.
	Reason string `json:"reason"`
	// Note is the optional one-line free text. It never leaves this collector,
	// so it needs no DLP pass.
	Note string `json:"note"`
	// ActorPersonID is the person who acknowledged, when one is known. There is
	// no person model in this slice, so it is always absent today.
	ActorPersonID string `json:"actor_person_id"`
}

// decodeAckBody reads the optional ack body. An absent or empty body is normal
// (the shipped UI sends `{}`), so only malformed JSON is an error.
//
// It writes its own refusal, which is why it takes the writer: the body used to
// be read through io.LimitReader, which TRUNCATES at the limit and says
// nothing, so an oversized note arrived at the decoder cut off mid-string and
// came back as "The request body is not valid JSON." — the same silent-truncation
// trap the contract upload path was fixed for.
func decodeAckBody(w http.ResponseWriter, r *http.Request) (ackRequestBody, bool) {
	var b ackRequestBody
	if !readOptionalJSONBody(w, r, maxSmallBodyBytes, &b, "request_too_large", msgRequestTooLarge) {
		return b, false
	}
	b.Reason = strings.TrimSpace(b.Reason)
	b.Note = strings.TrimSpace(b.Note)
	b.ActorPersonID = strings.TrimSpace(b.ActorPersonID)
	return b, true
}

// handleFindingAck / handleFindingUnack are POST /api/findings/{id}/ack|unack.
// LOCAL-ONLY mutation: guarded like every mutating route EXCEPT the
// control-plane check (guardLocalMutating) — acknowledging works on a
// disconnected or unconfigured collector, and nothing is ever sent anywhere.
// {id} is resolved to the finding's SIGNATURE server-side.
func (e *uiExtension) handleFindingAck(w http.ResponseWriter, r *http.Request) {
	e.findingAck(w, r, true)
}

func (e *uiExtension) handleFindingUnack(w http.ResponseWriter, r *http.Request) {
	e.findingAck(w, r, false)
}

func (e *uiExtension) findingAck(w http.ResponseWriter, r *http.Request, ack bool) {
	if !e.guardLocalMutating(w, r) {
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	finding, ok, err := st.GetFinding(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "finding_not_found", msgFindingNotFound)
		return
	}
	if !ackable(finding) {
		writeErr(w, http.StatusForbidden, "not_ackable", msgNotAckable)
		return
	}
	sig := findingSignature(finding)
	out := map[string]any{"finding_id": finding.ID, "acked": ack}
	if ack {
		body, ok := decodeAckBody(w, r)
		if !ok {
			return // decodeAckBody wrote the refusal — 413 or 400.
		}
		now := time.Now().UTC().Format(time.RFC3339)
		// The evidence version is derived from the finding, never supplied by
		// the caller: an ack can only ever cover the evidence on screen.
		ev := ackEvidenceVersion(finding)
		b, _ := json.Marshal(ackRecord{
			Signature: sig, Rule: finding.Rule, AckedAt: now, EvidenceVersion: ev,
			Reason: body.Reason, Note: body.Note, ActorPersonID: body.ActorPersonID,
		})
		if err := st.PutSetting(settingAckPrefix+sig, string(b)); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		out["acked_at"] = now
		if ev != "" {
			out["evidence_version"] = ev
		}
	} else {
		// The settings KV has no delete; an empty record + index removal is the
		// tombstone (the index is authoritative for the acked state).
		if err := st.PutSetting(settingAckPrefix+sig, ""); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
	}
	if err := mutateAckIndex(st, sig, ack); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
