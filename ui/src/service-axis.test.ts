// @vitest-environment happy-dom
//
// The caller's service.name, Datadog-style (CONTRACTS §2/§3, 2026-09-19).
//
// Two services calling ONE MCP server rendered one Overview line — or, when
// their integration ids happened to differ, two byte-identical ones. The line
// is now per (server, calling service), and Traffic filters by service. Both
// are template facts, so both are asserted mounted.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { identifiableServerRefs, mcpHeadline, serviceSlices } from './mcp';
import type { ContractSpec } from './contracts';
import type { Finding, RedactedCall } from './types';

const AT = '2026-09-19T10:00:00Z';
const LEAD = 'Server: acme-tools-mcp v1.2.0 · mcp.acme.test';

const contract = (integration: string): ContractSpec => ({
  integration,
  role: 'provider',
  format: 'mcp',
  endpoints: 1,
  loaded_at: AT,
  source: 'observed',
  edge_class: 'external',
  peer_host: 'mcp.acme.test',
  title: 'acme-tools-mcp',
  version: '1.2.0'
});

let seq = 0;
const call = (integration: string, service: string | undefined, validated: 'clean' | 'drifted'): RedactedCall =>
  ({
    schema_version: 1,
    id: `c${++seq}`,
    captured_at: AT,
    integration,
    service_name: service,
    direction: 'client',
    peer_host: 'mcp.acme.test',
    edge_class: 'external',
    method: 'tools/call',
    url: 'mcp://mcp.acme.test/search',
    route: '/search',
    status_code: 0,
    transport: 'mcp',
    mcp_tool_name: 'search',
    correlation: {},
    redaction: { applied: false, patterns: [], spec_aware: false },
    validated,
    drifted: validated === 'drifted'
  }) as unknown as RedactedCall;

const mismatch = (integration: string): Finding =>
  ({
    schema_version: 1,
    id: 'f1',
    kind: 'output_mismatch',
    severity: 'breaking',
    integration,
    endpoint: 'search',
    rule: 'type-mismatch',
    expected: 'type=integer',
    actual: 'type=string',
    detected_at: AT,
    first_seen: AT,
    last_seen: AT,
    occurrence_count: 4,
    peer_host: 'mcp.acme.test'
  }) as unknown as Finding;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stub(contracts: ContractSpec[], calls: RedactedCall[], findings: Finding[] = []) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const bodies: Record<string, unknown> = {
        '/api/health': { status: 'ok', window_rows: calls.length, calls: calls.length, findings: findings.length, cp_configured: false, connect_status: 'disconnected', collector_version: 'v0.0.0-test' },
        '/api/findings': { findings },
        '/api/calls': { calls },
        '/api/edges': { edges: [] },
        '/api/contracts': { contracts },
        '/api/connect': { status: 'disconnected' },
        '/api/threads': { threads: [], total: 0, has_more: false }
      };
      if (path === '/api/contracts/spec') {
        return new Response('{"tools":[{"name":"search","inputSchema":{"type":"object"}}]}', { status: 200, headers: { 'content-type': 'application/json' } });
      }
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
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function mountApp(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
  return w;
}

const statusEl = (w: VueWrapper) => w.find('.headline.status');
const statusLines = (w: VueWrapper) => w.findAll('.st-lines .st-text').map((s) => s.text());

