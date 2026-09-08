package store

import (
	"strings"
	"testing"

	"github.com/flanj-io/collector/internal/model"
)

// TestListSpecInfos_DocBytesIsMeasuredInBytes: the listing reports the stored
// document's size, and it reports it in BYTES.
//
// Both halves matter. The size is what lets the tiered contract channel
// recognise an over-cap row from the LISTING — a front stops re-requesting a
// document it will be refused, and the store pod's Contracts card can say why
// the edge is unchecked — so a listing that omits it leaves the whole
// condition invisible until a transfer is attempted.
//
// The unit is where this could silently go wrong. sqlite's length() over TEXT
// counts CHARACTERS and postgres's does the same, so a document of multi-byte
// UTF-8 would measure smaller the more non-ASCII it carries: a tools/list of
// CJK tool descriptions could sit well past an 8 MiB cap and be listed as
// comfortably under it, which is a size check that fails in exactly the
// direction that hides the fault. The multi-byte case is the assertion.
func TestListSpecInfos_DocBytesIsMeasuredInBytes(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)

		ascii := `{"tools":[]}`
		// 200 three-byte runes: 600 bytes, 200 characters. A character count
		// reports a third of the truth.
		multi := `{"tools":[],"note":"` + strings.Repeat("界", 200) + `"}`

		for _, tc := range []struct {
			integration string
			doc         string
		}{
			{"acme-ascii", ascii},
			{"acme-multibyte", multi},
		} {
			if err := s.PutSpecInfo(model.SpecInfo{
				Integration: tc.integration,
				Role:        model.SpecRoleProvider,
				PeerHost:    tc.integration + ".test",
				Format:      model.SpecFormatMCP,
				Source:      model.SpecSourceObserved,
				LoadedAt:    "2026-09-08T10:00:00Z",
			}, []byte(tc.doc)); err != nil {
				t.Fatalf("put %s: %v", tc.integration, err)
			}
		}

		infos, err := s.ListSpecInfos()
		if err != nil {
			t.Fatalf("list spec infos: %v", err)
		}
		got := map[string]int{}
		for _, si := range infos {
			got[si.Integration] = si.DocBytes
		}
		if got["acme-ascii"] != len(ascii) {
			t.Errorf("ascii DocBytes = %d, want %d", got["acme-ascii"], len(ascii))
		}
		if got["acme-multibyte"] != len(multi) {
			t.Errorf("multibyte DocBytes = %d, want %d (a character count would say %d)",
				got["acme-multibyte"], len(multi), len([]rune(multi)))
		}
	})
}

// TestListSpecInfos_DocBytesCrossesTheCap: a document past
// model.MaxContractDocBytes is listed with a size the caller can compare
// against the cap without asking for the document.
func TestListSpecInfos_DocBytesCrossesTheCap(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *testBackend) {
		s := b.open(t, 0, 0)
		over := model.MaxContractDocBytes + 4096
		if err := s.PutSpecInfo(model.SpecInfo{
			Integration: "acme-tools",
			Role:        model.SpecRoleProvider,
			PeerHost:    "tools.acme.test",
			Format:      model.SpecFormatMCP,
			Source:      model.SpecSourceObserved,
			LoadedAt:    "2026-09-08T10:00:00Z",
		}, []byte(strings.Repeat("x", over))); err != nil {
			t.Fatalf("put oversized spec info: %v", err)
		}
		infos, err := s.ListSpecInfos()
		if err != nil {
			t.Fatalf("list spec infos: %v", err)
		}
		if len(infos) != 1 {
			t.Fatalf("listed %d rows, want 1", len(infos))
		}
		if infos[0].DocBytes != over {
			t.Fatalf("DocBytes = %d, want %d", infos[0].DocBytes, over)
		}
		if infos[0].DocBytes <= model.MaxContractDocBytes {
			t.Fatalf("DocBytes = %d does not read as over the %d cap", infos[0].DocBytes, model.MaxContractDocBytes)
		}
	})
}
