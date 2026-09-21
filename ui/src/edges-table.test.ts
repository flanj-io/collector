// @vitest-environment happy-dom
//
// The Edges section (Overview), mounted: two real tables split by direction,
// the Status column's one chip, the naming control living on the name line,
// the cross-direction link, and the one-sided empty state. The pure rules
// (sort, NEW window, status resolution) are covered directly in
// edges-view.test.ts; this file is about what actually renders when App.vue
// puts them together.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { START_THREAD_LABEL, REMOVE_NAME_LABEL } from './edge-names';
import { INBOUND_CAPTION, INBOUND_DRIFT_CLAUSE, INBOUND_EMPTY, OUTBOUND_CAPTION, OUTBOUND_EMPTY, ALSO_CALLS_YOU } from './edges-view';

const HEALTH = { status: 'ok', connect_status: 'disconnected' };

const NOW = new Date('2026-09-20T12:00:00Z');
const RECENT = '2026-09-18T00:00:00Z'; // within the 7-day NEW window

const EDGES = [
  {
    peer_host: 'api.globex.test',
    registrable_domain: 'globex.test',
    direction: 'client',
    role: 'consumer',
    class: 'external',
    display_name: 'Globex FX',
    name_source: 'user',
    first_seen: '2026-08-02T00:00:00Z',
    last_seen: '2026-09-20T00:02:00Z',
    call_count: 12408,
    drift_count: 0
  },
  {
    peer_host: 'api.initech.test',
    registrable_domain: 'initech.test',
    direction: 'client',
    role: 'consumer',
    class: 'external',
    first_seen: '2026-07-14T00:00:00Z',
    last_seen: '2026-09-20T00:06:00Z',
    call_count: 3120,
    drift_count: 3
  },
  // Present in both directions — the twin link.
  {
    peer_host: 'api.acme.test',
    registrable_domain: 'acme.test',
    direction: 'client',
    role: 'consumer',
    class: 'external',
    display_name: 'Acme Payments',
    name_source: 'user',
    first_seen: RECENT,
    last_seen: '2026-09-20T00:01:00Z',
    call_count: 7701,
    drift_count: 0
  },
  {
    peer_host: 'webhooks.acme.test',
    registrable_domain: 'acme.test',
    direction: 'server',
    role: 'provider',
    class: 'external',
    first_seen: '2026-09-01T00:00:00Z',
    last_seen: '2026-09-20T00:09:00Z',
    call_count: 1880,
    drift_count: 0
  },
  // An INBOUND edge with drift: this org's own responses departed from the contract it publishes.
  {
    peer_host: 'gw.consumer-b.test',
    registrable_domain: 'consumer-b.test',
    direction: 'server',
    role: 'provider',
    class: 'external',
    first_seen: '2026-08-19T00:00:00Z',
    last_seen: '2026-09-20T00:30:00Z',
    call_count: 204,
    drift_count: 2
  }
];

const CONTRACTS = [
  {
    integration: 'api-globex-test',
    role: 'provider',
    format: 'openapi',
    peer_host: 'api.globex.test',
    title: 'Globex FX API',
    version: '2.4.0',
    endpoints: 4,
    loaded_at: '2026-08-02T00:00:00Z',
    source: 'upload',
    edge_class: 'external'
  },
  // A local-process MCP server: its own table (no Calls / First seen data at
  // all — that table's grid still has to share the other tables' column x).
  {
    integration: 'acme-tools-mcp',
    role: 'provider',
    format: 'mcp',
    peer_host: null,
    title: 'acme-tools-mcp',
    version: null,
    endpoints: 3,
    loaded_at: '2026-09-21T00:00:00Z',
    source: 'observed',
    edge_class: 'local-process'
  }
];

