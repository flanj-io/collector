import { describe, it, expect } from 'vitest';
import {
  ariaSort,
  driftChipTitle,
  driftLabel,
  edgeRowId,
  edgeStatus,
  inboundCaptionSub,
  isDirectionUnknown,
  isInbound,
  isNew,
  isOutbound,
  nextSort,
  outboundCaptionSub,
  pluralCount,
  slugify,
  sortEdges,
  twinEdge,
  type EdgeSortState,
  type SortableEdge,
  newBadgeIsInformative
} from './edges-view';

const edge = (over: Partial<SortableEdge> & { peer_host: string }): SortableEdge => ({
  registrable_domain: over.peer_host,
  call_count: 0,
  first_seen: '2026-09-01T00:00:00Z',
  last_seen: '2026-09-01T00:00:00Z',
  ...over
});

describe('the direction split', () => {
  it('client is outbound, server is inbound, anything else is unknown', () => {
    expect(isOutbound({ peer_host: 'a', direction: 'client' })).toBe(true);
    expect(isInbound({ peer_host: 'a', direction: 'client' })).toBe(false);
    expect(isOutbound({ peer_host: 'a', direction: 'server' })).toBe(false);
    expect(isInbound({ peer_host: 'a', direction: 'server' })).toBe(true);
    expect(isDirectionUnknown({ peer_host: 'a', direction: 'client' })).toBe(false);
    expect(isDirectionUnknown({ peer_host: 'a', direction: 'server' })).toBe(false);
    // A row whose direction is neither known value must still be placeable —
    // this is what keeps it from vanishing once the view is a hard split.
    expect(isDirectionUnknown({ peer_host: 'a', direction: '' })).toBe(true);
    expect(isDirectionUnknown({ peer_host: 'a', direction: 'peer' })).toBe(true);
  });
});

describe('sortEdges', () => {
  const rows: SortableEdge[] = [
    edge({ peer_host: 'zulu.test', call_count: 5, first_seen: '2026-09-03T00:00:00Z', last_seen: '2026-09-10T00:00:00Z' }),
    edge({ peer_host: 'alpha.test', call_count: 50, first_seen: '2026-09-01T00:00:00Z', last_seen: '2026-09-09T00:00:00Z' }),
    edge({ peer_host: 'mid.test', call_count: 20, first_seen: '2026-09-02T00:00:00Z', last_seen: '2026-09-11T00:00:00Z' })
  ];

  it('defaults to name ascending — the stable registrable-domain order', () => {
    const sort: EdgeSortState = { key: 'name', dir: 'asc' };
    expect(sortEdges(rows, sort).map((e) => e.peer_host)).toEqual(['alpha.test', 'mid.test', 'zulu.test']);
  });

  it('name descending reverses it', () => {
    const sort: EdgeSortState = { key: 'name', dir: 'desc' };
    expect(sortEdges(rows, sort).map((e) => e.peer_host)).toEqual(['zulu.test', 'mid.test', 'alpha.test']);
  });

  it('sorts by calls, first seen and last seen, each direction', () => {
    expect(sortEdges(rows, { key: 'calls', dir: 'asc' }).map((e) => e.peer_host)).toEqual(['zulu.test', 'mid.test', 'alpha.test']);
    expect(sortEdges(rows, { key: 'calls', dir: 'desc' }).map((e) => e.peer_host)).toEqual(['alpha.test', 'mid.test', 'zulu.test']);
    expect(sortEdges(rows, { key: 'first', dir: 'asc' }).map((e) => e.peer_host)).toEqual(['alpha.test', 'mid.test', 'zulu.test']);
    expect(sortEdges(rows, { key: 'last', dir: 'desc' }).map((e) => e.peer_host)).toEqual(['mid.test', 'zulu.test', 'alpha.test']);
  });

  it('ties fall back to the stable name order regardless of the primary direction — a 5s poll can never reorder an unchanged pair', () => {
    const tied: SortableEdge[] = [
      edge({ peer_host: 'zulu.test', call_count: 10 }),
      edge({ peer_host: 'alpha.test', call_count: 10 })
    ];
    expect(sortEdges(tied, { key: 'calls', dir: 'asc' }).map((e) => e.peer_host)).toEqual(['alpha.test', 'zulu.test']);
    expect(sortEdges(tied, { key: 'calls', dir: 'desc' }).map((e) => e.peer_host)).toEqual(['alpha.test', 'zulu.test']);
  });

  it('does not mutate its input', () => {
    const before = rows.map((e) => e.peer_host);
    sortEdges(rows, { key: 'calls', dir: 'desc' });
    expect(rows.map((e) => e.peer_host)).toEqual(before);
  });
});

