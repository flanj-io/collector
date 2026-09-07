import { describe, it, expect } from 'vitest';
import {
  callCoverage,
  callCoverageDetail,
  notCheckedTitle,
  ERROR_RESULT_NOT_CHECKED,
  validatedCallsMeta,
  NOT_CHECKED_LABEL,
  SINCE_LOAD,
  SINCE_SNAPSHOT,
  SINCE_UPLOAD,
  type CoverageSpec
} from './coverage';

const providerSpec = (peer_host?: string): CoverageSpec => ({ role: 'provider', format: 'openapi', peer_host });
const selfSpec: CoverageSpec = { role: 'self', format: 'openapi' };
const mcpSpec = (peer_host: string): CoverageSpec => ({ format: 'mcp', peer_host });

const out = (peer_host: string) => ({ peer_host, direction: 'client', edge_class: 'external' });
const inb = (peer_host: string) => ({ peer_host, direction: 'server', edge_class: 'external' });
const mcp = (peer_host: string) => ({ peer_host, direction: 'client', edge_class: 'external', transport: 'mcp' });

describe('callCoverage — mirrors processor/flanjdrift', () => {
  it('internal is its own state, never "not checked"', () => {
    // Metadata-only BY DESIGN — nothing is missing, so it must not read as a gap.
    expect(callCoverage({ ...out('ledger'), edge_class: 'internal' }, [])).toBe('internal');
    expect(callCoverage({ ...out('ledger'), edge_class: 'internal' }, [providerSpec()])).toBe('internal');
  });

  it('outbound: no contract at all → not checked (THE bug this fixes)', () => {
    expect(callCoverage(out('api.globex.test'), [])).toBe('not-checked');
  });

  it('outbound: a host-scoped spec checks only that host', () => {
    const specs = [providerSpec('api.acme.test')];
    expect(callCoverage(out('api.acme.test'), specs)).toBe('checked');
    expect(callCoverage(out('api.globex.test'), specs)).toBe('not-checked');
  });

  it('outbound: an UNBOUND contract covers nothing — binding is mandatory at upload', () => {
    // The config `spec_path` with no `peer_host` used to validate EVERY
    // outbound call against one document, and this asserted exactly that.
    // Contracts are uploaded now and bind to one host (CONTRACTS §8 dropped
    // both keys), so an unbound row — only reachable from a store written
    // before uploads existed — validates nothing. Reading it as coverage would
    // mark every outbound call "checked" while the processor checks none of
    // them, which is this module's original lie wearing a new hat.
    const specs = [providerSpec(undefined)];
    expect(callCoverage(out('api.acme.test'), specs)).toBe('not-checked');
    expect(callCoverage(out('anything.else'), specs)).toBe('not-checked');
  });

  it('a self contract does NOT check outbound calls, and a provider contract does not check inbound', () => {
    expect(callCoverage(out('api.acme.test'), [selfSpec])).toBe('not-checked');
    expect(callCoverage(inb('api.consumer-a.test'), [providerSpec('api.acme.test')])).toBe('not-checked');
  });

  it('inbound: checked once a self contract is loaded, for any peer (no host scoping)', () => {
    expect(callCoverage(inb('api.consumer-a.test'), [selfSpec])).toBe('checked');
    expect(callCoverage(inb('api.consumer-b.test'), [selfSpec])).toBe('checked');
    expect(callCoverage(inb('api.consumer-a.test'), [])).toBe('not-checked');
  });

  const TOOLS = {
    'acme-tools': [
      { name: 'get_balance', hasOutputSchema: true },
      { name: 'list_transactions', hasOutputSchema: false } // the mock omits it deliberately
    ]
  };
  const mcpCall = (tool: string) => ({ ...mcp('mcp.acme.test'), integration: 'acme-tools', mcp_tool_name: tool });

  it('mcp: needs a SNAPSHOT for that host', () => {
    // Traffic seen but no tools/list yet — nothing to validate against.
    expect(callCoverage(mcpCall('get_balance'), [], TOOLS)).toBe('not-checked');
    // Another server's snapshot must not cover this one.
    expect(callCoverage({ ...mcpCall('get_balance'), peer_host: 'mcp.other.test' }, [mcpSpec('mcp.acme.test')], TOOLS)).toBe('not-checked');
  });

  it('mcp: coverage is per TOOL — only a tool publishing an outputSchema is validated', () => {
    const specs = [mcpSpec('mcp.acme.test')];
    // get_balance publishes an outputSchema, so its RESULT is checked.
    expect(callCoverage(mcpCall('get_balance'), specs, TOOLS)).toBe('checked');
    // list_transactions publishes none — the processor never validates it, so
    // claiming "conforming" here would be the same lie in a new place.
    expect(callCoverage(mcpCall('list_transactions'), specs, TOOLS)).toBe('not-checked');
    // A tool absent from the snapshot, and rows not loaded at all, both resolve
    // conservatively rather than assuming coverage.
    expect(callCoverage(mcpCall('unknown_tool'), specs, TOOLS)).toBe('not-checked');
    expect(callCoverage(mcpCall('get_balance'), specs, {})).toBe('not-checked');
  });

  it('mcp: an isError result is NOT checked — error output is not contract evidence', () => {
    // The processor skips isError by name (`op.OutputSchema != nil &&
    // !call.MCPIsError`), so nothing ever compared this result to the schema.
    // Reading it as `conforming` claimed a check that never ran; the per-tool
    // drift fallback read it as DRIFTED, filing an execution failure against
    // the provider as a contract breach — two slots from the status tooltip
    // saying it is no such thing.
    const specs = [mcpSpec('mcp.acme.test')];
    expect(callCoverage({ ...mcpCall('get_balance'), mcp_is_error: true }, specs, TOOLS)).toBe('not-checked');
    // The same tool, same snapshot, answering normally: still checked. The
    // gate is per CALL — it must not take the tool's other calls down with it.
    expect(callCoverage(mcpCall('get_balance'), specs, TOOLS)).toBe('checked');
  });

  it('mcp is not covered by an unscoped REST spec, and vice versa', () => {
    expect(callCoverage(mcpCall('get_balance'), [providerSpec(undefined)], TOOLS)).toBe('not-checked');
    expect(callCoverage(out('api.acme.test'), [mcpSpec('api.acme.test')])).toBe('not-checked');
  });

  /* ── The chip names its CAUSE ────────────────────────────────────────────
   *
   * One chip, three causes, and they were all wearing the no-contract string.
   * "No contract uploaded for mcp.acme.test" on a tool that simply declares no
   * outputSchema is false to the cause AND non-actionable: MCP contracts are
   * never uploaded — the server publishes its own on tools/list — so the
   * sentence describes a control the operator does not have.
   */
  it('the not-checked causes are distinguished, not merged', () => {
    const specs = [mcpSpec('mcp.acme.test')];
    const reasonOf = (c: Parameters<typeof callCoverageDetail>[0]) =>
      callCoverageDetail(c, specs, TOOLS).reason;

    // No snapshot for the host at all — nothing is bound.
    expect(callCoverageDetail(mcpCall('get_balance'), [], TOOLS).reason).toBe('no-contract');
    // Bound, but this tool publishes nothing to check its result against.
    expect(reasonOf(mcpCall('list_transactions'))).toBe('no-output-contract');
    // Bound and declared, but the result was an execution failure.
    expect(reasonOf({ ...mcpCall('get_balance'), mcp_is_error: true })).toBe('error-result');
    // A checked call carries no reason at all.
    expect(callCoverageDetail(mcpCall('get_balance'), specs, TOOLS)).toEqual({ coverage: 'checked' });
    // REST keeps the original cause.
    expect(callCoverageDetail(out('api.globex.test'), [], {}).reason).toBe('no-contract');
  });

  it('copy: the chip, and one tooltip per cause', () => {
    expect(NOT_CHECKED_LABEL).toBe('not checked');

    // no-contract — unchanged, and still names the host so it is actionable.
    expect(notCheckedTitle('no-contract', { peer_host: 'api.globex.test' })).toBe(
      'No contract uploaded for api.globex.test — this call was captured, not validated.'
    );
    expect(notCheckedTitle('no-contract')).toBe('No contract uploaded — this call was captured, not validated.');

    // no-output-contract — the honest string the Contracts tab already shows
    // for this very tool, so the two surfaces cannot say different things.
    const noSchema = notCheckedTitle('no-output-contract', {
      integration: 'acme-tools',
      mcp_tool_name: 'list_transactions'
    });
    expect(noSchema).toBe(
      "No output contract declared — acme-tools doesn't say what list_transactions returns, " +
        "so output drift on this tool can't be checked."
    );
    expect(noSchema).not.toContain('uploaded');

    // error-result — its own sentence, echoing the status chip beside it.
    expect(notCheckedTitle('error-result', { mcp_is_error: true })).toBe(ERROR_RESULT_NOT_CHECKED);
    expect(ERROR_RESULT_NOT_CHECKED).toContain('isError');

    // THE POINT: three causes, three different strings.
    expect(new Set([notCheckedTitle('no-contract'), noSchema, ERROR_RESULT_NOT_CHECKED]).size).toBe(3);
  });
});

