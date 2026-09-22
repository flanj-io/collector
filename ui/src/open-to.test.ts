// @vitest-environment happy-dom
//
// thread-domain-gate — "Who can open it". The sheet
// offers three modes, always in this order — Only specific people, Anyone at a
// domain (checked), Anyone with the link — each followed by its own input and
// help, and only the selected option's are on the page. This file holds, on the
// real component, with the spec's strings written out (a copy change must fail
// here, not slip through a shared constant):
//   1. order, labels, the default, and that only the selected option renders;
//   2. guards wait for a touch or a Create; Create thread stays enabled, and a
//      Create with a guard posts nothing and puts the operator in the field;
//   3. what is typed: `Name <addr>`, the cross-field hints, long entries;
//   4. the aria wiring of each input to its label, help and guard;
//   5. the POST body per mode — always both keys;
//   6. the success warning per mode;
//   7. the directory prefill and both domain helps.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import FlagSheet from './FlagSheet.vue';
import type { Finding } from './types';
import type { ConnectState } from './threads';

const CONNECTED: ConnectState = {
  status: 'connected',
  workspace_display_name: 'Acme',
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

const FLAG_PROPS = { finding: FINDING, correlation: null, call: null, provider: 'Acme Payments', consumer: 'Acme', connect: CONNECTED, providerHost: 'api.acme-payments.test' };

const DOMAINS_HELP = "Their email domain, like acme.com — not always the API's domain. Separate several with commas.";
const EMAILS_HELP = 'Only these addresses can open the thread, after confirming once.';

interface Recorded {
  url: string;
  body: Record<string, unknown>;
}

/** The relay: every POST creates a thread (or never answers, with `hang`); the hint GET answers as `hint` says. */
function stubFetch(record: Recorded[], hint: Record<string, unknown> | null = null, hang = false) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        record.push({ url, body: JSON.parse(String(init.body ?? '{}')) });
        if (hang) return new Promise<Response>(() => {});
        return { ok: true, status: 201, text: async () => JSON.stringify(CREATED) } as unknown as Response;
      }
      if (hint === null) return { ok: false, status: 404, text: async () => JSON.stringify({ error: 'store_error', message: 'x' }) } as unknown as Response;
      return { ok: true, status: 200, text: async () => JSON.stringify(hint) } as unknown as Response;
    })
  );
}

let wrapper: VueWrapper | null = null;

