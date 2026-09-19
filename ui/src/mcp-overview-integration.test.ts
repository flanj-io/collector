// @vitest-environment happy-dom
//
// Two integrations calling the SAME MCP server at the SAME host rendered two
// byte-identical Overview lines — `Server: acme-tools-mcp v1.2.0 ·
// mcp.acme.test — …` twice (the e2e stack's TS and Python tenants), so nobody
// could tell which line was which. The origin fixed two servers sharing a
// name; this is the case it left. Asserted mounted, because which rows collide
// is only known across the whole list the Overview renders.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { NOTHING_VALIDATED_YET_CLAUSE } from './headline';
import { identifiableServerRefs, mcpHeadline } from './mcp';
import type { ContractSpec } from './contracts';
import type { Finding } from './types';

const AT = '2026-09-18T10:00:00Z';

const contract = (over: Partial<ContractSpec> & { integration: string }): ContractSpec => ({
  role: 'provider',
  format: 'mcp',
  endpoints: 1,
  loaded_at: AT,
  source: 'observed',
  edge_class: 'external',
  ...over
});

const TS_TENANT = contract({ integration: 'acme-tools', peer_host: 'mcp.acme.test', title: 'acme-tools-mcp', version: '1.2.0' });
const PY_TENANT = contract({ integration: 'acme-tools-py', peer_host: 'mcp.acme.test', title: 'acme-tools-mcp', version: '1.2.0' });
const OTHER = contract({ integration: 'weather', peer_host: 'mcp.weather.test', title: 'weather-mcp', version: '2.0.0' });

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubAppFetch(contracts: ContractSpec[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const bodies: Record<string, unknown> = {
        '/api/health': { status: 'ok', window_rows: 0, calls: 0, findings: 0, cp_configured: false, connect_status: 'disconnected', collector_version: 'v0.0.0-test' },
        '/api/findings': { findings: [] },
        '/api/calls': { calls: [] },
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

async function overviewLines(contracts: ContractSpec[]): Promise<string[]> {
  stubAppFetch(contracts);
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
  return w.findAll('section.mcp-headline strong').map((s) => s.text());
}

describe('the Overview MCP lines, when two integrations call one server', () => {
  it('differ, and each names its own integration', async () => {
    const lines = await overviewLines([TS_TENANT, PY_TENANT, OTHER]);
    expect(lines).toHaveLength(3);
    const acme = lines.filter((l) => l.startsWith('Server: acme-tools-mcp v1.2.0 · mcp.acme.test'));
    expect(acme).toHaveLength(2);
    expect(acme[0]).not.toBe(acme[1]);
    // `acme-tools` is a prefix of `acme-tools-py`: match up to the dash.
    const ts = acme.filter((l) => l.includes('integration: acme-tools —'));
    const py = acme.filter((l) => l.includes('integration: acme-tools-py —'));
    expect(ts).toHaveLength(1);
    expect(py).toHaveLength(1);
    expect(ts[0]).not.toContain('acme-tools-py');
  });

  it('leave a server nothing collides with exactly as it was', async () => {
    const lines = await overviewLines([TS_TENANT, PY_TENANT, OTHER]);
    expect(lines).toContain(`Server: weather-mcp v2.0.0 · mcp.weather.test — ${NOTHING_VALIDATED_YET_CLAUSE}`);
  });
});

describe('the Overview MCP line for a single integration', () => {
  it('renders exactly as before — no integration id', async () => {
    const lines = await overviewLines([TS_TENANT]);
    expect(lines).toEqual([`Server: acme-tools-mcp v1.2.0 · mcp.acme.test — ${NOTHING_VALIDATED_YET_CLAUSE}`]);
  });
});

describe('identifiableServerRefs', () => {
  const ref = (integration: string, origin = 'mcp.acme.test') => ({ name: 'acme-tools-mcp', version: '1.2.0', origin, integration });
  const fmt = (iso: string) => iso;

  it('keys on the lead, not the whole line: a drifted and a clean line on one server both carry their id', () => {
    const [ts, py] = identifiableServerRefs([ref('acme-tools'), ref('acme-tools-py')]);
    const drift = { kind: 'output_mismatch', integration: 'acme-tools', endpoint: 'search', occurrence_count: 1, first_seen: AT } as Finding;
    const a = mcpHeadline(ts, [drift], fmt, 1).text;
    const b = mcpHeadline(py, [], fmt, 1).text;
    expect(a).toBe(`Server: acme-tools-mcp v1.2.0 · mcp.acme.test · integration: acme-tools — output mismatch on search — 1 call since ${AT}.`);
    expect(b).toBe('Server: acme-tools-mcp v1.2.0 · mcp.acme.test · integration: acme-tools-py — no drift detected.');
  });

  it('covers two stdio servers of one name, and drops the id where the origin already tells them apart', () => {
    const out = identifiableServerRefs([ref('a', 'stdio'), ref('b', 'stdio'), ref('c', 'mcp.other.test')]);
    expect(out.map((r) => r.integration)).toEqual(['a', 'b', undefined]);
  });
});