/* ── The temporal gate ───────────────────────────────────────────────────
 *
 * A contract validates a call only from its own binding time forward. The
 * whole first-launch false-green family sat on the absence of this: an upload
 * flipped already-captured calls to CONFORMING with no new traffic, directly
 * under a notice reading "Calls already captured aren't re-checked".
 */

/** `TOOLS` above is scoped to the first suite; this one needs its own. */
const MCP_TOOLS = {
  'acme-tools': [{ name: 'get_balance', hasOutputSchema: true }]
};

const BOUND = '2026-09-02T12:00:00Z';
const BEFORE = '2026-09-02T11:59:59Z';
const AFTER = '2026-09-02T12:00:01Z';

describe('callCoverage — a contract cannot validate a call it never saw', () => {
  it('outbound: a call captured BEFORE the upload is not checked by it', () => {
    const specs = [{ ...providerSpec('api.acme.test'), loaded_at: BOUND }];
    expect(callCoverage({ ...out('api.acme.test'), captured_at: BEFORE }, specs)).toBe('not-checked');
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER }, specs)).toBe('checked');
  });

  it('the QA repro: uploading a contract does not retro-validate the calls already on screen', () => {
    // Six calls captured, THEN a document dropped in. Nothing re-runs them —
    // the drift processor saw them before the spec cache had anything to say.
    const captured = Array.from({ length: 6 }, (_, i) => ({
      ...out('api.acme.test'),
      captured_at: `2026-09-02T11:5${i}:00Z`
    }));
    const specs = [{ ...providerSpec('api.acme.test'), loaded_at: BOUND }];
    expect(captured.map((c) => callCoverage(c, specs))).toEqual(Array(6).fill('not-checked'));
    // The next call to arrive IS validated — the fix is temporal, not a blanket refusal.
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER }, specs)).toBe('checked');
  });

  it('a call captured at exactly the binding instant counts as checked', () => {
    const specs = [{ ...providerSpec('api.acme.test'), loaded_at: BOUND }];
    expect(callCoverage({ ...out('api.acme.test'), captured_at: BOUND }, specs)).toBe('checked');
  });

  it('the EARLIEST binding for the host wins when two rows carry the same one', () => {
    // Between them the host has been bound since 11:00, so an 11:30 call was
    // in front of the processor while a contract was loaded.
    const specs = [
      { ...providerSpec('api.acme.test'), loaded_at: '2026-09-02T13:00:00Z' },
      { ...providerSpec('api.acme.test'), loaded_at: '2026-09-02T11:00:00Z' }
    ];
    expect(callCoverage({ ...out('api.acme.test'), captured_at: '2026-09-02T11:30:00Z' }, specs)).toBe('checked');
    expect(callCoverage({ ...out('api.acme.test'), captured_at: '2026-09-02T10:30:00Z' }, specs)).toBe('not-checked');
  });

  it('inbound: the self contract is gated the same way', () => {
    const specs = [{ ...selfSpec, loaded_at: BOUND }];
    expect(callCoverage({ ...inb('api.consumer-a.test'), captured_at: BEFORE }, specs)).toBe('not-checked');
    expect(callCoverage({ ...inb('api.consumer-a.test'), captured_at: AFTER }, specs)).toBe('checked');
  });

  it('mcp: a call made before the tools/list snapshot arrived is not checked by it', () => {
    const specs = [{ ...mcpSpec('mcp.acme.test'), loaded_at: BOUND }];
    const call = (captured_at: string) => ({
      ...mcp('mcp.acme.test'),
      integration: 'acme-tools',
      mcp_tool_name: 'get_balance',
      captured_at
    });
    expect(callCoverage(call(BEFORE), specs, MCP_TOOLS)).toBe('not-checked');
    expect(callCoverage(call(AFTER), specs, MCP_TOOLS)).toBe('checked');
  });

  it('a contract bound to ANOTHER host stays not-checked whatever the timestamps say', () => {
    // The gate is an extra requirement on top of the host match, never a
    // substitute for it.
    const specs = [{ ...providerSpec('api.globex.test'), loaded_at: BEFORE }];
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER }, specs)).toBe('not-checked');
  });

  it('an unknown timestamp leaves the host-match answer standing, in EITHER slot', () => {
    // Rows written before these fields were recorded cannot be placed in time.
    // The gate is then not applied at all rather than guessed at — it must not
    // invent a fresh verdict from a blank, in either direction.
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER }, [providerSpec('api.acme.test')])).toBe('checked');
    expect(callCoverage(out('api.acme.test'), [{ ...providerSpec('api.acme.test'), loaded_at: BOUND }])).toBe('checked');
    expect(callCoverage({ ...out('api.acme.test'), captured_at: 'not a date' }, [
      { ...providerSpec('api.acme.test'), loaded_at: BOUND }
    ])).toBe('checked');
  });

  it('internal still short-circuits before any of this', () => {
    expect(
      callCoverage({ ...out('ledger'), edge_class: 'internal', captured_at: BEFORE }, [
        { ...providerSpec('ledger'), loaded_at: BOUND }
      ])
    ).toBe('internal');
  });
});

