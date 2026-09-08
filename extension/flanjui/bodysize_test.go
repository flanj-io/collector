package flanjui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// The small-envelope routes and the size trap they used to share with the
// contract upload path.
//
// PR #40 fixed the upload path and said so of the rest: "`acks.go` still reads
// its 64 KiB body through `io.LimitReader`, and the `MaxBytesReader` callers in
// `handlers.go`/`connect.go` map the size error to `invalid_json` — same trap,
// harmless at those sizes, left alone here."
//
// Harmless at those sizes was the wrong test. `/api/flag` carries a message the
// operator types into a textarea with no length limit, so pasting a log is an
// ordinary accident — and the answer was "The request body is not valid JSON.",
// which sends them looking for a syntax error in a field they never typed JSON
// into. `acks.go` was worse: io.LimitReader TRUNCATES and reports nothing, so an
// oversized note reached the decoder cut off mid-string and produced the same
// wrong sentence from a body that was perfectly well formed.
//
// Every one of them now goes through readJSONBody, and this pins the boundary
// on each: exactly at the cap the body is read, one byte over it is a size
// refusal in the deck's words.

// exactBody returns prefix+pad+suffix sized to exactly n bytes, padding inside
// a JSON string so the length is the wire length and nothing re-encodes it.
func exactBody(t *testing.T, prefix, suffix string, n int) []byte {
	t.Helper()
	pad := n - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("cannot build a %d-byte body from a %d-byte envelope", n, len(prefix)+len(suffix))
	}
	b := []byte(prefix + strings.Repeat("a", pad) + suffix)
	if len(b) != n {
		t.Fatalf("built %d bytes, want %d", len(b), n)
	}
	if !json.Valid(b) {
		t.Fatalf("built an invalid JSON body: %s…", b[:60])
	}
	return b
}

// postRaw sends bytes verbatim — r.do marshals a Go value, which cannot express
// an exact body length.
func (r *testRig) postRaw(t *testing.T, path string, body []byte) (*http.Response, map[string]any, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.ui.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flanj-UI", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s (%d bytes): %v", path, len(body), err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out, raw
}

