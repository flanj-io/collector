import { describe, it, expect } from 'vitest';
import {
  callCoverage,
  notCheckedTitle,
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

  it('mcp is not covered by an unscoped REST spec, and vice versa', () => {
    expect(callCoverage(mcpCall('get_balance'), [providerSpec(undefined)], TOOLS)).toBe('not-checked');
    expect(callCoverage(out('api.acme.test'), [mcpSpec('api.acme.test')])).toBe('not-checked');
  });

  it('copy: the chip and a host-naming tooltip', () => {
    expect(NOT_CHECKED_LABEL).toBe('not checked');
    expect(notCheckedTitle('api.globex.test')).toBe(
      'No contract uploaded for api.globex.test — this call was captured, not validated.'
    );
    expect(notCheckedTitle(undefined)).toBe('No contract uploaded — this call was captured, not validated.');
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
