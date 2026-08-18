package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vinifera-io/collector/internal/model"
)

func openTemp(t *testing.T, maxRows int, maxBytes int64) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vinifera.db")
	s, err := Open(path, maxRows, maxBytes)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makeCall(i int) model.RedactedCall {
	id := fmt.Sprintf("0191e8c4-0000-7000-8000-%012d", i)
	return model.RedactedCall{
		SchemaVersion: model.SchemaVersion,
		ID:            id,
		CapturedAt:    "2026-08-18T08:00:00.000Z",
		Integration:   "acme-payments",
		Direction:     "client",
		Method:        "POST",
		URL:           "https://api.acme.test/v1/charges",
		Route:         "/v1/charges",
		StatusCode:    200,
		RequestBody:   `{"amount":1200,"currency":"usd"}`,
		ResponseBody:  `{"id":"ch_1","amount":"1200"}`,
		Correlation:   model.Correlation{RequestID: fmt.Sprintf("req_%d", i)},
		Redaction:     model.Redaction{Applied: true, Patterns: []string{}},
	}
}

// TestRingBuffer_StableFill_RowCap fills far past the row cap and asserts the
// window holds at the cap (oldest evicted first, FIFO).
func TestRingBuffer_StableFill_RowCap(t *testing.T) {
	const cap = 10
	s := openTemp(t, cap, 0)
	for i := 0; i < 100; i++ {
		if err := s.InsertCall(makeCall(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	rows, _, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if rows != cap {
		t.Fatalf("row count = %d, want stable fill at %d", rows, cap)
	}
	// FIFO: the survivors must be the newest `cap` ids (90..99).
	calls, err := s.ListCalls(cap)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(calls) != cap {
		t.Fatalf("listed %d calls, want %d", len(calls), cap)
	}
	newest := makeCall(99).ID
	if calls[0].ID != newest {
		t.Errorf("newest survivor = %s, want %s", calls[0].ID, newest)
	}
	if _, ok, _ := s.GetCall(makeCall(0).ID); ok {
		t.Errorf("oldest call (0) should have been evicted")
	}
}

// TestRingBuffer_ByteCap evicts on the byte ceiling as well as the row ceiling.
func TestRingBuffer_ByteCap(t *testing.T) {
	// Each row's doc is a few hundred bytes; a 2KB cap keeps only a handful.
	s := openTemp(t, 0, 2048)
	for i := 0; i < 50; i++ {
		if err := s.InsertCall(makeCall(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	rows, bytes, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if bytes > 2048 {
		t.Fatalf("byte size = %d, exceeds cap 2048", bytes)
	}
	if rows == 0 || rows >= 50 {
		t.Fatalf("expected a bounded but non-empty window, got %d rows", rows)
	}
}

// TestRingBuffer_PinnedSurvive is the evidence-preservation invariant: a pinned
// call (pin-on-finding) is never evicted, even when the window is hammered far
// past its caps by unpinned traffic.
func TestRingBuffer_PinnedSurvive(t *testing.T) {
	const cap = 5
	s := openTemp(t, cap, 0)

	// Insert the call that a finding will reference.
	pinned := makeCall(1000)
	if err := s.InsertCall(pinned); err != nil {
		t.Fatalf("insert pinned call: %v", err)
	}
	// A finding on it pins it.
	src := pinned.ID
	if err := s.InsertFinding(model.Finding{
		SchemaVersion: model.SchemaVersion,
		ID:            "0191e8c4-ffff-7000-8000-000000000001",
		Kind:          model.KindLiveVsSpec,
		Severity:      model.SeverityBreaking,
		Integration:   "acme-payments",
		Endpoint:      "POST /v1/charges",
		Expected:      "type=integer",
		Actual:        `type=string ("1200")`,
		Rule:          "type-mismatch",
		SourceCallID:  &src,
		DetectedAt:    "2026-08-18T08:00:01.000Z",
	}); err != nil {
		t.Fatalf("insert finding: %v", err)
	}

	// Flood with unpinned traffic well past the cap.
	for i := 0; i < 100; i++ {
		if err := s.InsertCall(makeCall(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	if _, ok, _ := s.GetCall(pinned.ID); !ok {
		t.Fatalf("pinned call was evicted — evidence lost")
	}
	// The row cap is a hard total ceiling and pinned rows count toward it, so the
	// window holds at cap: 1 pinned survivor + (cap-1) newest unpinned.
	rows, _, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if rows != cap {
		t.Errorf("row count = %d, want stable fill at %d with the pinned row retained", rows, cap)
	}

	// evict-after-promote: unpin + stamp promoted_at, then the once-pinned call
	// re-enters the pool and evicts on the next insert.
	if err := s.MarkPromoted(pinned.ID); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}
	for i := 100; i < 110; i++ {
		if err := s.InsertCall(makeCall(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if _, ok, _ := s.GetCall(pinned.ID); ok {
		t.Errorf("promoted call should re-enter the eviction pool and evict")
	}
	rows, _, _ = s.Stats()
	if rows != cap {
		t.Errorf("post-promote row count = %d, want %d", rows, cap)
	}
}

// TestPersistence_SurvivesReopen proves data survives a collector restart: close
// the store and reopen the same file.
func TestPersistence_SurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vinifera.db")
	s, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c := makeCall(7)
	if err := s.InsertCall(c); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, ok, err := s2.GetCall(c.ID)
	if err != nil || !ok {
		t.Fatalf("call did not survive reopen (ok=%v err=%v)", ok, err)
	}
	if got.Correlation.RequestID != c.Correlation.RequestID {
		t.Errorf("reopened call corrupted: %q != %q", got.Correlation.RequestID, c.Correlation.RequestID)
	}
}
