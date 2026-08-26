package viniferaui

import "github.com/vinifera-io/collector/internal/store"

// Prior-data probe (qfix2-2026-08-26, ux-design-v2 §3.4).
//
// The collector's default theme flips from "follow the OS" to LIGHT. Anyone who
// had explicitly chosen Dark keeps Dark; everyone who was on the (now deleted)
// System setting moves to light. That move must not be silent — but the
// one-time notice that explains it must ALSO never reach a first-time user,
// because telling someone the default "is light now" describes a change they
// never experienced.
//
// So the notice is gated on BOTH conditions, and this file answers the second
// one: did this collector hold data before the upgrade? The browser answers the
// first (no stored theme choice) — see ui/src/theme.ts.
//
// The answer is probed ONCE and then frozen in the settings KV, because it is a
// statement about a moment in the past. Probing again later would flip to
// "true" for every collector as soon as the first call arrives.
const settingPriorData = "ui.prior_data"

// heldPriorData reports whether this collector already held data the first time
// a build carrying this probe served a request. The first call computes and
// freezes the answer; every later call reads it back.
//
// Known residual: on a genuinely fresh install the probe runs at the first UI
// request, so traffic that arrives before anyone opens the UI would read as
// prior data. Everything the probe looks at is either a settings-KV record
// (Connect, threads, acks — a fresh install has none) or a store row, so the
// window is "an install that ingested calls before its UI was ever opened".
// Documented rather than papered over; the notice is dismissible and one-time.
func heldPriorData(st store.Store) bool {
	if raw, ok, err := st.GetSetting(settingPriorData); err == nil && ok && raw != "" {
		return raw == "1"
	}
	answer := "0"
	if probePriorData(st) {
		answer = "1"
	}
	// A failed write only means the probe runs again next request — never a
	// wrong answer, and never a reason to fail the health poll.
	_ = st.PutSetting(settingPriorData, answer)
	return answer == "1"
}

// probePriorData looks for any trace of a collector that has been used: a
// Connect registration, a thread, an acknowledgement, or stored calls/findings.
func probePriorData(st store.Store) bool {
	if cs, err := loadConnect(st); err == nil &&
		(cs.CollectorKey != "" || cs.ContactEmail != "" || cs.ConsumerDisplayName != "" || cs.RegisteredAt != "") {
		return true
	}
	if ids, err := loadThreadIndex(st); err == nil && len(ids) > 0 {
		return true
	}
	if sigs, err := loadAckIndex(st); err == nil && len(sigs) > 0 {
		return true
	}
	if calls, findings, err := st.Counts(); err == nil && (calls > 0 || findings > 0) {
		return true
	}
	return false
}
