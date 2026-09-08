import { describe, expect, it } from 'vitest';
import {
  ACK_LABEL,
  ACK_TITLE,
  JSONRPC_ID_TITLE,
  FLAG_DESCRIPTION_GUARD,
  LOCAL_NOTICES_TITLE,
  MCP_BADGE_TOOLTIP,
  MCP_ERROR_TOOLTIP,
  MCP_NO_SPEC_NEEDED,
  UNDO_LABEL,
  UNDO_TITLE,
  ackEvidenceVersion,
  ackedLine,
  afterColLabel,
  beforeColLabel,
  breakingChipLabel,
  breakingCountTitle,
  defChangeDetail,
  defChangeNoCallSub,
  definitionClass,
  informationalChipLabel,
  informationalChipTitle,
  informationalCountTitle,
  isAckable,
  isAcked,
  isBreakingFinding,
  isDescriptionChange,
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

/** The DESCRIPTION class — flaggable since qfix2-2026-08-26. */
const descChange = (over: Partial<Finding> = {}) =>
  defChange({
    severity: 'warning',
    rule: 'description-changed',
    field_path: 'description',
    expected: '"Refund a charge."',
    actual: '"Refund a charge, with fees."',
    detail:
      'Definition change (DESCRIPTION): description-changed on `get_balance` at description — tools/list observed 2026-08-18T08:00:00Z → 2026-08-19T09:00:00Z.',
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

  it('stale_client is the ONLY local notice — and the only never-flaggable kind', () => {
    expect(isLocalNotice(finding({ kind: 'stale_client' }))).toBe(true);
    expect(isLocalNotice(defChange())).toBe(false);
    expect(isLocalNotice(finding({}))).toBe(false);
    // qfix2-2026-08-26: DESCRIPTION left the local-notice set when the owner
    // made it flaggable — the Overview band promises nothing in it can be
    // flagged, so it must not hold a row that now has a Flag control.
    expect(isLocalNotice(descChange())).toBe(false);
  });

  it('every definition_change class is flaggable; stale_client never is', () => {
    expect(isFlaggableMcp(finding({}))).toBe(true);
    expect(isFlaggableMcp(defChange())).toBe(true);
    expect(isFlaggableMcp(defChange({ severity: 'info' }))).toBe(true);
    expect(isFlaggableMcp(descChange())).toBe(true);
    expect(isFlaggableMcp(finding({ kind: 'stale_client' }))).toBe(false);
  });

  it('isDescriptionChange picks out the one class that carries the guard line', () => {
    expect(isDescriptionChange(descChange())).toBe(true);
    expect(isDescriptionChange(defChange())).toBe(false);
    expect(isDescriptionChange(defChange({ severity: 'info' }))).toBe(false);
    expect(isDescriptionChange(finding({}))).toBe(false);
  });
});

describe('badge tiers + acknowledge (qfix-2026-08-25)', () => {
  it('red tier = breaking severity, all sources — never the protocol', () => {
    expect(isBreakingFinding(finding({}))).toBe(true); // output_mismatch
    expect(isBreakingFinding(defChange())).toBe(true); // MCP BREAKING
    expect(isBreakingFinding({ severity: 'breaking' })).toBe(true); // REST live-vs-spec
    expect(isBreakingFinding(defChange({ severity: 'info' }))).toBe(false);
    expect(isBreakingFinding(defChange({ severity: 'warning', rule: 'description-changed' }))).toBe(false);
  });

  it('ackable: DESCRIPTION + NON-BREAKING definition changes ONLY', () => {
    expect(isAckable(defChange({ severity: 'warning', rule: 'description-changed' }))).toBe(true);
    expect(isAckable(defChange({ severity: 'info' }))).toBe(true);
    // Never: BREAKING, output_mismatch, stale_client, live-vs-spec.
    expect(isAckable(defChange())).toBe(false);
    expect(isAckable(finding({}))).toBe(false);
    expect(isAckable(finding({ kind: 'stale_client' }))).toBe(false);
    expect(isAckable({ kind: 'live-vs-spec', severity: 'breaking', rule: 'type' })).toBe(false);
  });

  it('acked state comes from the read-API join', () => {
    expect(isAcked(finding({ acked: true }))).toBe(true);
    expect(isAcked(finding({}))).toBe(false);
    expect(isAcked({ kind: 'output_mismatch', acked: undefined } as Finding)).toBe(false);
  });

  it('control + footer strings (verbatim)', () => {
    expect(ACK_LABEL).toBe('Acknowledge');
    expect(ACK_TITLE).toBe('Local only — clears it from the counts on this collector. Nothing is sent anywhere.');
    expect(UNDO_LABEL).toBe('Undo');
    expect(UNDO_TITLE).toBe('Puts it back in the count.');
    expect(ackedLine('5m ago')).toBe('Acknowledged 5m ago.');
  });

  it('tab-pill titles', () => {
    expect(breakingCountTitle(7)).toBe('7 breaking findings');
    expect(breakingCountTitle(1)).toBe('1 breaking finding');
    expect(informationalCountTitle(2)).toBe('2 non-breaking — acknowledge to clear');
    expect(informationalCountTitle(1)).toBe('1 non-breaking — acknowledge to clear');
  });

  it('card chips + composition title', () => {
    expect(breakingChipLabel(1)).toBe('1 BREAKING');
    expect(breakingChipLabel(6)).toBe('6 BREAKING');
    expect(informationalChipLabel(2)).toBe('2 NON-BREAKING');
    expect(informationalChipTitle(1, 1)).toBe('1 non-breaking change · 1 description change');
    expect(informationalChipTitle(2, 0)).toBe('2 non-breaking changes');
    expect(informationalChipTitle(0, 2)).toBe('2 description changes');
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
    const h = mcpHeadline(server, [finding({})], t, 12);
    expect(h.tone).toBe('drift');
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: output mismatch on get_balance — 12 calls since 14:02.');
  });

  it('definition-change headline (no output mismatch)', () => {
    const h = mcpHeadline(server, [defChange()], t, 3);
    expect(h.tone).toBe('drift');
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: definition change on get_balance — breaking, no calls affected yet.');
  });

  it('clean headline (local notices do not tip it)', () => {
    const h = mcpHeadline(server, [finding({ kind: 'stale_client' })], t, 3);
    expect(h.tone).toBe('ok');
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: no drift detected.');
  });

  // qfix2-2026-08-26 (§7 risk 3): a description change left the Local notices
  // band when it became flaggable. If the headline had no clause for it, a
  // server whose ONLY drift is a wording change would say "no drift detected"
  // in green here while the Contracts tab showed an amber row with a primary
  // `Flag this` — and the finding would appear nowhere on Overview at all.
  it('description-only server is never green', () => {
    const h = mcpHeadline(server, [descChange()], t, 3);
    expect(h.tone).toBe('drift');
    expect(h.text).toBe(
      'Server: acme-mcp v1.4.0. You: definition change on get_balance — description only, no schema change.'
    );
  });

  it('a breaking definition change still outranks a description one', () => {
    const h = mcpHeadline(server, [descChange({ id: 'f2', endpoint: 'list_txns' }), defChange()], t, 3);
    expect(h.tone).toBe('drift');
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: definition change on get_balance — breaking, no calls affected yet.');
  });

  // 2026-09-07 (the second QA walk): the REST headline's neutral zero state,
  // per server. A tools/list that has arrived lists the server on the
  // Contracts tab and renders this line — and may still have validated
  // nothing: every call so far hit a tool with no outputSchema, or came back
  // isError, or was captured before the snapshot landed. "no drift detected"
  // in green there is an all-clear nothing performed.
  it('a snapshot that has validated nothing is neutral, never green', () => {
    const h = mcpHeadline(server, [], t, 0);
    expect(h.tone).toBe('neutral');
    expect(h.text).toBe('Server: acme-mcp v1.4.0. You: nothing validated yet.');
    // A local notice is not evidence either way.
    expect(mcpHeadline(server, [finding({ kind: 'stale_client' })], t, 0).tone).toBe('neutral');
    // One validated call earns the all-clear.
    const ok = mcpHeadline(server, [], t, 1);
    expect(ok.tone).toBe('ok');
    expect(ok.text).toBe('Server: acme-mcp v1.4.0. You: no drift detected.');
  });

  it('findings are evidence in themselves: they report with zero validated calls', () => {
    // An output mismatch's call can be evicted while the finding outlives it,
    // and a definition change never had a call. Neither may fall to neutral.
    expect(mcpHeadline(server, [finding({})], t, 0).tone).toBe('drift');
    expect(mcpHeadline(server, [defChange()], t, 0).tone).toBe('drift');
    expect(mcpHeadline(server, [descChange()], t, 0).tone).toBe('drift');
  });

  it('the three tones are distinct states, never a boolean in disguise', () => {
    const tones = [
      mcpHeadline(server, [], t, 0).tone,
      mcpHeadline(server, [], t, 1).tone,
      mcpHeadline(server, [finding({})], t, 1).tone
    ];
    expect(tones).toEqual(['neutral', 'ok', 'drift']);
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

  it('notice lines: stale tool, stale args (the description line is DELETED)', () => {
    expect(noticeLine(finding({ kind: 'stale_client', rule: 'tool-not-listed' }), 'acme-mcp')).toBe(
      'Your agent still calls get_balance — acme-mcp no longer lists it. Update your client.'
    );
    expect(
      noticeLine(finding({ kind: 'stale_client', rule: 'type-mismatch', field_path: 'account' }), 'acme-mcp')
    ).toBe("Your agent's arguments to get_balance no longer match the current inputSchema at $.account. Update your client.");
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

// ─────────────────────────────────────────────────────────────────────────────
// qfix2-2026-08-26 — ux-design-v2 §2.8: the ack key
// ─────────────────────────────────────────────────────────────────────────────

describe('an ack binds to the evidence version it acknowledged (§2.8, §7 risk 2)', () => {
  it('a SECOND change on the same tool + field arrives UN-acknowledged', () => {
    // Both rows carry the IDENTICAL finding signature
    // (integration|endpoint|kind|rule|field_path) — that is the whole problem.
    const v1 = descChange({ spec_version_to: 'sha256:bbbb33334444' });
    const acked = { ...v1, acked: true, acked_evidence_version: 'sha256:bbbb33334444' };
    expect(isAcked(acked)).toBe(true);

    // <P> edits the same description again: same signature, NEW after-hash.
    const v2 = { ...acked, id: 'f2', spec_version_to: 'sha256:cccc55556666' };
    expect(isAcked(v2)).toBe(false);
  });

  it('acknowledging the new evidence re-covers the row', () => {
    const v2 = descChange({
      spec_version_to: 'sha256:cccc55556666',
      acked: true,
      acked_evidence_version: 'sha256:cccc55556666'
    });
    expect(isAcked(v2)).toBe(true);
  });

  it('a LEGACY ack (no evidence version) does not cover a definition_change', () => {
    // Fail-safe migration: the finding re-surfaces un-acknowledged rather than
    // staying silently acked behind a record that predates the key change.
    expect(isAcked(descChange({ acked: true }))).toBe(false);
    expect(isAcked(defChange({ severity: 'info', acked: true }))).toBe(false);
  });

  it('occurrence-counted kinds key on the signature alone — recurrence is text, not a re-alarm', () => {
    expect(ackEvidenceVersion(finding({}))).toBe('');
    expect(ackEvidenceVersion(descChange())).toBe('sha256:bbbb33334444');
    expect(ackEvidenceVersion({ kind: 'definition_change' } as Finding)).toBe('');
    // An output_mismatch stays acked no matter how many more calls land.
    expect(isAcked(finding({ acked: true, occurrence_count: 47 }))).toBe(true);
    // …even if a snapshot hash happens to ride along on the record.
    expect(isAcked(finding({ acked: true, spec_version_to: 'sha256:bbbb33334444' }))).toBe(true);
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// qfix2-2026-08-26 — ux-design-v2 §2.7.4: the DESCRIPTION flag sheet
// ─────────────────────────────────────────────────────────────────────────────

describe('flag sheet, DESCRIPTION variant (§2.7.4)', () => {
  const d = (iso: string) => (iso === '2026-08-19T09:00:00Z' ? 'Aug 19' : iso);

  it('evidence line claims the provider own two published versions', () => {
    expect(mcpEvidenceLine(descChange(), 'acme-mcp', d)).toBe(
      'get_balance — description changed in your tools/list on Aug 19. Both versions are your own published text.'
    );
  });

  it('prefers the structured snapshot_observed_at over parsing the detail line', () => {
    expect(mcpEvidenceLine(descChange({ snapshot_observed_at: '2026-08-19T09:00:00Z' }), 'acme-mcp', d)).toBe(
      'get_balance — description changed in your tools/list on Aug 19. Both versions are your own published text.'
    );
  });

  it('the other definition classes keep their shipped evidence line', () => {
    expect(mcpEvidenceLine(defChange(), 'acme-mcp')).toBe(
      'get_balance on acme-mcp — definition change (BREAKING): input-required-property-added. Two tools/list snapshots, 2026-08-18T08:00:00Z → 2026-08-19T09:00:00Z.'
    );
  });

  it('disclosure names the two descriptions, and raw calls never leave', () => {
    expect(mcpDisclosureLead(descChange(), 'get_balance')).toBe(
      'The two published descriptions, when each was observed, the endpoint, your message, and'
    );
    expect(mcpDisclosureTail(descChange())).toBe('Raw calls never leave.');
    // BREAKING / NON-BREAKING keep the shipped pair.
    expect(mcpDisclosureLead(defChange(), 'get_balance')).toBe(
      "The before/after fragments of get_balance's definition, the finding, the two snapshot hashes and observed-at times, the server name and version, your message, and"
    );
    expect(mcpDisclosureTail(defChange())).toBe('No call data is involved, so none leaves.');
  });

  it('prefilled message asks whether the wording was intended', () => {
    expect(mcpDefaultMessage(descChange(), d)).toBe(
      "Your tools/list description for get_balance changed on Aug 19. The schema didn't change, but the wording did, and our agent picks tools from that text. Can you confirm the new wording is intended and stable?"
    );
  });

  it('the mute-risk guard is verbatim', () => {
    expect(FLAG_DESCRIPTION_GUARD).toBe(
      "This isn't a bug report — you're asking whether the change was intended."
    );
  });

  it('the definition-change hint stands in for the call, on every class', () => {
    expect(defChangeNoCallSub('Acme Payments')).toBe(
      "No call is shared — the evidence is Acme Payments's own published definitions, before and after."
    );
  });
});

describe('the Overview headline distinguishes servers that share a name', () => {
  // REGRESSION: the live stack runs two MCP servers publishing the SAME
  // serverInfo.name, so Overview rendered two byte-identical health lines and
  // the owner reasonably read it as a duplicate.
  it('two servers with one name produce two different lines', () => {
    const http = mcpHeadline({ name: 'acme-tools-mcp', version: '1.2.0', origin: 'mcp.acme.test' }, [], () => '', 1);
    const stdio = mcpHeadline({ name: 'acme-tools-mcp', version: '1.2.0', origin: 'stdio' }, [], () => '', 1);
    expect(http.text).toBe('Server: acme-tools-mcp v1.2.0 · mcp.acme.test. You: no drift detected.');
    expect(stdio.text).toBe('Server: acme-tools-mcp v1.2.0 · stdio. You: no drift detected.');
    expect(http.text).not.toBe(stdio.text);
  });

  it('a server with no origin reads exactly as before', () => {
    expect(mcpHeadline({ name: 'acme-tools-mcp', version: '1.2.0' }, [], () => '', 1).text).toBe(
      'Server: acme-tools-mcp v1.2.0. You: no drift detected.'
    );
  });
});