describe('ariaSort / nextSort', () => {
  it('the active column carries its direction, every other sortable column carries none', () => {
    const sort: EdgeSortState = { key: 'calls', dir: 'desc' };
    expect(ariaSort(sort, 'calls')).toBe('descending');
    expect(ariaSort(sort, 'name')).toBe('none');
    expect(ariaSort(sort, 'first')).toBe('none');
    expect(ariaSort(sort, 'last')).toBe('none');
  });

  it('clicking the active column flips it; clicking another makes it active ascending', () => {
    expect(nextSort({ key: 'name', dir: 'asc' }, 'name')).toEqual({ key: 'name', dir: 'desc' });
    expect(nextSort({ key: 'name', dir: 'desc' }, 'name')).toEqual({ key: 'name', dir: 'asc' });
    expect(nextSort({ key: 'name', dir: 'desc' }, 'calls')).toEqual({ key: 'calls', dir: 'asc' });
  });
});

describe('isNew — the 7-day window', () => {
  const NOW = Date.parse('2026-09-20T12:00:00Z');

  it('is new just inside the window, and at the instant of first_seen', () => {
    expect(isNew('2026-09-20T12:00:00Z', NOW)).toBe(true);
    expect(isNew('2026-09-14T12:00:01Z', NOW)).toBe(true);
  });

  it('is not new exactly at 7 days, or past it', () => {
    expect(isNew('2026-09-13T12:00:00Z', NOW)).toBe(false);
    expect(isNew('2026-08-02T00:00:00Z', NOW)).toBe(false);
  });

  it('a future first_seen (clock skew) is never new', () => {
    expect(isNew('2026-09-21T00:00:00Z', NOW)).toBe(false);
  });

  it('is honest about what it cannot answer', () => {
    expect(isNew(undefined, NOW)).toBe(false);
    expect(isNew(null, NOW)).toBe(false);
    expect(isNew('', NOW)).toBe(false);
    expect(isNew('not a date', NOW)).toBe(false);
  });
});

describe('twinEdge — the cross-direction link', () => {
  const inbound = [
    { peer_host: 'webhooks.acme.test', registrable_domain: 'acme.test' },
    { peer_host: 'events.acme.test', registrable_domain: 'acme.test' },
    { peer_host: 'gw.other.test', registrable_domain: 'other.test' }
  ];

  it('matches on registrable domain, never on host', () => {
    const twin = twinEdge({ peer_host: 'api.acme.test', registrable_domain: 'acme.test' }, inbound);
    // Two inbound hosts share the domain — the first in the table's own
    // (peer_host-ascending) order wins, deterministically.
    expect(twin?.peer_host).toBe('events.acme.test');
  });

  it('is undefined when the domain has no row on the other side', () => {
    expect(twinEdge({ peer_host: 'api.nobody.test', registrable_domain: 'nobody.test' }, inbound)).toBeUndefined();
  });

  it('falls back to the bare host as the domain when none is given', () => {
    const twin = twinEdge({ peer_host: 'other.test' }, inbound);
    expect(twin?.peer_host).toBe('gw.other.test');
  });
});

describe('slugify / edgeRowId', () => {
  it('lowercases and joins non-alphanumeric runs with a single dash', () => {
    expect(slugify('api.acme.test')).toBe('api-acme-test');
    expect(slugify('Acme_Payments!!Test')).toBe('acme-payments-test');
    expect(slugify('---')).toBe('x');
    expect(slugify('')).toBe('x');
  });

  it('builds a direction-prefixed, host-based id', () => {
    expect(edgeRowId('client', 'api.acme.test')).toBe('edge-out-api-acme-test');
    expect(edgeRowId('server', 'webhooks.acme.test')).toBe('edge-in-webhooks-acme-test');
  });
});

