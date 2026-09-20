import { describe, it, expect } from 'vitest';
import {
  AHEAD_OF_LIVE_KINDS,
  ANNOUNCED_BREAK_KINDS,
  breakingNowTitle,
  countTiers,
  isAheadOfLive,
  tabAriaLabel,
  isWouldBreakRow,
  tierOf,
  unresolvedCount,
  wouldBreakChipLabel,
  wouldBreakTitle,
  worthKnowingTitle,
  type TierFinding
} from './contract-tiers';

/** A breaking live-vs-spec row: a real call failed against the contract. */
const live = (over: Partial<TierFinding> = {}): TierFinding => ({
  kind: 'live-vs-spec',
  severity: 'breaking',
  rule: 'response-property-type-changed',
  ...over
});

/** A breaking version-diff row: the uploaded contract breaks against the one
 *  it replaced. Call-less by construction — nothing has failed. */
const versionDiff = (over: Partial<TierFinding> = {}): TierFinding => ({
  kind: 'version-diff',
  severity: 'breaking',
  rule: 'response-property-type-changed',
  ...over
});

/** A wording-only MCP definition change: informational, ackable. */
const description = (over: Partial<TierFinding> = {}): TierFinding => ({
  kind: 'definition_change',
  severity: 'warning',
  rule: 'description-changed',
  spec_version_to: 'sha256:bbbb',
  ...over
});

const nonBreaking = (over: Partial<TierFinding> = {}): TierFinding =>
  description({ rule: 'output-schema-declared', ...over });

describe('which tier a row counts in', () => {
  it('a breaking LIVE row is red — something is failing right now', () => {
    expect(tierOf(live())).toBe('breaking-now');
  });

  it('a breaking MCP row is red too: an output mismatch is live evidence', () => {
    // The MCP kinds this tab treats as live evidence stay where the existing
    // design puts them. An output_mismatch came off a real call.
    expect(tierOf(live({ kind: 'output_mismatch' }))).toBe('breaking-now');
    expect(tierOf(live({ kind: 'definition_change', rule: 'output-property-removed' }))).toBe('breaking-now');
    expect(tierOf(live({ kind: 'value_change' }))).toBe('breaking-now');
    expect(tierOf(live({ kind: 'input_rejection' }))).toBe('breaking-now');
  });

  it('a breaking VERSION DIFF is copper, not red — nothing is breaking yet', () => {
    // The defect, in one assertion: this row is severity=breaking and used to
    // be counted red beside a headline that (correctly) did not count it.
    expect(tierOf(versionDiff())).toBe('would-break');
  });

  it('an un-acknowledged informational row is steel, whatever its class', () => {
    expect(tierOf(description())).toBe('worth-knowing');
    expect(tierOf(nonBreaking())).toBe('worth-knowing');
    expect(tierOf(description({ severity: 'info' }))).toBe('worth-knowing');
  });

  it('an acknowledged informational row leaves every pill but stays on the tab', () => {
    expect(tierOf(description({ acked: true, acked_evidence_version: 'sha256:bbbb' }))).toBe('acknowledged');
  });

  it('an ack bound to the WRONG evidence version does not clear the row', () => {
    // The second change on the same tool shares a signature; a stale ack must
    // not pre-acknowledge it (isAcked's evidence-version rule, repeated here
    // because the tiers are what the operator actually reads).
    expect(tierOf(description({ acked: true, acked_evidence_version: 'sha256:aaaa' }))).toBe('worth-knowing');
  });

  it('a breaking row is never acknowledged away, however it is flagged in the store', () => {
    expect(tierOf(live({ acked: true, acked_evidence_version: '' }))).toBe('breaking-now');
    expect(tierOf(versionDiff({ acked: true, acked_evidence_version: '' }))).toBe('would-break');
  });
});

