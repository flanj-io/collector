// @vitest-environment happy-dom
//
// thread-domain-gate (2026-09-14) — the sheet's "Open to" field. A thread is
// shared with the email domains the operator names, or — as an EXPLICIT choice —
// with anyone holding the link. Four things this file holds:
//   1. there is no silent default: Create thread is inert until one or the
//      other is said, and the guard says why;
//   2. what is typed reaches the wire normalized, and the toggle is JSON null
//      (present, never absent);
//   3. the directory prefills a CLAIMED domain and nothing less, and never over
//      what the operator typed;
//   4. the success state's warning stops promising "Anyone with this link".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import FlagSheet from './FlagSheet.vue';
import type { Finding } from './types';
import { OPEN_TO_ANYONE_LABEL, OPEN_TO_REQUIRED, openToPrefillNote, type ConnectState } from './threads';

const CONNECTED: ConnectState = {
  status: 'connected',
  consumer_display_name: 'Acme',
  contact_email: 'ops@acme.test',
  confirmed_contact_email: 'ops@acme.test'
};

const FINDING: Finding = {
  id: 'f1',
  kind: 'live-vs-spec',
  severity: 'breaking',
  integration: 'acme-payments',
  endpoint: 'POST /v1/charges',
  field_path: '$.response.body.amount',
  expected: 'integer',
  actual: 'string',
  rule: 'type',
  source_call_id: 'c1',
  first_seen: '2026-09-02T12:05:00Z',
  last_seen: '2026-09-02T12:05:00Z',
  peer_host: 'api.acme-payments.test'
};

const CREATED = { thread_id: 'thr_1', thread_public_id: 'pub', thread_url: 'https://cp.test/t/pub#k=tok', state: 'open', status: 'created' };

interface Recorded {
  url: string;
  body: Record<string, unknown>;
}

/** The relay: every POST creates a thread; the hint GET answers as `hint` says. */
function stubFetch(record: Recorded[], hint: Record<string, unknown> | null = null) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        record.push({ url, body: JSON.parse(String(init.body ?? '{}')) });
        return { ok: true, status: 201, text: async () => JSON.stringify(CREATED) } as unknown as Response;
      }
      if (hint === null) return { ok: false, status: 404, text: async () => JSON.stringify({ error: 'store_error', message: 'x' }) } as unknown as Response;
      return { ok: true, status: 200, text: async () => JSON.stringify(hint) } as unknown as Response;
    })
  );
}

let wrapper: VueWrapper | null = null;

async function open(props: Record<string, unknown> = {}): Promise<VueWrapper> {
  document.body.innerHTML = '<div id="app"><main></main><div id="host"></div></div>';
  const w = mount(FlagSheet, {
    attachTo: '#host',
    props: { finding: FINDING, correlation: null, call: null, provider: 'Acme Payments', consumer: 'Acme', connect: CONNECTED, providerHost: 'api.acme-payments.test', ...props }
  });
  wrapper = w;
  await flush(w);
  return w;
}

async function flush(w: VueWrapper): Promise<void> {
  for (let i = 0; i < 8; i += 1) {
    await Promise.resolve();
    await w.vm.$nextTick();
  }
}

const createButton = (w: VueWrapper) => w.findAll('button').find((b) => b.text().startsWith('Create thread') || b.text() === 'Creating…')!;
const openTo = (w: VueWrapper) => w.find('input.open-to');
const anyone = (w: VueWrapper) => w.find('input[name="open_to_anyone"]');

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('there is no silent default', () => {
  it('Create thread is inert until a domain is named or the box is ticked, and says why', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    expect(createButton(w).attributes('disabled')).toBeDefined();
    expect(w.find('.open-to-guard').text()).toBe(OPEN_TO_REQUIRED);
    expect(w.text()).toContain(OPEN_TO_ANYONE_LABEL);

    await openTo(w).setValue('acme-payments.test');
    expect(createButton(w).attributes('disabled')).toBeUndefined();
    expect(w.find('.open-to-guard').exists()).toBe(false);

    await openTo(w).setValue('');
    expect(createButton(w).attributes('disabled')).toBeDefined();
    await anyone(w).setValue(true);
    expect(createButton(w).attributes('disabled')).toBeUndefined();
    // The toggle takes the field out of play rather than clearing it.
    expect(openTo(w).attributes('disabled')).toBeDefined();
    expect(calls).toHaveLength(0);
  });

  it('a non-domain is refused before the click, naming the entry', async () => {
    stubFetch([]);
    const w = await open();
    await openTo(w).setValue('acme-payments.test, dana@acme.test');
    expect(createButton(w).attributes('disabled')).toBeDefined();
    expect(w.find('.open-to-guard').text()).toContain('"dana@acme.test" is not a domain');
  });
});

