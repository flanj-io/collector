// @vitest-environment happy-dom
//
// The document-cap row state, on the REAL card.
//
// A pure helper cannot see the gap this closes. The defect was that an
// oversized row rendered EXACTLY like a healthy one — heading, format badge,
// tool count, a contract that looks complete — beside a "no calls validated
// yet" chip, while every call on that edge was stamped not-validated on a front
// that had never managed to read the document. Which chips a card renders is a
// template fact, so it is asserted here, mounted, like the honesty walk's own
// defects next door.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { MAX_CONTRACT_BYTES, CONTRACT_OVER_CAP_TAG } from './contracts';

const HOST = 'mcp.acme.test';
const OBSERVED_AT = '2026-09-08T10:00:00Z';

const health = (servesFronts: boolean) => ({
  status: 'ok',
  window_rows: 0,
  calls: 0,
  findings: 0,
  cp_configured: false,
  connect_status: 'disconnected',
  collector_version: 'v0.0.0-test',
  serves_fronts: servesFronts
});

const mcpContract = (docBytes: number | undefined) => ({
  integration: 'acme-tools',
  role: 'provider',
  peer_host: HOST,
  format: 'mcp',
  title: 'acme-tools-mcp',
  endpoints: 12,
  loaded_at: OBSERVED_AT,
  edge_class: 'external',
  source: 'observed',
  ...(docBytes === undefined ? {} : { doc_bytes: docBytes })
});

const EDGES = [
  {
    peer_host: HOST,
    direction: 'client',
    role: 'provider',
    class: 'external',
    first_seen: OBSERVED_AT,
    last_seen: OBSERVED_AT,
    call_count: 4,
    drift_count: 0
  }
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

const fixtures = { servesFronts: true, docBytes: undefined as number | undefined };

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const bodies: Record<string, unknown> = {
        '/api/health': health(fixtures.servesFronts),
        '/api/findings': { findings: [] },
        '/api/calls': { calls: [] },
        '/api/edges': { edges: EDGES },
        '/api/contracts': { contracts: [mcpContract(fixtures.docBytes)] },
        '/api/connect': { status: 'disconnected' },
        '/api/threads': { threads: [], total: 0, has_more: false }
      };
      // The Contracts tab fetches each MCP snapshot to render its tool rows.
      if (path === '/api/contracts/spec') {
        return new Response('{"tools":[]}', { status: 200, headers: { 'content-type': 'application/json' } });
      }
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  fixtures.servesFronts = true;
  fixtures.docBytes = undefined;
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
  // Open the Contracts tab, where the provider cards live.
  const tab = w.findAll('button').find((b) => b.text().trim().startsWith('Contracts'));
  if (!tab) throw new Error('the Contracts tab is not on screen');
  await tab.trigger('click');
  await w.vm.$nextTick();
  return w;
}

describe('an over-cap contract row on the Contracts card', () => {
  it('carries the state and names the overage', async () => {
    fixtures.docBytes = MAX_CONTRACT_BYTES + 1.4 * 1024 * 1024;
    const w = await mountContracts();

    expect(w.text()).toContain(CONTRACT_OVER_CAP_TAG);
    expect(w.find('.prov-oversize').exists()).toBe(true);
    const line = w.find('.prov-oversize').text();
    expect(line).toContain('9.4 MB');
    expect(line).toContain('1.4 MB over the 8 MB cap');

    // The row is still LISTED and still identifiable — withholding it would
    // turn an unreadable edge into an edge with no contract, which is a
    // different and equally wrong story.
    expect(w.text()).toContain('acme-tools-mcp');
  });

  // The regression this gate exists to prevent. On a single pod the drift
  // processor reads the same row in-process, crosses no boundary and applies no
  // cap, so the document IS bound and validating — warning about it there would
  // be a false alarm on a working install.
  it('says nothing on a pod that serves no fronts', async () => {
    fixtures.servesFronts = false;
    fixtures.docBytes = MAX_CONTRACT_BYTES + 4 * 1024 * 1024;
    const w = await mountContracts();

    expect(w.text()).not.toContain(CONTRACT_OVER_CAP_TAG);
    expect(w.find('.prov-oversize').exists()).toBe(false);
  });

  // A collector that predates doc_bytes reports no size at all. Inventing a
  // fault from a missing number is the wrong direction to guess in.
  it('says nothing when the row carries no measured size', async () => {
    const w = await mountContracts();
    expect(w.text()).not.toContain(CONTRACT_OVER_CAP_TAG);
    expect(w.find('.prov-oversize').exists()).toBe(false);
  });

  it('leaves a row exactly at the cap alone', async () => {
    fixtures.docBytes = MAX_CONTRACT_BYTES;
    const w = await mountContracts();
    expect(w.text()).not.toContain(CONTRACT_OVER_CAP_TAG);
  });
});
