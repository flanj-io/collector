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
//   STEEL  worth knowing   — open informational rows (NON-BREAKING
//                            and DESCRIPTION). Always the steel outline: copper
//                            means "would break" now, and two coppers with
//                            different meanings is exactly the confusion the
//                            tiers exist to end.
//
// Invariant, and the reason this module is worth its own file:
//
//     red + copper + steel + resolved = the rows listed on the tab
//
// Every row lands in exactly one tier, so the pills can never over- or
// under-count the list underneath them. countTiers returns all four counts
// plus the total from one pass, so a caller cannot compute three of them from
// this module and the fourth by hand.
//
// Severity is never colour alone: each tier carries its own sentence
// (breakingNowTitle / wouldBreakTitle / worthKnowingTitle), rendered as both
// the pill's title and its accessible name.

import { isResolved, isBreakingFinding, informationalCountTitle, descriptionCountTitle } from './mcp';

/** The four tiers. `resolved` is a tier with no pill: it is what the other
 *  three do not count, and it is in the union so the invariant is total. */
export type ContractTier = 'breaking-now' | 'would-break' | 'worth-knowing' | 'resolved';

/** The minimum a finding needs to be tiered. Structural on purpose: the
 *  module never sees a whole Finding and cannot come to depend on one. */
export interface TierFinding {
  kind: string;
  severity: string;
  rule: string;
  /** True only while the resolution still covers the finding — decided
   *  server-side (isResolved just reads it). */
  resolved?: boolean;
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
 * Resolved is asked FIRST, ahead of would-break and breaking-now: a resolved
 * row leaves whichever pill it would otherwise have counted towards — a
 * resolved breaking row leaves red, a resolved version-diff or deprecation
 * leaves copper, a resolved informational row leaves steel — and moves to the
 * dimmed band instead. Only then does evidence get asked (ahead-of-live vs
 * live), then severity: a breaking-severity row whose evidence is a document
 * is not breaking anything yet, and red is reserved for what is failing in
 * live traffic now.
 *
 * A row keeps its own severity on its own row badge and stays flaggable: only
 * the SUMMARY it counts towards moves. What changed is which number on the tab
 * strip speaks for it, not what the row says about itself.
 */
export function tierOf(f: TierFinding): ContractTier {
  if (isResolved(f)) return 'resolved';
  if (isWouldBreakRow(f)) return 'would-break';
  // An ahead-of-live row that will NOT break anyone (a non-breaking version
  // diff) falls through to the informational tiers below, with everything else
  // that is merely worth knowing.
  if (isBreakingFinding(f) && !isAheadOfLive(f)) return 'breaking-now';
  return 'worth-knowing';
}

/** The three pill counts, the silent fourth tier, and the list they partition. */
export interface TierCounts {
  /** RED. */
  breakingNow: number;
  /** COPPER. */
  wouldBreak: number;
  /** STEEL. */
  worthKnowing: number;
  /** No pill: resolved, still listed on the tab, dimmed. */
  resolved: number;
  /** The rows listed on the tab. Equals the four above, by construction. */
  total: number;
}

/** Tier every row, in one pass. */
export function countTiers(rows: readonly TierFinding[]): TierCounts {
  const counts: TierCounts = { breakingNow: 0, wouldBreak: 0, worthKnowing: 0, resolved: 0, total: rows.length };
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
        counts.resolved++;
    }
  }
  return counts;
}

/**
 * The tab button's own accessible name: `Contracts: 1 breaking now, 3 would
 * break, 1 worth knowing`, or just the name when every tier is empty.
 *
 * Why the tab needs one at all. Each pill carries a full sentence as its
 * `aria-label`, so that no tier is conveyed by colour alone. But the pills sit
 * INSIDE the tab button, and a button with no name of its own is named by its
 * contents — so the three sentences concatenated into a forty-word tab name
 * that a screen reader read out on every focus, in full, every time.
 *
 * An explicit name on the button wins over its contents (accname: aria-label
 * before content), which collapses that to one short phrase while leaving each
 * pill's own sentence intact for anything that inspects a pill directly, and
 * leaving the hover titles untouched. Short enough to hear on every focus,
 * and it still says what each number counts — which is the actual requirement
 * the long labels were serving.
 */
export function tabAriaLabel(name: string, c: TierCounts): string {
  const parts: string[] = [];
  if (c.breakingNow) parts.push(`${c.breakingNow} breaking now`);
  if (c.wouldBreak) parts.push(`${c.wouldBreak} would break`);
  if (c.worthKnowing) parts.push(`${c.worthKnowing} worth knowing`);
  return parts.length ? `${name}: ${parts.join(', ')}` : name;
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
 * Copper pill and copper card chip title. Takes the ROWS, not a number, and
 * reads them — so the sentence is true of its own input at every point in
 * time, including the window after deprecations are detected and before
 * anyone revisits this copy.
 *
 * Pass the whole tab (or a card's whole list): it filters to the would-break
 * rows itself, so the count and the wording can never be derived from two
 * different populations.
 *
 *   only version diffs → `N breaking changes in a newer contract version …`
 *   only deprecations  → `N deprecations affecting your traffic …`
 *   any other mix      → `N changes that will break you later …`
 *
 * The mixed wording is also the FALLBACK, deliberately: a would-break kind
 * nobody has written a sentence for yet still gets one that is true of it,
 * rather than borrowing the version-diff sentence and lying.
 *
 * Every variant ends in the same clause. That clause is the tier — red is what
 * is failing now, copper is what will fail later — and it must survive
 * whatever the first half says.
 */
export function wouldBreakTitle(rows: readonly Pick<TierFinding, 'kind' | 'severity'>[]): string {
  const mine = rows.filter((f) => isWouldBreakRow(f));
  const n = mine.length;
  const tail = ' — nothing is breaking yet';
  const only = (kind: string) => mine.length > 0 && mine.every((f) => f.kind === kind);
  if (only('version-diff')) {
    return `${n} breaking change${n === 1 ? '' : 's'} in a newer contract version${tail}`;
  }
  if (only('deprecation')) {
    return `${n} deprecation${n === 1 ? '' : 's'} affecting your traffic${tail}`;
  }
  return `${n} change${n === 1 ? '' : 's'} that will break you later${tail}`;
}

/** Copper card chip: `3 WOULD BREAK`. Names no source, so unlike the title it
 *  needs no per-kind wording — it stays true whatever the tier holds. */
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
