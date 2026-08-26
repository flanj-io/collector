package viniferaui

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/vinifera-io/collector/internal/model"
	"github.com/vinifera-io/collector/internal/store"
)

// Local finding acknowledgements (qfix-2026-08-25): a collector-local "reviewed
// it" mark for INFORMATIONAL findings only — definition_change with class
// DESCRIPTION (rule description-changed) or NON-BREAKING (severity info).
// Acknowledging is LOCAL ONLY: nothing is ever sent to the control plane, and
// it is never a path to flagging — Finding.Flaggable() and the relay's
// 403 not_flaggable stay exactly as they are. BREAKING findings are never
// ackable (breaking drift is resolved by a fix or a thread, not muted), and
// stale_client stays an Overview notice with no control at all.
//
// Storage is the settings KV, keyed by finding SIGNATURE (not id): the mark
// survives a store reset, and a genuinely NEW change (new signature) arrives
// un-acked — exactly right, with no explanation needed.

const (
	// settingAckPrefix + <signature> → ackRecord JSON.
	settingAckPrefix = "finding.ack."
	// settingAcksIndex → JSON array of acked signatures. Same read-modify-write
	// retry caveat as threads.index (see saveThread): the index is re-read
	// immediately before each write and verified after, up to
	// indexWriteAttempts times; a residual two-writer race can still drop an
	// entry — the per-signature record itself is never lost.
	settingAcksIndex = "findings.acks"
)

// ackRecord is what the collector remembers about one acknowledged signature.
type ackRecord struct {
	Signature string `json:"signature"`
	Rule      string `json:"rule"`
	AckedAt   string `json:"acked_at"`
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

// errAckIndexRace mirrors errIndexRace for the acks index.
var errAckIndexRace = errors.New("findings.acks: concurrent writer won every attempt; record saved, index entry missing")

// mutateAckIndex adds or removes one signature in findings.acks with the same
// re-read-before-write + verify retry as addToThreadIndex.
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

// loadAckSet returns every acknowledged signature with its record (the index is
// authoritative for the acked STATE; a missing/corrupt record only loses the
// acked_at timestamp, never the mark).
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
		now := time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(ackRecord{Signature: sig, Rule: finding.Rule, AckedAt: now})
		if err := st.PutSetting(settingAckPrefix+sig, string(b)); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		out["acked_at"] = now
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