async function open(props: Record<string, unknown> = {}): Promise<VueWrapper> {
  wrapper?.unmount();
  document.body.innerHTML = '<div id="app"><main></main><div id="host"></div></div>';
  const w = mount(FlagSheet, { attachTo: '#host', props: { ...FLAG_PROPS, ...props } });
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

type Mode = 'emails' | 'domains' | 'anyone';
const createButton = (w: VueWrapper) =>
  w.findAll('button').find((b) => b.text().startsWith('Create thread') || b.text() === 'Creating…' || b.text() === 'Retry')!;
const radio = (w: VueWrapper, mode: Mode) => w.find(`input[name="open_to_mode"][value="${mode}"]`);
const choose = (w: VueWrapper, mode: Mode) => radio(w, mode).setValue(true);
const domains = (w: VueWrapper) => w.find('input.open-to');
const emails = (w: VueWrapper) => w.find('input.open-to-emails');
const guard = (w: VueWrapper) => w.find('p.open-to-guard');
/** The guard paragraph is always in the DOM (its id must resolve); it shows only while it has something to say. */
const guardShown = (w: VueWrapper) => {
  const g = guard(w);
  return g.exists() && (g.element as HTMLElement).style.display !== 'none' && g.text() !== '';
};
const notes = (w: VueWrapper) => w.findAll('.open-to-note').map((n) => n.text());
async function create(w: VueWrapper): Promise<void> {
  await createButton(w).trigger('click');
  await flush(w);
}
/** The option block right after a radio's label — present only for the selected mode. */
function detailAfter(w: VueWrapper, mode: Mode): Element | null {
  const next = (radio(w, mode).element as HTMLInputElement).closest('label')?.nextElementSibling ?? null;
  return next && next.classList.contains('open-to-detail') ? next : null;
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

describe('the choice: three modes, in spec order', () => {
  it('a "Who can open it" fieldset with three radios — people, a domain (checked), anyone', async () => {
    stubFetch([]);
    const w = await open();
    const fieldset = w.find('fieldset.open-to-choice');
    expect(fieldset.exists()).toBe(true);
    expect(fieldset.find('legend').text()).toBe('Who can open it');
    expect(fieldset.find('legend').classes()).toContain('field-label');
    const radios = fieldset.findAll('input[name="open_to_mode"]');
    expect(radios.map((r) => r.attributes('type'))).toEqual(['radio', 'radio', 'radio']);
    expect(radios.map((r) => r.attributes('value'))).toEqual(['emails', 'domains', 'anyone']);
    expect(radios.map((r) => (r.element.closest('label') as HTMLElement).textContent?.trim())).toEqual([
      'Only specific people',
      'Anyone at a domain',
      'Anyone with the link — not recommended'
    ]);
    expect(radios.map((r) => (r.element as HTMLInputElement).checked)).toEqual([false, true, false]);
  });

  it('each option’s input and help sit right after its radio, and only the selected option’s render', async () => {
    stubFetch([]);
    const w = await open();
    expect(detailAfter(w, 'emails')).toBeNull();
    expect(detailAfter(w, 'anyone')).toBeNull();
    expect(detailAfter(w, 'domains')?.querySelector('input.open-to[name="allowed_domains"]')).not.toBeNull();
    expect(domains(w).attributes('placeholder')).toBe('their-company.com');
    expect(emails(w).exists()).toBe(false);
    expect(notes(w)).toEqual([DOMAINS_HELP]);

    await choose(w, 'emails');
    expect(detailAfter(w, 'domains')).toBeNull();
    expect(detailAfter(w, 'emails')?.querySelector('input.open-to-emails[name="allowed_emails"]')).not.toBeNull();
    expect(emails(w).attributes('placeholder')).toBe('dana@their-company.com, sam@their-company.com');
    expect(domains(w).exists()).toBe(false);
    expect(notes(w)).toEqual([EMAILS_HELP]);

    await choose(w, 'anyone');
    expect(detailAfter(w, 'emails')).toBeNull();
    expect(detailAfter(w, 'anyone')?.querySelector('input')).toBeNull();
    expect(w.findAll('fieldset.open-to-choice input[type="text"]')).toHaveLength(0);
    expect(notes(w)).toEqual(['Anyone holding the link can read the redacted evidence. Paste it only where just their team can see it.']);
  });

  it('what was typed in one mode is still there after a trip through another', async () => {
    stubFetch([]);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('dana@acme.test');
    await choose(w, 'anyone');
    await choose(w, 'emails');
    expect((emails(w).element as HTMLInputElement).value).toBe('dana@acme.test');
  });
});

describe('guards wait for a touch or a Create, and Create thread stays enabled', () => {
  it('opens with no guard, and Create thread enabled though the field is empty', async () => {
    stubFetch([]);
    const w = await open();
    expect(guardShown(w)).toBe(false);
    expect(createButton(w).attributes('disabled')).toBeUndefined();
    await choose(w, 'emails');
    expect(guardShown(w)).toBe(false);
  });

  it('a Create with a guard posts nothing, shows the guard and focuses the field', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await create(w);
    expect(calls).toHaveLength(0);
    expect(guard(w).text()).toBe('Add at least one domain.');
    expect(document.activeElement).toBe(domains(w).element);

    await choose(w, 'emails');
    // The attempt stands: the field that just appeared says what it needs at once.
    expect(guard(w).text()).toBe('Add at least one email address.');
    await create(w);
    expect(calls).toHaveLength(0);
    expect(document.activeElement).toBe(emails(w).element);
  });

  it('a touched field shows its guard while typing, and clears it the moment the list is usable', async () => {
    stubFetch([]);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('dana');
    expect(guard(w).text()).toBe('"dana" is not an email address — write each like dana@acme.com.');
    await emails(w).setValue('dana@acme.test');
    expect(guardShown(w)).toBe(false);
    await emails(w).setValue('');
    expect(guard(w).text()).toBe('Add at least one email address.');

    await choose(w, 'domains');
    await domains(w).setValue('acme.test/pay');
    expect(guard(w).text()).toBe('"acme.test/pay" is not a domain — write each like acme.com, with no @, path or port.');
  });

  it('more than 20 is refused in either list', async () => {
    stubFetch([]);
    const w = await open();
    await domains(w).setValue(Array.from({ length: 21 }, (_, i) => `d${i}.test`).join(', '));
    expect(guard(w).text()).toBe('A thread can be open to at most 20 domains.');
    await choose(w, 'emails');
    await emails(w).setValue(Array.from({ length: 21 }, (_, i) => `p${i}@acme.test`).join(', '));
    expect(guard(w).text()).toBe('A thread can be open to at most 20 people.');
  });

  it('Create thread disables while a create is in flight', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls, null, true);
    const w = await open();
    expect(createButton(w).attributes('disabled')).toBeUndefined();

    await domains(w).setValue('acme-payments.test');
    await create(w);
    expect(calls).toHaveLength(1);
    expect(createButton(w).text()).toBe('Creating…');
    expect(createButton(w).attributes('disabled')).toBeDefined();
    expect(radio(w, 'emails').attributes('disabled')).toBeDefined();
    expect(domains(w).attributes('disabled')).toBeDefined();
  });
});

