// @vitest-environment happy-dom
//
// The reported case, mounted: a deployment with 2 breaking findings in live
// traffic and a contract whose upload diffed 3 breaking changes against the
// version it replaced showed
//
//   Overview headline   "2 contract drift findings"
//   Overview rows       two DRIFTED ×1 rows
//   Contracts tab       a red 5
//
// and read as a miscount. Every number was right about its own population —
// the red pill counted breaking-severity rows from every source, the headline
// counts live drift only — and nothing on the screen said so.
//
// The tab now counts three tiers in order of urgency, one colour each: red for
// what is breaking in live traffic NOW (the headline's population), copper for
// what WOULD break when a newer contract version takes effect, steel for what
// is merely worth knowing. Mounted, because which pill renders with which
// class and which number is a template fact.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

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

/** A live drift: a real call failed against the bound contract. */
const liveFinding = (n: number, endpoint: string) => ({
  id: `fnd_live_${n}`,
  kind: 'live-vs-spec',
  severity: 'breaking',
  integration: INTEGRATION,
  endpoint,
  field_path: 'amount',
  location: 'response',
  expected: 'integer',
  actual: 'string',
  rule: 'response-property-type-changed',
  source_call_id: `call_${n}`,
  detected_at: '2026-09-02T12:00:05Z',
  detail: 'the `amount` response property arrived as a string',
  occurrence_count: 1
});

/** A version diff: breaking against the document this upload replaced. No
 *  call, because the evidence is the two documents. */
const versionDiffFinding = (n: number, endpoint: string) => ({
  id: `fnd_vd_${n}`,
  kind: 'version-diff',
  severity: 'breaking',
  integration: INTEGRATION,
  endpoint,
  field_path: 'amount type/format integer/int64 string 200',
  location: null,
  expected: 'spec 1.0.0',
  actual: 'spec 2.0.0',
  rule: 'response-property-type-changed',
  spec_version_from: '1.0.0',
  spec_version_to: '2.0.0',
  source_call_id: null,
  detected_at: '2026-09-02T12:00:01Z',
  detail: 'the `amount` response property type changed from `integer` to `string`',
  occurrence_count: 1
});

/** One un-acknowledged wording change on the same provider. */
const INFORMATIONAL = {
  id: 'fnd_desc_1',
  kind: 'definition_change',
  severity: 'warning',
  change_kind: 'wording',
  integration: INTEGRATION,
  endpoint: 'GET /v1/charges',
  expected: 'List charges',
  actual: 'List charges for an account',
  rule: 'description-changed',
  spec_version_from: 'sha256:aaaa1111',
  spec_version_to: 'sha256:bbbb2222',
  source_call_id: null,
  detected_at: '2026-09-02T12:00:02Z',
  detail: 'the description changed',
  occurrence_count: 1
};

const FINDINGS = [
  liveFinding(1, 'POST /v1/charges'),
  liveFinding(2, 'GET /v1/charges/{id}'),
  versionDiffFinding(1, 'POST /v1/charges'),
  versionDiffFinding(2, 'PUT /v1/charges/{id}'),
  versionDiffFinding(3, 'DELETE /v1/charges/{id}'),
  INFORMATIONAL
];

/** One validated REST call, so the Overview headline is earned rather than
 *  neutral — the headline is half of what this test is about. */
const CALL = {
  id: 'call_1',
  ts: '2026-09-02T12:00:05Z',
  service_name: 'checkout',
  peer_host: HOST,
  integration: INTEGRATION,
  method: 'POST',
  route: '/v1/charges',
  status_code: 200,
  direction: 'outbound',
  validated: 'drifted'
};

