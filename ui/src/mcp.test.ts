import { describe, expect, it } from 'vitest';
import {
  JSONRPC_ID_TITLE,
  LOCAL_NOTE_NOT_FLAGGABLE,
  LOCAL_NOTICES_TITLE,
  MCP_BADGE_TOOLTIP,
  MCP_ERROR_TOOLTIP,
  MCP_NO_SPEC_NEEDED,
  afterColLabel,
  beforeColLabel,
  defChangeDetail,
  defChangeNoCallSub,
  definitionClass,
  isFlaggableMcp,
  isLocalNotice,
  isMcpCall,
  isMcpFinding,
  localNoticesSub,
  localNoticesSubFor,
  mcpBadgeLabel,
  mcpContractMeta,
  mcpCorrelationCount,
  mcpDefaultMessage,
  mcpDisclosureLead,
  mcpDisclosureTail,
  mcpEvidenceLine,
  mcpHeadline,
  mcpIdsLine,
  mcpIdsLineFor,
  mcpStatusLabel,
  methodFacetOf,
  noOutputContractNote,
  noticeLine,
  parseToolRows,
  snapshotTimes,
  statusFilterMatches,
  toolContractLabel,
  toolNameOf,
  typeOf,
  valueOf
} from './mcp';
import type { Finding } from './types';

// Deck placeholders: <S> acme-mcp · <V> 1.4.0 · <T> get_balance · <P> Acme Payments.
function finding(overrides: Partial<Finding>): Finding {
  return {
    id: 'f1',
    kind: 'output_mismatch',
    severity: 'breaking',
    integration: 'acme-payments',
    endpoint: 'get_balance',
    field_path: 'amount',
    location: '$.response.structuredContent.amount',
    expected: 'type=number',
    actual: 'type=string ("1200.00")',
    rule: 'type-mismatch',
    source_call_id: 'c1',
    first_seen: '2026-08-18T08:00:00Z',
    occurrence_count: 12,
    ...overrides
  } as Finding;
}

const defChange = (over: Partial<Finding> = {}) =>
  finding({
    kind: 'definition_change',
    severity: 'breaking',
    rule: 'input-required-property-added',
    source_call_id: null,
    expected: '(none)',
    actual: '{"type":"string"}',
    spec_version_from: 'sha256:aaaa11112222',
    spec_version_to: 'sha256:bbbb33334444',
    detail: 'Definition change (BREAKING): input-required-property-added on `get_balance` at inputSchema.properties.account — tools/list observed 2026-08-18T08:00:00Z → 2026-08-19T09:00:00Z.',
    ...over
  });

describe('kinds, classes, flaggability (spec §1/§6)', () => {
  it('classifies MCP findings and calls', () => {
    expect(isMcpFinding(finding({}))).toBe(true);
    expect(isMcpFinding({ kind: 'live-vs-spec' })).toBe(false);
    expect(isMcpCall({ transport: 'mcp' })).toBe(true);
    expect(isMcpCall({ transport: undefined })).toBe(false);
    expect(isMcpCall(null)).toBe(false);
  });

  it('definition class from severity + rule', () => {
    expect(definitionClass(defChange())).toBe('BREAKING');
    expect(definitionClass(defChange({ severity: 'info' }))).toBe('NON-BREAKING');
    expect(definitionClass(defChange({ severity: 'warning', rule: 'description-changed' }))).toBe('DESCRIPTION');
    expect(definitionClass(finding({}))).toBe('');
  });

  it('stale_client and DESCRIPTION-only changes are local notices — never flaggable', () => {
    expect(isLocalNotice(finding({ kind: 'stale_client' }))).toBe(true);
    expect(isLocalNotice(defChange({ severity: 'warning', rule: 'description-changed' }))).toBe(true);
    expect(isLocalNotice(defChange())).toBe(false);
    expect(isLocalNotice(finding({}))).toBe(false);
    expect(isFlaggableMcp(finding({}))).toBe(true);
    expect(isFlaggableMcp(defChange())).toBe(true);
    expect(isFlaggableMcp(defChange({ severity: 'info' }))).toBe(true);
    expect(isFlaggableMcp(finding({ kind: 'stale_client' }))).toBe(false);
    expect(isFlaggableMcp(defChange({ severity: 'warning', rule: 'description-changed' }))).toBe(false);
  });
});