describe('what is typed', () => {
  it('a pasted "Name <addr>" list reads as the addresses inside the brackets', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('Dana Lee <Dana@Acme.test>; sam@acme.test\n"Sam" <SAM@acme.test>, kim@globex.test');
    expect(guardShown(w)).toBe(false);
    await create(w);
    expect(calls).toHaveLength(1);
    expect(calls[0].body.allowed_emails).toEqual(['dana@acme.test', 'sam@acme.test', 'kim@globex.test']);
  });

  it('a domain typed as a person points at Anyone at a domain', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('dana@acme.test, acme.test');
    expect(guard(w).text()).toBe('"acme.test" is a domain — choose Anyone at a domain, or write an address like dana@acme.test.');
    await create(w);
    expect(calls).toHaveLength(0);
  });

  it('an address typed as a domain names the domain to write instead', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await domains(w).setValue('acme.test, Dana@Globex.test');
    expect(guard(w).text()).toBe('"Dana@Globex.test" is an address — write just the domain, globex.test.');
    await create(w);
    expect(calls).toHaveLength(0);
  });

  it('an entry over 48 characters shows as its first 48 and an ellipsis, and the guard wraps anywhere (QA 2026-09-14)', async () => {
    stubFetch([]);
    const w = await open();
    await domains(w).setValue(`${'x'.repeat(16_000)}/path`);
    expect(guard(w).text()).toBe(`"${'x'.repeat(48)}…" is not a domain — write each like acme.com, with no @, path or port.`);

    await choose(w, 'emails');
    await emails(w).setValue(`${'a'.repeat(60)}.test`);
    expect(guard(w).text()).toBe(`"${'a'.repeat(48)}…" is a domain — choose Anyone at a domain, or write an address like dana@${'a'.repeat(48)}….`);

    // The rule itself: scoped styles are not computed under happy-dom, so read the source.
    const source = readFileSync(join(__dirname, 'FlagSheet.vue'), 'utf8');
    expect(source).toMatch(/\.guard\s*\{[^}]*overflow-wrap:\s*anywhere/);
  });
});

