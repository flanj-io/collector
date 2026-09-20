// The Edges section (Overview): pure logic for the two direction tables plus
// the Local MCP servers and Direction not recorded tables — App.vue renders
// what this module decides, and never re-derives it inline. Same split as
// coverage.ts / contracts.ts: a rule that changes twice a year belongs in one
// function with a test, not in a template expression nobody diffs.
//
// What lives here: which table a row belongs in, the one-word-plus-clause
// Status vocabulary (drift beats everything, MCP beats coverage), the NEW
// badge window, the cross-direction "also calls you" link, and the sort
// comparator that keeps a 5s poll from ever reordering a row whose data did
// not change. What does NOT live here: per-call coverage (coverage.ts already
// answers "did this call run against a contract" — this module only asks "is
// there at least one such call on this edge").

import { ADD_CONTRACT } from './contracts';
import { NOT_CHECKED_LABEL } from './coverage';

/* ── Copy (exact strings) ──────────────────────────────────────────────── */

export const OUTBOUND_CAPTION = 'Outbound — you call them';
export const INBOUND_CAPTION = 'Inbound — they call you';
export const LOCAL_MCP_CAPTION = 'Local MCP servers';
export const LOCAL_MCP_SUB = 'MCP servers this deployment runs as a subprocess — no network edge.';
export const UNKNOWN_DIRECTION_CAPTION = 'Direction not recorded';
export const UNKNOWN_DIRECTION_SUB = 'These rows arrived without a direction. They are shown so nothing is hidden.';

export const OUTBOUND_EMPTY =
  'Nothing outbound yet — no calls from your systems to an external service have been observed.';
export const INBOUND_EMPTY =
  'Nothing inbound yet — no external service has called your systems. If your systems only call out, this stays empty.';

export const ALSO_CALLS_YOU = 'also calls you';
export const YOU_ALSO_CALL_THEM = 'you also call them';
export const VIEW_CATALOGUE_LABEL = 'View catalogue';
export const NEW_LABEL = 'New';

export const CHECKED_LABEL = 'checked';
export const SELF_REPORTED_LABEL = 'self-reported';
export const MCP_STATUS_CLAUSE = 'tools/list';
export const NOTHING_VALIDATED_CLAUSE = 'nothing validated yet';
export const NO_SELF_CONTRACT_CLAUSE = 'no self contract';

/* ── The direction split ──────────────────────────────────────────────── */

/** The minimum shape a row needs to be placed by direction. Widened past the
 *  two known values deliberately: a row whose direction is neither `client`
 *  nor `server` must still render somewhere (the "Direction not recorded"
 *  table) rather than silently vanish once the view is a hard split. */
export interface DirectedEdge {
  peer_host: string;
  direction: string;
}

export function isOutbound(e: DirectedEdge): boolean {
  return e.direction === 'client';
}

export function isInbound(e: DirectedEdge): boolean {
  return e.direction === 'server';
}

export function isDirectionUnknown(e: DirectedEdge): boolean {
  return !isOutbound(e) && !isInbound(e);
}

/* ── Sorting ───────────────────────────────────────────────────────────── */

export type EdgeSortKey = 'name' | 'calls' | 'first' | 'last';
export type EdgeSortDir = 'asc' | 'desc';

export interface EdgeSortState {
  key: EdgeSortKey;
  dir: EdgeSortDir;
}

/** The minimum shape the sort comparator needs. */
export interface SortableEdge {
  peer_host: string;
  registrable_domain?: string;
  call_count: number;
  first_seen: string;
  last_seen: string;
}

const domainKey = (e: SortableEdge): string => e.registrable_domain || e.peer_host;

/** Today's stable order (registrable_domain || peer_host, then peer_host),
 *  kept as the sort's tiebreak so a 5s poll — which never changes which rows
 *  exist, only their numbers — can never reorder two rows whose sort key is
 *  unchanged. This is also the whole comparator for the default `name` sort. */
function byDomain(a: SortableEdge, b: SortableEdge): number {
  return domainKey(a).localeCompare(domainKey(b)) || a.peer_host.localeCompare(b.peer_host);
}

function primaryCompare(a: SortableEdge, b: SortableEdge, key: EdgeSortKey): number {
  switch (key) {
    case 'calls':
      return a.call_count - b.call_count;
    case 'first':
      return (a.first_seen || '').localeCompare(b.first_seen || '');
    case 'last':
      return (a.last_seen || '').localeCompare(b.last_seen || '');
    case 'name':
    default:
      return byDomain(a, b);
  }
}

/** Sort one table's rows under the one selection that drives every table on
 *  the page (one shared sort state, not one per table — two direction tables
 *  with independent sorts are two different views of one graph). `dir` flips
 *  the PRIMARY key only — the tiebreak always runs ascending, because it
 *  exists to keep row order stable under a poll, not to express a reader's
 *  choice. */
