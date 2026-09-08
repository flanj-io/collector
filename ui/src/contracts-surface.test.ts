// @vitest-environment happy-dom
//
// The two Contracts-surface defects, asserted on the real components.
//
//  1. A version-diff finding reached NO surface. It was in the Finding union
//     and produced by the upload path, but the tab built its rows from
//     live-vs-spec + the two MCP kinds — so the uploader announced "N breaking
//     changes against the version it replaced" over a tab showing none of them,
//     and the control plane's `#contracts/<id>` deep link highlighted nothing.
//
//  2. The uploader had no keyboard path at all. The picker was a <label> (not
//     focusable) around an input the `hidden` attribute takes out of the tab
//     order, the drop zone had neither tabindex nor key handler, and focus never
//     entered the panel when it opened. With paste-the-document and URL fetch
//     both cut from v1 the picker is the ONLY way a contract enters the
//     collector, so this was not awkwardness — it was an unreachable Aha.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import ContractUploader from './ContractUploader.vue';

const HOST = 'api.acme.test';
const INTEGRATION = 'api-acme-test';

const CONTRACT = {
  integration: INTEGRATION,
  role: 'provider',
  peer_host: HOST,
  format: 'openapi',
  title: 'Acme Payments',
  version: '2.0.0',
  prev_version: '1.0.0',
  endpoints: 3,
  loaded_at: '2026-09-02T12:00:00Z',
  edge_class: 'external',
  source: 'upload'
};

/** What the upload path stores when a replace diffs breaking: severity
 *  `breaking`, no source call, and the contract's own integration — which is
 *  the only thing that can join it to a card. */
const VERSION_DIFF = {
  id: 'fnd_versiondiff_1',
  kind: 'version-diff',
  severity: 'breaking',
  integration: INTEGRATION,
  endpoint: 'POST /v1/charges',
  field_path: 'amount type/format integer/int64 string 200',
  location: null,
  expected: 'spec 1.0.0',
  actual: 'spec 2.0.0',
  rule: 'response-property-type-changed',
  spec_version_from: '1.0.0',
  spec_version_to: '2.0.0',
  source_call_id: null,
  detected_at: '2026-09-02T12:00:01Z',
  detail: 'the `amount` response property type changed from `integer` to `string` for status `200`',
  occurrence_count: 1
};

const HEALTH = {
  status: 'ok',
  integration: 'acme-payments',
  window_rows: 0,
  calls: 0,
  findings: 1,
  cp_configured: true,
  connect_status: 'disconnected',
  collector_version: 'v0.0.0-test'
};