describe('validatedCallsMeta — the card line that makes zero visible', () => {
  it('counts, pluralises, and says zero out loud', () => {
    expect(validatedCallsMeta(0)).toBe('validated 0 calls since upload');
    expect(validatedCallsMeta(1)).toBe('validated 1 call since upload');
    expect(validatedCallsMeta(6)).toBe('validated 6 calls since upload');
  });

  it('anchors the clause to how the contract actually got here', () => {
    // "since upload" on a config-loaded self contract, or on a tools/list
    // nobody put there, is the small kind of lie this module exists to stop.
    expect(validatedCallsMeta(2, SINCE_LOAD)).toBe('validated 2 calls since it loaded');
    expect(validatedCallsMeta(2, SINCE_SNAPSHOT)).toBe('validated 2 calls since this snapshot');
    expect(validatedCallsMeta(2, SINCE_UPLOAD)).toBe('validated 2 calls since upload');
  });
});

/* ── The processor's verdict decides (2026-09-07) ────────────────────────
 *
 * BUG: upload spec-v1 for api.acme.test, drive a drifting charge one to four
 * seconds later, and the chip read CONFORMING with no finding and no drifted
 * flag. The processor's spec cache had not loaded the document yet, so nothing
 * validated the call — while the temporal mirror above read `checked` off the
 * STORE's `loaded_at`. Permanent on a tiered front with the wrong
 * store_pod_token, which never loads the document at all. The processor now
 * stamps every call with what it did; this module reads the stamp, and the
 * mirror answers only for rows that predate it.
 */
