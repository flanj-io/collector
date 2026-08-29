package flanjui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/flanj-io/collector/internal/promote"
	"github.com/flanj-io/collector/internal/store"
)

// Threads (v0.1a; list source moved to the CP in slice-inbox): every
// conversation lives on the control plane, and since CONTRACTS-CP §5.5a the
// LIST does too. `GET /api/threads` makes ONE call to `GET /api/v1/threads`
// (Bearer collector key, scoped on the CP by collector id) and joins the local
// fields the UI needs onto each row.
//
// Three settings-KV keys back that join. None of them is a thread store, and
// EVERY write below is a single blind PutSetting — never a read-modify-write.
// The KV has no compare-and-swap (internal/store/store.go), so a
// read-then-write on a key two pods share is a lost-update waiting to happen;
// the way to have no race is to have no read in the write path.
//
//	thread.finding.<finding_id> -> threadRecord JSON — the local join record for
//	    a thread this collector CREATED from a finding. It is what renders the
//	    "In thread" chip on a finding and what `Copy thread link` copies (the
//	    thread-link token lives ONLY in a URL fragment and never comes back from
//	    the CP, so this is the only copy).
//	thread.id.<thread_id>       -> the finding id — the reverse pointer, because
//	    a CP row carries no finding_id and the settings KV has only
//	    GetSetting/PutSetting with NO prefix scan. Only ever written with a REAL
//	    finding id; absence means "no local record", which is exactly what an
//	    empty value would have meant, so the empty value is never written.
//	thread.link.<thread_id>     -> the thread link for a thread this collector
//	    holds NO finding record for (see handleThreadReplaceLink).
//
// These REPLACE the old `threads.index` JSON array. That array was a
// read-modify-write behind a KV with no compare-and-swap, so two concurrent
// writers could drop an id and hide a thread from the list. Per-thread keys are
// single-key writes, so the race does not move — it disappears. The legacy array
// is still READ (lazily, to recover a missing pointer) and is never written or
// cleared: another version of this collector may still be enumerating from it.
const (
	// settingThreadPrefix + <finding_id> → threadRecord JSON.
	settingThreadPrefix = "thread.finding."
	// settingThreadIDPrefix + <thread_id> → the finding id (a plain string).
	settingThreadIDPrefix = "thread.id."
	// settingThreadLinkPrefix + <thread_id> → the thread link for a thread with
	// no finding record. Keyed by thread id so the write is blind and the only
	// writer is Replace link for that exact thread.
	settingThreadLinkPrefix = "thread.link."
	// settingThreadsIndex is the LEGACY index a previous version wrote and still
	// reads. READ-ONLY here: it is another version's data, and a stale array is
	// harmless once nothing enumerates from it.
	settingThreadsIndex = "threads.index"

	// threadListTimeout bounds the single CP list call behind GET /api/threads.
	threadListTimeout = 8 * time.Second
)