func TestSmallEnvelopeRoutesRefuseAnOversizedBodyAsTooLarge(t *testing.T) {
	// An ackable finding: findingAck checks ackable() BEFORE it reads the body,
	// so the ack route's boundary is only reachable through one of these.
	const ackable = "fnd_ackable"
	newAckRig := func(t *testing.T) *testRig {
		r := newRig(t)
		r.start(t)
		_ = r.st.InsertFinding(model.Finding{
			SchemaVersion: 1, ID: ackable, Kind: model.KindDefinitionChange,
			Severity: model.SeverityInfo, Integration: "acme-payments",
			Endpoint: "charge", Rule: model.RuleDescriptionChanged,
			DetectedAt: "2026-09-08T10:00:00Z",
		})
		return r
	}

	cases := []struct {
		name   string
		path   string
		prefix string
		suffix string
		rig    func(*testing.T) *testRig
	}{
		{
			// handlers.go handleEdgeName.
			name:   "edge name",
			path:   "/api/edges/name",
			prefix: `{"host":"api.acme.test","name":"`,
			suffix: `"}`,
		},
		{
			// handlers.go handleFlag — the one a person reaches by pasting.
			name:   "flag",
			path:   "/api/flag",
			prefix: `{"finding_id":"fnd_1","message":"`,
			suffix: `"}`,
		},
		{
			// connect.go handleConnectPost. The email is deliberately invalid so
			// the at-cap case stops at 400 invalid_email — proof the body was
			// decoded, with no registration against the stub CP.
			name:   "connect",
			path:   "/api/connect",
			prefix: `{"consumer_display_name":"Acme","contact_email":"not-an-email","contact_display_name":"`,
			suffix: `"}`,
		},
		{
			// acks.go decodeAckBody — the io.LimitReader one.
			name:   "finding ack",
			path:   "/api/findings/" + ackable + "/ack",
			prefix: `{"note":"`,
			suffix: `"}`,
			rig:    newAckRig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mk := tc.rig
			if mk == nil {
				mk = func(t *testing.T) *testRig { r := newRig(t); r.start(t); return r }
			}

			// Exactly at the cap: read, decoded, and handled on its own terms.
			// http.MaxBytesReader fails only PAST the limit, so this is the byte
			// that must still work — an off-by-one here would refuse a body the
			// documented cap admits.
			r := mk(t)
			resp, out, raw := r.postRaw(t, tc.path, exactBody(t, tc.prefix, tc.suffix, maxSmallBodyBytes))
			if resp.StatusCode == http.StatusRequestEntityTooLarge {
				t.Errorf("a body of exactly %d bytes was refused as too large: %s", maxSmallBodyBytes, raw)
			}
			if out["error"] == "invalid_json" {
				t.Errorf("a body of exactly %d bytes came back invalid_json: %s", maxSmallBodyBytes, raw)
			}

			// One byte over: a SIZE refusal, in the deck's words. Before this
			// change every one of these answered 400 "The request body is not
			// valid JSON." — a syntax verdict on a body whose only fault was
			// its length.
			r = mk(t)
			resp, out, raw = r.postRaw(t, tc.path, exactBody(t, tc.prefix, tc.suffix, maxSmallBodyBytes+1))
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 for %d bytes: %s", resp.StatusCode, maxSmallBodyBytes+1, raw)
			}
			if out["error"] != "request_too_large" {
				t.Errorf("error = %v, want request_too_large: %s", out["error"], raw)
			}
			if msg, _ := out["message"].(string); msg != msgRequestTooLarge {
				t.Errorf("message = %q, want the deck's sentence %q", msg, msgRequestTooLarge)
			}
		})
	}
}

// TestAckStillAcceptsAnAbsentOrEmptyBody: the ack body is OPTIONAL — the
// shipped UI sends `{}` and older builds send nothing at all. Routing it through
// the shared reader must not turn that into a 400, which is why the ack route
// gets readOptionalJSONBody rather than readJSONBody.
func TestAckStillAcceptsAnAbsentOrEmptyBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"no body at all", nil},
		{"empty body", []byte("")},
		{"the empty object the UI sends", []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.start(t)
			const id = "fnd_ackable"
			_ = r.st.InsertFinding(model.Finding{
				SchemaVersion: 1, ID: id, Kind: model.KindDefinitionChange,
				Severity: model.SeverityInfo, Integration: "acme-payments",
				Endpoint: "charge", Rule: model.RuleDescriptionChanged,
				DetectedAt: "2026-09-08T10:00:00Z",
			})
			resp, out, raw := r.postRaw(t, "/api/findings/"+id+"/ack", tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, raw)
			}
			if out["acked"] != true {
				t.Errorf("acked = %v, want true: %s", out["acked"], raw)
			}
		})
	}
}

// TestAckStillRefusesMalformedJSON: the size branch must not swallow the syntax
// branch. A body that is neither empty nor valid JSON is still 400 invalid_json.
func TestAckStillRefusesMalformedJSON(t *testing.T) {
	r := newRig(t)
	r.start(t)
	const id = "fnd_ackable"
	_ = r.st.InsertFinding(model.Finding{
		SchemaVersion: 1, ID: id, Kind: model.KindDefinitionChange,
		Severity: model.SeverityInfo, Integration: "acme-payments",
		Endpoint: "charge", Rule: model.RuleDescriptionChanged,
		DetectedAt: "2026-09-08T10:00:00Z",
	})
	resp, out, raw := r.postRaw(t, "/api/findings/"+id+"/ack", []byte(`{"note": `))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, raw)
	}
	if out["error"] != "invalid_json" {
		t.Errorf("error = %v, want invalid_json: %s", out["error"], raw)
	}
}
