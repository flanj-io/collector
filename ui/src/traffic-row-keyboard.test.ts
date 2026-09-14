// @vitest-environment happy-dom
//
// UX review 2026-09-14: a traffic row was a clickable <div> with no tabindex,
// role or key handler, so Tab skipped every call and a keyboard user could
// never open the request/response detail. A row that toggles detail is a
// control: it sits in the tab order, says what it is, and Enter / Space do
// what the click does. Mounted, because the row's attributes and the detail
// it opens are template facts.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

const CALL = {
  id: 'c1',
  captured_at: '2026-09-13T20:44:52.964Z',
  integration: 'acme-payments',
  method: 'POST',
  route: '/v1/charges',
  status_code: 200,
  request_body: '{"amount":1200}',
  response_body: '{"ok":true}',
  correlation: { request_id: 'req_1' },
  redaction: { applied: true, patterns: [], spec_aware: false },
  direction: 'client',
  peer_host: 'api.acme.test',
  edge_class: 'external',
  validated: 'clean'
};

const OK_BODIES: Record<string, unknown> = {
  '/api/health': { status: 'ok', integration: 'acme-payments', window_rows: 1, calls: 1, findings: 0, cp_configured: false, collector_version: 'v0.0.0-test' },
  '/api/findings': { findings: [] },
  '/api/calls': { calls: [CALL] },
  '/api/edges': { edges: [] },
  '/api/contracts': { contracts: [] },
  '/api/connect': { status: 'disconnected' },
  '/api/threads': { threads: [], total: 0, has_more: false }
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '#traffic';
  vi.useFakeTimers();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = OK_BODIES[path];
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
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
  return w;
}

describe('Traffic rows are keyboard controls', () => {
  it('a row is focusable, a button to assistive tech, and Enter / Space open and close its detail', async () => {
    const w = await mountApp();
    const row = w.find('.tr-row');
    expect(row.exists(), 'the call rendered as a row').toBe(true);
    expect(row.attributes('tabindex')).toBe('0');
    expect(row.attributes('role')).toBe('button');
    expect(row.attributes('aria-expanded')).toBe('false');
    expect(w.find('.tr-detail').exists()).toBe(false);

    await row.trigger('keydown', { key: 'Enter' });
    expect(row.attributes('aria-expanded')).toBe('true');
    expect(w.find('.tr-detail').exists(), 'Enter opens the request/response detail').toBe(true);

    await row.trigger('keydown', { key: ' ' });
    expect(row.attributes('aria-expanded')).toBe('false');
    expect(w.find('.tr-detail').exists(), 'Space closes it again').toBe(false);

    // The click path still works, unchanged.
    await row.trigger('click');
    expect(w.find('.tr-detail').exists()).toBe(true);
  });

  it('the captured column renders the surface timestamp format, with the full instant on the title', async () => {
    const w = await mountApp();
    const when = w.find('.tr-row .c-when');
    expect(when.text()).toMatch(/\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/);
    expect(when.text()).not.toMatch(/AM|PM|Sep/);
    expect(when.attributes('title')).toBe(CALL.captured_at);
  });
});
