// The Overview headline — the first line an operator reads, and the one that
// was asserting an all-clear before anything had been checked.
//
// It lived as a computed inside App.vue, where vitest cannot see it, and it
// said `No drift detected` whenever the live-finding list was empty. Nothing
// gated that on whether there were any calls, whether a contract was bound, or
// whether a single call had actually been validated. The first-launch QA walk
// hit it on all three lanes:
//   - a store with zero calls and no contract: green
//   - after traffic, with `0 of 2 providers checked against a contract`
//     rendering directly below it: still green
//   - with a vendor hard down and nothing reaching the provider at all: still
//     green
//
// An empty finding list has two completely different causes and this line was
// reporting only the flattering one. "We looked and found nothing" and "nothing
// looked" are not the same sentence, and the second one is what a fresh install
// is actually in. So the zero state is now NEUTRAL — neither the green
// all-clear nor the red drift banner — and says nothing has been validated yet.
//
// This is known-open item A6 in docs/contract-upload-ux.md ("Open — owner
// calls", 4), whose ruling is the WORDING, not whether the branch exists. The
// branch ships; the string is one constant so the owner's call is a one-line
// change here and nowhere else.
//
// Pure and vitest-covered, deliberately: it is a lie in this line that the
// whole coverage story sits underneath, and App.vue logic is invisible to the
// suite.

/** The neutral zero state. A6's owner ruling changes THIS and nothing else. */
export const NOTHING_VALIDATED_YET = 'Nothing validated yet';

/** The all-clear, which now costs at least one validated call. */
export const NO_DRIFT_DETECTED = 'No drift detected';

/**
 * `neutral` is a third state, not a shade of the other two. Rendering it green
 * asserts the all-clear this whole change exists to stop; rendering it red
 * accuses a provider of something on evidence that does not exist either.
 */
export type HeadlineTone = 'ok' | 'drift' | 'neutral';

/** The minimum a finding needs to name itself on the headline. */
export interface HeadlineFinding {
  endpoint: string;
}

export interface HeadlineInput {
  /**
   * LIVE drift findings only — spec-version diffs are informational and stay
   * out of the divergence status (unchanged from the original computed).
   */
  liveFindings: readonly HeadlineFinding[];
  /**
   * Calls in the loaded window whose coverage is `checked` — i.e. a contract
   * was bound to their edge BEFORE they were captured (ui/src/coverage.ts).
   * Zero covers both walk-throughs: no calls at all, and calls that nothing
   * was in a position to validate.
   */
  validatedCalls: number;
  /**
   * Pre-traffic honesty (v1p1): absent from /api/health until the collector has
   * observed an external outbound edge (or a finding). While absent the
   * "on integration <slug>" fragment simply does not render — no replacement
   * copy, no fallback slug.
   */
  integration?: string;
}

export interface Headline {
  you: string;
  tone: HeadlineTone;
  integration: string;
}

/**
 * The headline, in priority order.
 *
 * Findings first: a finding is positive evidence that something was validated,
 * whatever the window's call rows happen to say about it — a drifting call can
 * be evicted, or its finding can outlive it. Then the neutral gate. The
 * all-clear is last and now has to be earned.
 */
export function headlineFor(input: HeadlineInput): Headline {
  const integration = input.integration || '';
  const n = input.liveFindings.length;

  if (n > 0) {
    const endpoints = Array.from(new Set(input.liveFindings.map((f) => f.endpoint)));
    return {
      you: `${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')}`,
      tone: 'drift',
      integration
    };
  }

  if (input.validatedCalls <= 0) {
    return { you: NOTHING_VALIDATED_YET, tone: 'neutral', integration };
  }

  return { you: NO_DRIFT_DETECTED, tone: 'ok', integration };
}
