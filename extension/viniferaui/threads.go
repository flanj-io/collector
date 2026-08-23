package viniferaui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/vinifera-io/collector/internal/promote"
	"github.com/vinifera-io/collector/internal/store"
)

// Threads (v0.1a): every conversation lives on the control plane. The collector
// keeps only a per-finding record of the thread it created (ids + the current
// Thread link, so the finding chip survives reloads) in the store's settings KV,
// and the Threads list polls the CP for state (`summary`) over this relay —
// outbound-only, state only; the conversation is read and answered on the CP.

const (
	// settingThreadPrefix + <finding_id> → threadRecord JSON.
	settingThreadPrefix = "thread.finding."
	// settingThreadsIndex → JSON array of finding ids with a thread (newest last).
	settingThreadsIndex = "threads.index"

	summaryConcurrency = 4
	summaryTimeout     = 8 * time.Second

	// indexWriteAttempts bounds the read-modify-write retry on threads.index.
	indexWriteAttempts = 3
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

func loadThreadIndex(st store.Store) ([]string, error) {
	raw, ok, err := st.GetSetting(settingThreadsIndex)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, nil // a corrupt index is treated as empty; records stay reachable by finding id
	}
	return ids, nil
}

func loadThread(st store.Store, findingID string) (threadRecord, bool, error) {
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

// saveThread upserts the record and adds the finding to the index (once).
//
// threads.index is a single JSON array behind a plain settings KV (no
// compare-and-swap), so adding an id is a read-modify-write. The index is
// re-read IMMEDIATELY before each write and the write is verified by a re-read
// afterwards, up to indexWriteAttempts times, which closes the common window
// (two flags landing on different pods within the same request). RESIDUAL
// RACE: two writers that both read, both write and both verify inside each
// other's window can still drop one id — without CAS in the KV this cannot be
// made airtight from here. The per-finding record itself is never lost (it has
// its own key); a dropped id only hides that thread from GET /api/threads until
// the next saveThread for that finding (re-flag or replace-link) re-adds it.
func saveThread(st store.Store, rec threadRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := st.PutSetting(settingThreadPrefix+rec.FindingID, string(b)); err != nil {
		return err
	}
	return addToThreadIndex(st, rec.FindingID)
}

// addToThreadIndex appends findingID to threads.index (once) with the
// re-read-before-write retry described on saveThread.
func addToThreadIndex(st store.Store, findingID string) error {
	var lastErr error
	for attempt := 0; attempt < indexWriteAttempts; attempt++ {
		ids, err := loadThreadIndex(st) // fresh read right before the write
		if err != nil {
			return err
		}
		if containsID(ids, findingID) {
			return nil
		}
		ib, _ := json.Marshal(append(ids, findingID))
		if err := st.PutSetting(settingThreadsIndex, string(ib)); err != nil {
			return err
		}
		// Verify the write survived (a concurrent writer may have clobbered it).
		after, err := loadThreadIndex(st)
		if err != nil {
			return err
		}
		if containsID(after, findingID) {
			return nil
		}
		lastErr = errIndexRace
	}
	return lastErr
}

// errIndexRace: the index write was overwritten by a concurrent writer on every
// attempt; the thread record itself is persisted, only the list entry is missing.
var errIndexRace = errors.New("threads.index: concurrent writer won every attempt; record saved, list entry missing until the next save")

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// listThreads returns every record, newest first.
func listThreads(st store.Store) ([]threadRecord, error) {
	ids, err := loadThreadIndex(st)
	if err != nil {
		return nil, err
	}
	out := make([]threadRecord, 0, len(ids))
	for _, id := range ids {
		rec, ok, err := loadThread(st, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// findThreadByID resolves a CP thread id to its record.
func findThreadByID(st store.Store, threadID string) (threadRecord, bool, error) {
	recs, err := listThreads(st)
	if err != nil {
		return threadRecord{}, false, err
	}
	for _, r := range recs {
		if r.ThreadID == threadID {
			return r, true, nil
		}
	}
	return threadRecord{}, false, nil
}

// threadView is one GET /api/threads row.
type threadView struct {
	threadRecord
	Summary *promote.ThreadSummary `json:"summary"`
	Error   string                 `json:"error,omitempty"`
}

// handleThreads is GET /api/threads: every thread this collector created, each
// with its CP summary (fetched in parallel; a CP failure yields summary:null +
// error so the list still renders).
func (e *uiExtension) handleThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only.")
		return
	}
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	recs, err := listThreads(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	cli := e.keyedClient(cs)
	views := make([]threadView, len(recs))
	ctx, cancel := context.WithTimeout(r.Context(), summaryTimeout)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, summaryConcurrency)
	for i, rec := range recs {
		views[i] = threadView{threadRecord: rec}
		if cli == nil {
			views[i].Error = "not_connected"
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sum, _, err := cli.Summary(ctx, id)
			if err != nil {
				if ce := promote.AsCPError(err); ce != nil && ce.Code != "" {
					views[i].Error = ce.Code
				} else if ce != nil {
					views[i].Error = "cp_error"
				} else {
					views[i].Error = "cp_unreachable"
				}
				return
			}
			views[i].Summary = &sum
		}(i, rec.ThreadID)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, views)
}

// threadForRequest resolves {id} to a record the collector created (404
// otherwise) and the keyed CP client (412 not_connected when there is none).
func (e *uiExtension) threadForRequest(w http.ResponseWriter, r *http.Request) (store.Store, threadRecord, *promote.Client, bool) {
	st := e.storeOrError(w)
	if st == nil {
		return nil, threadRecord{}, nil, false
	}
	rec, ok, err := findThreadByID(st, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return nil, threadRecord{}, nil, false
	}
	if !ok {
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

// handleThreadSummary is GET /api/threads/{id}/summary.
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
// copy of the Thread link shared so far and mint a new one; the persisted link
// is updated so the chip and the Threads list show the live one.
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
		rec.ThreadURL = out.ThreadURL
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := saveThread(st, rec); err != nil {
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
