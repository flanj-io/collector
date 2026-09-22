import { describe, it, expect } from 'vitest';
import {
  systemStatus,
  groupRestFindings,
  notValidatedItems,
  type SystemStatusInput,
  type RestDriftLine,
  type McpDriftLine,
  type CheckedCounts,
  ADD_REST_CONTRACT_ACTION,
  SHOW_THESE_CALLS_ACTION,
  HOW_TO_ADD_IT_ACTION,
  VIEW_TOOLS_ACTION
} from './status';
import { NOTHING_VALIDATED_YET, NO_DRIFT_DETECTED } from './headline';
import type { CoverageCall, CoverageSpec, McpToolCoverage } from './coverage';

const zero: CheckedCounts = { restProviders: 0, inbound: 0, mcpServers: 0 };
const t = (iso: string) => (iso ? '2026-09-22 09:14' : '');

function input(over: Partial<SystemStatusInput> = {}): SystemStatusInput {
  return { restLines: [], mcpLines: [], checked: zero, notes: [], itemCount: 0, ...over };
}

const acme: RestDriftLine = { name: 'Acme Payments API', host: 'api.acme.test', text: 'Acme Payments API · api.acme.test — 1 contract drift finding on POST /v1/charges — 5 calls since x.' };
const globex: RestDriftLine = { name: 'Globex FX', host: 'api.globex.test', text: 'Globex FX · api.globex.test — 1 contract drift finding on GET /v1/rates — 2 calls since x.' };
const mcpDrift: McpDriftLine = { name: 'acme-tools-mcp', text: 'acme-tools-mcp v1.2.0 — output mismatch on get_balance — 3 calls since x.', integration: 'acme-tools' };