describe('edgeStatus — resolution order and every branch', () => {
  const base = {
    direction: 'client' as const,
    driftCount: 0,
    isMcpOnly: false,
    contractsKnown: true,
    hasContract: false,
    checkedClause: '',
    hasValidatedCall: false
  };

  it('drift beats everything, including MCP and contractsKnown', () => {
    const s = edgeStatus({ ...base, driftCount: 3, isMcpOnly: true, contractsKnown: false });
    expect(s.kind).toBe('drift');
    expect(driftLabel(3)).toBe('Drifted ×3');
  });

  it('MCP self-reported beats coverage, even with a validated call on record', () => {
    const s = edgeStatus({ ...base, isMcpOnly: true, hasContract: true, hasValidatedCall: true });
    expect(s.kind).toBe('mcp');
    expect(s.word).toBe('self-reported');
    expect(s.clause).toBe('tools/list');
    expect(s.clauseLinksContracts).toBe(false);
  });

  it('an older collector with no /api/contracts answer renders the em dash', () => {
    const s = edgeStatus({ ...base, contractsKnown: false });
    expect(s.kind).toBe('unknown');
    expect(s.word).toBe('—');
  });

  it('checked requires a bound contract AND a validated call — never the contract list alone', () => {
    const boundOnly = edgeStatus({ ...base, hasContract: true, hasValidatedCall: false, checkedClause: 'v1.4.0' });
    expect(boundOnly.kind).toBe('not-checked');
    expect(boundOnly.clause).toBe('nothing validated yet');

    const both = edgeStatus({ ...base, hasContract: true, hasValidatedCall: true, checkedClause: 'v1.4.0' });
    expect(both.kind).toBe('checked');
    expect(both.word).toBe('checked');
    expect(both.clause).toBe('v1.4.0');
    expect(both.clauseLinksContracts).toBe(true);
  });

  it('checked renders exactly the clause the caller built — outbound and inbound format it differently', () => {
    const s = edgeStatus({ ...base, direction: 'server', hasContract: true, hasValidatedCall: true, checkedClause: 'self v2.1.0' });
    expect(s.clause).toBe('self v2.1.0');
  });

  it('outbound with no contract bound offers Add REST contract', () => {
    const s = edgeStatus({ ...base, direction: 'client', hasContract: false });
    expect(s.kind).toBe('not-checked');
    expect(s.clause).toBe('Add REST contract');
    expect(s.clauseLinksContracts).toBe(true);
    expect(s.clauseIsAdd).toBe(true);
  });

  it('inbound with no self contract says so, and is not a link', () => {
    const s = edgeStatus({ ...base, direction: 'server', hasContract: false });
    expect(s.kind).toBe('not-checked');
    expect(s.clause).toBe('no self contract');
    expect(s.clauseLinksContracts).toBe(false);
    expect(s.clauseIsAdd).toBe(false);
  });

  it('drift chip title names the row for what it is', () => {
    expect(driftChipTitle(1, 'client')).toBe('1 drifted call — open Contracts for this provider');
    expect(driftChipTitle(3, 'client')).toBe('3 drifted calls — open Contracts for this provider');
    // Inbound drift is THIS org's own response departing from the contract it publishes — never
    // the consumer's doing — and the title says whose it is.
    expect(driftChipTitle(2, 'server')).toBe('2 of your responses to this consumer drifted from the contract you publish — open Contracts');
    expect(driftChipTitle(1, 'server')).toBe('1 of your responses to this consumer drifted from the contract you publish — open Contracts');
    expect(driftChipTitle(1, 'unknown')).toBe('1 drifted call — open Contracts for this edge');
  });
});

describe('caption sub-lines', () => {
  it('pluralizes correctly at 0, 1 and many', () => {
    expect(pluralCount(0, 'provider', 'providers')).toBe('0 providers');
    expect(pluralCount(1, 'provider', 'providers')).toBe('1 provider');
    expect(pluralCount(5, 'provider', 'providers')).toBe('5 providers');
  });

  it('outbound always concatenates the count with the roll call text', () => {
    expect(outboundCaptionSub(5, '2 of 4 providers checked against a contract')).toBe(
      '5 providers · 2 of 4 providers checked against a contract'
    );
    expect(outboundCaptionSub(0, 'No providers checked against a contract yet — upload one to start drift detection on it.')).toBe(
      '0 providers · No providers checked against a contract yet — upload one to start drift detection on it.'
    );
  });

  it('inbound branches on whether a self contract is loaded', () => {
    expect(inboundCaptionSub(3, 'v2.1.0')).toBe('3 consumers · checked against the contract you publish (self, v2.1.0)');
    expect(inboundCaptionSub(1, null)).toBe('1 consumer · no self contract loaded — inbound calls are captured, not validated');
  });
});

describe('newBadgeIsInformative', () => {
  const NOW = Date.parse('2026-09-20T12:00:00.000Z');
  const daysAgo = (d: number) => new Date(NOW - d * 24 * 60 * 60 * 1000).toISOString();

  it('is false while every edge is inside the window — a first-week deployment draws no badge at all', () => {
    expect(newBadgeIsInformative([{ first_seen: daysAgo(1) }, { first_seen: daysAgo(6) }], NOW)).toBe(false);
  });

  it('turns true as soon as one edge is older than the window', () => {
    expect(newBadgeIsInformative([{ first_seen: daysAgo(1) }, { first_seen: daysAgo(8) }], NOW)).toBe(true);
  });

  it('is false for no edges at all', () => {
    expect(newBadgeIsInformative([], NOW)).toBe(false);
  });
});
