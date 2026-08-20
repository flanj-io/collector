// PAN recognizer. LOCATES candidates structurally — chains of digit groups joined by
// single spaces/dashes (bare, 4-4-4-4, 4-6-5, 4-4-4-4-3, dashed …) — normalizes them
// to digits, and lets the Luhn validator DECIDE. Detect on normalized digits, redact
// the original span. Anchored: a chain glued to a letter/digit/underscore on either
// side is not a candidate (so ids/hashes/UUIDs never fire and digit runs are never
// split). Mirrors recognizers/pan.ts. Contract = contracts/redaction-vectors.json +
// contracts/redaction-fixtures.json.
package redact

// PAN length bounds (ISO/IEC 7812) and the most groups a printed PAN uses (4-4-4-4-3).
const (
	panMinDigits = 13
	panMaxDigits = 19
	panMaxGroups = 5
)

// digitGroups returns the maximal runs of ASCII digits in s.
func digitGroups(s string) []Span {
	var groups []Span
	i := 0
	for i < len(s) {
		if isDigitAt(s, i) {
			start := i
			for isDigitAt(s, i) {
				i++
			}
			groups = append(groups, Span{Start: start, End: i})
		} else {
			i++
		}
	}
	return groups
}

// joinable: two adjacent groups are joinable when exactly one space or dash separates them.
func joinable(s string, a, b Span) bool {
	if b.Start != a.End+1 {
		return false
	}
	sep := s[a.End]
	return sep == ' ' || sep == '-'
}

// findPAN implements the PAN recognizer. Within a chain every sub-chain of
// ≤ panMaxGroups groups is tried longest-first from each start, left to right, so a
// PAN preceded or followed by other separated digit groups ("ref 1234 4111 1111 1111
// 1111", "4111111111111111 1225") is still found — the classic leftmost-greedy regex
// tests the wrong window and leaks the card.
func findPAN(text string, _ Context) []Span {
	var spans []Span
	groups := digitGroups(text)
	i := 0
	for i < len(groups) {
		// Build the maximal chain starting at group i.
		j := i
		for j+1 < len(groups) && joinable(text, groups[j], groups[j+1]) {
			j++
		}
		chain := groups[i : j+1]

		k := 0
		for k < len(chain) {
			matched := false
			// Left anchor: a sub-chain starting mid-chain is preceded by a separator
			// (fine); the chain's first group must not be glued to a word character.
			leftOk := k > 0 || !isWordCharAt(text, chain[0].Start-1)
			if leftOk {
				lastE := min(len(chain)-1, k+panMaxGroups-1)
				for e := lastE; e >= k; e-- {
					// Right anchor: a sub-chain ending mid-chain is followed by a separator.
					rightOk := e < len(chain)-1 || !isWordCharAt(text, chain[e].End)
					if !rightOk {
						continue
					}
					digits := make([]byte, 0, panMaxDigits)
					for g := k; g <= e; g++ {
						digits = append(digits, text[chain[g].Start:chain[g].End]...)
					}
					if len(digits) < panMinDigits {
						break // shorter sub-chains only get shorter
					}
					if len(digits) > panMaxDigits {
						continue
					}
					if luhn(string(digits)) {
						spans = append(spans, Span{Start: chain[k].Start, End: chain[e].End})
						k = e + 1
						matched = true
						break
					}
				}
			}
			if !matched {
				k++
			}
		}
		i = j + 1
	}
	return spans
}