// An edge with neither 'client' nor 'server' direction — renders in its own
// "Unknown direction" table, which (like Inbound) carries no per-row action.
const UNKNOWN_DIR_EDGE = {
  peer_host: 'relay.unknown-dir.test',
  registrable_domain: 'unknown-dir.test',
  direction: 'unknown',
  role: 'provider',
  class: 'external',
  first_seen: '2026-09-10T00:00:00Z',
  last_seen: '2026-09-20T00:15:00Z',
  call_count: 40,
  drift_count: 0
};

const CALLS = [
  {
    id: 'c-globex-1',
    captured_at: '2026-09-20T00:02:00Z',
    integration: 'api-globex-test',
    method: 'GET',
    route: '/v1/rates',
    status_code: 200,
    request_body: '',
    response_body: '',
    redaction: { applied: false, patterns: [], spec_aware: false },
    correlation: {},
    direction: 'client',
    peer_host: 'api.globex.test',
    validated: 'clean'
  }
];

const CONNECT = { status: 'disconnected' };

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubFetch(edges: unknown[] = EDGES) {
  const bodies: Record<string, unknown> = {
    '/api/health': HEALTH,
    '/api/findings': { findings: [] },
    '/api/calls': { calls: CALLS },
    '/api/edges': { edges },
    '/api/contracts': { contracts: CONTRACTS },
    '/api/connect': CONNECT,
    '/api/threads': { threads: [], total: 0, has_more: false }
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

let wrapper: VueWrapper | null = null;

async function settle(w: VueWrapper) {
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
}

async function mountApp(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  await settle(w);
  return w;
}

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '';
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the Edges section renders two real tables, split by direction', () => {
  it('captions the two tables distinctly, each a real <table> with a <caption>', async () => {
    stubFetch();
    const w = await mountApp();
    const captions = w.findAll('.edges-cap-title').map((c) => c.text());
    expect(captions).toContain(OUTBOUND_CAPTION);
    expect(captions).toContain(INBOUND_CAPTION);
    // Outbound first — it answers "who do we depend on".
    expect(captions.indexOf(OUTBOUND_CAPTION)).toBeLessThan(captions.indexOf(INBOUND_CAPTION));
    for (const table of w.findAll('table.edges-table')) {
      expect(table.find('caption').exists()).toBe(true);
    }
  });

  it('gives every header cell a scope and every counterparty cell scope="row"', async () => {
    stubFetch();
    const w = await mountApp();
    for (const table of w.findAll('table.edges-table')) {
      for (const th of table.findAll('thead th')) expect(th.attributes('scope')).toBe('col');
      for (const th of table.findAll('tbody th')) expect(th.attributes('scope')).toBe('row');
    }
  });

  it('an inbound row carries no Actions cell — Start a thread is outbound only', async () => {
    stubFetch();
    const w = await mountApp();
    const inboundRow = w.find('#edge-in-webhooks-acme-test');
    expect(inboundRow.exists()).toBe(true);
    expect(inboundRow.text()).not.toContain(START_THREAD_LABEL);
    expect(inboundRow.find('.cell-actions').exists()).toBe(false);

    const outboundRow = w.find('#edge-out-api-globex-test');
    expect(outboundRow.find('.cell-actions').exists()).toBe(true);
    expect(outboundRow.text()).toContain(START_THREAD_LABEL);
  });

  it('an INBOUND drifted row says whose drift it is — your responses, never the consumer’s', async () => {
    stubFetch();
    const w = await mountApp();
    const row = w.find('#edge-in-gw-consumer-b-test');
    expect(row.exists()).toBe(true);
    const chip = row.find('.edge-chip.drift');
    expect(chip.exists()).toBe(true);
    // Visible, not only a tooltip: a title is invisible on touch and to anyone who does not hover.
    expect(row.find('.cell-status').text()).toContain(INBOUND_DRIFT_CLAUSE);
    expect(chip.attributes('title')).toBe('2 of your responses to this consumer drifted from the contract you publish — open Contracts');
    // …and the OUTBOUND drifted row carries no such clause: there it IS the provider that drifted.
    expect(w.find('#edge-out-api-initech-test .cell-status').text()).not.toContain(INBOUND_DRIFT_CLAUSE);
  });

  it('the DRIFTED chip is the row’s only chip and routes to Contracts', async () => {
    stubFetch();
    const w = await mountApp();
    const row = w.find('#edge-out-api-initech-test');
    expect(row.classes()).toContain('drift');
    const chip = row.find('button.edge-chip.drift');
    expect(chip.exists()).toBe(true);
    expect(chip.text()).toBe('Drifted ×3');
    expect(chip.attributes('title')).toContain('open Contracts for this provider');

    await chip.trigger('click');
    await settle(w);
    expect(window.location.hash).toBe('#contracts');
  });

  it('a quiet, unchecked row shows plain text — no chip at all', async () => {
    stubFetch();
    const w = await mountApp();
    const row = w.find('#edge-in-webhooks-acme-test');
    expect(row.find('.edge-chip').exists()).toBe(false);
    expect(row.find('.edge-status-word').text()).toBe('not checked');
  });

  it('a checked row shows the checked word and its contract clause, as a link into Contracts', async () => {
    stubFetch();
    const w = await mountApp();
    const row = w.find('#edge-out-api-globex-test');
    expect(row.find('.edge-status-word').text()).toBe('checked');
    const clause = row.find('.cell-status .edge-contract-link');
    expect(clause.exists()).toBe(true);
    expect(clause.text()).toContain('v2.4.0');

    await clause.trigger('click');
    await settle(w);
    expect(window.location.hash).toBe('#contracts');
  });

  it('the naming control sits in the actions cell, beside Start a thread, and opens the existing inline editor', async () => {
    stubFetch();
    const w = await mountApp();
    const row = w.find('#edge-out-api-globex-test');
    // Rename lives in the actions cell, one deliberate place beside the row's
    // other action — never glued inline after the name (which used to render
    // "name [MCP] Rename" run together on the name line).
    const editBtn = row.find('.cell-actions .edge-name-edit');
    expect(editBtn.exists()).toBe(true);
    expect(editBtn.text()).toBe('Edit name'); // name_source: 'user'
    expect(row.find('.cell-name').text()).not.toContain('Edit name');
    expect(row.find('.cell-actions').text()).toContain(START_THREAD_LABEL);

    await editBtn.trigger('click');
    await settle(w);
    const editor = w.find('.edge-edit-row .edge-rename');
    expect(editor.exists()).toBe(true);
    expect((editor.find('.edge-rename-input').element as HTMLInputElement).value).toBe('Globex FX');
    // Remove name now lives inside the editor, not as a permanent row button.
    const removeBtn = editor.findAll('button').find((b) => b.text() === REMOVE_NAME_LABEL);
    expect(removeBtn?.exists()).toBe(true);
  });

  it('a NEW badge marks a row first seen within 7 days, and not an older one', async () => {
    stubFetch();
    const w = await mountApp();
    expect(w.find('#edge-out-api-acme-test .edge-new').exists()).toBe(true);
    expect(w.find('#edge-out-api-globex-test .edge-new').exists()).toBe(false);
  });

  it('a counterparty in both directions gets a twin link, not a merged row', async () => {
    stubFetch();
    const w = await mountApp();
    const outboundRow = w.find('#edge-out-api-acme-test');
    const also = outboundRow.find('a.edge-also');
    expect(also.exists()).toBe(true);
    expect(also.text()).toBe(ALSO_CALLS_YOU);
    expect(also.attributes('href')).toBe('#edge-in-webhooks-acme-test');
    // Still two rows, not one.
    expect(w.find('#edge-out-api-acme-test').exists()).toBe(true);
    expect(w.find('#edge-in-webhooks-acme-test').exists()).toBe(true);
  });

  it('one-sided data renders the stated empty-state copy for the missing direction', async () => {
    // Only an outbound edge exists — inbound has nothing at all.
    stubFetch([EDGES[0]]);
    const w = await mountApp();
    expect(w.text()).toContain(OUTBOUND_CAPTION);
    expect(w.text()).toContain(INBOUND_CAPTION);
    const inboundTable = w
      .findAll('table.edges-table')
      .find((t) => t.find('.edges-cap-title').text() === INBOUND_CAPTION)!;
    expect(inboundTable.text()).toContain(INBOUND_EMPTY);
    expect(w.find('#edge-out-api-globex-test').exists()).toBe(true);
    expect(w.find('.edges-empty').exists()).toBe(true);
  });

  it('sortable headers carry aria-sort, and Status carries none', async () => {
    stubFetch();
    const w = await mountApp();
    const outboundTable = w
      .findAll('table.edges-table')
      .find((t) => t.find('.edges-cap-title').text() === OUTBOUND_CAPTION)!;
    const headers = outboundTable.findAll('thead th');
    const byText = (label: string) => headers.find((h) => h.text().startsWith(label))!;
    expect(byText('Counterparty').attributes('aria-sort')).toBe('ascending');
    expect(byText('Calls').attributes('aria-sort')).toBe('none');
    expect(byText('Status').attributes('aria-sort')).toBeUndefined();

    await byText('Calls').find('button.th-sort').trigger('click');
    await settle(w);
    expect(byText('Calls').attributes('aria-sort')).toBe('ascending');
    expect(byText('Counterparty').attributes('aria-sort')).toBe('none');
  });

  it('every stacked table shares one column grid — Outbound, Inbound, Local MCP servers, Unknown direction', async () => {
    // Local MCP servers (no Calls / First seen data) and Unknown direction
    // (no per-row action) are both present here, alongside Outbound and
    // Inbound, so every stacked table renders at once.
    stubFetch([...EDGES, UNKNOWN_DIR_EDGE]);
    const w = await mountApp();
    const tables = w.findAll('table.edges-table');
    // All four tables this fixture produces, or the fixture stopped
    // exercising the shape this test is pinning.
    expect(tables.length).toBe(4);

    const gridOf = (t: (typeof tables)[number]) =>
      t.findAll('colgroup col').map((c) => c.classes().find((cls) => cls.startsWith('col-')));

    const grids = tables.map(gridOf);
    // Every table declares the exact same six columns, in the exact same
    // order — name, status, calls, first seen, last seen, actions — whether
    // or not that table has data (or even a header) for all of them. A table
    // that dropped a column here would shift every column after it out of
    // the shared grid the other tables use.
    const expectedGrid = ['col-name', 'col-status', 'col-calls', 'col-first', 'col-last', 'col-actions'];
    for (const grid of grids) expect(grid).toEqual(expectedGrid);

    // A column that has no data for a given table (Local MCP's Calls / First
    // seen; Inbound's and Unknown's actions) still occupies a real cell in
    // every row and in the header, empty rather than omitted — that is what
    // keeps the grid shared instead of merely coincidentally the same width.
    for (const table of tables) {
      const headerCount = table.findAll('thead th').length;
      expect(headerCount).toBe(6);
      for (const row of table.findAll('tbody tr.edge-row')) {
        expect(row.findAll('th, td').length).toBe(6);
      }
    }
  });

  it('a status secondary line never starts with the bullet separator', async () => {
    // Covers every row shape that carries a secondary status line: a
    // checked/contract-linked row, an unchecked row (Add REST contract), an
    // inbound drifted row, and a Local MCP row's tools/list clause.
    stubFetch([...EDGES]);
    const w = await mountApp();
    const secondaryLines = w.findAll('.edge-status-clause, .edge-contract-link');
    expect(secondaryLines.length).toBeGreaterThan(0);
    for (const line of secondaryLines) {
      const text = line.text();
      expect(text.startsWith('·')).toBe(false);
    }
  });
});