describe('systemStatus — the truth table', () => {
  it('case 1: REST finding only → drift, title names the provider', () => {
    const s = systemStatus(input({ restLines: [acme], checked: { restProviders: 0, inbound: 2, mcpServers: 1 } }));
    expect(s.tone).toBe('drift');
    expect(s.title).toBe('Drift detected on Acme Payments API');
    expect(s.lines).toEqual([acme]);
  });

  it('case 3: MCP breaking definition change is a drift place, no REST evidence needed', () => {
    const breaking: McpDriftLine = { name: 'acme-tools-mcp', text: 'acme-tools-mcp v1.2.0 — definition change on get_balance — breaking, no calls affected yet.', integration: 'acme-tools' };
    const s = systemStatus(input({ mcpLines: [breaking] }));
    expect(s.tone).toBe('drift');
    expect(s.title).toBe('Drift detected on acme-tools-mcp');
  });

  it('case 4/5: description-only notes never move the tone, checked or not', () => {
    const note = { text: 'acme-tools-mcp reworded the description of get_balance. Wording only, no schema change.', tool: 'get_balance' };
    const checked = systemStatus(input({ checked: { restProviders: 1, inbound: 0, mcpServers: 0 }, notes: [note] }));
    expect(checked.tone).toBe('ok');
    expect(checked.title).toBe(NO_DRIFT_DETECTED);
    expect(checked.notes.length).toBe(1);

    const unchecked = systemStatus(input({ notes: [note] }));
    expect(unchecked.tone).toBe('neutral');
    expect(unchecked.title).toBe(NOTHING_VALIDATED_YET);
    expect(unchecked.notes.length).toBe(1);
  });

  it('case 6: two findings on two surfaces → "Drift detected in 2 places", two lines', () => {
    const s = systemStatus(input({ restLines: [acme, globex] }));
    expect(s.tone).toBe('drift');
    expect(s.title).toBe('Drift detected in 2 places');
    expect(s.lines.length).toBe(2);
  });

  it('case 6b: one REST + one MCP place also reads "in 2 places"', () => {
    const s = systemStatus(input({ restLines: [acme], mcpLines: [mcpDrift] }));
    expect(s.title).toBe('Drift detected in 2 places');
  });

  // The sub-line must never spend a surface that has a line above it — see
  // `checked`'s own doc comment: the caller (ui/App.vue checkedCounts) has
  // already dropped every host/integration named in `restLines`/`mcpLines`
  // from these numbers, so the tally can only ever name what stayed clean.
  it('case 6c: REST-drift-only — the drifting provider never re-appears as clean, the clean MCP server still does', () => {
    const s = systemStatus(input({ restLines: [acme], checked: { restProviders: 0, inbound: 0, mcpServers: 1 } }));
    expect(s.tone).toBe('drift');
    expect(s.sub).toBe('No drift in the rest of what was checked: 1 MCP server.');
    expect(s.sub).not.toContain('REST provider');
  });

  it('case 6d: MCP-drift-only — the drifting server never re-appears as clean, the clean REST provider still does', () => {
    const s = systemStatus(input({ mcpLines: [mcpDrift], checked: { restProviders: 1, inbound: 0, mcpServers: 0 } }));
    expect(s.tone).toBe('drift');
    expect(s.sub).toBe('No drift in the rest of what was checked: 1 REST provider.');
    expect(s.sub).not.toContain('MCP server');
  });

  it('case 6e: REST and MCP both drifting, nothing else checked — the sentence drops entirely rather than naming zero surfaces as clean', () => {
    const s = systemStatus(input({ restLines: [acme], mcpLines: [mcpDrift], checked: zero }));
    expect(s.tone).toBe('drift');
    expect(s.sub).toBe('');
  });

  it('case 7: nothing drifted, only MCP calls checked, REST host with no contract → ok + button', () => {
    // The premise reversal: this used to be REST-neutral (headlineFor). Now
    // the status is `ok` (MCP evidence checked SOMETHING) and the button
    // carries the REST gap — never hidden by inventing REST-only scope.
    const s = systemStatus(input({ checked: { restProviders: 0, inbound: 0, mcpServers: 1 }, itemCount: 1 }));
    expect(s.tone).toBe('ok');
    expect(s.title).toBe(NO_DRIFT_DETECTED);
    expect(s.showButton).toBe(true);
    expect(s.sub).toContain('Not validated');
  });

  it('case 8: nothing checked anywhere, calls exist → neutral + items > 0', () => {
    const s = systemStatus(input({ itemCount: 3 }));
    expect(s.tone).toBe('neutral');
    expect(s.showButton).toBe(true);
  });

  it('case 9: zero calls, zero findings → neutral, items = 0, button hidden', () => {
    const s = systemStatus(input());
    expect(s.tone).toBe('neutral');
    expect(s.showButton).toBe(false);
  });

  it('case 10: everything checked → ok, items = 0, no button, sub ends "everything this collector saw"', () => {
    const s = systemStatus(input({ checked: { restProviders: 2, inbound: 2, mcpServers: 1 } }));
    expect(s.tone).toBe('ok');
    expect(s.showButton).toBe(false);
    expect(s.sub).toContain('That is everything this collector saw.');
  });

  it('case 11: invariant — tone ok never renders with any drift line, and items>0 implies the button', () => {
    const grid: SystemStatusInput[] = [
      input(),
      input({ checked: { restProviders: 1, inbound: 0, mcpServers: 0 } }),
      input({ checked: { restProviders: 1, inbound: 0, mcpServers: 0 }, itemCount: 2 }),
      input({ restLines: [acme] }),
      input({ restLines: [acme], checked: { restProviders: 0, inbound: 1, mcpServers: 1 } }),
      input({ mcpLines: [mcpDrift], checked: { restProviders: 1, inbound: 1, mcpServers: 0 } }),
      input({ itemCount: 4 })
    ];
    for (const i of grid) {
      const s = systemStatus(i);
      if (s.tone === 'ok') {
        expect(s.lines.length).toBe(0);
        const anyChecked = i.checked.restProviders + i.checked.inbound + i.checked.mcpServers > 0;
        expect(anyChecked).toBe(true);
      }
      if (i.itemCount > 0) expect(s.showButton).toBe(true);
      // Never `ok` with a drift line present.
      if (i.restLines.length + i.mcpLines.length > 0) expect(s.tone).toBe('drift');
    }
  });
});