describe('the Overview status, per calling service', () => {
  it('two services clean on one server: no drift line — the tally counts the SERVER once, not per caller', async () => {
    // The default install: neither SDK configures an integration, both derive
    // the same one from the host, so both services land on one row — proven
    // directly against serviceSlices/identifiableServerRefs below (still two
    // distinct, named refs). Neither call drifted, so `mcpHeadline` returns
    // null for both (ui/src/mcp.ts) — a clean per-service line is gone, not
    // folded — and the status card's tally counts the one underlying MCP
    // server `checkedCounts` saw checked calls on, not the two callers.
    stub([contract('mcp-acme-test')], [call('mcp-acme-test', 'org-app', 'clean'), call('mcp-acme-test', 'org-app-py', 'clean')]);
    const w = await mountApp();
    expect(statusLines(w)).toEqual([]);
    expect(statusEl(w).classes()).toContain('ok');
    expect(statusEl(w).text()).toContain('1 MCP server');
  });

  it('two servers, one service each, both clean: the tally counts both', async () => {
    // The integration stack: two configured integrations, one service each.
    // The per-row, per-service distinctness itself is asserted directly
    // against serviceSlices/identifiableServerRefs in the describe block
    // below.
    stub([contract('acme-tools'), contract('acme-tools-py')], [call('acme-tools', 'org-app', 'clean'), call('acme-tools-py', 'org-app-py', 'clean')]);
    const w = await mountApp();
    expect(statusLines(w)).toEqual([]);
    expect(statusEl(w).classes()).toContain('ok');
    expect(statusEl(w).text()).toContain('2 MCP servers');
  });

  it('puts an output mismatch on the line of the service whose calls drifted; the clean service says nothing at all', async () => {
    stub(
      [contract('mcp-acme-test')],
      [call('mcp-acme-test', 'org-app', 'clean'), call('mcp-acme-test', 'org-app-py', 'drifted')],
      [mismatch('mcp-acme-test')]
    );
    const w = await mountApp();
    const lines = statusLines(w);
    // org-app-py's own line is drift — positive evidence about ONE service.
    expect(lines.find((l) => l.includes('called by org-app-py —'))).toMatch(/— output mismatch on search — 4 calls since /);
    // org-app came back clean: it never had a line to fold — there is none.
    expect(lines.find((l) => l.includes('called by org-app —'))).toBeUndefined();
    expect(lines).toHaveLength(1);
    expect(statusEl(w).classes()).toContain('drift');
    // There is exactly one MCP server here (integration mcp-acme-test), and
    // it is the one drifting — org-app's clean call is evidence about a
    // server that is, as a whole, drifting. The sub-line must never count
    // that same server as part of "the rest of what was checked": with
    // nothing else checked, it has nothing left to name and disappears.
    expect(statusEl(w).text()).not.toContain('No drift in the rest of what was checked');
  });

  it('puts a mismatch no call in view can place on EVERY line — evicted evidence never reads clean', async () => {
    stub(
      [contract('mcp-acme-test')],
      [call('mcp-acme-test', 'org-app', 'clean'), call('mcp-acme-test', 'org-app-py', 'clean')],
      [mismatch('mcp-acme-test')]
    );
    const w = await mountApp();
    const lines = statusLines(w);
    expect(lines).toHaveLength(2);
    for (const l of lines) expect(l).toContain('— output mismatch on search');
    expect(statusEl(w).text()).toContain('Drift detected in 2 places');
  });

  it('a row nobody has called yet: neutral, no line, no Not validated row — zero calls means zero items', async () => {
    stub([contract('acme-tools')], []);
    const w = await mountApp();
    expect(statusLines(w)).toEqual([]);
    expect(statusEl(w).classes()).toContain('neutral');
    expect(statusEl(w).text()).toContain('Nothing validated yet');
    expect(w.find('.not-validated').exists()).toBe(false);
  });

  it('two distinct inbound consumers, both checked: the tally counts 2, not a flag pinned at 1', async () => {
    const inboundCall = (id: string, peerHost: string): RedactedCall =>
      ({
        schema_version: 1,
        id,
        captured_at: AT,
        integration: 'self',
        direction: 'server',
        peer_host: peerHost,
        edge_class: 'external',
        method: 'GET',
        url: 'https://self.example/v1/x',
        route: '/v1/x',
        status_code: 200,
        transport: 'http',
        correlation: {},
        redaction: { applied: false, patterns: [], spec_aware: false },
        validated: 'clean',
        drifted: false
      }) as unknown as RedactedCall;
    stub([], [inboundCall('i1', 'client-a.example'), inboundCall('i2', 'client-b.example')]);
    const w = await mountApp();
    expect(statusEl(w).classes()).toContain('ok');
    expect(statusEl(w).text()).toContain('2 inbound consumers');
    expect(statusEl(w).text()).not.toContain('1 inbound consumer');
  });
});