describe('deck §1 — edges', () => {
  it('transport badge + tooltip', () => {
    expect(mcpBadgeLabel('external')).toBe('MCP');
    expect(mcpBadgeLabel('local-process')).toBe('MCP · stdio');
    expect(MCP_BADGE_TOOLTIP).toBe('An MCP server — its tools/list is the contract.');
  });
});

describe('deck §2 — health', () => {
  const server = { name: 'acme-mcp', version: '1.4.0' };
  const t = (iso: string) => (iso ? '14:02' : '');

  it('drift headline', () => {
    const h = mcpHeadline(server, [finding({})], t);
    expect(h.ok).toBe(false);
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: output mismatch on get_balance — 12 calls since 14:02.');
  });

  it('definition-change headline (no output mismatch)', () => {
    const h = mcpHeadline(server, [defChange()], t);
    expect(h.ok).toBe(false);
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: definition change on get_balance — breaking, no calls affected yet.');
  });

  it('clean headline (local notices do not tip it)', () => {
    const h = mcpHeadline(server, [finding({ kind: 'stale_client' })], t);
    expect(h.ok).toBe(true);
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: no drift detected.');
  });

  it('local notices band copy', () => {
    expect(LOCAL_NOTICES_TITLE).toBe('Local notices');
    expect(localNoticesSub('Acme Payments')).toBe(
      "Visible to you only. Nothing here can be flagged — these aren't evidence against Acme Payments."
    );
  });

  it('band sub-line: named for one provider, neutral once notices span several', () => {
    expect(localNoticesSubFor(['Acme Payments', 'Acme Payments'])).toBe(localNoticesSub('Acme Payments'));
    expect(localNoticesSubFor(['Acme Payments', 'Globex FX'])).toBe(
      "Visible to you only. Nothing here can be flagged — these aren't evidence against the provider."
    );
    expect(localNoticesSubFor([])).toBe(
      "Visible to you only. Nothing here can be flagged — these aren't evidence against the provider."
    );
  });

  it('notice lines: stale tool, stale args, description-only', () => {
    expect(noticeLine(finding({ kind: 'stale_client', rule: 'tool-not-listed' }), 'acme-mcp', 'Acme Payments')).toBe(
      'Your agent still calls get_balance — acme-mcp no longer lists it. Update your client.'
    );
    expect(
      noticeLine(finding({ kind: 'stale_client', rule: 'type-mismatch', field_path: 'account' }), 'acme-mcp', 'Acme Payments')
    ).toBe("Your agent's arguments to get_balance no longer match the current inputSchema at $.account. Update your client.");
    expect(
      noticeLine(defChange({ severity: 'warning', rule: 'description-changed' }), 'acme-mcp', 'Acme Payments')
    ).toBe(
      "Description changed on get_balance — schema unchanged. This can change which tools your model picks. Wording is Acme Payments's to change, so this stays a local note."
    );
  });
});

