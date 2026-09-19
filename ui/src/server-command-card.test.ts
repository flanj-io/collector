// @vitest-environment happy-dom
//
// A stdio server's launch line, on the REAL surfaces.
//
// Which line a card renders — and that the argv is text, never markup — is a
// template fact, so it is asserted mounted: the Contracts card shows
// `Launched as: …` for a local-process contract and for nothing else, and the
// Flag sheet (which is handed the same contract row) neither shows it nor
// sends it. The `unknown` edge class rides along: its card must render whole.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import App from './App.vue';
import FlagSheet from './FlagSheet.vue';
import type { Finding } from './types';
import type { ConnectState } from './threads';
import type { ContractSpec } from './contracts';

const AT = '2026-09-18T10:00:00Z';
const XSS = '<img src=x onerror="window.__pwned=1">';
const STDIO_COMMAND = JSON.stringify(['npx', '-y', '@stripe/mcp@0.2.1', XSS]);

const contract = (over: Partial<ContractSpec> & { integration: string }): ContractSpec => ({
  role: 'provider',
  format: 'mcp',
  endpoints: 1,
  loaded_at: AT,
  source: 'observed',
  ...over
});

const CONTRACTS = [
  contract({ integration: 'stripe-stdio', peer_host: 'stripe-mcp', title: 'stripe-mcp', edge_class: 'local-process', server_command: STDIO_COMMAND }),
  // A URL-addressed server carrying one anyway (an SDK bug): still never rendered.
  contract({ integration: 'acme-tools', peer_host: 'mcp.acme.test', title: 'acme-tools-mcp', edge_class: 'external', server_command: '["npx","-y","@acme/should-not-render"]' }),
  // The Python SDK's `unknown`: peer_host is the serverInfo.name.
  contract({ integration: 'acme-py', peer_host: 'acme-py-mcp', title: 'acme-py-mcp', edge_class: 'unknown' })
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function stubAppFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      const path = String(input).split('?')[0];
      const bodies: Record<string, unknown> = {
        '/api/health': { status: 'ok', window_rows: 0, calls: 0, findings: 0, cp_configured: false, connect_status: 'disconnected', collector_version: 'v0.0.0-test' },
        '/api/findings': { findings: [] },
        '/api/calls': { calls: [] },
        '/api/edges': { edges: [] },
        '/api/contracts': { contracts: CONTRACTS },
        '/api/connect': { status: 'disconnected' },
        '/api/threads': { threads: [], total: 0, has_more: false }
      };
      if (path === '/api/contracts/spec') {
        return new Response('{"tools":[{"name":"list_charges","inputSchema":{"type":"object"}}]}', { status: 200, headers: { 'content-type': 'application/json' } });
      }
      const body = bodies[path];
      return json(body ?? {}, body === undefined ? 404 : 200);
    })
  );
}

let wrapper: VueWrapper | null = null;

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the launch line on the Contracts card', () => {
  beforeEach(() => {
    localStorage.clear();
    window.location.hash = '';
    vi.useFakeTimers();
    stubAppFetch();
  });

  async function mountContracts(): Promise<VueWrapper> {
    const w = mount(App, { attachTo: document.body });
    wrapper = w;
    await vi.advanceTimersByTimeAsync(0);
    for (let i = 0; i < 12; i++) await Promise.resolve();
    await w.vm.$nextTick();
    const tab = w.findAll('button').find((b) => b.text().trim().startsWith('Contracts'));
    if (!tab) throw new Error('the Contracts tab is not on screen');
    await tab.trigger('click');
    await w.vm.$nextTick();
    return w;
  }

  const card = (w: VueWrapper, name: string) => {
    const c = w.findAll('article.provider').find((a) => a.find('.prov-name').text() === name);
    if (!c) throw new Error(`no card for ${name}: ${w.findAll('.prov-name').map((n) => n.text())}`);
    return c;
  };

  it('renders the argv as one mono line on a local-process card', async () => {
    const w = await mountContracts();
    const line = card(w, 'stripe-mcp').find('.prov-command');
    expect(line.exists()).toBe(true);
    expect(line.classes()).toContain('mono');
    expect(line.text()).toBe(`Launched as: npx -y @stripe/mcp@0.2.1 ${JSON.stringify(XSS)}`);
  });

  it('escapes it: an element that looks like markup is text, never an element', async () => {
    const w = await mountContracts();
    const c = card(w, 'stripe-mcp');
    expect(c.find('.prov-command img').exists()).toBe(false);
    expect(c.find('img').exists()).toBe(false);
    expect(c.find('.prov-command').text()).toContain('<img src=x');
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
  });

  it('renders nothing on a card of any other class, even when a value is present', async () => {
    const w = await mountContracts();
    expect(card(w, 'acme-tools-mcp').find('.prov-command').exists()).toBe(false);
    expect(w.text()).not.toContain('@acme/should-not-render');
    expect(card(w, 'acme-py-mcp').find('.prov-command').exists()).toBe(false);
    expect(w.findAll('.prov-command')).toHaveLength(1);
  });

  it('renders an `unknown`-class card whole, with its origin said plainly', async () => {
    const w = await mountContracts();
    const c = card(w, 'acme-py-mcp');
    expect(c.find('.prov-origin').text()).toBe('· location unknown');
    expect(c.find('.fmt-badge').text()).toBe('MCP');
    expect(c.find('.prov-links').exists()).toBe(true);
  });
});

