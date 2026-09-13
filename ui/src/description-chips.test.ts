// @vitest-environment happy-dom
//
// UX review 2026-09-14 (must-fix): one wording-only MCP change was rendered
// under four severities at once — a red Overview headline, a copper-FILLED tab
// pill, a copper-filled `1 NON-BREAKING` card chip, and a steel DESCRIPTION
// row badge. The description class now has ONE vocabulary end to end: the
// row's steel outline and its own word on the card chip and the tab pill; the
// copper fill is kept for genuine NON-BREAKING schema classes. Mounted,
// because which chip renders with which class is a template fact.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';

const DESCRIPTION = {
  id: 'fnd_desc_1',
  kind: 'definition_change',
  severity: 'info',
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
  it('description-only: the tab pill keeps its class and count but takes the steel outline; the card chip says DESCRIPTION', async () => {
    const w = await mountApp([DESCRIPTION]);
    // e2e (mcp.spec / triage.spec) reads `.tab-count.warn` and its title's tail.
    const pill = w.find('.tab-count.warn');
    expect(pill.exists()).toBe(true);
    expect(pill.text()).toBe('1');
    expect(pill.classes()).toContain('desc');
    expect(pill.attributes('title')).toBe('1 description change — wording only, non-breaking — acknowledge to clear');
    expect(pill.attributes('title')).toMatch(/non-breaking — acknowledge to clear$/);
    // The card: a steel DESCRIPTION chip, and NO copper NON-BREAKING chip.
    expect(w.find('.provider .tag.desc').exists()).toBe(true);
    expect(w.find('.provider .tag.desc').text()).toBe('1 DESCRIPTION');
    expect(w.find('.provider .tag.warn').exists()).toBe(false);
    // The row badge is the same word.
    const badge = w.find(`#finding-${DESCRIPTION.id} .badge`);
    expect(badge.text()).toContain('DESCRIPTION');
    expect(badge.classes()).toContain('description');
  });

  it('a genuine NON-BREAKING schema class keeps the copper fill, and the two chips add up to the pill', async () => {
    const w = await mountApp([DESCRIPTION, NON_BREAKING]);
    const pill = w.find('.tab-count.warn');
    expect(pill.text()).toBe('2');
    expect(pill.classes()).not.toContain('desc');
    expect(pill.attributes('title')).toBe('2 non-breaking — acknowledge to clear');
    const warn = w.find('.provider .tag.warn');
    const desc = w.find('.provider .tag.desc');
    expect(warn.text()).toBe('1 NON-BREAKING');
    expect(desc.text()).toBe('1 DESCRIPTION');
    expect(parseInt(warn.text(), 10) + parseInt(desc.text(), 10)).toBe(parseInt(pill.text(), 10));
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
