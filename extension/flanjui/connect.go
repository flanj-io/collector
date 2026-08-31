package flanjui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/flanj-io/collector/internal/promote"
	"github.com/flanj-io/collector/internal/redact"
	"github.com/flanj-io/collector/internal/store"
)

// Connect (v0.1a): the collector registers ONCE per deployment with the
// install-time deploy token and receives a per-deployment collector key, which
// authorizes every later CP call; the contact confirms their email with one
// click. Everything lives in the store's settings KV (`connect.*`) — shared by
// every pod of a deployment, never a per-pod file — and is read on every relay
// call. The key is never logged and never returned to the UI.
//
// Once a key exists, every later register (resend / change of contact) is sent
// with Bearer <collector key> (CONTRACTS-CP §5.1) — the CP has one deploy token
// and cannot tell deployments apart by it; the key never changes. A change of
// contact leaves the previously confirmed email usable for threads until the
// new one confirms (`confirmed_contact_email` from `me`), so the relay gates
// Create thread on "a confirmed contact exists", not on "the latest contact is
// confirmed".

const (
	settingCollectorKey        = "connect.collector_key"
	settingCollectorPublicID   = "connect.collector_public_id"
	settingConsumerDisplayName = "connect.consumer_display_name"
	settingContactEmail        = "connect.contact_email"
	settingContactDisplayName  = "connect.contact_display_name"
	settingRegisteredAt        = "connect.registered_at"
	settingContactStatus       = "connect.contact_status" // pending | confirmed (refreshed from `me`)
	settingConfirmedAt         = "connect.confirmed_at"
	settingLocalUIURL          = "connect.local_ui_url"
	// settingConfirmedContactEmail is the contact currently usable for threads
	// (may differ from contact_email while a newer contact is pending).
	settingConfirmedContactEmail = "connect.confirmed_contact_email"

	contactPending   = "pending"
	contactConfirmed = "confirmed"

	// meCacheTTL bounds how often GET /api/connect re-asks the CP (`me`).
	meCacheTTL = 10 * time.Second
)

// connectState is the persisted Connect record.
type connectState struct {
	CollectorKey          string // the secret — never serialized, never logged
	CollectorPublicID     string
	ConsumerDisplayName   string
	ContactEmail          string // the most recent (possibly pending) contact
	ContactDisplayName    string
	RegisteredAt          string
	ContactStatus         string // status of ContactEmail
	ConfirmedAt           string
	LocalUIURL            string
	ConfirmedContactEmail string // the contact usable for threads ("" until the first confirmation)
}

// hasConfirmedContact is the Create-thread gate: a confirmed contact exists —
// either the current one, or a previous one that stays usable while a newer
// contact is pending (CONTRACTS-CP §5.3).
func (cs connectState) hasConfirmedContact() bool {
	return cs.ContactStatus == contactConfirmed || cs.ConfirmedContactEmail != ""
}

// status derives disconnected | pending | connected.
func (cs connectState) status() string {
	switch {
	case cs.CollectorKey == "":
		return "disconnected"
	case cs.ContactStatus == contactConfirmed:
		return "connected"
	default:
		return "pending"
	}
}

// view is the JSON the UI sees (GET/POST /api/connect) — no key. Unset
// fields are null (a fresh collector has no public id, no dates).
func (cs connectState) view() map[string]any {
	return map[string]any{
		"status":                cs.status(),
		"consumer_display_name": nullable(cs.ConsumerDisplayName),
		"contact_email":         nullable(cs.ContactEmail),
		"contact_display_name":  nullable(cs.ContactDisplayName),
		"collector_public_id":   nullable(cs.CollectorPublicID),
		"registered_at":         nullable(cs.RegisteredAt),
		"confirmed_at":          nullable(cs.ConfirmedAt),
		"local_ui_url":          nullable(cs.LocalUIURL),
		// The contact threads are created with right now; while a new contact
		// is pending this is the previous confirmed one (Create thread stays
		// available), null until the first confirmation.
		"confirmed_contact_email": nullable(cs.confirmedContactEmail()),
	}
}

