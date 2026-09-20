// The Contracts tab's count tiers — one colour each, in order of urgency.
//
// Pure and vitest-covered, like coverage.ts and headline.ts, because the thing
// that goes wrong here is arithmetic the mounted app hides: a tab pill that
// counts a different population from the headline beside it reads as a
// miscount, and nobody can tell from the screen which of the two is wrong.
//
// The defect this replaces: the red pill counted breaking-severity rows from
// EVERY source, so a deployment with 2 live drifts and 3 breaking changes
// found by diffing an uploaded contract against the version it replaced showed
// a red `5` over a headline saying `2 contract drift findings` and two drifted
// rows. Both numbers were right about their own population and the screen made
// them look like one number disagreeing with itself.
//
// Three tiers, in order of urgency:
//
//   RED    breaking NOW    — breaking-severity findings from LIVE traffic.
//                            Something is failing against the contract while
//                            you read this. Equals the Overview headline's
//                            count and the drifted rows beside it, by
//                            construction: same population, same question.
//   COPPER would break     — a change that WILL break you, evidenced ahead of
//                            live traffic: today, breaking changes found by
//                            diffing two VERSIONS of a contract. Nothing is
//                            failing yet; providers usually run a deprecation
//                            window, so this is a warning with time on it,
//                            never red. A deprecation notice belongs here for
//                            the same reason, and the kind lists below already
//                            put it here.
//   STEEL  worth knowing   — un-acknowledged informational rows (NON-BREAKING
//                            and DESCRIPTION). Always the steel outline: copper
//                            means "would break" now, and two coppers with
//                            different meanings is exactly the confusion the
//                            tiers exist to end.
//
// Invariant, and the reason this module is worth its own file:
//
//     red + copper + steel + acknowledged = the rows listed on the tab
//
// Every row lands in exactly one tier, so the pills can never over- or
// under-count the list underneath them. countTiers returns all four counts
// plus the total from one pass, so a caller cannot compute three of them from
// this module and the fourth by hand.
//
// Severity is never colour alone: each tier carries its own sentence
// (breakingNowTitle / wouldBreakTitle / worthKnowingTitle), rendered as both
// the pill's title and its accessible name.

import { isAcked, isBreakingFinding, informationalCountTitle, descriptionCountTitle } from './mcp';

/** The four tiers. `acknowledged` is a tier with no pill: it is what the other
 *  three do not count, and it is in the union so the invariant is total. */
export type ContractTier = 'breaking-now' | 'would-break' | 'worth-knowing' | 'acknowledged';

/** The minimum a finding needs to be tiered. Structural on purpose: the
 *  module never sees a whole Finding and cannot come to depend on one. */
export interface TierFinding {
  kind: string;
  severity: string;
  rule: string;
  acked?: boolean;
  /** Nullable like the read API's own column: an ack bound to nothing is not
   *  an ack (isAcked applies the evidence-version rule). */
  acked_evidence_version?: string | null;
  spec_version_to?: string | null;
}

/**
 * The kinds whose evidence sits AHEAD of live traffic — a document or an
 * announcement, not a call. A row of one of these kinds can never be "breaking
 * now", however severe it is, because nothing has failed yet.
 *
 * SEAM. `deprecation` is listed before anything emits it, on purpose: a
 * provider announcing that a field or an operation is going away is the same
 * shape of news as a version diff (it will break you, later, with a window to
 * act in), and listing it here means the counting needs no change on the day
 * the collector starts detecting them. Nothing detects them today and nothing
 * here pretends otherwise — this module only sorts rows it is handed.
 */
export const AHEAD_OF_LIVE_KINDS: readonly string[] = ['version-diff', 'deprecation'];

/**
 * Of those, the kinds that ANNOUNCE a break whatever severity they are stamped
 * with. A deprecation notice is the announcement itself: it says something
 * will stop working, and the window is the whole point, so it is copper at
 * `warning` severity just as much as at `breaking`. A version diff is not —
 * its non-breaking rows really are only worth knowing, and calling them
 * "would break" under a sentence that says "breaking changes" would be a lie
 * the tier exists to prevent.
 */
export const ANNOUNCED_BREAK_KINDS: readonly string[] = ['deprecation'];

/** Is this row's evidence ahead of live traffic (a document, an announcement)
 *  rather than a call that failed? */
export function isAheadOfLive(f: Pick<TierFinding, 'kind'>): boolean {
  return AHEAD_OF_LIVE_KINDS.includes(f.kind);
}

