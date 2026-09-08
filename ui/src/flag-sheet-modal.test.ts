// @vitest-environment happy-dom
//
// The Flag sheet says `aria-modal="true"` — a promise that, while it is open,
// the rest of the page is unreachable. Launch-week item 9 found it was not:
// nothing behind the sheet was inert, Tab walked straight out into the
// Overview, and closing it dropped focus on <body>. A screen reader believed
// the page was gone while a keyboard user was standing in it — the attribute
// lying is worse than its absence. These tests hold the promise on the real
// component: the page behind is inert exactly while the sheet is open, Tab and
// Shift+Tab wrap inside it, Escape still closes, and focus goes back to the
// control that opened it.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import FlagSheet from './FlagSheet.vue';
import type { Finding } from './types';
import type { ConnectState } from './threads';

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

// Connected with a confirmed contact → the compose phase (disclosure toggle,
// message, Create thread, Cancel), which is the sheet keyboard users meet.
const CONNECTED: ConnectState = {
  status: 'connected',
  consumer_display_name: 'Acme',
  contact_email: 'ops@acme.test',
  confirmed_contact_email: 'ops@acme.test'
};

/** A page with something behind the sheet: the Flag button that opens it and
 *  a control a leaked Tab would land on. The sheet mounts where App mounts it —
 *  a sibling of the page's content, inside the app root — so the inert walk
 *  has real siblings to mark. */
function page(): HTMLButtonElement {
  document.body.innerHTML = `
    <div id="app">
      <div class="page">
        <main>
          <div class="actions"><button id="trigger" type="button">Flag this</button></div>
          <input id="behind" />
        </main>
        <div id="host"></div>
      </div>
    </div>`;
  const trigger = document.getElementById('trigger') as HTMLButtonElement;
  trigger.focus();
  return trigger;
}

let wrapper: VueWrapper | null = null;

async function open(): Promise<VueWrapper> {
  const w = mount(FlagSheet, {
    attachTo: '#host',
    props: { finding: FINDING, correlation: null, call: null, provider: 'Acme Payments', consumer: 'Acme', connect: CONNECTED }
  });
  wrapper = w;
  await w.vm.$nextTick();
  return w;
}

const main = () => document.querySelector('main') as HTMLElement;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const controls = () => Array.from(dialog().querySelectorAll<HTMLElement>('button:not([disabled]), textarea:not([disabled])'));
const first = () => controls()[0];
const last = () => controls()[controls().length - 1];

/** A key press as the browser delivers it: on the focused element, bubbling
 *  up to the document listener. Returns whether the sheet claimed it. */
function press(key: string, shiftKey = false): boolean {
  const ev = new KeyboardEvent('keydown', { key, shiftKey, bubbles: true, cancelable: true });
  (document.activeElement ?? document).dispatchEvent(ev);
  return ev.defaultPrevented;
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the flag sheet is the modal it claims to be', () => {
  it('marks the page behind it inert while open, and un-marks it on close', async () => {
    page();
    const w = await open();
    expect(main().hasAttribute('inert'), 'the content behind the sheet').toBe(true);
    // ...but never the sheet's own line of descent — an inert dialog is a dead one.
    expect(dialog().closest('[inert]')).toBeNull();

    w.unmount();
    wrapper = null;
    expect(main().hasAttribute('inert')).toBe(false);
  });

  it('opens with focus inside the sheet', async () => {
    page();
    await open();
    expect(dialog().contains(document.activeElement)).toBe(true);
  });

  it('Tab from the last control wraps to the first', async () => {
    page();
    await open();
    last().focus();
    expect(document.activeElement).toBe(last());
    expect(press('Tab')).toBe(true);
    expect(document.activeElement).toBe(first());
  });

  it('Shift+Tab from the first control wraps to the last', async () => {
    page();
    await open();
    first().focus();
    expect(press('Tab', true)).toBe(true);
    expect(document.activeElement).toBe(last());
  });

  it('leaves a Tab between two inner controls to the browser', async () => {
    page();
    await open();
    const textarea = dialog().querySelector('textarea') as HTMLTextAreaElement;
    textarea.focus();
    expect(press('Tab')).toBe(false);
    expect(document.activeElement).toBe(textarea);
  });

  it('pulls focus back in when it is nowhere (body) and Tab is pressed', async () => {
    page();
    await open();
    (document.activeElement as HTMLElement).blur();
    expect(dialog().contains(document.activeElement)).toBe(false);
    expect(press('Tab')).toBe(true);
    expect(document.activeElement).toBe(first());
    expect(press('Tab', true)).toBe(true);
    expect(document.activeElement).toBe(last());
  });

  it('keeps focus inside while a create is in flight (the disclosure toggle is the one live control)', async () => {
    // A POST that never answers keeps `busy` set: textarea + both buttons are
    // disabled, so the trap has a single control to wrap onto — itself.
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})));
    page();
    const w = await open();
    await w.get('button.primary').trigger('click');
    expect(controls()).toHaveLength(1);
    const toggle = controls()[0];
    toggle.focus();
    expect(press('Tab')).toBe(true);
    expect(document.activeElement).toBe(toggle);
    expect(press('Tab', true)).toBe(true);
    expect(document.activeElement).toBe(toggle);
  });

  it('Escape still closes', async () => {
    page();
    const w = await open();
    press('Escape');
    expect(w.emitted('close')).toHaveLength(1);
  });

  it('returns focus to the Flag button that opened it', async () => {
    const trigger = page();
    const w = await open();
    expect(document.activeElement).not.toBe(trigger);
    w.unmount();
    wrapper = null;
    expect(document.activeElement).toBe(trigger);
  });

  it('lands where the opener stood when the opener is gone (the row now shows the In-thread chip)', async () => {
    const trigger = page();
    const w = await open();
    // Create thread → Done: the row has swapped Flag this for the chip + View thread.
    const actions = trigger.parentElement as HTMLElement;
    trigger.remove();
    actions.innerHTML = '<span class="chip">In thread</span><button id="view" type="button">View thread</button>';
    w.unmount();
    wrapper = null;
    expect(document.activeElement).toBe(document.getElementById('view'));
  });

  it('Tab and Shift+Tab from the sheet div itself (focus on no control) wrap into the controls', async () => {
    page();
    await open();
    const sheet = dialog();
    sheet.focus();
    expect(document.activeElement).toBe(sheet);
    press('Tab', true);
    expect(document.activeElement).toBe(last());
    sheet.focus();
    press('Tab');
    expect(document.activeElement).toBe(first());
  });
});
