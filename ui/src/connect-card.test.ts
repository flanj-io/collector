// @vitest-environment happy-dom
//
// UX review 2026-09-14: the Connected state — the one Settings state most
// operators ever see — was a sentence ("Connected as X · y@z confirmed ·
// replies as …") under a heading and two paragraphs that said "we", "your
// vendor" and "network". The design draws it as a k/v grid (Organization /
// Contact / Collector address) the eye can scan, and the content rules
// retire all three words. Mounted: the grid is a template fact.
import { describe, it, expect, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import ConnectPanel from './ConnectPanel.vue';
import type { ConnectState } from './threads';

const CONNECTED: ConnectState = {
  status: 'connected',
  collector_name: 'prod-eu',
  workspace_display_name: 'CustomerX',
  contact_email: 'maya@customerx.example',
  contact_display_name: 'Maya',
  confirmed_contact_email: 'maya@customerx.example',
  cp_configured: true
};

let wrapper: VueWrapper | null = null;
afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
});

function facts(w: VueWrapper): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of w.findAll('.connect-field')) out[f.find('.k').text()] = f.find('.v').text();
  return out;
}

describe('Connect card, connected', () => {
  it('renders the design’s k/v grid: collector name with Rename, workspace, contact with a confirmed mark, collector address', () => {
    wrapper = mount(ConnectPanel, { props: { state: { ...CONNECTED, local_ui_url: 'http://localhost:5535' } } });
    const w = wrapper;
    expect(w.find('.connect-state.ok').exists()).toBe(true);
    const f = facts(w);
    expect(Object.keys(f)).toEqual(['Collector name', 'Workspace', 'Contact', 'Collector address']);
    // The name is the deployment's identity in the workspace (2026-09-14) — first, with its rename.
    expect(f['Collector name']).toContain('prod-eu');
    const nameField = w.findAll('.connect-field').find((x) => x.find('.k').text() === 'Collector name')!;
    expect(nameField.find('button').text()).toBe('Rename');
    expect(f.Workspace).toBe('CustomerX');
    expect(f.Contact).toContain('maya@customerx.example');
    expect(f.Contact).toContain('confirmed');
    expect(f.Contact).toContain('replies as Maya');
    expect(w.find('.connect-field .v-mark.ok .hx.tone-ok').exists(), 'confirmed is a green-bolt suffix').toBe(true);
    expect(f['Collector address']).toBe('http://localhost:5535');
    expect(w.findAll('button').map((b) => b.text())).toContain('Change contact');
    expect(w.findAll('button').map((b) => b.text())).not.toContain('Add address');
  });

  it('a missing address shows the Add address affordance inline in its own field', () => {
    wrapper = mount(ConnectPanel, { props: { state: CONNECTED } });
    const w = wrapper;
    const f = facts(w);
    expect(f['Collector address']).toContain('not set');
    const field = w.findAll('.connect-field').find((x) => x.find('.k').text() === 'Collector address')!;
    expect(field.find('button').text()).toBe('Add address');
  });

  it('the copy has no "we", no "your vendor" and no network vocabulary', () => {
    wrapper = mount(ConnectPanel, { props: { state: null, defaultOrg: 'CustomerX' } });
    const text = wrapper.text();
    expect(text).toContain('Connect to Flanj');
    expect(text).toContain('A detection becomes a thread the other side can act on.');
    expect(text).not.toMatch(/\bwe\b/i);
    expect(text).not.toMatch(/vendor/i);
    expect(text).not.toMatch(/network/i);
  });
});