describe('deck §3 — contracts', () => {
  it('server meta + no-spec copy', () => {
    expect(MCP_NO_SPEC_NEEDED).toBe('No spec file needed — the server publishes its own contract on tools/list.');
    expect(mcpContractMeta(3, 'Aug 24, 14:02')).toBe('3 tools · contract observed from tools/list · updated Aug 24, 14:02');
    expect(mcpContractMeta(1, 'now')).toBe('1 tool · contract observed from tools/list · updated now');
  });

  it('per-tool rows from the snapshot document', () => {
    const rows = parseToolRows(
      JSON.stringify({
        tools: [
          { name: 'get_balance', inputSchema: {}, outputSchema: { type: 'object' } },
          { name: 'list_transactions', inputSchema: {} }
        ]
      })
    );
    expect(rows).toEqual([
      { name: 'get_balance', hasOutputSchema: true },
      { name: 'list_transactions', hasOutputSchema: false }
    ]);
    expect(parseToolRows('not json')).toEqual([]);
    expect(toolContractLabel(true)).toBe('input + output contract');
    expect(toolContractLabel(false)).toBe('input contract only');
    expect(noOutputContractNote('acme-mcp', 'list_transactions')).toBe(
      "No output contract declared — acme-mcp doesn't say what list_transactions returns, so output drift on this tool can't be checked."
    );
  });

  it('definition_change columns + detail + local-note suffix', () => {
    const t = snapshotTimes(defChange().detail);
    expect(t).toEqual({ from: '2026-08-18T08:00:00Z', to: '2026-08-19T09:00:00Z' });
    expect(snapshotTimes(undefined)).toEqual({ from: '', to: '' });
    expect(beforeColLabel('sha256:aaaa11112222', 'T1')).toBe('before (snapshot sha256:aaaa11112222 · T1)');
    expect(afterColLabel('sha256:bbbb33334444', 'T2')).toBe('after (snapshot sha256:bbbb33334444 · T2)');
    expect(defChangeDetail('T1', 'T2', 'Acme Payments')).toBe("Their tools/list at T1 vs at T2 — both Acme Payments's own words.");
    expect(LOCAL_NOTE_NOT_FLAGGABLE).toBe('Local note — not flaggable.');
  });
});

describe('deck §4 — traffic', () => {
  it('tool name, status and honest id title', () => {
    expect(toolNameOf({ mcp_tool_name: 'get_balance', route: '/x' })).toBe('get_balance');
    expect(toolNameOf({ mcp_tool_name: '', route: '/create_refund' })).toBe('create_refund');
    expect(mcpStatusLabel({ mcp_is_error: false })).toBe('ok');
    expect(mcpStatusLabel({ mcp_is_error: true })).toBe('error');
    expect(MCP_ERROR_TOOLTIP).toBe('The server returned isError — an execution failure, not contract drift.');
    expect(JSONRPC_ID_TITLE).toBe('JSON-RPC id (client-generated)');
  });

  it("method facet: MCP rows are TOOL, never the wire's tools/call", () => {
    expect(methodFacetOf({ transport: 'mcp', method: 'tools/call' })).toBe('TOOL');
    expect(methodFacetOf({ method: 'post' })).toBe('POST');
    // An HTTP method filter therefore excludes MCP rows; TOOL matches only them.
    expect(methodFacetOf({ transport: 'mcp', method: 'tools/call' })).not.toBe('TOOLS/CALL');
  });

  it('status facet: MCP rows filter as ok/error, not by HTTP class', () => {
    const ok = { transport: 'mcp', mcp_is_error: false, status_code: 0 };
    const err = { transport: 'mcp', mcp_is_error: true, status_code: 0 };
    expect(statusFilterMatches('', ok)).toBe(true);
    expect(statusFilterMatches('err', err)).toBe(true);
    expect(statusFilterMatches('err', ok)).toBe(false);
    expect(statusFilterMatches('2xx', ok)).toBe(true);
    expect(statusFilterMatches('2xx', err)).toBe(false);
    // An MCP error has no HTTP class to claim.
    for (const bucket of ['3xx', '4xx', '5xx']) {
      expect(statusFilterMatches(bucket, ok)).toBe(false);
      expect(statusFilterMatches(bucket, err)).toBe(false);
    }
    // HTTP rows keep the class semantics.
    expect(statusFilterMatches('2xx', { status_code: 201 })).toBe(true);
    expect(statusFilterMatches('4xx', { status_code: 404 })).toBe(true);
    expect(statusFilterMatches('err', { status_code: 500 })).toBe(true);
    expect(statusFilterMatches('err', { status_code: 200 })).toBe(false);
  });
});