describe('callCoverage — the drift processor stamp is the fact', () => {
  const bound = [{ ...providerSpec('api.acme.test'), loaded_at: BOUND }];
  const stamped = (validated: string, validated_reason?: string, extra: Record<string, unknown> = {}) => ({
    ...out('api.acme.test'),
    captured_at: AFTER,
    validated,
    validated_reason,
    ...extra
  });

  it('THE bug: a call stamped not-validated is not checked, whatever the timestamps say', () => {
    // Captured AFTER the upload landed in the store — the temporal gate says
    // checked — but the processor had nothing for the host when it went through.
    const c = stamped('not-validated', 'no-contract');
    expect(callCoverage(c, bound)).toBe('not-checked');
    // A contract IS bound now, so the cause is named as such — "No contract
    // uploaded" would be false to the operator looking at the card.
    expect(callCoverageDetail(c, bound).reason).toBe('contract-not-reached');
  });

  it('the same stamp with nothing bound now is the plain no-contract', () => {
    expect(callCoverageDetail(stamped('not-validated', 'no-contract'), []).reason).toBe('no-contract');
    // ...and a not-validated stamp with no reason at all resolves the same way.
    expect(callCoverageDetail(stamped('not-validated'), []).reason).toBe('no-contract');
    expect(callCoverageDetail(stamped('not-validated'), bound).reason).toBe('contract-not-reached');
  });

  it('clean and drifted are checked — even when the UI no longer sees a contract for the host', () => {
    // The check HAPPENED. A contract removed afterwards, or a call captured
    // before the store's loaded_at (a replaced document), changes nothing.
    expect(callCoverage({ ...stamped('clean'), captured_at: BEFORE }, [])).toBe('checked');
    expect(callCoverage({ ...stamped('drifted'), captured_at: BEFORE }, [])).toBe('checked');
    expect(callCoverageDetail(stamped('clean'), bound)).toEqual({ coverage: 'checked' });
  });

  it('a record with no verdict at all is not checked, and says so', () => {
    // An older front, or a pipeline with no drift processor: the store writes
    // `unknown`. The temporal mirror would have said checked.
    expect(callCoverageDetail(stamped('unknown'), bound)).toEqual({ coverage: 'not-checked', reason: 'no-verdict' });
  });

  it('a verdict word this UI does not know is never checked', () => {
    expect(callCoverageDetail(stamped('validated-partially'), bound)).toEqual({
      coverage: 'not-checked',
      reason: 'unspecified'
    });
    expect(callCoverageDetail(stamped('not-validated', 'some-future-gate'), bound).reason).toBe('unspecified');
  });

  it('the processor reasons pass through as their own causes', () => {
    for (const reason of [
      'not-routable',
      'status-undeclared',
      'media-type-undeclared',
      'body-not-decodable',
      'validator-error',
      'tool-not-listed',
      'input-required',
      'no-output-contract',
      'error-result',
      'task-handle',
      'result-not-json'
    ] as const) {
      expect(callCoverageDetail(stamped('not-validated', reason), bound).reason).toBe(reason);
    }
  });

  it('a stamped MCP call is judged off the stamp, not the snapshot rows', () => {
    // Pre-stamp, an MCP tool with no outputSchema was `not-checked` only if the
    // snapshot rows had loaded. The processor already knows.
    const mcpCall = { ...mcp('mcp.acme.test'), integration: 'acme-tools', mcp_tool_name: 'list_transactions' };
    expect(callCoverageDetail({ ...mcpCall, validated: 'not-validated', validated_reason: 'no-output-contract' }, [], {}).reason).toBe(
      'no-output-contract'
    );
    expect(callCoverage({ ...mcpCall, mcp_tool_name: 'get_balance', validated: 'clean' }, [], {})).toBe('checked');
  });

  it('internal still short-circuits before the stamp', () => {
    expect(callCoverage({ ...stamped('clean'), edge_class: 'internal' }, bound)).toBe('internal');
  });

  it('the legacy mirror answers ONLY for rows with no verdict', () => {
    // The pre-migration row: no `validated` at all. The temporal gate decides.
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER }, bound)).toBe('checked');
    expect(callCoverage({ ...out('api.acme.test'), captured_at: BEFORE }, bound)).toBe('not-checked');
    // An EMPTY string is the same absence (the column's migration default).
    expect(callCoverage({ ...out('api.acme.test'), captured_at: AFTER, validated: '' }, bound)).toBe('checked');
  });
});