describe('what reaches the wire', () => {
  it('the typed list, normalized and de-duplicated', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await openTo(w).setValue(' Acme-Payments.test, @acme-payments.test globex.test. ');
    await createButton(w).trigger('click');
    await flush(w);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/flag');
    expect(calls[0].body.allowed_domains).toEqual(['acme-payments.test', 'globex.test']);
  });

  it('Anyone with the link is an explicit null — present on the wire, never absent', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await anyone(w).setValue(true);
    await createButton(w).trigger('click');
    await flush(w);
    expect(calls).toHaveLength(1);
    expect('allowed_domains' in calls[0].body).toBe(true);
    expect(calls[0].body.allowed_domains).toBeNull();
    // And the success warning keeps the open-link sentence for the open-link thread.
    expect(w.find('.warning').text()).toContain('Anyone with this link can read the redacted evidence and reply');
  });

  it('a gated thread’s success warning names who can open it, never "Anyone with this link"', async () => {
    stubFetch([]);
    const w = await open();
    await openTo(w).setValue('acme-payments.test');
    await createButton(w).trigger('click');
    await flush(w);
    const warning = w.find('.warning').text();
    expect(warning).toContain('People with an @acme-payments.test address can open this link');
    expect(warning).toContain('read the redacted evidence and reply');
    expect(warning).toContain('Nobody else can read it');
    expect(warning).not.toContain('Anyone with this link');
  });
});

describe('the directory prefill', () => {
  const claimed = { host: 'api.acme-payments.test', domain: 'acme-payments.test', name: 'Acme Payments', tier: 'claimed', claimed: true };
  const curated = { ...claimed, tier: 'curated', claimed: false };

  it('prefills a CLAIMED domain and says why', async () => {
    stubFetch([], claimed);
    const w = await open();
    expect((openTo(w).element as HTMLInputElement).value).toBe('acme-payments.test');
    expect(w.find('.open-to-note').text()).toBe(openToPrefillNote('acme-payments.test'));
    expect(createButton(w).attributes('disabled')).toBeUndefined();
  });

  it('a curated entry proves nothing about a mailbox and prefills nothing', async () => {
    stubFetch([], curated);
    const w = await open();
    expect((openTo(w).element as HTMLInputElement).value).toBe('');
    expect(createButton(w).attributes('disabled')).toBeDefined();
  });

  it('asks about the edge host in question mode, and never overwrites what was typed', async () => {
    let resolveHint: (() => void) | null = null;
    const gate = new Promise<void>((r) => (resolveHint = r));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === 'POST') return { ok: true, status: 201, text: async () => JSON.stringify(CREATED) } as unknown as Response;
        expect(url).toBe('/api/directory/hint?host=api.globex.test');
        await gate;
        return { ok: true, status: 200, text: async () => JSON.stringify({ host: 'api.globex.test', domain: 'globex.test', name: 'Globex', tier: 'claimed', claimed: true }) } as unknown as Response;
      })
    );
    const w = await open({ finding: null, providerHost: null, edge: { host: 'api.globex.test', domain: 'globex.test' }, provider: 'Globex' });
    await openTo(w).setValue('mail.globex.test');
    resolveHint!();
    await flush(w);
    expect((openTo(w).element as HTMLInputElement).value).toBe('mail.globex.test');
  });
});