describe('aria', () => {
  it('each input is labelled by its option, described by its help and guard, and aria-invalid only while the guard shows', async () => {
    stubFetch([]);
    const w = await open();
    const seen = new Set<string>();
    for (const [mode, input, label, valid] of [
      ['domains', domains, 'Anyone at a domain', 'acme.test'],
      ['emails', emails, 'Only specific people', 'dana@acme.test']
    ] as const) {
      await choose(w, mode);
      const [helpId, guardId, ...rest] = (input(w).attributes('aria-describedby') ?? '').split(' ');
      expect(rest).toEqual([]);
      expect(document.getElementById(helpId)?.classList.contains('open-to-note')).toBe(true);
      expect(document.getElementById(input(w).attributes('aria-labelledby') ?? '')?.textContent).toBe(label);
      expect(input(w).attributes('aria-invalid')).toBeUndefined();

      // The guard's id resolves BEFORE it has anything to say, hidden and empty inside a live wrapper that is already
      // in the DOM (QA 2026-09-15: a v-if guard left aria-describedby dangling and arrived with its own live region,
      // so a screen reader could miss it).
      const idle = document.getElementById(guardId);
      expect(idle, 'the guard id resolves before the guard speaks').not.toBeNull();
      expect(idle!.textContent).toBe('');
      expect((idle as HTMLElement).style.display).toBe('none');
      expect(idle!.parentElement?.getAttribute('aria-live')).toBe('polite');

      await input(w).setValue('not valid');
      expect(guard(w).attributes('id')).toBe(guardId);
      expect(guardShown(w)).toBe(true);
      // ...the same element, inside the live wrapper that was there BEFORE the guard, so its arrival is announced.
      expect(guard(w).element).toBe(idle);
      expect(guard(w).element.parentElement?.getAttribute('aria-live')).toBe('polite');
      expect(input(w).attributes('aria-invalid')).toBe('true');

      await input(w).setValue(valid);
      expect(input(w).attributes('aria-invalid')).toBeUndefined();
      for (const id of [helpId, guardId]) {
        expect(seen.has(id)).toBe(false);
        seen.add(id);
      }
    }
  });
});

describe('what reaches the wire: always both keys', () => {
  it('Anyone at a domain: the normalized, de-duplicated list, and allowed_emails null', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await domains(w).setValue(' Acme-Payments.test, @acme-payments.test globex.test. ');
    await create(w);
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe('/api/flag');
    expect(calls[0].body.allowed_domains).toEqual(['acme-payments.test', 'globex.test']);
    expect('allowed_emails' in calls[0].body).toBe(true);
    expect(calls[0].body.allowed_emails).toBeNull();
  });

  it('Only specific people: the normalized list, and allowed_domains null', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('Dana@Acme.test dana@acme.test sam@acme.test');
    await create(w);
    expect(calls).toHaveLength(1);
    expect(calls[0].body.allowed_emails).toEqual(['dana@acme.test', 'sam@acme.test']);
    expect('allowed_domains' in calls[0].body).toBe(true);
    expect(calls[0].body.allowed_domains).toBeNull();
  });

  it('Anyone with the link: both keys present, both null', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls);
    const w = await open();
    await domains(w).setValue('acme-payments.test');
    await choose(w, 'anyone');
    await create(w);
    expect(calls).toHaveLength(1);
    expect('allowed_domains' in calls[0].body && 'allowed_emails' in calls[0].body).toBe(true);
    expect(calls[0].body.allowed_domains).toBeNull();
    expect(calls[0].body.allowed_emails).toBeNull();
  });
});