describe('the Traffic service filter', () => {
  it('offers each calling service and narrows the rows to one', async () => {
    window.location.hash = '#traffic';
    stub([], [call('mcp-acme-test', 'org-app', 'clean'), call('mcp-acme-test', 'org-app-py', 'clean'), call('mcp-acme-test', 'org-app-py', 'clean')]);
    const w = await mountApp();
    const select = w.find('select[aria-label="Filter by service"]');
    expect(select.exists()).toBe(true);
    expect(select.findAll('option').map((o) => o.text())).toEqual(['service: all', 'org-app', 'org-app-py']);
    expect(w.findAll('.tr-row')).toHaveLength(3);
    // A Service column beside counterparty, on every row.
    const head = w.findAll('.tr-head > span').map((h) => h.text());
    expect(head.indexOf('service')).toBe(head.indexOf('counterparty') - 1);
    expect(w.findAll('.tr-row .c-svc').map((c) => c.text()).sort()).toEqual(['org-app', 'org-app-py', 'org-app-py']);
    await select.setValue('org-app-py');
    expect(w.findAll('.tr-row')).toHaveLength(2);
    await select.setValue('org-app');
    expect(w.findAll('.tr-row')).toHaveLength(1);
  });

  it('is not shown when no call carries a service — the column shows a dash', async () => {
    window.location.hash = '#traffic';
    stub([], [call('mcp-acme-test', undefined, 'clean')]);
    const w = await mountApp();
    expect(w.findAll('.tr-row')).toHaveLength(1);
    expect(w.find('select[aria-label="Filter by service"]').exists()).toBe(false);
    // The column is still there; the cell says so rather than going blank.
    expect(w.find('.tr-row .c-svc').text()).toBe('—');
  });
});

describe('serviceSlices + identifiableServerRefs', () => {
  const rc = (service: string, drifted = false) => ({ service, tool: 'search', drifted, validated: true });

  it('keeps a definition change on every service line: it is the server’s, not a caller’s', () => {
    const def = { ...mismatch('x'), kind: 'definition_change', rule: 'output-type-changed' } as Finding;
    const slices = serviceSlices([def], [rc('a'), rc('b')]);
    expect(slices.map((s) => [s.service, s.findings.length])).toEqual([['a', 1], ['b', 1]]);
  });

  it('adds the integration id only where one service reaches one server under two ids', () => {
    const ref = (integration: string, service: string) => ({ name: 'acme-tools-mcp', version: '1.2.0', origin: 'mcp.acme.test', service, integration });
    const [a, b, c] = identifiableServerRefs([ref('one', 'org-app'), ref('two', 'org-app'), ref('three', 'org-app-py')]);
    const fmt = (iso: string) => iso;
    // mcpHeadline only ever speaks drift (ui/src/mcp.ts) — the disambiguation
    // is proven on the one branch that still renders a sentence.
    const finding = mismatch('x');
    expect(mcpHeadline(a, [finding], fmt)?.text).toBe(`${LEAD} · called by org-app · integration: one — output mismatch on search — 4 calls since ${AT}.`);
    expect(mcpHeadline(b, [finding], fmt)?.text).toBe(`${LEAD} · called by org-app · integration: two — output mismatch on search — 4 calls since ${AT}.`);
    expect(mcpHeadline(c, [finding], fmt)?.text).toBe(`${LEAD} · called by org-app-py — output mismatch on search — 4 calls since ${AT}.`);
  });
});
