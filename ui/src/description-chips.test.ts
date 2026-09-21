// @vitest-environment happy-dom
//
// UX review 2026-09-14 (must-fix): one wording-only MCP change was rendered
// under four severities at once — a red Overview headline, a copper-FILLED tab
// pill, a copper-filled `1 NON-BREAKING` card chip, and a steel DESCRIPTION
// row badge. The description class now has ONE vocabulary end to end: its own
// word on the card chip and its own sentence on the tab pill.
//
// The colour half of that rule went further when the tab took three tiers
// (ui/src/contract-tiers.ts): copper now means "would break when a newer
// contract version takes effect", so the informational tier is ALWAYS steel —
// NON-BREAKING included. Two coppers meaning two different things is the one
// outcome to avoid; the two informational classes stay apart by their words.
// Mounted, because which chip renders with which class is a template fact.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

const DESCRIPTION = {
  id: 'fnd_desc_1',
  kind: 'definition_change',
  // A reworded description is wording / WARNING.
  severity: 'warning',
  change_kind: 'wording',
  integration: 'acme-tools',
  endpoint: 'list_transactions',
  expected: 'List transactions',
  actual: 'List transactions for an account',
  rule: 'description-changed',
  spec_version_from: 'sha256:aaaa11112222',
  spec_version_to: 'sha256:bbbb33334444',
  source_call_id: null,
  detected_at: '2026-09-13T20:44:53Z',
  detail: 'tools/list observed 2026-09-13T20:40:00.000Z → 2026-09-13T20:44:52.964Z.',
  occurrence_count: 1
};
const NON_BREAKING = {
  ...DESCRIPTION,
  id: 'fnd_nb_1',
  endpoint: 'create_refund',
  rule: 'output-schema-declared',
  expected: '—',
  actual: 'outputSchema declared'
};

