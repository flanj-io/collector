// @vitest-environment happy-dom
//
// v1 phase 3: the Threads tab's link-out to the person's CP workspace.
//
// The two facts worth pinning are both about restraint. It rides the EXISTING
// `dashboard_url` field, so it inherits that field's rule — absent means the
// collector has no address a browser off-host could open, and absence must
// render nothing rather than a dead link (the defect the pill's own test
// records). And it is one muted line under a list that already exists: the
// brief's rule for this slice is NO NEW NAG SURFACES, so there is no banner, no
// card and nothing dismissable to assert.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { WORKSPACE_LINK_OUT } from './threads';

const HEALTH = {
  status: 'ok',
  window_rows: 0,
  calls: 0,
  findings: 0,
  cp_configured: true,
  connect_status: 'connected',
  collector_version: 'v0.0.0-test'
};

const CONNECTED = {
  status: 'connected',
  consumer_display_name: 'Acme Consumer Ltd',
  contact_email: 'ops@acme.test',
  contact_display_name: 'Dana',
  collector_public_id: 'col_pub_1',
  registered_at: '2026-09-07T10:00:00Z',
  confirmed_at: '2026-09-07T10:01:00Z',
  confirmed_contact_email: 'ops@acme.test',
  cp_configured: true
};

const DASHBOARD = 'http://localhost:3001/d';

const bodies: Record<string, unknown> = {};

function serve(connect: Record<string, unknown>) {
  Object.assign(bodies, {
    '/api/health': HEALTH,
    '/api/findings': { findings: [] },
    '/api/calls': { calls: [] },
    '/api/edges': { edges: [] },
    '/api/contracts': { contracts: [] },
    '/api/connect': connect,
    '/api/threads': { threads: [], total: 0, has_more: false }
  });
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

async function settle(w: VueWrapper) {
  for (let i = 0; i < 6; i++) await Promise.resolve();
  await w.vm.$nextTick();
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '';
  vi.useFakeTimers();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mountApp(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  await settle(w);
  return w;
}

describe('the Threads tab workspace link-out (v1 phase 3)', () => {
  it('offers the workspace when the collector has an address a browser can open', async () => {
    serve({ ...CONNECTED, dashboard_url: DASHBOARD });
    const w = await mountApp();

    const link = w.find('.th-workspace a');
    expect(link.exists()).toBe(true);
    expect(link.text()).toBe(WORKSPACE_LINK_OUT);
    expect(link.attributes('href')).toBe(DASHBOARD);
    // A different origin on the operator's own machine — never take this tab with it.
    expect(link.attributes('target')).toBe('_blank');
    expect(link.attributes('rel')).toContain('noopener');
  });

  it('renders NOTHING without a dashboard_url — never a dead link', async () => {
    serve(CONNECTED); // Connected, but no browser-facing address to give
    const w = await mountApp();

    expect(w.find('.th-workspace').exists()).toBe(false);
    // ...and the tab is otherwise untouched: the read-only note is still there.
    expect(w.find('.th-readonly').exists()).toBe(true);
  });

  it('is not a nag surface: one line, no banner, no dismissable card', async () => {
    serve({ ...CONNECTED, dashboard_url: DASHBOARD });
    const w = await mountApp();

    const rows = w.findAll('.th-workspace');
    expect(rows).toHaveLength(1);
    // No button anywhere in it — an offer, not a prompt with an action to refuse.
    expect(w.find('.th-workspace button').exists()).toBe(false);
    // The copy promises the SET, which is the only reason the line earns its place:
    // this tab lists one collector's threads, the workspace lists all of the person's.
    expect(WORKSPACE_LINK_OUT).toContain('all your threads');
  });
});