const OK_BODIES: Record<string, unknown> = {
  '/api/health': HEALTH,
  '/api/findings': { findings: [VERSION_DIFF] },
  '/api/calls': { calls: [] },
  '/api/edges': { edges: [] },
  '/api/contracts': { contracts: [CONTRACT] },
  '/api/connect': { status: 'disconnected' },
  '/api/threads': { threads: [], total: 0, has_more: false }
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = OK_BODIES[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
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
  stubFetch();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/* ── 1. The version diff reaches its card ──────────────────────────────── */

describe('a version-diff finding renders on the card its contract owns', () => {
  async function mountApp(): Promise<VueWrapper> {
    const w = mount(App, { attachTo: document.body });
    wrapper = w;
    await vi.advanceTimersByTimeAsync(0);
    await settle(w);
    return w;
  }

  it('renders as a row inside the provider card, not on a card of its own', async () => {
    const w = await mountApp();
    const cards = w.findAll('.provider');
    expect(cards.length).toBe(1);

    const row = cards[0].find(`#finding-${VERSION_DIFF.id}`);
    expect(row.exists()).toBe(true);
    expect(row.text()).toContain(VERSION_DIFF.endpoint);
    expect(row.text()).toContain(VERSION_DIFF.rule);
  });

  it('carries the deep-link anchor the control plane sends people to', async () => {
    const w = await mountApp();
    // `#contracts/<finding_id>` — the findings page's link into this UI. The
    // anchor has to exist for the highlight to land on anything.
    expect(document.getElementById(`finding-${VERSION_DIFF.id}`)).not.toBeNull();
  });

  it('counts in the red tab pill, because the model calls it breaking', async () => {
    const w = await mountApp();
    // Two-tier taxonomy: red counts breaking-severity rows from EVERY source —
    // severity decides the tier, never the protocol. A version diff is
    // severity=breaking (CONTRACTS §4), so it belongs in the red count.
    const red = w.find('.tab-count.bad');
    expect(red.exists()).toBe(true);
    expect(red.text()).toBe('1');
    // ...and in no other tier.
    expect(w.find('.tab-count.warn').exists()).toBe(false);
    // The per-card chip is the same taxonomy, so the sum of chips equals the pill.
    expect(w.find('.provider').text()).toContain('1');
  });

  it('does not fabricate a call count for a finding found by diffing documents', async () => {
    const w = await mountApp();
    const row = w.find(`#finding-${VERSION_DIFF.id}`);
    expect(row.text()).not.toContain('1 call');
    expect(row.find('.occ').exists()).toBe(false);
  });

  it('says why it has no Flag control without calling a breaking finding informational', async () => {
    const w = await mountApp();
    const row = w.find(`#finding-${VERSION_DIFF.id}`);
    // No flag control: it is call-less and the relay refuses it.
    expect(row.find('button.flag').exists()).toBe(false);
    // ...and the reason given is the evidence, never the severity. The badge on
    // this very row says `breaking`.
    expect(row.find('.badge').text()).toBe('breaking');
    expect(row.text()).not.toContain('Informational');
    expect(row.text()).toContain('no failing call');
  });
});

/* ── 2. The uploader's keyboard path ───────────────────────────────────── */

describe('the uploader can be driven from the keyboard', () => {
  /** Everything focusable inside the uploader, in DOM order — which IS tab
   *  order here: nothing sets a positive tabindex. happy-dom does not implement
   *  Tab, so the order is asserted structurally rather than by synthesising a
   *  key the environment ignores. */
  function tabOrder(root: Element): string[] {
    const sel = 'a[href], button:not([disabled]), input:not([disabled]):not([hidden]), [tabindex]:not([tabindex="-1"])';
    return Array.from(root.querySelectorAll(sel)).map((el) => {
      const tag = el.tagName.toLowerCase();
      if (tag === 'button') return `button:${el.textContent?.trim()}`;
      if (el.classList.contains('dropzone')) return 'dropzone';
      if (tag === 'input') return `input:${(el as HTMLInputElement).type}`;
      return tag;
    });
  }

  it('Tab from the host field reaches the drop zone and then Choose a file', () => {
    const w = mount(ContractUploader, { props: { host: '' }, attachTo: document.body });
    wrapper = w as unknown as VueWrapper;
    const order = tabOrder(w.element);

    const host = order.indexOf('input:text');
    const zone = order.indexOf('dropzone');
    const choose = order.indexOf('button:Choose a file');

    expect(host).toBeGreaterThanOrEqual(0);
    // The whole defect: this used to be -1. A <label> is not focusable and the
    // `hidden` file input is out of the tab order, so the recorded order ran
    // host field -> Cancel -> page header and never reached the picker.
    expect(choose).toBeGreaterThanOrEqual(0);
    expect(zone).toBeGreaterThan(host);
    expect(choose).toBeGreaterThan(zone);
    // Cancel comes after the controls that do the work, not before them.
    expect(order.indexOf('button:Cancel')).toBeGreaterThan(choose);
  });

  it('focus enters the uploader when it opens', () => {
    // Pre-traffic route: there is a host field, and it is the first answer.
    const named = mount(ContractUploader, { props: { host: '' }, attachTo: document.body });
    wrapper = named as unknown as VueWrapper;
    expect(document.activeElement).toBe(named.find('input[type="text"]').element);
    named.unmount();

    // From a provider row the host is already known, so the picker is.
    const bound = mount(ContractUploader, { props: { host: HOST }, attachTo: document.body });
    wrapper = bound as unknown as VueWrapper;
    expect((document.activeElement as HTMLElement).textContent?.trim()).toBe('Choose a file');
  });

  it('Enter on the drop zone opens the picker, and so does the button', async () => {
    const w = mount(ContractUploader, { props: { host: HOST }, attachTo: document.body });
    wrapper = w as unknown as VueWrapper;
    const fileInput = w.find('input[type="file"]').element as HTMLInputElement;
    const click = vi.spyOn(fileInput, 'click').mockImplementation(() => {});

    await w.find('.dropzone').trigger('keydown.enter');
    expect(click).toHaveBeenCalledTimes(1);

    await w.find('.dropzone').trigger('keydown.space');
    expect(click).toHaveBeenCalledTimes(2);

    await w.find('button.btn.small').trigger('click');
    expect(click).toHaveBeenCalledTimes(3);
  });

  it('releases the confirm button once a host re-parse lands, and even when it fails', async () => {
    // QA walk, 2026-09-02 (VERIFIED blocker): the hold that stops a binding
    // being committed against a stale preview was released in the wrong
    // function, so editing the host at the confirm step disabled "Add contract"
    // permanently — the preview came back 200 and the button never came back.
    const previews: Array<(v: unknown) => void> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown, init?: RequestInit) => {
        const path = String(input).split('?')[0];
        if (path === '/api/contracts/preview') {
          void init;
          return json({
            peer_host: HOST, integration: INTEGRATION, title: 'Acme', version: '1.0.0',
            endpoints: 1, servers: [`https://${HOST}`], servers_match: true, has_traffic: false
          });
        }
        return json({}, 404);
      })
    );
    void previews;

    const w = mount(ContractUploader, { props: { host: '' }, attachTo: document.body });
    wrapper = w as unknown as VueWrapper;
    const vm = w.vm as unknown as Record<string, unknown>;

    // Reach the confirm step through the component's own path.
    (vm as { typedHost: string }).typedHost = HOST;
    (vm as { doc: string }).doc = 'openapi: 3.0.0';
    (vm as { filename: string }).filename = 'spec.yaml';
    await (vm as { runPreview: () => Promise<void> }).runPreview();
    await w.vm.$nextTick();
    // The assertion that matters is the CONTROL, not the flag behind it: this
    // is the button the operator clicks to bind the contract.
    const addContract = () => w.find('.uploader-actions button');
    expect(addContract().attributes('disabled')).toBeUndefined();

    // Edit the host: the button is HELD while the re-parse is in flight, so a
    // binding is never committed against a preview describing a different host.
    (vm as { onHostEdited: () => void }).onHostEdited();
    await w.vm.$nextTick();
    expect(addContract().attributes('disabled')).toBeDefined();
    await vi.advanceTimersByTimeAsync(500);
    await w.vm.$nextTick();
    // ...and RELEASED once it lands.
    expect(addContract().attributes('disabled')).toBeUndefined();

    // A re-parse that FAILS must release it too — otherwise the confirm button
    // strands disabled with nothing the operator can do about it.
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'unparseable_document', message: 'nope' }, 400)));
    (vm as { onHostEdited: () => void }).onHostEdited();
    await w.vm.$nextTick();
    await vi.advanceTimersByTimeAsync(500);
    await w.vm.$nextTick();
    expect((vm as { hostDirty: boolean }).hostDirty).toBe(false);
  });

  it('shows the server\'s own sentence when the document is too large', async () => {
    // Launch-week item 6 (2026-09-07): a 9 MB document came back from the relay
    // as 400 invalid_json, so the uploader showed "The request body is not
    // valid JSON." for a size problem. The relay now answers 413
    // document_too_large; the uploader has no copy of its own for that code —
    // it renders the relay's sentence — so this pins that the sentence reaches
    // the error line unchanged.
    const TOO_LARGE = "That document is larger than 8 MB. Contracts this size are usually a bundle — upload the API's own document.";
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'document_too_large', message: TOO_LARGE }, 413)));

    const w = mount(ContractUploader, { props: { host: HOST }, attachTo: document.body });
    wrapper = w as unknown as VueWrapper;
    const vm = w.vm as unknown as Record<string, unknown>;
    (vm as { doc: string }).doc = 'openapi: 3.0.0';
    (vm as { filename: string }).filename = 'huge.yaml';
    await (vm as { runPreview: () => Promise<void> }).runPreview();
    await w.vm.$nextTick();

    expect(w.find('.uploader-error').text()).toBe(TOO_LARGE);
    expect(w.find('.uploader-actions button').exists()).toBe(false);
  });

  it('keeps the reset that lets the same file be chosen twice', async () => {
    const w = mount(ContractUploader, { props: { host: HOST }, attachTo: document.body });
    wrapper = w as unknown as VueWrapper;
    const input = w.find('input[type="file"]');
    // No files selected: readFile returns early, and the value reset still runs
    // — that reset is what stops a retry after a parse error looking dead.
    await input.trigger('change');
    expect((input.element as HTMLInputElement).value).toBe('');
  });
});