describe('the success warning says who can open it', () => {
  const TAIL_FLAG = "Nobody else can read it. Paste it where you already talk to Acme Payments's team. It lasts 30 days and extends with each reply.";

  it('a domain thread names every domain with its @', async () => {
    stubFetch([]);
    const w = await open();
    await domains(w).setValue('acme-payments.test, globex.test');
    await create(w);
    expect(w.find('.warning').text()).toBe(
      `People with an @acme-payments.test or @globex.test address can open this link — they confirm it once, then read the redacted evidence and reply. ${TAIL_FLAG}`
    );
  });

  it('a people thread names up to three addresses', async () => {
    stubFetch([]);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('a@acme.test, b@acme.test, c@acme.test');
    await create(w);
    expect(w.find('.warning').text()).toBe(
      `Only a@acme.test, b@acme.test and c@acme.test can open this link — they confirm their address once, then read the redacted evidence and reply. ${TAIL_FLAG}`
    );
  });

  it('more than three people: the first three and "and N others"', async () => {
    stubFetch([]);
    const w = await open();
    await choose(w, 'emails');
    await emails(w).setValue('a@acme.test, b@acme.test, c@acme.test, d@acme.test, e@acme.test');
    await create(w);
    expect(w.find('.warning').text()).toBe(
      `Only a@acme.test, b@acme.test, c@acme.test and 2 others can open this link — they confirm their address once, then read the redacted evidence and reply. ${TAIL_FLAG}`
    );
  });

  it('Anyone with the link keeps the open-link sentence', async () => {
    stubFetch([]);
    const w = await open();
    await choose(w, 'anyone');
    await create(w);
    expect(w.find('.warning').text()).toBe(
      "Anyone with this link can read the redacted evidence and reply. Paste it where you already talk to Acme Payments's team. It lasts 30 days and extends with each reply."
    );
  });
});

describe('the directory prefill', () => {
  const claimed = { host: 'api.acme-payments.test', domain: 'acme-payments.test', name: 'Acme Payments', tier: 'claimed', claimed: true };
  const curated = { ...claimed, tier: 'curated', claimed: false };

  it('prefills a CLAIMED domain and says where it came from; an edit swaps in the plain help', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls, claimed);
    const w = await open();
    expect((domains(w).element as HTMLInputElement).value).toBe('acme-payments.test');
    expect(notes(w)).toEqual([
      'Prefilled from the Flanj directory: acme-payments.test is verified. Change it if their email addresses end in something else.'
    ]);
    expect(guardShown(w)).toBe(false);

    await domains(w).setValue('mail.acme-payments.test');
    expect(notes(w)).toEqual([DOMAINS_HELP]);
    await create(w);
    expect(calls[0].body.allowed_domains).toEqual(['mail.acme-payments.test']);
  });

  it('a curated entry proves nothing about a mailbox: nothing prefilled, the plain help', async () => {
    const calls: Recorded[] = [];
    stubFetch(calls, curated);
    const w = await open();
    expect((domains(w).element as HTMLInputElement).value).toBe('');
    expect(notes(w)).toEqual([DOMAINS_HELP]);
    await create(w);
    expect(calls).toHaveLength(0);
  });

  it('asks about the provider host, and never overwrites what was typed', async () => {
    let resolveHint: (() => void) | null = null;
    const gate = new Promise<void>((r) => (resolveHint = r));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === 'POST') return { ok: true, status: 201, text: async () => JSON.stringify(CREATED) } as unknown as Response;
        expect(url).toBe('/api/directory/hint?host=api.acme-payments.test');
        await gate;
        return { ok: true, status: 200, text: async () => JSON.stringify({ host: 'api.acme-payments.test', domain: 'acme-payments.test', name: 'Acme', tier: 'claimed', claimed: true }) } as unknown as Response;
      })
    );
    const w = await open();
    await domains(w).setValue('mail.acme-payments.test');
    resolveHint!();
    await flush(w);
    expect((domains(w).element as HTMLInputElement).value).toBe('mail.acme-payments.test');
    expect(notes(w)).toEqual([DOMAINS_HELP]);
  });
});
