import { describe, expect, it } from 'vitest';
import { DEFAULT_TAB, TABS, hashForFinding, hashForTab, hashForThread, isTab, routeFromHash } from './route';

// Launch-week item 10 (postgres walk, 2026-09-07): Back stripped the hash but
// the tab stayed on Threads, because the tab buttons never wrote the hash while
// two programmatic paths did. This module is the one mapping both directions
// now use; App.vue reads it on load / hashchange and writes it on every setTab.

describe('routeFromHash ↔ hashForTab', () => {
  it('every tab round-trips through its own hash, with no row ids', () => {
    for (const tab of TABS) {
      const hash = hashForTab(tab);
      expect(hash.startsWith('#'), `${tab} writes a fragment`).toBe(true);
      expect(routeFromHash(hash)).toEqual({ tab, threadId: null, findingId: null, known: true });
    }
  });

  it('the five hashes are distinct, and the contract tab is spelled #contracts on the wire', () => {
    const hashes = TABS.map(hashForTab);
    expect(new Set(hashes).size).toBe(TABS.length);
    // The plural the control plane's deep link (`#contracts/<finding_id>`) already uses —
    // the bare tab hash is its prefix, not a second word.
    expect(hashForTab('contract')).toBe('#contracts');
    expect(hashForTab('overview')).toBe('#overview');
    expect(hashForTab('traffic')).toBe('#traffic');
    expect(hashForTab('threads')).toBe('#threads');
    expect(hashForTab('settings')).toBe('#settings');
  });
});

describe('deep-link forms are richer variants of their tab', () => {
  it('#threads/<id> → the Threads tab on that row', () => {
    expect(routeFromHash('#threads/0191-abc')).toEqual({ tab: 'threads', threadId: '0191-abc', findingId: null, known: true });
    expect(routeFromHash(hashForThread('a/b c'))).toEqual({ tab: 'threads', threadId: 'a/b c', findingId: null, known: true });
    expect(hashForThread('a/b c')).toBe('#threads/a%2Fb%20c');
  });

  it('#contracts/<id> → the Contracts tab on that finding row (the CP findings index links here)', () => {
    expect(routeFromHash('#contracts/0191-def')).toEqual({ tab: 'contract', threadId: null, findingId: '0191-def', known: true });
    expect(routeFromHash('#contracts/0191%2Fx').findingId).toBe('0191/x');
    expect(routeFromHash(hashForFinding('0191/x'))).toEqual({ tab: 'contract', threadId: null, findingId: '0191/x', known: true });
  });

  it('a malformed %-sequence keeps the tab and drops the row, and never throws', () => {
    // applyHash runs in onMounted — an uncaught URIError there kills the whole dashboard.
    expect(routeFromHash('#threads/%')).toEqual({ tab: 'threads', threadId: null, findingId: null, known: true });
    expect(routeFromHash('#contracts/%E0%A4%A')).toEqual({ tab: 'contract', threadId: null, findingId: null, known: true });
  });

  it('the two deep links never claim each other, and a row id never lands under the wrong tab', () => {
    expect(routeFromHash('#threads/x').findingId).toBeNull();
    expect(routeFromHash('#contracts/x').threadId).toBeNull();
    // Only the two row-bearing tabs have a deep-link form.
    for (const h of ['#overview/x', '#traffic/x', '#settings/x']) {
      expect(routeFromHash(h), h).toEqual({ tab: DEFAULT_TAB, threadId: null, findingId: null, known: false });
    }
  });
});

describe('anything else falls back to Overview and is NOT known', () => {
  it('empty, missing, bare `#`, unknown words, wrong case, trailing junk', () => {
    for (const h of ['', '#', '#foo', '#Overview', '#OVERVIEW', '#overview ', '#overviews', '#threads-x', '#contract', undefined, null]) {
      expect(routeFromHash(h), String(h)).toEqual({ tab: DEFAULT_TAB, threadId: null, findingId: null, known: false });
    }
    expect(DEFAULT_TAB).toBe('overview');
  });

  it("the control plane's thread-link token fragment is never a tab", () => {
    // `/t/<id>#k=<token>` is the CP's thread page; the token lives only in that
    // fragment and is never pushed into history. A stray copy on this origin must
    // not read as a route (and `known: false` tells the app not to rewrite it).
    for (const h of ['#k=abc123', '#k=', '#o=xyz', '#k=abc#threads', '#threads#k=abc']) {
      const r = routeFromHash(h);
      expect(r.tab, h).toBe(DEFAULT_TAB);
      expect(r.known, h).toBe(false);
      expect(r.threadId, h).toBeNull();
      expect(r.findingId, h).toBeNull();
    }
    // …and nothing the app writes can ever look like one.
    for (const tab of TABS) expect(hashForTab(tab)).not.toMatch(/[#?&]k=/);
    expect(hashForThread('k=abc')).toBe('#threads/k%3Dabc');
  });
});

describe('isTab', () => {
  it('accepts exactly the five tab ids — the wire word `contracts` is not a tab id', () => {
    for (const tab of TABS) expect(isTab(tab)).toBe(true);
    for (const x of ['contracts', 'Overview', '', '#overview', 'k=abc', null, undefined, 1]) expect(isTab(x), String(x)).toBe(false);
  });
});