const bodies = (): Record<string, unknown> => ({
  '/api/health': {
    status: 'ok',
    integration: 'acme-payments',
    window_rows: 1,
    calls: 1,
    findings: FINDINGS.length,
    cp_configured: false,
    collector_version: 'v0.0.0-test'
  },
  '/api/findings': { findings: FINDINGS },
  '/api/calls': { calls: [CALL] },
  '/api/edges': { edges: [] },
  '/api/contracts': { contracts: [CONTRACT] },
  '/api/connect': { status: 'disconnected' },
  '/api/threads': { threads: [], total: 0, has_more: false }
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '';
  vi.useFakeTimers();
  const map = bodies();
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = map[path];
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

describe('2 live breaking + 3 version-diff breaking + 1 informational', () => {
  it('shows red 2, copper 3, steel 1 — and the red number equals the headline', async () => {
    const w = await mountApp();

    const red = w.find('.tab-count.bad');
    const copper = w.find('.tab-count.would-break');
    const steel = w.find('.tab-count.worth-knowing');

    // The whole defect: this used to read `5`.
    expect(red.text()).toBe('2');
    expect(copper.text()).toBe('3');
    expect(steel.text()).toBe('1');

    // ...and `2` is the number the Overview headline says, from the same
    // population. The two surfaces cannot disagree by construction now.
    expect(w.find('.headline').text()).toContain('2 contract drift findings');
  });

  it('every pill says what it counts, so no tier rests on its colour', async () => {
    const w = await mountApp();

    const red = w.find('.tab-count.bad');
    const copper = w.find('.tab-count.would-break');
    const steel = w.find('.tab-count.worth-knowing');

    expect(red.attributes('title')).toBe('2 breaking drift findings in live traffic');
    expect(copper.attributes('title')).toBe('3 breaking changes in a newer contract version — nothing is breaking yet');
    expect(steel.attributes('title')).toBe('1 description change — wording only, non-breaking — acknowledge to clear');

    // The accessible name matches the title: a screen reader hears the tier,
    // not a bare digit it cannot place.
    for (const pill of [red, copper, steel]) {
      expect(pill.attributes('aria-label')).toBe(pill.attributes('title'));
    }
  });

  it('the pills add up to the rows the tab lists', async () => {
    const w = await mountApp();
    window.location.hash = '#contracts';
    await w.vm.$nextTick();

    const n = (sel: string) => (w.find(sel).exists() ? parseInt(w.find(sel).text(), 10) : 0);
    const pills = n('.tab-count.bad') + n('.tab-count.would-break') + n('.tab-count.worth-knowing');

    // Nothing is acknowledged here, so the three pills account for every row.
    const rows = w.findAll('.provider .finding').length;
    expect(rows).toBe(FINDINGS.length);
    expect(pills).toBe(rows);
  });

  it('the card chips of each tier sum to that tier’s pill', async () => {
    const w = await mountApp();
    window.location.hash = '#contracts';
    await w.vm.$nextTick();

    const sum = (sel: string) =>
      w.findAll(sel).reduce((total, el) => total + parseInt(el.text(), 10), 0);

    expect(sum('.provider .tag.drift')).toBe(2);
    expect(sum('.provider .tag.would-break')).toBe(3);
    // The steel tier's two chips split by class; together they are its pill.
    expect(sum('.provider .tag.nonbreaking') + sum('.provider .tag.desc')).toBe(1);

    expect(w.find('.provider .tag.would-break').text()).toBe('3 WOULD BREAK');
    expect(w.find('.provider .tag.drift').text()).toBe('2 BREAKING');
  });

  it('a version-diff row keeps its own breaking severity and its Flag control', async () => {
    const w = await mountApp();
    window.location.hash = '#contracts';
    await w.vm.$nextTick();

    // Only the SUMMARY moved tier. The row still says what it is, and can
    // still start a thread with the provider.
    const row = w.find('#finding-fnd_vd_1');
    expect(row.exists()).toBe(true);
    expect(row.find('.badge').text()).toBe('BREAKING');
    expect(row.find('button.flag').exists()).toBe(true);
  });
});

describe('with no version diffs, the copper pill is not rendered at all', () => {
  it('an empty tier shows nothing rather than a zero', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const map = { ...bodies(), '/api/findings': { findings: [liveFinding(1, 'POST /v1/charges')] } };
        const path = String(input).split('?')[0];
        const body = (map as Record<string, unknown>)[path];
        return json(body ?? {}, body === undefined ? 404 : 200);
      })
    );
    const w = await mountApp();
    expect(w.find('.tab-count.bad').text()).toBe('1');
    expect(w.find('.tab-count.would-break').exists()).toBe(false);
    expect(w.find('.tab-count.worth-knowing').exists()).toBe(false);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// FORWARD-COMPAT. Nothing in this repo detects deprecations, and nothing here
// adds detection: this drives a hand-written finding through the stubbed
// findings API, exactly as the read API would deliver one.
//
// It exists because the tiering seam is INERT without the row set. The pure
// module counts a deprecation row copper, but the tab only ever sees the rows
// in `contractTabRows` — so a unit test that feeds the module directly passes
// while the finding reaches no surface at all. That is the version diff's own
// history repeating: in the model, produced, and rendered nowhere. This test
// goes through the mounted app for that reason and no other.
// ─────────────────────────────────────────────────────────────────────────────

const DEPRECATION = {
  id: 'fnd_dep_1',
  kind: 'deprecation',
  severity: 'warning',
  integration: INTEGRATION,
  endpoint: 'POST /v1/charges',
  field_path: 'source',
  location: 'request',
  expected: 'source',
  actual: 'deprecated',
  rule: 'deprecated-parameter',
  source_call_id: 'call_1',
  detected_at: '2026-09-02T12:00:03Z',
  detail: 'the `source` parameter is marked deprecated',
  occurrence_count: 1
};

describe('a deprecation finding reaches the tab and lands in the copper tier', () => {
  async function mountWith(findings: unknown[]): Promise<VueWrapper> {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: unknown) => {
        const map = { ...bodies(), '/api/findings': { findings } };
        const path = String(input).split('?')[0];
        const body = (map as Record<string, unknown>)[path];
        return json(body ?? {}, body === undefined ? 404 : 200);
      })
    );
    return mountApp();
  }

  it('is listed on its provider card, not dropped on the floor', async () => {
    const w = await mountWith([DEPRECATION]);
    window.location.hash = '#contracts';
    await w.vm.$nextTick();
    const row = w.find(`#finding-${DEPRECATION.id}`);
    expect(row.exists(), 'a kind in none of the row filters renders nowhere at all').toBe(true);
    expect(w.find('.provider').text()).toContain(DEPRECATION.endpoint);
    // The generic row gives it the same Flag control as every other non-local
    // kind — no bespoke row design is needed for the plumbing to be correct.
    expect(row.find('button.flag').exists()).toBe(true);
  });

  it('counts copper, never red and never steel — it is not failing yet', async () => {
    const w = await mountWith([DEPRECATION]);
    expect(w.find('.tab-count.would-break').text()).toBe('1');
    expect(w.find('.tab-count.bad').exists()).toBe(false);
    expect(w.find('.tab-count.worth-knowing').exists()).toBe(false);
    // ...and the copper sentence describes THIS row, rather than naming a
    // contract version that has nothing to do with it.
    const pill = w.find('.tab-count.would-break');
    expect(pill.attributes('title')).toBe('1 deprecation affecting your traffic — nothing is breaking yet');
    expect(pill.attributes('aria-label')).toBe(pill.attributes('title'));
  });

  it('is not live drift: the Overview headline and the tab invariant both hold', async () => {
    const w = await mountWith([...FINDINGS, DEPRECATION]);
    // The headline counts LIVE drift only. A deprecation is an announcement,
    // so the headline must read exactly as it did without it.
    expect(w.find('.headline').text()).toContain('2 contract drift findings');
    expect(w.find('.tab-count.bad').text()).toBe('2');
    // Copper absorbs it beside the version diffs, and the sentence widens to
    // one true of both rather than keeping a version-only claim.
    expect(w.find('.tab-count.would-break').text()).toBe('4');
    expect(w.find('.tab-count.would-break').attributes('title')).toBe(
      '4 changes that will break you later — nothing is breaking yet'
    );

    window.location.hash = '#contracts';
    await w.vm.$nextTick();
    const n = (sel: string) => (w.find(sel).exists() ? parseInt(w.find(sel).text(), 10) : 0);
    const pills = n('.tab-count.bad') + n('.tab-count.would-break') + n('.tab-count.worth-knowing');
    expect(pills, 'red + copper + steel + acknowledged = the rows listed').toBe(
      w.findAll('.provider .finding').length
    );
  });
});
