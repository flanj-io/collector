// Package condition tracks STANDING conditions — a fact that stays true across
// many refresh ticks — so each one reaches the log when it STARTS and when it
// ENDS, and not once a tick for as long as it lasts.
//
// It exists because the collector's two contract-channel ends both discover
// their conditions by re-deriving them from scratch on a timer. The drift
// processor's refresh loop lists the store pod's contracts every ten seconds
// and reports whatever went wrong with each row; the store pod's contract
// endpoint answers that listing and every document request. A row that is
// permanently unusable — a document past model.MaxContractDocBytes is the case
// this was written for — is therefore re-discovered by both pods on every tick,
// forever, and before this package each re-discovery was a Warn line. One
// oversized MCP catalogue produced six lines a minute on the front and six on
// the store pod, which buries the one line an operator needed and reads like a
// storm of new events rather than one unchanged fact.
//
// The transition is the event. "This started" and "this stopped" are what an
// operator acts on; the thousand repetitions in between are the same sentence.
package condition

import (
	"sort"
	"sync"
)

// Standing is a set of conditions currently believed true, each carrying one
// integer detail (a byte count, a status, a count) that distinguishes a
// GENUINELY NEW condition from a repeat of the same one.
//
// The zero value is ready to use. Safe for concurrent callers: the store pod
// serves one goroutine per request and the front refreshes on its own.
type Standing struct {
	mu sync.Mutex
	on map[string]int64
}

// Change is one transition: which condition, and the detail it carries. On a
// cleared condition the detail is the one last reported for it, so a log line
// about the end of a condition can still name what it was.
type Change struct {
	Key    string
	Detail int64
}

// Raise records ONE observation of key and reports whether it is worth
// logging: true when the condition was not already standing, or when its
// detail has MOVED.
//
// A moved detail is deliberately a new event rather than a repeat. For the
// document cap that means a different document — the row was replaced and the
// replacement is over the cap too — which is a thing that happened, not the
// same thing continuing. It is bounded by how often a document is stored, never
// by the tick.
//
// Raise never clears anything: a single observation is evidence that one
// condition holds and says nothing about the others. Use Observe for a sweep
// that saw every row.
func (s *Standing) Raise(key string, detail int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on == nil {
		s.on = map[string]int64{}
	}
	prev, was := s.on[key]
	s.on[key] = detail
	return !was || prev != detail
}

// Observe replaces the whole set with what a full sweep found true NOW, and
// returns what moved: the conditions that are newly standing (or whose detail
// changed), and the ones that have cleared.
//
// Both slices are ordered by key, so a tick that raises several conditions logs
// them in the same order every time — a log an operator can diff.
//
// The caller MUST have looked at every row it tracks before calling this. A
// partial sweep would report every condition it did not look at as cleared,
// which is the false all-clear that this package's whole purpose is to make
// legible.
func (s *Standing) Observe(now map[string]int64) (raised, cleared []Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on == nil {
		s.on = map[string]int64{}
	}
	for key, detail := range now {
		prev, was := s.on[key]
		if !was || prev != detail {
			raised = append(raised, Change{Key: key, Detail: detail})
		}
	}
	for key, detail := range s.on {
		if _, still := now[key]; !still {
			cleared = append(cleared, Change{Key: key, Detail: detail})
		}
	}
	next := make(map[string]int64, len(now))
	for key, detail := range now {
		next[key] = detail
	}
	s.on = next
	sortChanges(raised)
	sortChanges(cleared)
	return raised, cleared
}

// Standing reports whether key is currently held, for a caller that needs to
// ask without recording anything.
func (s *Standing) Held(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.on[key]
	return ok
}

func sortChanges(c []Change) {
	sort.Slice(c, func(i, j int) bool { return c[i].Key < c[j].Key })
}