describe('the kind lists are the seam', () => {
  it('names the kinds whose evidence is ahead of live traffic', () => {
    expect([...AHEAD_OF_LIVE_KINDS]).toEqual(['version-diff', 'deprecation']);
    expect(isAheadOfLive({ kind: 'version-diff' })).toBe(true);
    expect(isAheadOfLive({ kind: 'deprecation' })).toBe(true);
    expect(isAheadOfLive({ kind: 'live-vs-spec' })).toBe(false);
    expect(isAheadOfLive({ kind: 'output_mismatch' })).toBe(false);
  });

  it('names the kinds that announce a break whatever severity they carry', () => {
    expect([...ANNOUNCED_BREAK_KINDS]).toEqual(['deprecation']);
  });

  // FORWARD-COMPAT. Nothing emits a deprecation finding today; this invents no
  // detection and asserts none. It pins the tiering so that the day one
  // arrives, it is already counted copper and no counting code has to change.
  it('forward-compat: a warning-severity deprecation is copper, not steel', () => {
    const deprecation: TierFinding = { kind: 'deprecation', severity: 'warning', rule: 'operation-deprecated' };
    expect(isWouldBreakRow(deprecation)).toBe(true);
    expect(tierOf(deprecation)).toBe('would-break');
    // ...and it is not red, whatever severity the collector stamps on it: a
    // deprecation notice is never something failing right now. That is what
    // the window is for.
    expect(tierOf({ ...deprecation, severity: 'breaking' })).toBe('would-break');
    expect(tierOf({ ...deprecation, severity: 'info' })).toBe('would-break');
  });

  // The rule that makes the tier by KIND rather than by severity=warning. MCP
  // wording and schema changes are already stamped `warning`, and they are the
  // steel tier. Routing every warning to copper would recolour every one of
  // them and undo the one-vocabulary rule the description class has.
  it('a warning severity alone never means copper — informational rows are stamped warning too', () => {
    expect(tierOf(description())).toBe('worth-knowing');
    expect(tierOf(nonBreaking())).toBe('worth-knowing');
    expect(isWouldBreakRow(description())).toBe(false);
  });

  it('a NON-breaking version diff is only worth knowing — it will not break anyone', () => {
    // The copper sentence says "breaking changes". A non-breaking version diff
    // under it would be a lie of exactly the kind these tiers exist to end.
    expect(tierOf(versionDiff({ severity: 'warning' }))).toBe('worth-knowing');
    expect(isWouldBreakRow(versionDiff({ severity: 'warning' }))).toBe(false);
  });
});

describe('the counts partition the rows listed on the tab', () => {
  it("counts the owner's case: 2 live breaking + 3 version diffs + 1 informational", () => {
    const rows = [
      live({ rule: 'a' }),
      live({ rule: 'b' }),
      versionDiff({ rule: 'c' }),
      versionDiff({ rule: 'd' }),
      versionDiff({ rule: 'e' }),
      description()
    ];
    // Before: red 5, copper 1. After: red 2 — the same 2 the headline names.
    expect(countTiers(rows)).toEqual({
      breakingNow: 2,
      wouldBreak: 3,
      worthKnowing: 1,
      acknowledged: 0,
      total: 6
    });
  });

  it('red + copper + steel + acknowledged = the rows listed, always', () => {
    const rows = [
      live(),
      live({ kind: 'output_mismatch' }),
      versionDiff(),
      description(),
      nonBreaking(),
      description({ acked: true, acked_evidence_version: 'sha256:bbbb' }),
      description({ severity: 'info' })
    ];
    const c = countTiers(rows);
    expect(c.breakingNow + c.wouldBreak + c.worthKnowing + c.acknowledged).toBe(c.total);
    expect(c.total).toBe(rows.length);
  });

  it('an empty tab counts zero in every tier', () => {
    expect(countTiers([])).toEqual({
      breakingNow: 0,
      wouldBreak: 0,
      worthKnowing: 0,
      acknowledged: 0,
      total: 0
    });
  });

  it('unresolvedCount is what the three pills show together', () => {
    const c = countTiers([live(), versionDiff(), description(), description({ acked: true, acked_evidence_version: 'sha256:bbbb' })]);
    expect(unresolvedCount(c)).toBe(3);
    // The acknowledged row is still listed on the tab — it just has no pill.
    expect(c.total).toBe(4);
  });
});

