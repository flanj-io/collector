// @vitest-environment happy-dom
//
// v1 phase 4 — the QUESTION sheet: the same FlagSheet component with an `edge`
// and no `finding`. It exists because an edge is a registrable domain, not a
// drift, so there is nothing local to attach and the thread carries only what
// the operator writes.
//
// These tests hold the two things that make it honest. (1) Nothing on it claims
// evidence: no "Evidence (1)" line, no request-id sentence, no
// "redacted request/response" in the disclosure, no "read the redacted
// evidence" on the share warning. (2) The message is genuinely required —
// Create thread is inert until it is written, because on a flag the message is
// optional and a copy-paste of that sheet would send an empty thread.
//
// The Connect gate, the 412 handling, the focus trap and the copy path are the
// SAME code as the flag sheet (that is the point of reusing the component), and
// `flag-sheet-modal.test.ts` covers them.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import FlagSheet from './FlagSheet.vue';
import type { Finding } from './types';
import type { ConnectState } from './threads';
import { QUESTION_LEAD, QUESTION_MESSAGE_LABEL, QUESTION_MESSAGE_REQUIRED } from './threads';

const CONNECTED: ConnectState = {
  status: 'connected',
  consumer_display_name: 'Acme',
  contact_email: 'ops@acme.test',
  confirmed_contact_email: 'ops@acme.test'
};

const EDGE = { host: 'api.globex.test', domain: 'globex.test' };

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
  last_seen: '2026-09-02T12:05:00Z'
};

let wrapper: VueWrapper | null = null;

function host(): void {
  document.body.innerHTML = '<div id="app"><div class="page"><main></main><div id="host"></div></div></div>';
}

async function openQuestion(): Promise<VueWrapper> {
  host();
  const w = mount(FlagSheet, {
    attachTo: '#host',
    props: { edge: EDGE, provider: 'Globex Payments', consumer: 'Acme', connect: CONNECTED }
  });
  wrapper = w;
  await w.vm.$nextTick();
  return w;
}

async function openFlag(): Promise<VueWrapper> {
  host();
  const w = mount(FlagSheet, {
    attachTo: '#host',
    props: { finding: FINDING, correlation: null, call: null, provider: 'Acme Payments', consumer: 'Acme', connect: CONNECTED }
  });
  wrapper = w;
  await w.vm.$nextTick();
  return w;
}

const createButton = (w: VueWrapper) => w.findAll('button').find((b) => b.text().startsWith('Create thread') || b.text() === 'Creating…')!;

beforeEach(() => {
  localStorage.clear();
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = '';
});

describe('the question sheet claims no evidence it does not have', () => {
  it('renders no evidence line, and says what the thread carries instead', async () => {
    const w = await openQuestion();
    expect(w.find('.evidence').exists()).toBe(false);
    expect(w.text()).not.toContain('Evidence (1)');
    expect(w.text()).toContain(QUESTION_LEAD);
  });

  it('renders no request-id sentence — nothing was captured, so there are no ids of theirs', async () => {
    const w = await openQuestion();
    expect(w.text()).not.toContain('request ID');
    expect(w.text()).not.toContain('No request IDs were captured');
  });

  it('the disclosure names the domain and the message, never a request/response', async () => {
    const w = await openQuestion();
    // The disclosure is open on a first visit (nothing in localStorage).
    const body = w.find('.disclosure-body').text();
    expect(body).toContain('globex.test');
    expect(body).toContain('Acme');
    expect(body).toContain('ops@acme.test');
    expect(body).not.toContain('redacted request/response');
    expect(body).toContain('No calls, no findings');
  });

  it('the share warning promises a message, not redacted evidence', async () => {
    const w = await openQuestion();
    vi.stubGlobal('fetch', fakeFetch());
    await w.find('textarea').setValue('Are you versioning /v1/refunds?');
    await createButton(w).trigger('click');
    await flush(w);

    const warning = w.find('.warning').text();
    expect(warning).toContain('read your message');
    expect(warning).not.toContain('redacted evidence');
    expect(warning).toContain("Globex Payments's team");
  });

  it('"Copy link + message" pastes the domain and the link — no drift claim, no request id', async () => {
    const w = await openQuestion();
    vi.stubGlobal('fetch', fakeFetch());
    await w.find('textarea').setValue('Are you versioning /v1/refunds?');
    await createButton(w).trigger('click');
    await flush(w);

    const paste = w.find('.paste').text();
    expect(paste).toBe('A question about globex.test — reply here: https://cp.test/t/pub#k=tok');
  });
});