describe('the launch line never reaches the Flag sheet', () => {
  const CONNECTED: ConnectState = {
    status: 'connected',
    consumer_display_name: 'Acme',
    contact_email: 'ops@acme.test',
    confirmed_contact_email: 'ops@acme.test'
  };
  const FINDING: Finding = {
    id: 'fnd_stdio',
    kind: 'output_mismatch',
    severity: 'breaking',
    integration: 'stripe-stdio',
    endpoint: 'list_charges',
    field_path: 'amount',
    expected: 'type=integer',
    actual: 'type=string ("1200")',
    rule: 'type-mismatch',
    source_call_id: 'c1',
    first_seen: AT,
    last_seen: AT,
    peer_host: 'stripe-mcp'
  };
  const CREATED = { thread_id: 'thr_1', thread_public_id: 'pub', thread_url: 'https://cp.test/t/pub#k=tok', state: 'open', status: 'created' };

  it('is not on the sheet and not in the POST body', async () => {
    const posted: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'POST') {
          posted.push(String(init.body ?? ''));
          return { ok: true, status: 201, text: async () => JSON.stringify(CREATED) } as unknown as Response;
        }
        return { ok: false, status: 404, text: async () => JSON.stringify({ error: 'store_error', message: 'x' }) } as unknown as Response;
      })
    );
    document.body.innerHTML = '<div id="app"><main></main><div id="host"></div></div>';
    const w = mount(FlagSheet, {
      attachTo: '#host',
      props: {
        finding: FINDING,
        correlation: null,
        call: null,
        provider: 'stripe-mcp',
        consumer: 'Acme',
        connect: CONNECTED,
        providerHost: 'stripe-mcp',
        spec: CONTRACTS[0]
      }
    });
    wrapper = w;
    for (let i = 0; i < 8; i++) {
      await Promise.resolve();
      await w.vm.$nextTick();
    }
    const sheetText = document.body.textContent ?? '';
    const sheetValues = [...document.querySelectorAll('textarea, input')].map((e) => (e as HTMLInputElement).value).join('\n');
    for (const leak of ['@stripe/mcp@0.2.1', 'Launched as', 'onerror']) {
      expect(sheetText).not.toContain(leak);
      expect(sheetValues).not.toContain(leak);
    }

    // Anyone with the link: no address to type, so Create posts straight away.
    await w.find('input[name="open_to_mode"][value="anyone"]').setValue(true);
    const create = w.findAll('button').find((b) => b.text().startsWith('Create thread'));
    if (!create) throw new Error('no Create thread button');
    await create.trigger('click');
    for (let i = 0; i < 8; i++) {
      await Promise.resolve();
      await w.vm.$nextTick();
    }
    expect(posted.length).toBeGreaterThan(0);
    for (const body of posted) {
      for (const leak of ['@stripe/mcp', 'server_command', 'onerror', 'npx']) {
        expect(body).not.toContain(leak);
      }
    }
  });
});
