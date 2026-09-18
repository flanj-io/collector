// @vitest-environment happy-dom
//
// Regression for collector#92 (commit a450abb, "fetch a provider's published
// spec from a URL"): the fetched-source `<p v-if="hasFetchedSource(p.spec)">`
// landed directly ahead of `<p v-else-if="mcpHosts.has(p.peerHost)">` /
// `<p v-else>`, on the SAME line those two used to chain off
// `<div v-if="p.spec" class="prov-links">`. Vue attaches v-else-if/v-else to
// the immediately preceding v-if sibling, so the no-spec lines silently
// re-chained off `hasFetchedSource(p.spec)` instead of `p.spec`.
//
// Confirmed by mounting the app (this file): an uploaded OpenAPI card showed
// the "no contract" line right under its own bound contract, and an MCP card
// with an observed tools/list snapshot showed "No spec file needed" — a line
// that is supposed to mean there is no spec at all. Asserted here on the real
// card, like the honesty walk's own defects next door — a pure helper cannot
// see a v-else-if attach to the wrong sibling.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { NO_CONTRACT_ROW } from './contracts';
import { MCP_NO_SPEC_NEEDED } from './mcp';

const HOST_UPLOADED = 'api.acme.test';
const HOST_MCP = 'mcp.acme.test';
const ORPHAN_INTEGRATION = 'legacy-orphan';

// (a) An uploaded OpenAPI contract — bound to a host, never fetched from a URL.
const CONTRACT_UPLOADED = {
  integration: 'api-acme-test',
  role: 'provider',
  peer_host: HOST_UPLOADED,
  format: 'openapi',
  title: 'Acme Payments',
  version: '1.0.0',
  endpoints: 3,
  loaded_at: '2026-09-02T12:00:00Z',
  edge_class: 'external',
  source: 'upload'
};

// (b) An MCP server whose contract is its own observed tools/list snapshot —
// bound, just not by upload or fetch.
const CONTRACT_MCP_OBSERVED = {
  integration: 'acme-tools',
  role: 'provider',
  peer_host: HOST_MCP,
  format: 'mcp',
  title: 'acme-tools-mcp',
  endpoints: 12,
  loaded_at: '2026-09-08T10:00:00Z',
  edge_class: 'external',
  source: 'observed'
};

// (c) A provider with genuinely NO contract: an old finding whose integration
// matches no current contract, so it keeps its own card with `spec: null`
// (App.vue's `leftover` path) — the only route to a real "no contract" card.
const ORPHAN_FINDING = {
  id: 'f-orphan-1',
  kind: 'live-vs-spec',
  severity: 'breaking',
  integration: ORPHAN_INTEGRATION,
  endpoint: 'GET /v1/old-thing',
  expected: 'string',
  actual: 'number',
  rule: 'type',
  peer_host: 'orphan.example.test'
};

const HEALTH = {
  status: 'ok',
  window_rows: 0,
  calls: 0,
  findings: 1,
  cp_configured: false,
  connect_status: 'disconnected',
  collector_version: 'v0.0.0-test'
};

const EDGES = [
  {
    peer_host: HOST_UPLOADED,
    direction: 'client',
    role: 'provider',
    class: 'external',
    first_seen: '2026-09-02T11:00:00Z',
    last_seen: '2026-09-02T11:01:00Z',
    call_count: 2,
    drift_count: 0
  },
  {
    peer_host: HOST_MCP,
    direction: 'client',
    role: 'provider',
    class: 'external',
    first_seen: '2026-09-08T09:00:00Z',
    last_seen: '2026-09-08T09:01:00Z',
    call_count: 4,
    drift_count: 0
  }
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      if (path === '/api/contracts/spec') {
        return new Response('{"tools":[]}', { status: 200, headers: { 'content-type': 'application/json' } });
      }
      const bodies: Record<string, unknown> = {
        '/api/health': HEALTH,
        '/api/findings': { findings: [ORPHAN_FINDING] },
        '/api/calls': { calls: [] },
        '/api/edges': { edges: EDGES },
        '/api/contracts': { contracts: [CONTRACT_UPLOADED, CONTRACT_MCP_OBSERVED] },
        '/api/connect': { status: 'disconnected' },
        '/api/threads': { threads: [], total: 0, has_more: false }
      };
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '';
  vi.useFakeTimers();
  stubFetch();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mountContracts(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
  const tab = w.findAll('button').find((b) => b.text().trim().startsWith('Contracts'));
  if (!tab) throw new Error('the Contracts tab is not on screen');
  await tab.trigger('click');
  await w.vm.$nextTick();
  return w;
}

/** The one `article.provider` whose header names `label`. Every card in this
 *  fixture set has a distinct name, so a text match is unambiguous. */
function findCard(w: VueWrapper, label: string) {
  const card = w.findAll('article.provider').find((a) => a.find('.prov-name').text() === label);
  if (!card) throw new Error(`no provider card named "${label}"`);
  return card;
}

describe('the provider card fetched-source / no-spec chain', () => {
  it('shows only its bound-contract links for an uploaded OpenAPI contract, never the no-contract line', async () => {
    const w = await mountContracts();
    const card = findCard(w, 'Acme Payments');

    expect(card.find('.prov-links').exists()).toBe(true);
    // Never fetched from a URL — no provenance line.
    expect(card.find('.prov-source').exists()).toBe(false);
    // Has a bound contract — never the "no contract" lines.
    expect(card.find('.prov-nospec').exists()).toBe(false);
    expect(card.text()).not.toContain(NO_CONTRACT_ROW);
    expect(card.text()).not.toContain(MCP_NO_SPEC_NEEDED);
  });

  it('shows no "no spec needed" line for an MCP card with an observed snapshot', async () => {
    const w = await mountContracts();
    const card = findCard(w, 'acme-tools-mcp');

    // Has a bound contract (the observed snapshot) — never the no-spec lines.
    expect(card.find('.prov-nospec').exists()).toBe(false);
    expect(card.text()).not.toContain(MCP_NO_SPEC_NEEDED);
    expect(card.text()).not.toContain(NO_CONTRACT_ROW);
    // Bound by observation, not by URL fetch.
    expect(card.find('.prov-source').exists()).toBe(false);
  });

  it('still shows the no-contract line for a provider that truly has none', async () => {
    const w = await mountContracts();
    const card = findCard(w, 'Legacy Orphan');

    expect(card.find('.prov-nospec').exists()).toBe(true);
    expect(card.text()).toContain(NO_CONTRACT_ROW);
    expect(card.text()).not.toContain(MCP_NO_SPEC_NEEDED);
    expect(card.find('.prov-source').exists()).toBe(false);
  });
});