describe('groupRestFindings', () => {
  it('groups by host, dedupes endpoints, sums occurrences, earliest since', () => {
    const lines = groupRestFindings(
      [
        { name: 'Acme Payments API', host: 'api.acme.test', endpoint: 'POST /v1/charges', occurrence_count: 3, first_seen: '2026-09-22T08:41:00Z' },
        { name: 'Acme Payments API', host: 'api.acme.test', endpoint: 'POST /v1/charges', occurrence_count: 2, first_seen: '2026-09-22T09:00:00Z' }
      ],
      t
    );
    expect(lines.length).toBe(1);
    expect(lines[0].text).toContain('2 contract drift findings on POST /v1/charges');
    expect(lines[0].text).toContain('5 calls since');
  });

  it('one host with two endpoints stays one line, one place', () => {
    const lines = groupRestFindings(
      [
        { name: 'Acme', host: 'api.acme.test', endpoint: 'POST /v1/charges' },
        { name: 'Acme', host: 'api.acme.test', endpoint: 'GET /v1/refunds' }
      ],
      t
    );
    expect(lines.length).toBe(1);
    expect(lines[0].text).toContain('POST /v1/charges, GET /v1/refunds');
  });
});

/* ── notValidatedItems ────────────────────────────────────────────────── */

const names = {
  restName: (host: string) => (host === 'api.globex.test' ? 'Globex Foreign Exchange' : host),
  mcpServer: (integration: string) => ({ name: integration, version: '1.2.0', host: integration === 'acme-tools' ? 'mcp.acme.test' : undefined })
};

function restCall(over: Partial<CoverageCall> = {}): CoverageCall {
  return { peer_host: 'api.globex.test', direction: 'client', transport: undefined, method: 'GET', route: '/v1/rates', ...over };
}

