package viniferaui

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/vinifera-io/collector/internal/promote"
)

// writeErr writes the relay's JSON error shape {error, message}.
func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// guardMutating enforces the relay's rules for every state-changing route
// (CONTRACTS / spec Step 4b): POST only (405), the `X-Vinifera-UI: 1` header
// (a custom header forces a CORS preflight the relay never answers — no CORS
// headers are ever set — so a foreign page cannot drive it), a JSON content
// type, and no foreign `Origin` (when a browser sends one it must name this
// very host). It also requires the control plane to be configured. Returns
// false after writing the error.
func (e *uiExtension) guardMutating(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", msgPostOnly)
		return false
	}
	if r.Header.Get("X-Vinifera-UI") != "1" {
		writeErr(w, http.StatusForbidden, "ui_header_required", msgUIHeaderRequired)
		return false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "json_required", msgJSONRequired)
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || origin == "null" || !sameHost(u.Host, r.Host) {
			writeErr(w, http.StatusForbidden, "forbidden_origin", msgForeignOrigin)
			return false
		}
	}
	if e.cp == nil {
		writeErr(w, http.StatusServiceUnavailable, "cp_not_configured", msgCPNotConfigured)
		return false
	}
	return true
}

// sameHost compares an Origin host[:port] with the request Host (case-insensitive).
func sameHost(originHost, reqHost string) bool {
	return originHost != "" && strings.EqualFold(originHost, reqHost)
}

// writeCPError maps a control-plane client error to the relay's answer. A typed
// CP error (412 not_connected | contact_unconfirmed, 403 wrong_origin, 401,
// 404, 429 …) passes through with its status + code, the CP's message when it
// sent one, else our own copy; a transport failure is 502 cp_unreachable with
// the deck's "nothing happened" line for that action.
func writeCPError(w http.ResponseWriter, err error, unreachableMsg string) {
	if ce := promote.AsCPError(err); ce != nil {
		code := ce.Code
		if code == "" {
			code = "cp_error"
		}
		msg := ce.Message
		switch code {
		case "not_connected":
			msg = msgNotConnected
		case "contact_unconfirmed":
			msg = msgContactUnconfirmed
		case "wrong_origin":
			msg = msgWrongOrigin
		}
		if msg == "" {
			msg = msgCPUnreachable
		}
		writeErr(w, ce.Status, code, msg)
		return
	}
	writeErr(w, http.StatusBadGateway, "cp_unreachable", unreachableMsg)
}