// threadRecord is what the collector remembers about one thread it created.
type threadRecord struct {
	ThreadID       string `json:"thread_id"`
	ThreadPublicID string `json:"thread_public_id"`
	FindingID      string `json:"finding_id"`
	Endpoint       string `json:"endpoint"`
	Provider       string `json:"provider"`
	Integration    string `json:"integration,omitempty"`
	ThreadURL      string `json:"thread_url"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

func loadThread(st store.Store, findingID string) (threadRecord, bool, error) {
	if findingID == "" {
		return threadRecord{}, false, nil
	}
	raw, ok, err := st.GetSetting(settingThreadPrefix + findingID)
	if err != nil || !ok {
		return threadRecord{}, false, err
	}
	var rec threadRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return threadRecord{}, false, nil
	}
	return rec, true, nil
}

// saveThread upserts the record and its reverse pointer as TWO independent
// single-key writes. Neither is a read-modify-write, so there is no lost-update
// window to retry around: concurrent writers for different findings touch
// different keys, and concurrent writers for the SAME finding write the same
// pointer value.
func saveThread(st store.Store, rec threadRecord) error {
	if rec.FindingID != "" {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := st.PutSetting(settingThreadPrefix+rec.FindingID, string(b)); err != nil {
			return err
		}
	}
	if rec.ThreadID == "" || rec.FindingID == "" {
		return nil
	}
	return st.PutSetting(settingThreadIDPrefix+rec.ThreadID, rec.FindingID)
}

// findThreadByID resolves a CP thread id to what this collector knows about it.
// For a non-empty id it ALWAYS resolves: when no pointer exists, or the pointer
// is stale, or the record it names is gone, the answer is a MINIMAL record —
// the thread id, plus any link Replace link persisted for it.
//
// `joined` reports whether a real finding record was joined. It is not an
// authorization answer, and nothing here grants access: the control plane
// authorizes every thread operation by collector key AND origin match, so a
// bogus id fails at the CP, not here. Resolving locally only decides which
// local fields a row can carry.
//
// STALE POINTER (why the ThreadID must be checked): thread.finding.<fid> is
// rewritten IN PLACE when a finding is re-flagged onto a new thread
// (handlers.go), and the KV has no delete, so the old thread.id.<old> pointer
// survives and still names that finding. Following it blindly would hand back a
// record whose ThreadID is the NEW thread — and every caller acts on
// rec.ThreadID, so a stale tab or a deep link would close the wrong thread and
// Replace link would revoke the wrong thread's live link. A mismatch is
// therefore "no local record", never a hit.
func findThreadByID(st store.Store, threadID string) (threadRecord, bool, error) {
	if threadID == "" {
		return threadRecord{}, false, nil
	}
	findingID, ok, err := st.GetSetting(settingThreadIDPrefix + threadID)
	if err != nil {
		return threadRecord{}, false, err
	}
	if ok && findingID != "" {
		rec, found, err := loadThread(st, findingID)
		if err != nil {
			return threadRecord{}, false, err
		}
		if found && rec.ThreadID == threadID {
			if rec.ThreadURL == "" {
				// The record won the join but carries no link: a link parked
				// under thread.link.<id> (Replace link, before this pointer
				// resolved) is still this collector's only copy.
				if parked, _, err := st.GetSetting(settingThreadLinkPrefix + threadID); err == nil {
					rec.ThreadURL = parked
				}
			}
			return rec, true, nil
		}
	}
	return minimalThread(st, threadID)
}

// minimalThread is what this collector knows about a thread it holds no finding
// record for: the id, plus the link Replace link persisted under
// thread.link.<thread_id>.
func minimalThread(st store.Store, threadID string) (threadRecord, bool, error) {
	url, _, err := st.GetSetting(settingThreadLinkPrefix + threadID)
	if err != nil {
		return threadRecord{}, false, err
	}
	return threadRecord{ThreadID: threadID, ThreadURL: url}, false, nil
}

// legacyIndex is the LAZY, self-healing replacement for the old one-shot
// threads.index migration.
//
// The migration backfilled every pointer and then cleared the array behind a
// sync.Once. Both halves were wrong. Clearing destroyed a key the PREVIOUS
// version still derives its whole list from, so one request served by a new pod
// emptied every old pod's Threads tab for the rest of a rolling upgrade; and a
// PutSetting failure part-way spent the sync.Once anyway, leaving that pod
// degraded for its whole life.
//
// Recovery is now per-listed-thread and idempotent: when the CP lists a thread
// no pointer resolves, the legacy array is consulted ONCE per request for a
// record whose ThreadID matches, and the REAL finding id is written as a single
// blind PutSetting. Nothing is cleared, nothing is read-modify-written, and a
// failure just means the next list tries again.
type legacyIndex struct {
	loaded   bool
	byThread map[string]string // thread id -> finding id
}

// findingFor returns the finding id the legacy array holds for threadID, or ""
// when the array is absent, corrupt, or names no matching record. The array is
// read (and every record it names loaded) at most once per legacyIndex, and only
// when something actually needs recovering.
func (l *legacyIndex) findingFor(st store.Store, threadID string) (string, error) {
	if !l.loaded {
		byThread, err := loadLegacyIndex(st)
		if err != nil {
			return "", err
		}
		l.byThread, l.loaded = byThread, true
	}
	return l.byThread[threadID], nil
}

func loadLegacyIndex(st store.Store) (map[string]string, error) {
	byThread := map[string]string{}
	raw, ok, err := st.GetSetting(settingThreadsIndex)
	if err != nil || !ok || raw == "" {
		return byThread, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return byThread, nil // corrupt: nothing to recover from, never an error
	}
	for _, findingID := range ids {
		rec, found, err := loadThread(st, findingID)
		if err != nil {
			return nil, err
		}
		if found && rec.ThreadID != "" {
			byThread[rec.ThreadID] = findingID
		}
	}
	return byThread, nil
}

// recoverThreadPointer heals one missing thread.id.<thread_id> pointer from the
// legacy index. Best effort throughout: a store failure only means the row keeps
// rendering without its local link, and the next list retries.
func (e *uiExtension) recoverThreadPointer(st store.Store, legacy *legacyIndex, threadID string) (threadRecord, bool) {
	findingID, err := legacy.findingFor(st, threadID)
	if err != nil {
		e.telemetry.Logger.Warn("reading the previous thread index failed (threads still list; a local link copy may be missing): " + err.Error())
		return threadRecord{}, false
	}
	if findingID == "" {
		return threadRecord{}, false
	}
	rec, found, err := loadThread(st, findingID)
	if err != nil || !found || rec.ThreadID != threadID {
		return threadRecord{}, false
	}
	if err := st.PutSetting(settingThreadIDPrefix+threadID, findingID); err != nil {
		e.telemetry.Logger.Warn("recovering a thread pointer from the previous thread index failed: " + err.Error())
	}
	return rec, true
}

// threadView is one GET /api/threads row: the CP summary (the list's source of
// truth) with the local fields the UI cannot get from the CP joined on
// (finding_id for the chip, integration, thread_url for Copy thread link).
type threadView struct {
	threadRecord
	Summary *promote.ThreadSummary `json:"summary"`
}

// threadListView is the GET /api/threads envelope. It is the collector's OWN
// internal shape, not a published contract, and it exists so the tab can be
// honest: §5.5a has no cursor, so a collector with more threads than the hard
// cap is silently truncated unless total/has_more are relayed.
type threadListView struct {
	Threads []threadView `json:"threads"`
	Count   int          `json:"count"`
	Total   int          `json:"total"`
	Limit   int          `json:"limit"`
	HasMore bool         `json:"has_more"`
}

// mergeThreadRow builds one row from a CP summary plus the local record the
// reverse pointer resolved to (a minimal record when there is none). The CP wins
// on everything it reports; the local record only fills what a §5.5a row cannot
// carry — and a row with no local record still renders, with an empty
// thread_url the UI turns into a disabled Copy thread link.
func mergeThreadRow(sum promote.ThreadSummary, rec threadRecord) threadView {
	out := threadRecord{
		ThreadID:       sum.ID,
		ThreadPublicID: sum.ThreadPublicID,
		FindingID:      rec.FindingID,
		Endpoint:       sum.Endpoint,
		Provider:       sum.ProviderDisplayName,
		Integration:    rec.Integration,
		ThreadURL:      rec.ThreadURL,
		CreatedAt:      sum.CreatedAt,
		UpdatedAt:      sum.UpdatedAt,
	}
	if out.ThreadPublicID == "" {
		out.ThreadPublicID = rec.ThreadPublicID
	}
	if out.Endpoint == "" {
		out.Endpoint = rec.Endpoint
	}
	if out.Provider == "" {
		out.Provider = rec.Provider
	}
	if out.CreatedAt == "" {
		out.CreatedAt = rec.CreatedAt
	}
	if out.UpdatedAt == "" {
		out.UpdatedAt = rec.UpdatedAt
	}
	s := sum
	return threadView{threadRecord: out, Summary: &s}
}

// handleThreads is GET /api/threads: ONE call to the control plane's §5.5a list
// (most-recently-active first) joined to the local records by thread id.
//
// There is no local enumeration any more, so there is no stale list to fall
// back on: a CP failure is an honest error state, not a half-truth. That is a
// deliberate trade — every thread operation needs the CP anyway. For the same
// reason "not connected" and "no control plane configured" are NOT an empty
// list: an empty list would tell a collector that has threads it has none. They
// answer with the same code handleThreadSummary uses (412 not_connected / 503
// cp_not_configured) and the UI shows that message instead of an empty state.
//
// The list path never WRITES a pointer for a thread it cannot resolve. It once
// did — GetSetting-then-PutSetting of an empty value — which is a check-then-act
// on a key two pods share: a flag landing between the two calls had its real
// pointer overwritten with "", orphaning the record that holds the live thread
// link with no key left reaching it. An absent pointer already means exactly
// what an empty one meant, so the write is gone.
func (e *uiExtension) handleThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only.")
		return
	}
	if e.cp == nil {
		writeErr(w, http.StatusServiceUnavailable, "cp_not_configured", msgCPNotConfigured)
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	cli := e.keyedClient(cs)
	if cli == nil {
		writeErr(w, http.StatusPreconditionFailed, "not_connected", msgThreadsNotConnected)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), threadListTimeout)
	defer cancel()
	list, _, err := cli.ListThreads(ctx, promote.ListThreadsMaxLimit)
	if err != nil {
		writeCPError(w, err, msgCPUnreachable)
		return
	}
	var legacy legacyIndex
	views := make([]threadView, 0, len(list.Threads))
	for _, sum := range list.Threads {
		rec, joined, err := findThreadByID(st, sum.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
			return
		}
		if !joined {
			if healed, ok := e.recoverThreadPointer(st, &legacy, sum.ID); ok {
				rec = healed
			}
		}
		views = append(views, mergeThreadRow(sum, rec))
	}
	// total / has_more are the CP's, defended against an older CP that omits
	// them: what is shown can never exceed the total, and more rows than were
	// asked for is more than was shown.
	total := list.Total
	if total < len(views) {
		total = len(views)
	}
	limit := list.Limit
	if limit <= 0 {
		limit = promote.ListThreadsMaxLimit
	}
	writeJSON(w, http.StatusOK, threadListView{
		Threads: views,
		Count:   len(views),
		Total:   total,
		Limit:   limit,
		HasMore: list.HasMore || total > len(views),
	})
}

// threadForRequest resolves {id} to what this collector knows about that thread
// and the keyed CP client (412 not_connected when there is none).
//
// A thread the CP listed but this collector holds no record for resolves to a
// MINIMAL record rather than a 404: the record is only a local join, and the CP
// authorizes every operation by collector key AND origin match, so an id this
// collector has no business touching fails at the CP. 404 stays for an empty id.
func (e *uiExtension) threadForRequest(w http.ResponseWriter, r *http.Request) (store.Store, threadRecord, *promote.Client, bool) {
	st := e.storeOrError(w)
	if st == nil {
		return nil, threadRecord{}, nil, false
	}
	rec, _, err := findThreadByID(st, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return nil, threadRecord{}, nil, false
	}
	if rec.ThreadID == "" {
		writeErr(w, http.StatusNotFound, "thread_not_found", msgThreadNotFound)
		return nil, threadRecord{}, nil, false
	}
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return nil, threadRecord{}, nil, false
	}
	cli := e.keyedClient(cs)
	if cli == nil {
		writeErr(w, http.StatusPreconditionFailed, "not_connected", msgNotConnected)
		return nil, threadRecord{}, nil, false
	}
	return st, rec, cli, true
}

// handleThreadSummary is GET /api/threads/{id}/summary — the single-thread
// refresh the flag sheet and the row poll still use. Only the LIST fan-out went
// away.
func (e *uiExtension) handleThreadSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only.")
		return
	}
	if e.cp == nil {
		writeErr(w, http.StatusServiceUnavailable, "cp_not_configured", msgCPNotConfigured)
		return
	}
	_, rec, cli, ok := e.threadForRequest(w, r)
	if !ok {
		return
	}
	sum, _, err := cli.Summary(r.Context(), rec.ThreadID)
	if err != nil {
		writeCPError(w, err, msgCPUnreachable)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// handleThreadOpen is POST /api/threads/{id}/open → {owner_url, expires_at}: a
// 10-minute single-use owner handoff the UI opens in a new tab. Never stored,
// never logged.
func (e *uiExtension) handleThreadOpen(w http.ResponseWriter, r *http.Request) {
	if !e.guardMutating(w, r) {
		return
	}
	_, rec, cli, ok := e.threadForRequest(w, r)
	if !ok {
		return
	}
	h, _, err := cli.Handoff(r.Context(), rec.ThreadID)
	if err != nil {
		writeCPError(w, err, msgCPUnreachable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"owner_url": h.OwnerURL, "expires_at": h.ExpiresAt})
}

// handleThreadClose / handleThreadReopen relay the key-authorized state change.
func (e *uiExtension) handleThreadClose(w http.ResponseWriter, r *http.Request) {
	e.threadStateChange(w, r, func(ctx context.Context, cli *promote.Client, id string) (promote.ThreadStateResponse, error) {
		out, _, err := cli.Close(ctx, id)
		return out, err
	})
}

func (e *uiExtension) handleThreadReopen(w http.ResponseWriter, r *http.Request) {
	e.threadStateChange(w, r, func(ctx context.Context, cli *promote.Client, id string) (promote.ThreadStateResponse, error) {
		out, _, err := cli.Reopen(ctx, id)
		return out, err
	})
}

func (e *uiExtension) threadStateChange(w http.ResponseWriter, r *http.Request, do func(context.Context, *promote.Client, string) (promote.ThreadStateResponse, error)) {
	if !e.guardMutating(w, r) {
		return
	}
	_, rec, cli, ok := e.threadForRequest(w, r)
	if !ok {
		return
	}
	out, err := do(r.Context(), cli, rec.ThreadID)
	if err != nil {
		writeCPError(w, err, msgCPUnreachable)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleThreadReplaceLink is POST /api/threads/{id}/replace-link: revoke every
// copy of the Thread link shared so far and mint a new one, then PERSIST it so
// the chip and the Threads list show the live one.
//
// The CP revokes the old token the moment it mints the new one, so failing to
// persist leaves a thread with zero working links and no way to make another.
// EVERY row therefore gets its new link stored, including one with no finding
// record: that one goes to thread.link.<thread_id>, a key only this thread's
// Replace link ever writes, as a single blind PutSetting. There is no read in
// the write path, so there is no lost-update window — the failure mode that made
// the list path orphan records cannot be reintroduced here.
func (e *uiExtension) handleThreadReplaceLink(w http.ResponseWriter, r *http.Request) {
	if !e.guardMutating(w, r) {
		return
	}
	st, rec, cli, ok := e.threadForRequest(w, r)
	if !ok {
		return
	}
	out, _, err := cli.ReplaceLink(r.Context(), rec.ThreadID)
	if err != nil {
		writeCPError(w, err, msgCPUnreachable)
		return
	}
	if out.ThreadURL != "" {
		if err := persistThreadLink(st, rec, out.ThreadURL); err != nil {
			e.telemetry.Logger.Warn("replace-link: persisting the new link failed: " + err.Error())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id":  rec.ThreadID,
		"thread_url": out.ThreadURL,
		"expires_at": out.ExpiresAt,
		"revoked":    out.Revoked,
	})
}

// persistThreadLink stores a freshly minted thread link where the row that owns
// it will find it again: on the finding record for a finding-originated thread,
// otherwise under thread.link.<thread_id>. Both are blind single-key writes.
func persistThreadLink(st store.Store, rec threadRecord, url string) error {
	if rec.FindingID == "" {
		return st.PutSetting(settingThreadLinkPrefix+rec.ThreadID, url)
	}
	rec.ThreadURL = url
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return saveThread(st, rec)
}
