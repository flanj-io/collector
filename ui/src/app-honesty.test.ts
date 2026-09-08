// @vitest-environment happy-dom
//
// The three false-green defects of the first-launch QA, asserted on the real
// component. These are template-level facts — which chip renders, which banner,
// which pill — and a pure helper cannot see any of them: the store-outage bug
// WAS a template consequence (a pill appearing) of a data bug (an error object
// assigned into `health`), and that pairing is exactly what went unnoticed.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

/* ── Fixtures ──────────────────────────────────────────────────────────── */

const HOST = 'api.acme.test';
// Two calls captured, and THEN a contract uploaded. Nothing re-runs them.
const CAPTURED_BEFORE = ['2026-09-02T11:50:00Z', '2026-09-02T11:51:00Z'];
const BOUND_AT = '2026-09-02T12:00:00Z';

const call = (id: string, captured_at: string) => ({
  id,
  captured_at,
  integration: 'acme-payments',
  method: 'POST',
  route: '/v1/charges',
  status_code: 200,
  request_body: '{"amount":1200}',
  response_body: '{"ok":true}',
  correlation: {},
  redaction: { applied: true, patterns: [], spec_aware: false },
  direction: 'client',
  peer_host: HOST,
  edge_class: 'external'
});

const HEALTH = {
  status: 'ok',
  integration: 'acme-payments',
  window_rows: 2,
  calls: 2,
  findings: 0,
  cp_configured: true,
  connect_status: 'disconnected',
  collector_version: 'v0.0.0-test'
};

const CONTRACT = {
  integration: 'api-acme-test',
  role: 'provider',
  peer_host: HOST,
  format: 'openapi',
  title: 'Acme Payments',
  version: '1.0.0',
  endpoints: 3,
  loaded_at: BOUND_AT,
  edge_class: 'external',
  source: 'upload'
};

const EDGES = [
  {
    peer_host: HOST,
    direction: 'client',
    role: 'provider',
    class: 'external',
    first_seen: CAPTURED_BEFORE[0],
    last_seen: CAPTURED_BEFORE[1],
    call_count: 2,
    drift_count: 0
  }
];

const OK_BODIES: Record<string, unknown> = {
  '/api/health': HEALTH,
  '/api/findings': { findings: [] },
  '/api/calls': { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) },
  '/api/edges': { edges: EDGES },
  '/api/contracts': { contracts: [CONTRACT] },
  '/api/connect': { status: 'disconnected' },
  '/api/threads': { threads: [], total: 0, has_more: false }
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' }
  });
}

/** Serves the healthy fixtures until `down` is flipped, then 500s everything. */
const state = { down: false };
function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      if (state.down) {
        return json({ error: 'store_unavailable', message: 'store is unavailable' }, 500);
      }
      const body = OK_BODIES[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

async function settle(w: VueWrapper) {
  // Three microtask drains: refresh() awaits Promise.all, then refreshContracts.
  for (let i = 0; i < 6; i++) await Promise.resolve();
  await w.vm.$nextTick();
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  state.down = false;
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

async function mountApp(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  await settle(w);
  return w;
}

/* ── 1. The card ───────────────────────────────────────────────────────── */

describe('a contract validates nothing until traffic reaches it', () => {
  it('zero validated calls renders the neutral state, never CONFORMING', async () => {
    const w = await mountApp();
    const card = w.find('.provider');
    expect(card.exists()).toBe(true);

    // The two calls predate the upload, so this contract has checked nothing.
    expect(card.find('.tag.ok').exists()).toBe(false);
    expect(card.text()).toContain('no calls validated yet');
    // ...and the count says so out loud, rather than leaving zero implied.
    expect(card.text()).toContain('validated 0 calls since upload');
  });

  it('a call captured AFTER the upload is evidence, and the chip turns over', async () => {
    OK_BODIES['/api/calls'] = { calls: [call('after', '2026-09-02T12:05:00Z')] };
    const w = await mountApp();
    const card = w.find('.provider');
    expect(card.find('.tag.ok').text()).toBe('conforming');
    expect(card.text()).toContain('validated 1 call since upload');
    OK_BODIES['/api/calls'] = { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) };
  });
});

/* ── 2. The headline ───────────────────────────────────────────────────── */

describe('the Overview headline', () => {
  it('does not assert an all-clear over calls nothing validated', async () => {
    const w = await mountApp();
    const hl = w.find('.headline');
    expect(hl.text()).toContain('Nothing validated yet');
    expect(hl.text()).not.toContain('No drift detected');
    // Neither verdict tone — this install has not reached one.
    expect(hl.classes()).toContain('neutral');
    expect(hl.classes()).not.toContain('ok');
    expect(hl.classes()).not.toContain('drift');
  });

  it('says No drift detected once a call has actually been validated', async () => {
    OK_BODIES['/api/calls'] = { calls: [call('after', '2026-09-02T12:05:00Z')] };
    const w = await mountApp();
    const hl = w.find('.headline');
    expect(hl.text()).toContain('No drift detected');
    expect(hl.classes()).toContain('ok');
    OK_BODIES['/api/calls'] = { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) };
  });
});

