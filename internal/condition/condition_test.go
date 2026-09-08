package condition

import (
	"reflect"
	"testing"
)

// TestRaiseLogsTheStartAndNotTheRepeats: the first observation is a transition,
// every identical one after it is not. This is the whole point — an unusable
// contract row is re-discovered on every ten-second refresh tick, and before
// this the tick was the log rate.
func TestRaiseLogsTheStartAndNotTheRepeats(t *testing.T) {
	var s Standing
	if !s.Raise("acme-tools", 9_000_000) {
		t.Fatal("the FIRST refusal was not reported — nothing would ever be logged")
	}
	for i := 0; i < 100; i++ {
		if s.Raise("acme-tools", 9_000_000) {
			t.Fatalf("repeat %d reported as a transition — this is the log storm", i)
		}
	}
	if !s.Held("acme-tools") {
		t.Error("the condition is not held after being raised")
	}
}

// TestRaiseReportsAMovedDetail: a different document past the cap is a new
// event, not a repeat of the old one. Bounded by how often a document is
// stored, never by the tick.
func TestRaiseReportsAMovedDetail(t *testing.T) {
	var s Standing
	s.Raise("acme-tools", 9_000_000)
	if !s.Raise("acme-tools", 11_000_000) {
		t.Error("a NEW oversized document was silent — the operator sees the first size forever")
	}
	if s.Raise("acme-tools", 11_000_000) {
		t.Error("the moved detail then repeated as a transition")
	}
}

// TestObserveReportsBothEnds: a sweep raises what is newly true and clears what
// stopped being true. The clear is the half that tells an operator the problem
// they were chasing is over.
func TestObserveReportsBothEnds(t *testing.T) {
	var s Standing
	raised, cleared := s.Observe(map[string]int64{"a": 1, "b": 2})
	if !reflect.DeepEqual(raised, []Change{{Key: "a", Detail: 1}, {Key: "b", Detail: 2}}) {
		t.Fatalf("first sweep raised %v, want a and b in key order", raised)
	}
	if len(cleared) != 0 {
		t.Fatalf("first sweep cleared %v, want nothing", cleared)
	}

	// Same two, unchanged: silence.
	if raised, cleared = s.Observe(map[string]int64{"a": 1, "b": 2}); len(raised)+len(cleared) != 0 {
		t.Fatalf("an unchanged sweep spoke: raised=%v cleared=%v", raised, cleared)
	}

	// `a` shrank under the cap and is gone from the sweep; `c` appeared.
	raised, cleared = s.Observe(map[string]int64{"b": 2, "c": 3})
	if !reflect.DeepEqual(raised, []Change{{Key: "c", Detail: 3}}) {
		t.Errorf("raised %v, want only c", raised)
	}
	if !reflect.DeepEqual(cleared, []Change{{Key: "a", Detail: 1}}) {
		t.Errorf("cleared %v, want a carrying the detail it last reported", cleared)
	}
	if s.Held("a") {
		t.Error("a is still held after clearing")
	}
}

// TestObserveClearsWhatRaiseSet: the two entry points share one set, so the
// store pod's per-request refusal and its per-listing sweep cannot each keep
// their own idea of what is standing and log the same row twice.
func TestObserveClearsWhatRaiseSet(t *testing.T) {
	var s Standing
	s.Raise("acme-tools", 9_000_000)
	raised, cleared := s.Observe(map[string]int64{"acme-tools": 9_000_000})
	if len(raised) != 0 {
		t.Errorf("the sweep re-raised what Raise already reported: %v", raised)
	}
	if len(cleared) != 0 {
		t.Errorf("the sweep cleared a standing condition: %v", cleared)
	}
	_, cleared = s.Observe(nil)
	if !reflect.DeepEqual(cleared, []Change{{Key: "acme-tools", Detail: 9_000_000}}) {
		t.Errorf("an empty sweep cleared %v, want the Raise-set condition", cleared)
	}
}

// TestZeroValueIsUsable: no constructor, so a struct field needs no wiring.
func TestZeroValueIsUsable(t *testing.T) {
	var s Standing
	if s.Held("anything") {
		t.Error("the zero value holds a condition nobody raised")
	}
	if raised, cleared := s.Observe(nil); len(raised)+len(cleared) != 0 {
		t.Errorf("an empty first sweep spoke: raised=%v cleared=%v", raised, cleared)
	}
}
