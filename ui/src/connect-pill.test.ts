// @vitest-environment happy-dom
//
// The Connected pill is the ONE door out of the local UI to the control plane,
// and whether it is a door at all is decided by a single field: `dashboard_url`
// on GET /api/connect. The collector omits it wherever it has no address a
// browser can open (launch-week item 8: it used to send its own in-network
// cp_base_url — `http://cp-api:3001`, a k8s Service — and the pill was a dead
// link on every split network). Which element renders is a template fact, so
// it is asserted on the mounted App, like app-honesty.test.ts.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

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

describe('the Connected pill', () => {
  it('is a link to the dashboard when the collector offers one', async () => {
    serve({ ...CONNECTED, dashboard_url: DASHBOARD });
    const w = await mountApp();

    const door = w.find('header a.pill-link');
    expect(door.exists()).toBe(true);
    expect(door.attributes('href')).toBe(DASHBOARD);
    // The label stays the status; the destination rides the tooltip and the ↗.
    expect(door.text()).toContain('Connected');
    expect(door.attributes('target')).toBe('_blank');
    expect(door.attributes('rel')).toContain('noopener');
    // Exactly one pill for the connect state — never both a link and a button.
    expect(w.findAll('header .pill-btn')).toHaveLength(1);
  });

  it('degrades to the Settings button when no dashboard_url is offered — never a dead link', async () => {
    serve(CONNECTED); // Connected, but the collector had no browser-facing address to give
    const w = await mountApp();

    expect(w.find('header a.pill-link').exists()).toBe(false);
    const pill = w.find('header button.pill-btn');
    expect(pill.exists()).toBe(true);
    expect(pill.text()).toBe('Connected');
    expect(pill.classes()).toContain('ok');
    expect(pill.attributes('title')).toBe('Connect settings');

    // ...and it is a real button into Settings, not a dead control.
    await pill.trigger('click');
    await w.vm.$nextTick();
    expect(w.find('nav.tabs button.active').text()).toMatch(/settings/i);
  });

  it('is never a door before the collector is Connected, even if a dashboard_url were present', async () => {
    serve({ status: 'pending', consumer_display_name: 'Acme Consumer Ltd', contact_email: 'ops@acme.test', dashboard_url: DASHBOARD });
    const w = await mountApp();

    expect(w.find('header a.pill-link').exists()).toBe(false);
    const pill = w.find('header button.pill-btn');
    expect(pill.exists()).toBe(true);
    expect(pill.text()).toBe('Confirm your contact');
    expect(pill.classes()).toContain('warn');
  });
});