// confirmedContactEmail resolves the address usable for threads.
func (cs connectState) confirmedContactEmail() string {
	if cs.ConfirmedContactEmail != "" {
		return cs.ConfirmedContactEmail
	}
	if cs.ContactStatus == contactConfirmed {
		return cs.ContactEmail
	}
	return ""
}

// nullable maps "" to JSON null.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// loadConnect reads the Connect record from the store.
func loadConnect(st store.Store) (connectState, error) {
	var cs connectState
	fields := []struct {
		key string
		dst *string
	}{
		{settingCollectorKey, &cs.CollectorKey},
		{settingCollectorPublicID, &cs.CollectorPublicID},
		{settingConsumerDisplayName, &cs.ConsumerDisplayName},
		{settingContactEmail, &cs.ContactEmail},
		{settingContactDisplayName, &cs.ContactDisplayName},
		{settingRegisteredAt, &cs.RegisteredAt},
		{settingContactStatus, &cs.ContactStatus},
		{settingConfirmedAt, &cs.ConfirmedAt},
		{settingLocalUIURL, &cs.LocalUIURL},
		{settingConfirmedContactEmail, &cs.ConfirmedContactEmail},
	}
	for _, f := range fields {
		v, ok, err := st.GetSetting(f.key)
		if err != nil {
			return cs, err
		}
		if ok {
			*f.dst = v
		}
	}
	return cs, nil
}

