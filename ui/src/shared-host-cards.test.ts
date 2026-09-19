// @vitest-environment happy-dom
//
// One host, two kinds (2026-09-19): api.acme.test serves a REST API with an
// uploaded contract AND an MCP server with an observed tools/list. The collector
// stores them apart and /api/contracts lists two rows with ONE integration,
// told apart by `format`. The Contracts tab must show two cards and never merge
// them: each card has its own anchor, the MCP card reads its tool list by
// format, and each finding lands on the card of its own kind.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import type { ContractSpec } from './contracts';
import type { Finding } from './types';

const AT = '2026-09-19T10:00:00Z';
const HOST = 'api.acme.test';
const INTEGRATION = 'api-acme-test';

const CONTRACTS: ContractSpec[] = [
  { integration: INTEGRATION, role: 'provider', format: 'openapi', peer_host: HOST, title: 'Acme Payments API', version: '1.0.0', endpoints: 3, loaded_at: AT, source: 'upload', edge_class: 'external' },
  { integration: INTEGRATION, role: 'provider', format: 'mcp', peer_host: HOST, title: 'acme-tools-mcp', version: '0.3.0', endpoints: 1, loaded_at: AT, source: 'observed', edge_class: 'external' }
];

const finding = (over: Partial<Finding> & Pick<Finding, 'id' | 'kind' | 'endpoint'>): Finding =>
  ({
    severity: 'breaking',
    integration: INTEGRATION,
    expected: 'integer',
    actual: 'string',
    rule: 'type',
    peer_host: HOST,
    detected_at: AT,
    first_seen: AT,
    last_seen: AT,
    occurrence_count: 1,
    ...over
  }) as Finding;

const FINDINGS: Finding[] = [
  finding({ id: 'f-rest', kind: 'live-vs-spec', endpoint: 'POST /v1/charges' }),
  finding({ id: 'f-mcp', kind: 'output_mismatch', endpoint: 'charge' })
];

const TOOLS = '{"tools":[{"name":"charge","inputSchema":{"type":"object"}}]}';
const OPENAPI = 'openapi: 3.0.0\ninfo: {title: Acme Payments API, version: 1.0.0}\npaths: {}\n';

let specRequests: string[] = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubAppFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const url = String(input);
      const path = url.split('?')[0];
      if (path === '/api/contracts/spec') {
        specRequests.push(url);
        // The collector's rule: the format picks the row; without one the REST
        // contract answers. An MCP card that forgot the format gets OpenAPI.
        const format = new URL(url, 'http://ui.test').searchParams.get('format');
        return format === 'mcp'
          ? new Response(TOOLS, { status: 200, headers: { 'content-type': 'application/json' } })
          : new Response(OPENAPI, { status: 200, headers: { 'content-type': 'application/yaml' } });
      }
      const bodies: Record<string, unknown> = {
        '/api/health': { status: 'ok', window_rows: 0, calls: 0, findings: 2, cp_configured: false, connect_status: 'disconnected', collector_version: 'v0.0.0-test' },
        '/api/findings': { findings: FINDINGS },
        '/api/calls': { calls: [] },
        '/api/edges': { edges: [] },
        '/api/contracts': { contracts: CONTRACTS },
        '/api/connect': { status: 'disconnected' },
        '/api/threads': { threads: [], total: 0, has_more: false }
      };
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

let wrapper: VueWrapper | null = null;

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('a host with both a REST contract and an MCP server', () => {
  beforeEach(() => {
    localStorage.clear();
    window.location.hash = '';
    specRequests = [];
    vi.useFakeTimers();
    stubAppFetch();
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
    for (let i = 0; i < 12; i++) await Promise.resolve();
    await w.vm.$nextTick();
    return w;
  }

  const card = (w: VueWrapper, name: string) => {
    const c = w.findAll('article.provider').find((a) => a.find('.prov-name').text() === name);
    if (!c) throw new Error(`no card for ${name}: ${w.findAll('.prov-name').map((n) => n.text())}`);
    return c;
  };

  it('shows two cards, each with its own anchor', async () => {
    const w = await mountContracts();
    const rest = card(w, 'Acme Payments API');
    const mcp = card(w, 'acme-tools-mcp');
    expect(rest.attributes('id')).toBe('contract-' + HOST);
    expect(mcp.attributes('id')).toBe('mcp-contract-' + HOST);
    expect(document.querySelectorAll('[id="contract-' + HOST + '"]').length).toBe(1);
  });

  it('reads the MCP card’s tool list by format', async () => {
    const w = await mountContracts();
    expect(specRequests.some((u) => u.includes('format=mcp'))).toBe(true);
    expect(card(w, 'acme-tools-mcp').findAll('.tool-row .tool-name').map((n) => n.text())).toEqual(['charge']);
  });

  it('puts each finding on the card of its own kind', async () => {
    const w = await mountContracts();
    const ids = (name: string) => card(w, name).findAll('article.finding').map((f) => f.attributes('id'));
    expect(ids('Acme Payments API')).toEqual(['finding-f-rest']);
    expect(ids('acme-tools-mcp')).toEqual(['finding-f-mcp']);
  });
});