describe('notCheckedTitle — one sentence per cause, each true', () => {
  const rest = { peer_host: 'api.acme.test', method: 'POST', route: '/v1/charges' };
  const tool = { integration: 'acme-tools', mcp_tool_name: 'get_balance' };

  it('contract-not-reached names the host and does not blame a missing upload', () => {
    const s = notCheckedTitle('contract-not-reached', rest);
    expect(s).toContain('for api.acme.test is bound now');
    expect(s).toContain('had not reached the drift processor');
    expect(s).not.toContain('No contract uploaded');
  });

  it('not-routable names the call the document does not describe', () => {
    expect(notCheckedTitle('not-routable', rest)).toContain('POST /v1/charges');
    expect(notCheckedTitle('not-routable', {})).toContain('this call');
  });

  it('an undeclared status and an undeclared media type are different sentences — the fixes differ', () => {
    // The peer-review case: a problem+json body under a contract that declares
    // application/json for the status. kin-openapi refuses before any schema
    // comparison, and the finding path used to read that refusal as "no
    // findings" — clean. The operator's fix is to declare (or map) the media
    // type; for an undeclared status it is to declare the status.
    const mt = notCheckedTitle('media-type-undeclared', {
      ...rest,
      status_code: 422,
      response_content_type: 'application/problem+json'
    });
    expect(mt).toContain('POST /v1/charges');
    expect(mt).toContain('status 422');
    expect(mt).toContain('application/problem+json');
    expect(mt).toContain('nothing was compared');
    const st = notCheckedTitle('status-undeclared', { ...rest, status_code: 422 });
    expect(st).toContain('declares no response for status 422');
    expect(st).not.toContain('application/');
    expect(notCheckedTitle('status-undeclared', {})).toContain('this status');
    expect(notCheckedTitle('media-type-undeclared', {})).toContain('this media type');
    expect(notCheckedTitle('body-not-decodable', { response_content_type: 'application/json' })).toContain('application/json');
    expect(notCheckedTitle('body-not-decodable', {})).toContain('its declared media type');
    expect(notCheckedTitle('validator-error')).toContain('not validated');
  });

  it('the MCP causes name the tool', () => {
    expect(notCheckedTitle('tool-not-listed', tool)).toContain('get_balance');
    expect(notCheckedTitle('result-not-json', tool)).toContain("get_balance's outputSchema");
    expect(notCheckedTitle('task-handle', tool)).toContain('Tasks handle');
    expect(notCheckedTitle('input-required', tool)).toContain('input_required');
  });

  it('no-verdict and unspecified never claim a check happened', () => {
    for (const r of ['no-verdict', 'unspecified'] as const) {
      expect(notCheckedTitle(r)).toContain('not validated');
    }
  });

  it('every cause has its own sentence', () => {
    const reasons = [
      'no-contract',
      'contract-not-reached',
      'no-verdict',
      'not-routable',
      'status-undeclared',
      'media-type-undeclared',
      'body-not-decodable',
      'validator-error',
      'tool-not-listed',
      'input-required',
      'no-output-contract',
      'error-result',
      'task-handle',
      'result-not-json',
      'unspecified'
    ] as const;
    const strings = reasons.map((r) => notCheckedTitle(r, { ...rest, ...tool }));
    expect(new Set(strings).size).toBe(reasons.length);
    for (const s of strings) expect(s.length).toBeGreaterThan(20);
  });
});