const bodies = (findings: unknown[]): Record<string, unknown> => ({
  '/api/health': { status: 'ok', window_rows: 0, calls: 0, findings: findings.length, cp_configured: false, collector_version: 'v0.0.0-test' },
  '/api/findings': { findings },
  '/api/calls': { calls: [] },
  '/api/edges': { edges: [] },
  '/api/contracts': { contracts: [] },
  '/api/connect': { status: 'disconnected' },
  '/api/threads': { threads: [], total: 0, has_more: false }
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

let wrapper: VueWrapper | null = null;

function stubFetch(findings: unknown[]) {
  const map = bodies(findings);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const body = map[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '#contracts';
  vi.useFakeTimers();
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mountApp(findings: unknown[]): Promise<VueWrapper> {
  stubFetch(findings);
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 12; i++) await Promise.resolve();
  await w.vm.$nextTick();
  return w;
}

describe('a DESCRIPTION change wears one vocabulary from the tab to the row', () => {
  it('description-only: the steel pill keeps its count and says wording only; the card chip says DESCRIPTION', async () => {
    const w = await mountApp([DESCRIPTION]);
    const pill = w.find('.tab-count.worth-knowing');
    expect(pill.exists()).toBe(true);
    expect(pill.text()).toBe('1');
    // The steel outline is now the tier's one colour, so it no longer depends
    // on every row happening to be a wording change — but the SENTENCE still
    // does, and it still ends with the tier's shared tail.
    expect(pill.attributes('title')).toBe('1 description change — wording only, non-breaking — resolve to clear');
    expect(pill.attributes('title')).toMatch(/non-breaking — resolve to clear$/);
    expect(pill.attributes('aria-label')).toBe(pill.attributes('title'));
    // Nothing is failing and nothing would break: no other pill at all.
    expect(w.find('.tab-count.bad').exists()).toBe(false);
    expect(w.find('.tab-count.would-break').exists()).toBe(false);
    // The card: a steel DESCRIPTION chip, and NO NON-BREAKING chip.
    expect(w.find('.provider .tag.desc').exists()).toBe(true);
    expect(w.find('.provider .tag.desc').text()).toBe('1 DESCRIPTION');
    expect(w.find('.provider .tag.nonbreaking').exists()).toBe(false);
    // Copper belongs to the would-break tier now; a wording change never wears it.
    expect(w.find('.provider .tag.would-break').exists()).toBe(false);
    // The row carries TWO labels: the severity, coloured, and the
    // change kind, neutral — where one mixed DESCRIPTION badge used to be.
    const badges = w.findAll(`#finding-${DESCRIPTION.id} .badge`);
    expect(badges[0].text()).toContain('WARNING');
    expect(badges[0].classes()).toContain('warning');
    expect(badges[1].text()).toBe('wording');
    expect(badges[1].classes()).toContain('kind');
  });

  it('a genuine NON-BREAKING schema class keeps its own word, and the two chips add up to the pill', async () => {
    const w = await mountApp([DESCRIPTION, NON_BREAKING]);
    const pill = w.find('.tab-count.worth-knowing');
    expect(pill.text()).toBe('2');
    // Mixed classes: the tier's shared sentence, not the wording-only one.
    expect(pill.attributes('title')).toBe('2 non-breaking — resolve to clear');
    // Both chips are steel now — copper means "would break" — but they keep
    // two different words, so the classes are still told apart without hue.
    const nb = w.find('.provider .tag.nonbreaking');
    const desc = w.find('.provider .tag.desc');
    expect(nb.text()).toBe('1 NON-BREAKING');
    expect(desc.text()).toBe('1 DESCRIPTION');
    expect(parseInt(nb.text(), 10) + parseInt(desc.text(), 10)).toBe(parseInt(pill.text(), 10));
  });

  it('the snapshot labels keep their case — a digest is a machine identifier — and render the surface timestamp', async () => {
    const w = await mountApp([DESCRIPTION]);
    const labels = w.findAll(`#finding-${DESCRIPTION.id} .col .k`);
    expect(labels.length).toBe(2);
    for (const k of labels) expect(k.classes()).toContain('snap');
    expect(labels[0].text()).toMatch(/^before \(snapshot sha256:aaaa11112222 · \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\)$/);
    expect(labels[1].text()).toMatch(/^after \(snapshot sha256:bbbb33334444 · \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\)$/);
    // The diff of a wording change is plain ink on both sides.
    expect(w.find(`#finding-${DESCRIPTION.id} .drift-row`).classes()).toContain('plain');
  });
});

// INFO stays local. An info finding is SHOWN on its
// card, with its two labels, and carries NO Flag control — the relay and the
// control plane refuse it server-side as well.
describe('an INFO finding stays local', () => {
  const RENAME = {
    ...DESCRIPTION,
    id: 'fnd_rename_1',
    severity: 'info',
    change_kind: 'input',
    rule: 'input-property-renamed',
    field_path: 'input.branchId',
  };
  it('renders with its labels and a stays-local hint, and no Flag this', async () => {
    const w = await mountApp([RENAME]);
    const row = w.find(`#finding-${RENAME.id}`);
    expect(row.exists()).toBe(true);
    const badges = row.findAll('.badge');
    expect(badges[0].text()).toContain('INFO');
    expect(badges[1].text()).toBe('input');
    expect(row.find('button.flag').exists()).toBe(false);
    expect(row.text()).not.toContain('Flag this');
    expect(row.find('.info-local').text()).toContain('never flagged to another organisation');
  });
  it('a WARNING finding beside it keeps its Flag control', async () => {
    const w = await mountApp([DESCRIPTION]);
    expect(w.find(`#finding-${DESCRIPTION.id} button.flag`).exists()).toBe(true);
  });
});

// The collector-only rows (2026-09-17) are provider-side evidence and sit on
// the same cards as output_mismatch and definition_change.
describe('value_change and input_rejection render on the contract card', () => {
  it('a value_change shows WARNING + value and can be flagged', async () => {
    const VALUE = {
      ...DESCRIPTION,
      id: 'fnd_value_1',
      kind: 'value_change',
      severity: 'warning',
      change_kind: 'value',
      rule: 'value-format-changed',
      field_path: 'created',
      expected: 'timestamp:iso-8601',
      actual: 'timestamp:epoch-seconds',
      source_call_id: 'call_1',
    };
    const w = await mountApp([VALUE]);
    const row = w.find(`#finding-${VALUE.id}`);
    expect(row.exists()).toBe(true);
    const badges = row.findAll('.badge');
    expect(badges[0].text()).toContain('WARNING');
    expect(badges[1].text()).toBe('value');
    expect(row.text()).toContain('held before (value format)');
    expect(row.find('button.flag').exists()).toBe(true);
  });
});
