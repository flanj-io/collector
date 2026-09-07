// @vitest-environment happy-dom
//
// The Connect panel's pending state, asserted on the real component. The defect (2026-09-07,
// reproduced on sqlite, postgres and tiered): after the very FIRST Connect the panel printed the
// deck's "Check your inbox — we sent …" line AND "Sent again to …" — a resend that never happened
// (Mailpit held exactly one mail). One string served both the Connect button and Resend.
// connect-mail.test.ts pins the wording per branch; THIS file pins that the two call sites reach
// the helper with the right flag — a call site passing the wrong one is invisible to a pure test.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import ConnectPanel from './ConnectPanel.vue';
import type { ConnectState } from './threads';

const ORG = 'Acme Consumer Ltd';
const EMAIL = 'ops@acme.example';
const NEW_EMAIL = 'dana@acme.example';

/** What the relay answers to a register call that sent a mail: 202 + the mail's own outcome. */
const pendingReply = (contact_email: string): ConnectState => ({
  status: 'pending',
  consumer_display_name: ORG,
  contact_email,
  collector_public_id: 'col_test',
  registered_at: '2026-09-07T12:00:00Z',
  cp_configured: true,
  confirmation_mail: 'sent'
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

/** Every POST /api/connect the panel made, in order — the honest count of sends attempted. */
let posts: Array<Record<string, unknown>> = [];

function stubRelay() {
  posts = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input).split('?')[0];
      const method = (init?.method ?? 'GET').toUpperCase();
      if (path.endsWith('/api/connect') && method === 'POST') {
        const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
        posts.push(body);
        return json(pendingReply(String(body.contact_email)), 202);
      }
      return json({}, 404);
    })
  );
}

async function settle(w: VueWrapper) {
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await w.vm.$nextTick();
}

/** The parent (App) owns the state: feed the panel's `update:state` back into its prop. */
async function adoptEmittedState(w: VueWrapper) {
  const emitted = w.emitted<[ConnectState]>('update:state') ?? [];
  const last = emitted.at(-1);
  expect(last, 'the panel must emit the relay reply as the new state').toBeTruthy();
  await w.setProps({ state: last![0] });
  await settle(w);
}

async function submitForm(w: VueWrapper, email: string) {
  await w.find('input[autocomplete="organization"]').setValue(ORG);
  await w.find('input[type="email"]').setValue(email);
  await w.find('form').trigger('submit');
  await settle(w);
  await adoptEmittedState(w);
}

function pendingText(w: VueWrapper): string {
  const panel = w.find('.connect-state.pending');
  expect(panel.exists(), 'the pending panel must be on screen').toBe(true);
  return panel.text().replace(/\s+/g, ' ');
}

function button(w: VueWrapper, label: string) {
  const b = w.findAll('button').find((x) => x.text().trim() === label);
  expect(b, `a "${label}" button must be on screen`).toBeTruthy();
  return b!;
}

let wrapper: VueWrapper | null = null;

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.unstubAllGlobals();
});

describe('Connect panel: the first send says "Check your inbox" and nothing more; only Resend says "Sent again"', () => {
  it('first Connect → the deck line alone; Resend → "Sent again"; Change email to a new address → the deck line alone again', async () => {
    stubRelay();
    wrapper = mount(ConnectPanel, { props: { state: null, defaultOrg: ORG } });
    const w = wrapper;

    // (1) The very first send. One POST, one mail — and the panel must not claim a second one.
    await submitForm(w, EMAIL);
    expect(posts).toHaveLength(1);
    let text = pendingText(w);
    expect(text).toContain(`Check your inbox — we sent "Confirm your Flanj contact" to ${EMAIL}. The link works once, for 72 hours.`);
    expect(text, 'a first send is not a resend').not.toContain('Sent again');
    expect(w.find('.connect-note').exists(), 'no extra note under the standing line on a first send').toBe(false);
    expect(button(w, 'Resend').attributes('disabled')).toBeUndefined();

    // (2) Resend: a second mail really went out, so "Sent again" is now the truth.
    await button(w, 'Resend').trigger('click');
    await settle(w);
    await adoptEmittedState(w);
    expect(posts).toHaveLength(2);
    text = pendingText(w);
    expect(text).toContain('Check your inbox');
    expect(text).toContain(`Sent again to ${EMAIL}.`);

    // (3) Change email to a NEW address: the first mail to that address — no "again" for it.
    await button(w, 'Change email').trigger('click');
    await settle(w);
    expect(w.find('form.connect-form').exists()).toBe(true);
    await submitForm(w, NEW_EMAIL);
    expect(posts).toHaveLength(3);
    expect(posts[2].contact_email).toBe(NEW_EMAIL);
    text = pendingText(w);
    expect(text).toContain(`we sent "Confirm your Flanj contact" to ${NEW_EMAIL}`);
    expect(text, 'the first send to a new address is not a resend').not.toContain('Sent again');
  });

  it('a background poll (state without confirmation_mail) never conjures a "Sent again" of its own', async () => {
    stubRelay();
    wrapper = mount(ConnectPanel, { props: { state: null, defaultOrg: ORG } });
    const w = wrapper;
    await submitForm(w, EMAIL);
    // The ~5s poll carries the contact's state and no mail outcome (CONTRACTS-CP §5.1).
    const polled: ConnectState = { ...pendingReply(EMAIL), confirmation_mail: undefined };
    await w.setProps({ state: polled });
    await settle(w);
    const text = pendingText(w);
    expect(text).toContain('Check your inbox');
    expect(text).not.toContain('Sent again');
    expect(posts).toHaveLength(1);
  });
});