describe('each tier says what it counts, so the colour is never the only signal', () => {
  it('red names live traffic', () => {
    expect(breakingNowTitle(2)).toBe('2 breaking drift findings in live traffic');
    expect(breakingNowTitle(1)).toBe('1 breaking drift finding in live traffic');
  });

  // The copper sentence reads its own rows, so it is true of whatever the tier
  // holds at the time — not a string that has to be swapped later by whoever
  // adds the next would-break kind.
  it('copper, version diffs only: names the newer version', () => {
    expect(wouldBreakTitle([versionDiff(), versionDiff(), versionDiff()])).toBe(
      '3 breaking changes in a newer contract version — nothing is breaking yet'
    );
    expect(wouldBreakTitle([versionDiff()])).toBe(
      '1 breaking change in a newer contract version — nothing is breaking yet'
    );
  });

  it('copper counts only the would-break rows out of whatever it is handed', () => {
    // The whole tab goes in. Count and wording therefore cannot be derived
    // from two different populations.
    expect(wouldBreakTitle([live(), versionDiff(), description(), nonBreaking()])).toBe(
      '1 breaking change in a newer contract version — nothing is breaking yet'
    );
  });

  // FORWARD-COMPAT, both cases. Nothing emits a deprecation finding today.
  it('copper, deprecations only: names the deprecations, never a contract version', () => {
    const dep = (): TierFinding => ({ kind: 'deprecation', severity: 'warning', rule: 'operation-deprecated' });
    expect(wouldBreakTitle([dep(), dep()])).toBe('2 deprecations affecting your traffic — nothing is breaking yet');
    expect(wouldBreakTitle([dep()])).toBe('1 deprecation affecting your traffic — nothing is breaking yet');
  });

  it('copper, mixed: falls back to wording true of both', () => {
    const dep = (): TierFinding => ({ kind: 'deprecation', severity: 'warning', rule: 'operation-deprecated' });
    expect(wouldBreakTitle([versionDiff(), dep()])).toBe(
      '2 changes that will break you later — nothing is breaking yet'
    );
    // The mixed wording is the fallback for an unknown would-break kind too:
    // it must never borrow the version-diff sentence and say something false.
    expect(wouldBreakTitle([{ kind: 'deprecation', severity: 'warning', rule: 'r' }, versionDiff()])).toMatch(
      /^2 changes that will break you later/
    );
  });

  it('every copper variant keeps the clause that IS the tier', () => {
    const dep = (): TierFinding => ({ kind: 'deprecation', severity: 'warning', rule: 'operation-deprecated' });
    for (const rows of [[versionDiff()], [dep()], [versionDiff(), dep()]]) {
      expect(wouldBreakTitle(rows)).toMatch(/ — nothing is breaking yet$/);
    }
  });

  it('the copper card chip carries the tier in its own words', () => {
    expect(wouldBreakChipLabel(3)).toBe('3 WOULD BREAK');
  });

  it('steel keeps both of the sentences the tier already had', () => {
    expect(worthKnowingTitle(2, false)).toBe('2 non-breaking — acknowledge to clear');
    expect(worthKnowingTitle(1, true)).toBe('1 description change — wording only, non-breaking — acknowledge to clear');
  });

  it('no two tiers share a sentence', () => {
    const titles = [
      breakingNowTitle(1),
      wouldBreakTitle([versionDiff()]),
      worthKnowingTitle(1, false),
      worthKnowingTitle(1, true)
    ];
    expect(new Set(titles).size).toBe(titles.length);
  });
});

describe('the tab names itself, so the pills do not name it for it', () => {
  const counts = (rows: TierFinding[]) => countTiers(rows);

  it('lists each populated tier, short enough to hear on every focus', () => {
    expect(tabAriaLabel('Contracts', counts([live(), versionDiff(), versionDiff(), versionDiff(), description()]))).toBe(
      'Contracts: 1 breaking now, 3 would break, 1 worth knowing'
    );
  });

  it('leaves out an empty tier rather than announcing a zero', () => {
    expect(tabAriaLabel('Contracts', counts([versionDiff()]))).toBe('Contracts: 1 would break');
    expect(tabAriaLabel('Contracts', counts([live(), description()]))).toBe(
      'Contracts: 1 breaking now, 1 worth knowing'
    );
  });

  it('is just the name when there is nothing to count', () => {
    expect(tabAriaLabel('Contracts', counts([]))).toBe('Contracts');
    // An acknowledged row is listed but has no pill, so it adds nothing here.
    expect(tabAriaLabel('Contracts', counts([description({ acked: true, acked_evidence_version: 'sha256:bbbb' })]))).toBe(
      'Contracts'
    );
  });

  it('stays far shorter than the pill sentences it replaces', () => {
    const c = counts([live(), versionDiff(), description()]);
    const pillWords = [breakingNowTitle(1), wouldBreakTitle([versionDiff()]), worthKnowingTitle(1, true)]
      .join(' ')
      .split(/\s+/).length;
    expect(tabAriaLabel('Contracts', c).split(/\s+/).length).toBeLessThan(pillWords / 2);
  });
});
