import { describe, it, expect } from 'vitest';
import {
  WOULD_BREAK_KINDS,
  breakingNowTitle,
  countTiers,
  isWouldBreakKind,
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

describe('the would-break kind list is the seam', () => {
  it('lists the version diff today and nothing else', () => {
    expect([...WOULD_BREAK_KINDS]).toEqual(['version-diff']);
  });

  it('decides the tier by kind alone, so adding a kind is the whole change', () => {
    expect(isWouldBreakKind({ kind: 'version-diff' })).toBe(true);
    expect(isWouldBreakKind({ kind: 'live-vs-spec' })).toBe(false);
    // Deprecations are not detected yet — the seam is the list above, and
    // nothing here pretends otherwise.
    expect(isWouldBreakKind({ kind: 'deprecation' })).toBe(false);
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

  it('copper says nothing is breaking yet, in the same breath as "breaking"', () => {
    expect(wouldBreakTitle(3)).toBe('3 breaking changes in a newer contract version — nothing is breaking yet');
    expect(wouldBreakTitle(1)).toBe('1 breaking change in a newer contract version — nothing is breaking yet');
  });

  it('the copper card chip carries the tier in its own words', () => {
    expect(wouldBreakChipLabel(3)).toBe('3 WOULD BREAK');
  });

  it('steel keeps both of the sentences the tier already had', () => {
    expect(worthKnowingTitle(2, false)).toBe('2 non-breaking — acknowledge to clear');
    expect(worthKnowingTitle(1, true)).toBe('1 description change — wording only, non-breaking — acknowledge to clear');
  });

  it('no two tiers share a sentence', () => {
    const titles = [breakingNowTitle(1), wouldBreakTitle(1), worthKnowingTitle(1, false), worthKnowingTitle(1, true)];
    expect(new Set(titles).size).toBe(titles.length);
  });
});
