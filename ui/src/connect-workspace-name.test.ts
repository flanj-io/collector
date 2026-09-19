// @vitest-environment happy-dom
//
// The collector no longer names its organization (CONTRACTS §5, 2026-09-19). A workspace has ONE
// display name, chosen by its contact on the control plane's confirmation page. The Connect form
// therefore has no org-name field and sends none; once Connected, the panel shows the workspace's
// name as the relay last read it — and only once it is known.
import { describe, it, expect, vi, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import ConnectPanel from './ConnectPanel.vue';
import type { ConnectState } from './threads';

let posts: Array<Record<string, unknown>> = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubRelay() {
  posts = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown, init?: RequestInit) => {
      const path = String(input).split('?')[0];
      if (path.endsWith('/api/connect') && (init?.method ?? 'GET').toUpperCase() === 'POST') {
        const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
        posts.push(body);
        return json({ status: 'pending', collector_name: body.collector_name, contact_email: body.contact_email, workspace_display_name: null, cp_configured: true, confirmation_mail: 'sent' }, 202);
      }
      return json({}, 404);
    })
  );
}

async function settle(w: VueWrapper) {
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await w.vm.$nextTick();
}

const connected = (workspace: string | null): ConnectState => ({
  status: 'connected',
  collector_name: 'prod-eu',
  workspace_display_name: workspace,
  contact_email: 'ops@acme.example',
  contact_display_name: 'Dana',
  confirmed_contact_email: 'ops@acme.example',
  cp_configured: true
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('the Connect form asks for no organization name', () => {
  it('has no org-name input and promises no org name', () => {
    const w = mount(ConnectPanel, { props: { state: { status: 'disconnected', cp_configured: true } } });
    expect(w.find('input[autocomplete="organization"]').exists()).toBe(false);
    const text = w.text();
    expect(text).not.toContain('Your organization');
    expect(text).not.toContain('Defaults to your organization');
    // What it says instead: who names the workspace, where, and who sees it.
    expect(text).toContain('confirmation page');
    expect(text).toContain('other organizations');
  });

  it('Connects with a collector name and an email alone, and sends no consumer_display_name', async () => {
    stubRelay();
    const w = mount(ConnectPanel, { props: { state: { status: 'disconnected', cp_configured: true } } });
    await w.find('input[placeholder="e.g. prod-eu"]').setValue('prod-eu');
    await w.find('input[type="email"]').setValue('ops@acme.example');
    await w.find('form').trigger('submit');
    await settle(w);
    expect(w.text()).not.toContain('Add your organization first.');
    expect(posts).toHaveLength(1);
    expect(posts[0]).not.toHaveProperty('consumer_display_name');
    expect(posts[0]).toMatchObject({ collector_name: 'prod-eu', contact_email: 'ops@acme.example' });
  });
});

describe('the Connected panel names the workspace only once known', () => {
  it('shows the workspace display name', () => {
    const w = mount(ConnectPanel, { props: { state: connected('Acme Ltd') } });
    expect(w.text()).toContain('Workspace');
    expect(w.text()).toContain('Acme Ltd');
  });

  it('renders no workspace row, and no blank name, before the control plane reports one', () => {
    const w = mount(ConnectPanel, { props: { state: connected(null) } });
    const keys = w.findAll('.connect-field .k').map((k) => k.text());
    expect(keys).not.toContain('Workspace');
    expect(keys).not.toContain('Organization');
  });
});
