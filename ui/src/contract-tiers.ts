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
//   COPPER would break     — breaking changes found by diffing two VERSIONS of
//                            a contract. Nothing is failing yet; providers
//                            usually run a deprecation window, so this is a
//                            warning with time on it, never red.
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
 * The kinds whose evidence is two VERSIONS of a contract rather than live
 * traffic — the copper tier. A finding of one of these kinds describes what
 * WILL break when the newer version takes effect; nothing has failed yet.
 *
 * SEAM — deprecation findings: a provider announcing that a field or an
 * operation is going away is the same shape of news (breaking, later, with a
 * window to act in) and belongs in this tier. The collector does not detect
 * them today. When it does, adding that kind to this list is the whole change
 * on this side: the pill, its sentence, the card chip and the invariant all
 * follow from the list. Do NOT add detection here — this module only sorts.
 */
export const WOULD_BREAK_KINDS: readonly string[] = ['version-diff'];

/** Is this row's evidence a version diff rather than live traffic? */
export function isWouldBreakKind(f: Pick<TierFinding, 'kind'>): boolean {
  return WOULD_BREAK_KINDS.includes(f.kind);
}

/**
 * Which tier a row counts in. Exactly one, always — the branches are total.
 *
 * A version-diff row keeps its own `breaking` severity on its own row badge
 * and stays flaggable: only the SUMMARY it counts towards moves. What changed
 * is which number on the tab strip speaks for it, not what the row says about
 * itself.
 */
export function tierOf(f: TierFinding): ContractTier {
  if (isBreakingFinding(f)) return isWouldBreakKind(f) ? 'would-break' : 'breaking-now';
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

/** Copper pill: `3 breaking changes in a newer contract version — nothing is
 *  breaking yet`. The second clause is the tier: red is what is failing now,
 *  copper is what will fail when the newer version takes effect. */
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