describe('deck §5 — flag sheet', () => {
  it('renders declared/got types and values from the finding strings', () => {
    expect(typeOf('type=number')).toBe('number');
    expect(typeOf('type=string ("1200.00")')).toBe('string');
    expect(valueOf('type=string ("1200.00")')).toBe('"1200.00"');
    expect(valueOf('type=object')).toBe('type=object');
  });

  it('output_mismatch evidence line', () => {
    expect(mcpEvidenceLine(finding({ field_path: 'amount' }), 'acme-mcp')).toBe(
      'get_balance on acme-mcp — output mismatch at $.amount: declared number, got string'
    );
  });

  it('definition_change evidence line (call-less)', () => {
    expect(mcpEvidenceLine(defChange(), 'acme-mcp')).toBe(
      'get_balance on acme-mcp — definition change (BREAKING): input-required-property-added. Two tools/list snapshots, 2026-08-18T08:00:00Z → 2026-08-19T09:00:00Z.'
    );
    expect(defChangeNoCallSub('Acme Payments')).toBe(
      "No call is shared — the evidence is Acme Payments's own published definitions, before and after."
    );
  });

  it('IDs line labels the JSON-RPC id as client-generated', () => {
    expect(mcpIdsLine('Acme Payments')).toBe(
      "1 request ID will be shared. It's your client's JSON-RPC id — it shows up in Acme Payments's logs only if they log it."
    );
    expect(mcpCorrelationCount({ client_request_id: '42' })).toBe(1);
    expect(mcpCorrelationCount(null)).toBe(0);
  });

  it("IDs line: the deck's JSON-RPC line only while the client id is the SOLE key", () => {
    // Sole client-generated id → the deck line verbatim.
    expect(mcpIdsLineFor({ client_request_id: '42' }, 'Acme Payments')).toBe(mcpIdsLine('Acme Payments'));
    // Mixed keys → the standard count line + the honest client-id note.
    expect(mcpIdsLineFor({ client_request_id: '42', request_id: 'req_9' }, 'Acme Payments')).toBe(
      "2 request IDs will be shared so their team can check their own logs. One of them is your client's own JSON-RPC id — in Acme Payments's logs only if they log it."
    );
    expect(mcpIdsLineFor({ client_request_id: '42', request_id: 'req_9', trace_id: 't1' }, 'Acme Payments')).toBe(
      "3 request IDs will be shared so their team can check their own logs. One of them is your client's own JSON-RPC id — in Acme Payments's logs only if they log it."
    );
    // No client id → the standard line, no note.
    expect(mcpIdsLineFor({ request_id: 'req_9' }, 'Acme Payments')).toBe(
      '1 request ID will be shared so their team can check their own logs.'
    );
    expect(mcpIdsLineFor(null, 'Acme Payments')).toBe('No request IDs were captured on this call.');
  });

  it('what-leaves disclosure, both kinds', () => {
    expect(mcpDisclosureLead(finding({}), 'get_balance')).toBe(
      "This redacted tool call and result, the finding, the tool's declared output schema, the JSON-RPC id, the tool and server name, your message, and"
    );
    expect(mcpDisclosureTail(finding({}))).toBe('Raw calls never leave.');
    expect(mcpDisclosureLead(defChange(), 'get_balance')).toBe(
      "The before/after fragments of get_balance's definition, the finding, the two snapshot hashes and observed-at times, the server name and version, your message, and"
    );
    expect(mcpDisclosureTail(defChange())).toBe('No call data is involved, so none leaves.');
  });

  it('message prefills', () => {
    const fmt = (iso: string) => (iso ? 'Aug 18' : '');
    expect(mcpDefaultMessage(finding({}), fmt)).toBe(
      'Seeing get_balance return a string at $.amount since Aug 18 — your outputSchema says number. Can you confirm on your side?'
    );
    expect(mcpDefaultMessage(defChange(), fmt)).toBe(
      'Your tools/list changed get_balance between Aug 18 and Aug 18 — input-required-property-added. Was this intentional? Anything we should migrate to?'
    );
  });
});