export function sortEdges<T extends SortableEdge>(edges: readonly T[], sort: EdgeSortState): T[] {
  const dirMul = sort.dir === 'desc' ? -1 : 1;
  const out = edges.slice();
  out.sort((a, b) => {
    const primary = primaryCompare(a, b, sort.key);
    if (primary !== 0) return primary * dirMul;
    if (sort.key === 'name') return 0; // byDomain IS the tiebreak; nothing left to break
    return byDomain(a, b);
  });
  return out;
}

/** `aria-sort` for one column's `<th>`: `ascending` / `descending` on the
 *  active column, `none` on every OTHER sortable column (never a missing
 *  attribute — a screen reader must be able to tell "not this one" from "this
 *  table has no sort at all"). Call only for columns that are sortable at
 *  all; `Status` carries no `aria-sort` attribute, handled by the caller. */
export function ariaSort(sort: EdgeSortState, key: EdgeSortKey): 'ascending' | 'descending' | 'none' {
  if (sort.key !== key) return 'none';
  return sort.dir === 'desc' ? 'descending' : 'ascending';
}

/** Clicking the active column flips its direction; clicking another column
 *  makes IT active at ascending. */
export function nextSort(sort: EdgeSortState, key: EdgeSortKey): EdgeSortState {
  if (sort.key === key) return { key, dir: sort.dir === 'asc' ? 'desc' : 'asc' };
  return { key, dir: 'asc' };
}

/* ── The NEW badge ────────────────────────────────────────────────────── */

export const NEW_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;

/** `first_seen` within 7 days of `now`. A `first_seen` that is somehow in the
 *  future (clock skew between this browser and the collector) is never NEW —
 *  a badge that means "just showed up" must not claim a row nobody has
 *  observed yet, however briefly the clocks disagree. */
export function isNew(firstSeen: string | undefined | null, now: number = Date.now()): boolean {
  if (!firstSeen) return false;
  const t = Date.parse(firstSeen);
  if (Number.isNaN(t)) return false;
  const diff = now - t;
  return diff >= 0 && diff < NEW_WINDOW_MS;
}

/* ── The cross-direction twin link ───────────────────────────────────────
 *
 * Matched on registrable domain, never on host — two hosts under one domain
 * still count as one counterparty. A row's ID is host-based (below), so when
 * several rows on the far side share the domain (two subdomains both calling
 * in) the link resolves to the first of them in the table's own default
 * order — deterministic, and the common case (one host each way) is never
 * ambiguous. */

export interface DomainedEdge {
  peer_host: string;
  registrable_domain?: string;
}

/** The first row on the OTHER side that shares this row's registrable
 *  domain, or undefined when there is none. */
export function twinEdge<T extends DomainedEdge>(e: DomainedEdge, others: readonly T[]): T | undefined {
  const domain = e.registrable_domain || e.peer_host;
  return others
    .filter((o) => (o.registrable_domain || o.peer_host) === domain)
    .sort((a, b) => a.peer_host.localeCompare(b.peer_host))[0];
}

/** `[a-z0-9]` runs joined by `-`; never empty (a host that slugs to nothing —
 *  practically unreachable — still gets a stable non-empty id). */
export function slugify(s: string): string {
  const slug = s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return slug || 'x';
}

/** `edge-out-api-acme-test` / `edge-in-webhooks-acme-test` — host-based, so
 *  it is always unique per row even when several hosts share a domain. */
export function edgeRowId(direction: 'client' | 'server', peerHost: string): string {
  return `edge-${direction === 'server' ? 'in' : 'out'}-${slugify(peerHost)}`;
}

/* ── The Status cell ──────────────────────────────────────────────────────
 *
 * One word (or one chip), then a muted clause. The ONLY chip in this column
 * is DRIFTED — every other state is quiet text, so scanning the column finds
 * exactly the rows that need attention instead of a wall of identical pills.
 * Resolution order, top to bottom: drift beats everything, MCP beats
 * coverage. `checked` is never claimed from the contract list alone — it
 * costs at least one call this window that the drift processor actually
 * validated (coverage.ts `callCoverage() === 'checked'`); a bound contract
 * that has validated nothing renders the true sentence, `not checked ·
 * nothing validated yet`. */

export type EdgeStatusKind = 'drift' | 'mcp' | 'checked' | 'not-checked' | 'unknown';

