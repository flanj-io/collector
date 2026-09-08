// @vitest-environment happy-dom
//
// v1 phase 4, on the Edges panel: the OUTBOUND edge row gains one cross-org
// action — "Start a thread" — and it opens the flag sheet in QUESTION mode
// (no finding, no evidence block). Inbound rows do not get it: an inbound
// `peer_host` is a forgeable XFF first hop and is never identity (v1 spec §5),
// and the relay refuses one anyway.
//
// The sheet's own honesty is `question-sheet.test.ts`; this file is about the
// row: who gets the control, and what the click hands the sheet.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import { START_THREAD_LABEL } from './edge-names';

const HEALTH = {
  status: 'ok',
  integration_id: 'acme-payments',
  consumer_display_name: 'Acme Consumer Ltd',
  connect_status: 'connected'
};

const EDGES = [
  {
    peer_host: 'api.globex.test',
    registrable_domain: 'globex.test',
    direction: 'client',
    role: 'consumer',
    class: 'external',
    display_name: 'Globex Payments',
    name_source: 'user',
    first_seen: '2026-09-08T08:00:00Z',
    last_seen: '2026-09-08T09:00:00Z',
    call_count: 3,
    drift_count: 0
  },
  {
    peer_host: 'in.caller.test',
    registrable_domain: 'caller.test',
    direction: 'server',
    role: 'provider',
    class: 'external',
    first_seen: '2026-09-08T08:00:00Z',
    last_seen: '2026-09-08T09:00:00Z',
    call_count: 2,
    drift_count: 0
  }
];

const CONNECT = {
  status: 'connected',
  consumer_display_name: 'Acme Consumer Ltd',
  contact_email: 'ops@acme.test',
  confirmed_contact_email: 'ops@acme.test'
};

const OK_BODIES: Record<string, unknown> = {
  '/api/health': HEALTH,
  '/api/findings': { findings: [] },
  '/api/calls': { calls: [] },
  '/api/edges': { edges: EDGES },
  '/api/contracts': { contracts: [] },
  '/api/connect': CONNECT,
  '/api/threads': { threads: [], total: 0, has_more: false }
};

/** Every POST the sheet makes, so the click-through can be asserted. */
const posted: Array<{ url: string; body: Record<string, unknown> }> = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input).split('?')[0];
      if (init?.method === 'POST') {
        posted.push({ url: path, body: JSON.parse(String(init.body ?? '{}')) });
        return json({
          thread_id: 'thr_1',
          thread_public_id: 'pub',
          thread_url: 'https://cp.test/t/pub#k=tok',
          state: 'open',
          status: 'created'
        }, 201);
      }
      const body = OK_BODIES[path];
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
  posted.length = 0;
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

/** Every "Start a thread" control on the page. */
const startButtons = (w: VueWrapper) => w.findAll('button').filter((b) => b.text() === START_THREAD_LABEL);

describe('the edge row can start a thread', () => {
  it('offers the control on OUTBOUND rows only — an inbound peer_host is never identity', async () => {
    const w = await mountApp();
    expect(startButtons(w)).toHaveLength(1);

    // It lives on the outbound row, beside Rename, not on the inbound table.
    const row = startButtons(w)[0].element.closest('.edge-row') as HTMLElement;
    expect(row.textContent).toContain('Globex Payments');
    expect(row.textContent).toContain('api.globex.test');
  });

  it('opens the sheet in QUESTION mode: no evidence block, an empty message, the edge named', async () => {
    const w = await mountApp();
    await startButtons(w)[0].trigger('click');
    await settle(w);

    const sheet = w.find('[role="dialog"]');
    expect(sheet.exists()).toBe(true);
    // The row's RESOLVED name titles the sheet — never a humanized host.
    expect(sheet.find('.sheet-title').text()).toBe('New thread with Globex Payments');
    expect(sheet.find('.evidence').exists()).toBe(false);
    expect((sheet.find('textarea').element as HTMLTextAreaElement).value).toBe('');
  });

  it('sends the edge host to the edge route, and never the flag route', async () => {
    const w = await mountApp();
    await startButtons(w)[0].trigger('click');
    await settle(w);

    const sheet = w.find('[role="dialog"]');
    await sheet.find('textarea').setValue('Are you versioning /v1/refunds this quarter?');
    const create = sheet.findAll('button').find((b) => b.text() === 'Create thread')!;
    await create.trigger('click');
    await settle(w);

    const flagPosts = posted.filter((p) => p.url === '/api/flag');
    const edgePosts = posted.filter((p) => p.url === '/api/edges/thread');
    expect(flagPosts).toHaveLength(0);
    expect(edgePosts).toHaveLength(1);
    expect(edgePosts[0].body.host).toBe('api.globex.test');
    expect(edgePosts[0].body.message).toContain('/v1/refunds');
  });

  it('closes cleanly, so the next row opens its own sheet rather than the stale one', async () => {
    const w = await mountApp();
    await startButtons(w)[0].trigger('click');
    await settle(w);
    expect(w.find('[role="dialog"]').exists()).toBe(true);

    const cancel = w.find('[role="dialog"]').findAll('button').find((b) => b.text() === 'Cancel')!;
    await cancel.trigger('click');
    await settle(w);
    expect(w.find('[role="dialog"]').exists()).toBe(false);
  });
});