describe('the message is the whole thread, so it is required', () => {
  it('starts EMPTY — a prefill would put words in the operator’s mouth', async () => {
    const w = await openQuestion();
    expect((w.find('textarea').element as HTMLTextAreaElement).value).toBe('');
  });

  it('labels the field as the question, not as an optional message', async () => {
    const w = await openQuestion();
    expect(w.find('.field-label').text()).toBe(QUESTION_MESSAGE_LABEL);
    expect(w.text()).not.toContain('Message (optional)');
  });

  it('holds Create thread inert until something is written, and says why', async () => {
    const w = await openQuestion();
    const fetchSpy = vi.fn();
    vi.stubGlobal('fetch', fetchSpy);

    expect(createButton(w).attributes('disabled')).toBeDefined();
    expect(w.text()).toContain(QUESTION_MESSAGE_REQUIRED);

    // Whitespace is not a question either.
    await w.find('textarea').setValue('   ');
    expect(createButton(w).attributes('disabled')).toBeDefined();

    await w.find('textarea').setValue('Are you versioning /v1/refunds?');
    expect(createButton(w).attributes('disabled')).toBeUndefined();
    expect(w.text()).not.toContain(QUESTION_MESSAGE_REQUIRED);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('posts to the edge route with the host and one stable request id', async () => {
    const w = await openQuestion();
    const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
    vi.stubGlobal('fetch', fakeFetch(calls));

    await w.find('textarea').setValue('Are you versioning /v1/refunds?');
    await createButton(w).trigger('click');
    await flush(w);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/edges/thread');
    expect(calls[0].body.host).toBe('api.globex.test');
    expect(calls[0].body.message).toBe('Are you versioning /v1/refunds?');
    expect(typeof calls[0].body.request_id).toBe('string');
    expect((calls[0].body.request_id as string).length).toBeGreaterThan(8);
    // No finding id on this route, and never the flag route.
    expect(calls[0].body.finding_id).toBeUndefined();
  });
});

describe('the flag sheet is unchanged', () => {
  it('still prefills an OPTIONAL message, shows Evidence (1), and posts to /api/flag', async () => {
    const w = await openFlag();
    expect(w.find('.evidence').text()).toContain('Evidence (1):');
    expect(w.find('.field-label').text()).toBe('Message (optional)');
    expect((w.find('textarea').element as HTMLTextAreaElement).value.length).toBeGreaterThan(0);
    expect(createButton(w).attributes('disabled')).toBeUndefined();
    expect(w.text()).not.toContain(QUESTION_MESSAGE_REQUIRED);

    const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
    vi.stubGlobal('fetch', fakeFetch(calls));
    await createButton(w).trigger('click');
    await flush(w);
    expect(calls[0].url).toBe('/api/flag');
    expect(calls[0].body.finding_id).toBe('f1');
    expect(w.find('.warning').text()).toContain('redacted evidence');
  });

  it('an EMPTY message is still allowed on a flag — the evidence carries it', async () => {
    const w = await openFlag();
    await w.find('textarea').setValue('');
    expect(createButton(w).attributes('disabled')).toBeUndefined();
  });
});

/** A relay that answers every POST with a created thread. */
function fakeFetch(record?: Array<{ url: string; body: Record<string, unknown> }>) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    record?.push({ url, body: JSON.parse(String(init?.body ?? '{}')) });
    return {
      ok: true,
      status: 201,
      text: async () =>
        JSON.stringify({
          thread_id: 'thr_1',
          thread_public_id: 'pub',
          thread_url: 'https://cp.test/t/pub#k=tok',
          state: 'open',
          status: 'created'
        })
    } as unknown as Response;
  });
}

async function flush(w: VueWrapper): Promise<void> {
  for (let i = 0; i < 6; i += 1) {
    await Promise.resolve();
    await w.vm.$nextTick();
  }
}