describe('notValidatedItems', () => {
  it('case 9/13: no calls → no items', () => {
    expect(notValidatedItems([], [], {}, names)).toEqual([]);
  });

  it('case 12: a REST host with no contract → rest-no-contract, "Add REST contract"', () => {
    const items = notValidatedItems([restCall(), restCall()], [], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('rest-no-contract');
    expect(items[0].name).toBe('Globex Foreign Exchange');
    expect(items[0].why).toContain('No contract uploaded. 2 calls not checked.');
    expect(items[0].action?.label).toBe(ADD_REST_CONTRACT_ACTION);
  });

  it('case 12: not-routable on a bound host → rest-contract-gap naming METHOD route', () => {
    const spec: CoverageSpec = { role: 'provider', peer_host: 'api.acme.test', format: 'openapi', loaded_at: '2026-09-01T00:00:00Z' };
    const call = restCall({
      peer_host: 'api.acme.test',
      method: 'GET',
      route: '/v1/refunds',
      captured_at: '2026-09-15T00:00:00Z',
      validated: 'not-validated',
      validated_reason: 'not-routable'
    });
    const items = notValidatedItems([call], [spec], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('rest-contract-gap');
    expect(items[0].why).toContain('GET /v1/refunds');
    expect(items[0].action?.label).toBe(SHOW_THESE_CALLS_ACTION);
  });

  it('rest-contract-not-reached names a call count, like every other REST row', () => {
    // A contract IS bound for this host (`spec`), but the call carries no
    // `validated_reason` — the pre-processor state — so `serverReason`
    // (ui/src/coverage.ts) derives 'contract-not-reached' rather than
    // 'no-contract'.
    const spec: CoverageSpec = { role: 'provider', peer_host: 'api.acme.test', format: 'openapi', loaded_at: '2026-09-01T00:00:00Z' };
    const call = restCall({
      peer_host: 'api.acme.test',
      captured_at: '2026-09-15T00:00:00Z',
      validated: 'not-validated'
    });
    const items = notValidatedItems([call, call], [spec], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('rest-contract-not-reached');
    expect(items[0].why).toContain('2 calls not checked.');
  });

  it('case 12: inbound with no self contract → inbound-no-self, "How to add it"', () => {
    const call = restCall({ peer_host: '', direction: 'server', validated: 'not-validated', validated_reason: 'no-contract' });
    const items = notValidatedItems([call], [], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('inbound-no-self');
    expect(items[0].name).toBe('Your API');
    expect(items[0].why).toContain('flanjdrift.self_spec_path');
    expect(items[0].action?.label).toBe(HOW_TO_ADD_IT_ACTION);
  });

  it('case 12: internal edge → no row', () => {
    const call = restCall({ edge_class: 'internal' });
    expect(notValidatedItems([call], [], {}, names)).toEqual([]);
  });

  it('case 12: a schema-less tool → mcp-no-output-schema listing only that tool', () => {
    const tools: Record<string, McpToolCoverage[]> = {
      'acme-tools': [
        { name: 'get_balance', hasOutputSchema: true },
        { name: 'list_transactions', hasOutputSchema: false }
      ]
    };
    const checked = { transport: 'mcp', integration: 'acme-tools', mcp_tool_name: 'get_balance', validated: 'clean' } as CoverageCall;
    const schemaless = {
      transport: 'mcp',
      integration: 'acme-tools',
      mcp_tool_name: 'list_transactions',
      validated: 'not-validated',
      validated_reason: 'no-output-contract'
    } as CoverageCall;
    const items = notValidatedItems([checked, schemaless], [], tools, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('mcp-no-output-schema');
    expect(items[0].why).toContain('list_transactions declares no output schema');
    expect(items[0].why).not.toContain('get_balance declares');
    expect(items[0].action?.label).toBe(VIEW_TOOLS_ACTION);
  });

  it('case 12: two schema-less tools → "declare", not "declares"', () => {
    const tools: Record<string, McpToolCoverage[]> = {
      'acme-tools': [
        { name: 'list_transactions', hasOutputSchema: false },
        { name: 'create_refund', hasOutputSchema: false }
      ]
    };
    const first = {
      transport: 'mcp',
      integration: 'acme-tools',
      mcp_tool_name: 'list_transactions',
      validated: 'not-validated',
      validated_reason: 'no-output-contract'
    } as CoverageCall;
    const second = {
      transport: 'mcp',
      integration: 'acme-tools',
      mcp_tool_name: 'create_refund',
      validated: 'not-validated',
      validated_reason: 'no-output-contract'
    } as CoverageCall;
    const items = notValidatedItems([first, second], [], tools, names);
    expect(items.length).toBe(1);
    expect(items[0].why).toContain('list_transactions, create_refund declare no output schema');
    expect(items[0].why).not.toContain('declares');
  });

  it('case 12: isError calls on a server with other checked calls → no row', () => {
    const checked = { transport: 'mcp', integration: 'acme-tools', mcp_tool_name: 'get_balance', validated: 'clean' } as CoverageCall;
    const errored = {
      transport: 'mcp',
      integration: 'acme-tools',
      mcp_tool_name: 'get_balance',
      mcp_is_error: true,
      validated: 'not-validated',
      validated_reason: 'error-result'
    } as CoverageCall;
    expect(notValidatedItems([checked, errored], [], {}, names)).toEqual([]);
  });

  it('case 12: a server whose every call errored → mcp-nothing-checked', () => {
    const errored = {
      transport: 'mcp',
      integration: 'acme-tools',
      mcp_tool_name: 'get_balance',
      mcp_is_error: true,
      validated: 'not-validated',
      validated_reason: 'error-result'
    } as CoverageCall;
    const items = notValidatedItems([errored, errored], [], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('mcp-nothing-checked');
    expect(items[0].why).toContain('returned an error result');
  });

  it('case 13: a REST provider with a bound contract and gap calls gives ONE row, not two', () => {
    const spec: CoverageSpec = { role: 'provider', peer_host: 'api.acme.test', format: 'openapi', loaded_at: '2026-09-01T00:00:00Z' };
    const gap1 = restCall({ peer_host: 'api.acme.test', captured_at: '2026-09-15T00:00:00Z', validated: 'not-validated', validated_reason: 'not-routable' });
    const gap2 = restCall({ peer_host: 'api.acme.test', captured_at: '2026-09-15T00:00:00Z', validated: 'not-validated', validated_reason: 'status-undeclared' });
    const items = notValidatedItems([gap1, gap2], [spec], {}, names);
    expect(items.length).toBe(1);
    expect(items[0].kind).toBe('rest-contract-gap');
  });

  it('case 13: a host with both REST and MCP traffic gives one REST row and one MCP row', () => {
    const restNotChecked = restCall({ peer_host: 'acme.test', validated: 'not-validated', validated_reason: 'no-contract' });
    const mcpSchemaless = {
      transport: 'mcp',
      integration: 'acme.test',
      mcp_tool_name: 'search',
      validated: 'not-validated',
      validated_reason: 'no-output-contract'
    } as CoverageCall;
    const items = notValidatedItems([restNotChecked, mcpSchemaless], [], {}, names);
    expect(items.length).toBe(2);
    expect(items.map((i) => i.group).sort()).toEqual(['mcp', 'rest']);
  });
});
