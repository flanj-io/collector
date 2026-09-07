/**
 * The hash ↔ tab mapping — pure, so vitest can pin it (App.vue is invisible to
 * vitest; same reason headline.ts exists).
 *
 * Tab selection is the single source of truth in BOTH directions: every tab
 * change writes the hash it owns, and the hash drives the tab (load, reload,
 * hashchange on Back / Forward). Before this module the tab buttons wrote
 * nothing while two programmatic paths did, so the URL drifted from the screen:
 * Threads → "Add address" wrote `#settings`, clicking Overview wrote nothing,
 * and a reload landed on Settings.
 *
 * Wire forms:
 *   `#overview` `#traffic` `#contracts` `#threads` `#settings`   — one per tab
 *   `#threads/<thread_id>`     — the Threads tab opened on a row
 *   `#contracts/<finding_id>`  — the Contracts tab opened on a finding row (the
 *                                control plane's findings index links here)
 *
 * Anything else — an empty hash, an unknown word, a deep-link form under the
 * wrong tab, and in particular the control plane's `#k=<token>` thread-link
 * fragment — is NOT a tab: it parses to the default (Overview) with
 * `known: false`, and the app never rewrites such a fragment. The `#k=` rule
 * belongs to the CP's thread page (the token lives only in the fragment and is
 * never pushed into history); this module just makes sure a stray copy of it
 * can never be mistaken for a route.
 */
import { findingIdFromHash, threadIdFromHash } from './threads';

export type Tab = 'overview' | 'traffic' | 'contract' | 'threads' | 'settings';

export const TABS: readonly Tab[] = ['overview', 'traffic', 'contract', 'threads', 'settings'];

/** The default tab — where an empty or unknown hash lands. */
export const DEFAULT_TAB: Tab = 'overview';

/** The hash each tab writes. `contract` is spelled `#contracts` on the wire —
 *  the plural the CP's deep link (`#contracts/<finding_id>`) already uses. */
const HASH_BY_TAB: Readonly<Record<Tab, string>> = {
  overview: '#overview',
  traffic: '#traffic',
  contract: '#contracts',
  threads: '#threads',
  settings: '#settings'
};

export interface Route {
  tab: Tab;
  /** `#threads/<id>` → the row to highlight on the Threads tab; else null. */
  threadId: string | null;
  /** `#contracts/<id>` → the finding row to highlight on the Contracts tab; else null. */
  findingId: string | null;
  /** True when the hash named a tab (bare or deep-link form). False for an empty,
   *  unknown or token fragment — `tab` is then the default, and the caller must
   *  not rewrite the URL. */
  known: boolean;
}

export function isTab(x: unknown): x is Tab {
  return typeof x === 'string' && (TABS as readonly string[]).includes(x);
}

/** The hash a tab writes when selected. */
export function hashForTab(tab: Tab): string {
  return HASH_BY_TAB[tab];
}

/** The richer hash for a Threads row (`#threads/<id>`), id %-encoded. */
export function hashForThread(threadId: string): string {
  return HASH_BY_TAB.threads + '/' + encodeURIComponent(threadId);
}

/** The richer hash for a finding row (`#contracts/<id>`), id %-encoded. */
export function hashForFinding(findingId: string): string {
  return HASH_BY_TAB.contract + '/' + encodeURIComponent(findingId);
}

/** Parse `location.hash` into the tab it names. Never throws (a malformed
 *  %-sequence in a deep link yields the tab with a null id — the tab is still
 *  legible even when the row is not). */
export function routeFromHash(hash: string | null | undefined): Route {
  const h = hash || '';
  for (const tab of TABS) {
    if (h === HASH_BY_TAB[tab]) return { tab, threadId: null, findingId: null, known: true };
  }
  // Deep-link forms: `<tab hash>/<id>`. The id parsers are strict about their
  // own prefix, so `#threads/x` can never read as a contracts route or vice versa.
  if (h.startsWith(HASH_BY_TAB.threads + '/')) {
    return { tab: 'threads', threadId: threadIdFromHash(h), findingId: null, known: true };
  }
  if (h.startsWith(HASH_BY_TAB.contract + '/')) {
    return { tab: 'contract', threadId: null, findingId: findingIdFromHash(h), known: true };
  }
  return { tab: DEFAULT_TAB, threadId: null, findingId: null, known: false };
}