/* ── 3. The store outage ───────────────────────────────────────────────── */

describe('a failing store must not render as a fresh install', () => {
  it('keeps last-known data, shows the banner, and never claims the CP is unconfigured', async () => {
    const w = await mountApp();
    // Baseline: the host loaded, and cp_configured is true so no pill.
    expect(w.text()).toContain(HOST);
    expect(w.find('.pill.warn').exists()).toBe(false);

    // The database stops. Every poll now 500s with an error body — which the
    // old `fetch(...).then(r => r.json())` resolved and assigned into state.
    state.down = true;
    await vi.advanceTimersByTimeAsync(5000);
    await settle(w);

    // The banner is the honest surface.
    const banner = w.find('.error.banner');
    expect(banner.exists()).toBe(true);
    expect(banner.text()).toContain('Failed to load');
    expect(banner.text()).toContain('store is unavailable');

    // Nothing was overwritten: the header pill stayed away, the edge is still
    // on screen, and the page does not invite the operator to fix working config.
    expect(w.find('.pill.warn').exists()).toBe(false);
    expect(w.text()).not.toContain('control plane not configured');
    expect(w.text()).toContain(HOST);
  });

  it('an unreachable relay gets the deck line, not a raw TypeError', async () => {
    // Found by the QA walk: aborting the polls at the transport layer put
    // "Failed to load: TypeError: Failed to fetch" in front of the operator —
    // the exact string the per-finding ack path already refuses to show.
    const w = await mountApp();
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch');
      })
    );
    await vi.advanceTimersByTimeAsync(5000);
    await settle(w);

    const banner = w.find('.error.banner');
    expect(banner.exists()).toBe(true);
    expect(banner.text()).toContain('Could not reach this collector.');
    expect(banner.text()).not.toContain('TypeError');
    // Still no invented fresh-install state.
    expect(w.find('.pill.warn').exists()).toBe(false);
    expect(w.text()).toContain(HOST);
  });

  it('recovers on the next good poll and clears the banner', async () => {
    const w = await mountApp();
    state.down = true;
    await vi.advanceTimersByTimeAsync(5000);
    await settle(w);
    expect(w.find('.error.banner').exists()).toBe(true);

    state.down = false;
    await vi.advanceTimersByTimeAsync(5000);
    await settle(w);
    expect(w.find('.error.banner').exists()).toBe(false);
    expect(w.text()).toContain(HOST);
  });
});

/* ── 4. The processor's verdict, on the card ───────────────────────────── */

describe("the drift processor's stamp decides what counts as evidence", () => {
  it('a call captured AFTER the upload but stamped not-validated is neither evidence nor conforming', async () => {
    // THE 2026-09-07 bug: the upload had landed in the store (loaded_at is in
    // the past) but the processor's cache had not loaded it when the call went
    // through. The temporal gate alone rendered this CONFORMING.
    OK_BODIES['/api/calls'] = {
      calls: [{ ...call('late', '2026-09-02T12:05:00Z'), validated: 'not-validated', validated_reason: 'no-contract' }]
    };
    const w = await mountApp();
    const card = w.find('.provider');
    expect(card.find('.tag.ok').exists()).toBe(false);
    expect(card.text()).toContain('validated 0 calls since upload');
    expect(w.find('.headline').text()).toContain('Nothing validated yet');
    OK_BODIES['/api/calls'] = { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) };
  });

  it('a call stamped clean is evidence even when captured before the store says the contract loaded', async () => {
    // A replaced document keeps only the CURRENT row's loaded_at; the processor
    // validated this call against the one it replaced. The stamp knows better
    // than the timestamp.
    OK_BODIES['/api/calls'] = { calls: [{ ...call('early', CAPTURED_BEFORE[0]), validated: 'clean' }] };
    const w = await mountApp();
    const card = w.find('.provider');
    expect(card.find('.tag.ok').text()).toBe('conforming');
    expect(card.text()).toContain('validated 1 call since upload');
    expect(w.find('.headline').text()).toContain('No drift detected');
    OK_BODIES['/api/calls'] = { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) };
  });

  it('a record with no verdict at all is not evidence', async () => {
    OK_BODIES['/api/calls'] = { calls: [{ ...call('old-front', '2026-09-02T12:05:00Z'), validated: 'unknown' }] };
    const w = await mountApp();
    expect(w.find('.provider').text()).toContain('validated 0 calls since upload');
    expect(w.find('.headline').text()).toContain('Nothing validated yet');
    OK_BODIES['/api/calls'] = { calls: CAPTURED_BEFORE.map((t, i) => call(`c${i}`, t)) };
  });
});