/**
 * Does this row say something WILL break? True for a breaking version diff,
 * and for a deprecation at any severity. This is the copper tier's predicate.
 */
export function isWouldBreakRow(f: Pick<TierFinding, 'kind' | 'severity'>): boolean {
  if (!isAheadOfLive(f)) return false;
  return isBreakingFinding(f) || ANNOUNCED_BREAK_KINDS.includes(f.kind);
}

/**
 * Which tier a row counts in. Exactly one, always — the branches are total,
 * which is what makes the invariant above hold rather than nearly hold.
 *
 * Evidence is asked FIRST, severity second. A breaking-severity row whose
 * evidence is a document is not breaking anything yet, and red is reserved for
 * what is failing in live traffic now.
 *
 * A row keeps its own severity on its own row badge and stays flaggable: only
 * the SUMMARY it counts towards moves. What changed is which number on the tab
 * strip speaks for it, not what the row says about itself.
 */
export function tierOf(f: TierFinding): ContractTier {
  if (isWouldBreakRow(f)) return 'would-break';
  // An ahead-of-live row that will NOT break anyone (a non-breaking version
  // diff) falls through to the informational tiers below, with everything else
  // that is merely worth knowing.
  if (isBreakingFinding(f) && !isAheadOfLive(f)) return 'breaking-now';
  return isAcked(f) ? 'acknowledged' : 'worth-knowing';
}

/** The three pill counts, the silent fourth tier, and the list they partition. */
export interface TierCounts {
  /** RED. */
  breakingNow: number;
  /** COPPER. */
  wouldBreak: number;
  /** STEEL. */
  worthKnowing: number;
  /** No pill: acknowledged locally, still listed on the tab. */
  acknowledged: number;
  /** The rows listed on the tab. Equals the four above, by construction. */
  total: number;
}

/** Tier every row, in one pass. */
export function countTiers(rows: readonly TierFinding[]): TierCounts {
  const counts: TierCounts = { breakingNow: 0, wouldBreak: 0, worthKnowing: 0, acknowledged: 0, total: rows.length };
  for (const f of rows) {
    switch (tierOf(f)) {
      case 'breaking-now':
        counts.breakingNow++;
        break;
      case 'would-break':
        counts.wouldBreak++;
        break;
      case 'worth-knowing':
        counts.worthKnowing++;
        break;
      default:
        counts.acknowledged++;
    }
  }
  return counts;
}

/** Rows a tier pill speaks for — everything the three pills count together. */
export function unresolvedCount(c: TierCounts): number {
  return c.breakingNow + c.wouldBreak + c.worthKnowing;
}

/** Red pill: `2 breaking drift findings in live traffic`. Says LIVE, because
 *  the number's whole job is to mean the same thing as the headline. */
export function breakingNowTitle(n: number): string {
  return `${n} breaking drift finding${n === 1 ? '' : 's'} in live traffic`;
}

/**
 * Copper pill: `3 breaking changes in a newer contract version — nothing is
 * breaking yet`. The second clause is the tier: red is what is failing now,
 * copper is what will fail when the newer version takes effect.
 *
 * HANDOFF, and the ONE thing on this side a deprecation change must touch:
 * the tiering already counts a deprecation row here (ANNOUNCED_BREAK_KINDS),
 * but this sentence names a newer contract VERSION, which does not describe a
 * deprecation notice. Whoever teaches the collector to detect deprecations
 * owns making this line true of a mixed tier — the counting needs no change,
 * the copy does. Leaving it as-is would put a pill over rows it misdescribes,
 * which is the exact failure the tiers were introduced to end.
 */
export function wouldBreakTitle(n: number): string {
  return `${n} breaking change${n === 1 ? '' : 's'} in a newer contract version — nothing is breaking yet`;
}

/** Copper card chip: `3 WOULD BREAK`. */
export function wouldBreakChipLabel(n: number): string {
  return `${n} WOULD BREAK`;
}

/**
 * Steel pill. The tier keeps the two sentences it already had — the
 * description-only wording when every row in it is a wording change, the
 * shared one otherwise — because the rows and their vocabulary did not move;
 * only the colour is now fixed, since copper was taken.
 */
export function worthKnowingTitle(n: number, descriptionOnly: boolean): string {
  return descriptionOnly ? descriptionCountTitle(n) : informationalCountTitle(n);
}