// saveConnect persists the record. The key is written only when non-empty (a
// replay never blanks the key we already hold).
func saveConnect(st store.Store, cs connectState) error {
	puts := map[string]string{
		settingCollectorPublicID:     cs.CollectorPublicID,
		settingConsumerDisplayName:   cs.ConsumerDisplayName,
		settingContactEmail:          cs.ContactEmail,
		settingContactDisplayName:    cs.ContactDisplayName,
		settingRegisteredAt:          cs.RegisteredAt,
		settingContactStatus:         cs.ContactStatus,
		settingConfirmedAt:           cs.ConfirmedAt,
		settingLocalUIURL:            cs.LocalUIURL,
		settingConfirmedContactEmail: cs.ConfirmedContactEmail,
	}
	if cs.CollectorKey != "" {
		puts[settingCollectorKey] = cs.CollectorKey
	}
	for k, v := range puts {
		if err := st.PutSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

// meCache remembers the last successful `me` refresh so the Connect panel's
// polling never hammers the CP (≤ 1 call / meCacheTTL per pod).
type meCache struct {
	mu  sync.Mutex
	at  time.Time
	key string
}

// keyedClient returns the CP client authenticated with the deployment's
// collector key, or nil when not Connected.
func (e *uiExtension) keyedClient(cs connectState) *promote.Client {
	if e.cp == nil || cs.CollectorKey == "" {
		return nil
	}
	return e.cp.WithCollectorKey(cs.CollectorKey)
}

// refreshConnect re-reads `me` from the CP (when Connected) and folds the
// answer into the stored record. force bypasses the cache (used right before a
// flag while the contact is still pending, so confirmation is seen the moment
// it lands). A CP failure leaves the stored record as is and returns the error
// code for the UI to show.
func (e *uiExtension) refreshConnect(ctx context.Context, st store.Store, cs connectState, force bool) (connectState, string) {
	cli := e.keyedClient(cs)
	if cli == nil {
		return cs, ""
	}
	e.me.mu.Lock()
	fresh := !force && e.me.key == cs.CollectorKey && time.Since(e.me.at) < meCacheTTL
	e.me.mu.Unlock()
	if fresh {
		return cs, ""
	}
	me, _, err := cli.Me(ctx)
	if err != nil {
		if ce := promote.AsCPError(err); ce != nil {
			if ce.Code != "" {
				return cs, ce.Code
			}
			return cs, "cp_error"
		}
		return cs, "cp_unreachable"
	}
	e.me.mu.Lock()
	e.me.at, e.me.key = time.Now(), cs.CollectorKey
	e.me.mu.Unlock()

	changed := false
	set := func(dst *string, v string) {
		if v != "" && *dst != v {
			*dst = v
			changed = true
		}
	}
	set(&cs.CollectorPublicID, me.CollectorPublicID)
	set(&cs.ConsumerDisplayName, me.ConsumerDisplayName)
	set(&cs.ContactEmail, me.ContactEmail)
	set(&cs.ContactDisplayName, me.ContactDisplayName)
	set(&cs.RegisteredAt, me.RegisteredAt)
	set(&cs.ContactStatus, me.ContactStatus)
	set(&cs.ConfirmedAt, me.ConfirmedAt)
	if me.ContactStatus == contactPending && cs.ConfirmedAt != "" && me.ConfirmedAt == "" {
		// a new pending contact replaced the confirmed one (older CP: no confirmed_at while pending)
		cs.ConfirmedAt, changed = "", true
	}
	// confirmed_contact_email is authoritative (null until the first
	// confirmation; stays set while a newer contact is pending). An older CP
	// without the field: the confirmed contact is the current one when confirmed.
	confirmed := ""
	switch {
	case me.ConfirmedContactEmail != nil:
		confirmed = *me.ConfirmedContactEmail
	case me.ContactStatus == contactConfirmed:
		confirmed = me.ContactEmail
	}
	if cs.ConfirmedContactEmail != confirmed {
		cs.ConfirmedContactEmail, changed = confirmed, true
	}
	if changed {
		if err := saveConnect(st, cs); err != nil {
			e.telemetry.Logger.Warn("connect: persisting refreshed state failed: " + err.Error())
		}
	}
	return cs, ""
}

// handleConnect serves GET /api/connect (state, refreshed from `me`) and
// POST /api/connect (Connect / resend / change contact).
func (e *uiExtension) handleConnect(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		e.handleConnectGet(w, r)
	case http.MethodPost:
		e.handleConnectPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST only.")
	}
}

func (e *uiExtension) handleConnectGet(w http.ResponseWriter, r *http.Request) {
	st := e.storeOrError(w)
	if st == nil {
		return
	}
	cs, err := loadConnect(st)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	cs, cpErr := e.refreshConnect(r.Context(), st, cs, false)
	out := cs.view()
	out["cp_configured"] = e.cp != nil
	// The UI's one link OUT to the control plane. Emitted only when this
	// deployment is actually Connected, so the SPA can never offer a door to a
	// place this collector has no identity at. The collector composes the path
	// rather than handing over a base URL: which page is the dashboard is
	// contract knowledge, not something the SPA should assemble.
	if e.cp != nil && cs.CollectorKey != "" {
		// Read the base URL off the CLIENT, not the config: the client is what
		// requests actually go to, so a deployment that set it any other way
		// still gets a working door, and the two can never disagree.
		if base := strings.TrimRight(e.cp.BaseURL, "/"); base != "" {
			out["dashboard_url"] = base + "/d"
		}
	}
	if cpErr != "" {
		out["error"] = cpErr
	}
	writeJSON(w, http.StatusOK, out)
}

// connectRequestBody is POST /api/connect.
type connectRequestBody struct {
	ConsumerDisplayName string `json:"consumer_display_name"`
	ContactEmail        string `json:"contact_email"`
	ContactDisplayName  string `json:"contact_display_name"`
	LocalUIURL          string `json:"local_ui_url"`
}

// handleConnectPost is Connect. Idempotent: read-before-register; the same
// contact email = a resend (same key, no second registration state); a
// different email = a new pending contact on the same collector (the CP keeps
// the previous confirmed one until the new one confirms). Nothing leaves the
// collector until this is called.
func (e *uiExtension) handleConnectPost(w http.ResponseWriter, r *http.Request) {
	if !e.guardMutating(w, r) {
		return
	}
	var body connectRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", msgInvalidJSON)
		return
	}
	body.ConsumerDisplayName = strings.TrimSpace(body.ConsumerDisplayName)
	body.ContactEmail = strings.TrimSpace(body.ContactEmail)
	body.ContactDisplayName = strings.TrimSpace(body.ContactDisplayName)
	body.LocalUIURL = strings.TrimSpace(body.LocalUIURL)
	if body.ConsumerDisplayName == "" || body.ContactEmail == "" {
		writeErr(w, http.StatusBadRequest, "missing_fields", msgConnectFields)
		return
	}
	// Display names are free text that leaves the collector (and is shown on
	// every thread): run them through the redaction floor, like the flag
	// message, before anything is sent or persisted.
	rd := redact.New()
	body.ConsumerDisplayName = strings.TrimSpace(rd.Redact(body.ConsumerDisplayName).Text)
	body.ContactDisplayName = strings.TrimSpace(rd.Redact(body.ContactDisplayName).Text)
	if addr, err := mail.ParseAddress(body.ContactEmail); err != nil || addr.Address != body.ContactEmail || !strings.Contains(body.ContactEmail, "@") {
		writeErr(w, http.StatusBadRequest, "invalid_email", msgInvalidEmail)
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

	// First Connect: register with the deploy token; the collector key is
	// returned once. Afterwards EVERY register goes out with the collector key
	// (CONTRACTS-CP §5.1): the same email only re-sends the confirmation; a
	// different email starts a new pending contact on the same collector. The
	// deploy token is never used again once a key exists — with it the CP
	// could not tell this deployment apart and would register a new collector.
	req := promote.RegisterRequest{
		ConsumerDisplayName: body.ConsumerDisplayName,
		ContactEmail:        body.ContactEmail,
		ContactDisplayName:  body.ContactDisplayName,
		LocalUIURL:          body.LocalUIURL,
	}
	var resp promote.RegisterResponse
	if cs.CollectorKey != "" {
		resp, _, err = e.cp.WithCollectorKey(cs.CollectorKey).RegisterWithKey(r.Context(), req)
	} else {
		resp, _, err = e.cp.Register(r.Context(), req)
	}
	if err != nil {
		writeCPError(w, err, msgCPUnreachableSend)
		return
	}
	emailChanged := !strings.EqualFold(cs.ContactEmail, body.ContactEmail)
	if cs.CollectorKey == "" && resp.CollectorKey != "" {
		cs.CollectorKey = resp.CollectorKey
	}
	if cs.CollectorKey == "" {
		// The CP replayed (it already knows this deploy token) but the key is
		// returned only once and this store never held it — nothing we can do
		// from here; the operator must rotate the deploy token.
		writeErr(w, http.StatusConflict, "key_missing", msgKeyMissing)
		return
	}
	if resp.CollectorPublicID != "" {
		cs.CollectorPublicID = resp.CollectorPublicID
	}
	cs.ConsumerDisplayName = body.ConsumerDisplayName
	cs.ContactEmail = body.ContactEmail
	cs.ContactDisplayName = body.ContactDisplayName
	if cs.ContactDisplayName == "" {
		cs.ContactDisplayName = body.ConsumerDisplayName
	}
	if body.LocalUIURL != "" {
		cs.LocalUIURL = body.LocalUIURL
	}
	if cs.RegisteredAt == "" {
		cs.RegisteredAt = time.Now().UTC().Format(time.RFC3339)
	}
	switch {
	case resp.ContactStatus == contactConfirmed:
		cs.ContactStatus = contactConfirmed
		cs.ConfirmedContactEmail = body.ContactEmail
		if cs.ConfirmedAt == "" {
			cs.ConfirmedAt = time.Now().UTC().Format(time.RFC3339)
		}
	case resp.ContactStatus == contactPending || emailChanged || cs.ContactStatus == "":
		// A new pending contact. The previously confirmed one (if any) stays in
		// ConfirmedContactEmail and keeps Create thread available until the new
		// one confirms; `me` refreshes it.
		cs.ContactStatus = contactPending
		cs.ConfirmedAt = ""
	}
	if err := saveConnect(st, cs); err != nil {
		// The key only exists in memory now — this must surface loudly (without the key).
		e.telemetry.Logger.Error("connect: persisting registration failed: " + err.Error())
		writeErr(w, http.StatusInternalServerError, "store_error", msgStoreUnavailable)
		return
	}
	// Forget the me-cache so the next GET re-reads the CP.
	e.me.mu.Lock()
	e.me.at = time.Time{}
	e.me.mu.Unlock()

	out := cs.view()
	out["cp_configured"] = true
	if cs.status() == "connected" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