export interface EdgeStatusInput {
  direction: 'client' | 'server';
  driftCount: number;
  /** This host is an MCP server and nothing else (ui/src/contracts.ts `mcpOnlyHosts`). */
  isMcpOnly: boolean;
  /** Has `/api/contracts` answered at all? False on an older collector — the
   *  em dash, never a guessed "not checked". */
  contractsKnown: boolean;
  /** A contract is bound to this row: the host's provider contract (outbound)
   *  or a loaded self contract (inbound). */
  hasContract: boolean;
  /** The clause to show once `checked` — pre-built by the caller, because the
   *  two directions say it differently and always have: outbound reuses the
   *  existing rich per-row contract line (`contracts.ts` `edgeContractLine` —
   *  version, provenance and recency, unchanged from before this table
   *  existed), inbound is the new, simpler `self <version>`. Read only when
   *  the row turns out `checked`; ignored otherwise. */
  checkedClause: string;
  /** At least one call on this edge, in the loaded window, whose coverage
   *  verdict is `checked` (coverage.ts `callCoverage`). */
  hasValidatedCall: boolean;
}

export interface EdgeStatus {
  kind: EdgeStatusKind;
  /** The plain word before the clause. '' for `drift` — a chip replaces it. */
  word: string;
  /** The muted clause after the word. '' when there is none. */
  clause: string;
  /** The clause is a button routing into Contracts (`goToContracts`). */
  clauseLinksContracts: boolean;
  /** The clause uses the dotted-underline "add" register — Add REST contract
   *  only, never the drift or checked clauses. */
  clauseIsAdd: boolean;
}

export function edgeStatus(input: EdgeStatusInput): EdgeStatus {
  if (input.driftCount > 0) {
    return { kind: 'drift', word: '', clause: '', clauseLinksContracts: false, clauseIsAdd: false };
  }
  if (input.isMcpOnly) {
    return {
      kind: 'mcp',
      word: SELF_REPORTED_LABEL,
      clause: MCP_STATUS_CLAUSE,
      clauseLinksContracts: false,
      clauseIsAdd: false
    };
  }
  if (!input.contractsKnown) {
    return { kind: 'unknown', word: '—', clause: '', clauseLinksContracts: false, clauseIsAdd: false };
  }
  if (input.hasContract && input.hasValidatedCall) {
    return { kind: 'checked', word: CHECKED_LABEL, clause: input.checkedClause, clauseLinksContracts: true, clauseIsAdd: false };
  }
  if (input.direction === 'client' && !input.hasContract) {
    return { kind: 'not-checked', word: NOT_CHECKED_LABEL, clause: ADD_CONTRACT, clauseLinksContracts: true, clauseIsAdd: true };
  }
  if (input.hasContract) {
    return {
      kind: 'not-checked',
      word: NOT_CHECKED_LABEL,
      clause: NOTHING_VALIDATED_CLAUSE,
      clauseLinksContracts: false,
      clauseIsAdd: false
    };
  }
  return {
    kind: 'not-checked',
    word: NOT_CHECKED_LABEL,
    clause: NO_SELF_CONTRACT_CLAUSE,
    clauseLinksContracts: false,
    clauseIsAdd: false
  };
}

export function driftLabel(n: number): string {
  return `Drifted ×${n}`;
}

/** The drift chip's tooltip — the word matches what the row IS: a provider on
 *  an outbound row, a consumer on an inbound one, an edge when the direction
 *  itself was not recorded. */
export function driftChipTitle(n: number, direction: 'client' | 'server' | 'unknown'): string {
  const who = direction === 'server' ? 'consumer' : direction === 'client' ? 'provider' : 'edge';
  return `${n} drifted ${n === 1 ? 'call' : 'calls'} — open Contracts for this ${who}`;
}

/* ── Caption sub-lines ────────────────────────────────────────────────── */

export function pluralCount(n: number, singular: string, plural: string): string {
  return `${n} ${n === 1 ? singular : plural}`;
}

/** `5 providers · <rollCall text>` — the roll call (ui/src/contracts.ts
 *  `rollCall`) already covers the zero state (`ROLL_CALL_ZERO`), so this
 *  always concatenates rather than branching on it. */
export function outboundCaptionSub(count: number, rollCallText: string): string {
  return `${pluralCount(count, 'provider', 'providers')} · ${rollCallText}`;
}

/** `3 consumers · checked against the contract you publish (self, v2.1.0)`,
 *  or, with no self contract loaded, the honest alternative. `selfVersion` is
 *  `versionLabel(selfSpec.version)` when a self contract is loaded, else null —
 *  the null/non-null split IS whether a self contract is loaded, so the
 *  caller never passes both a version and "no contract" separately. */
export function inboundCaptionSub(count: number, selfVersion: string | null): string {
  const lead = pluralCount(count, 'consumer', 'consumers');
  return selfVersion
    ? `${lead} · checked against the contract you publish (self, ${selfVersion})`
    : `${lead} · no self contract loaded — inbound calls are captured, not validated`;
}
