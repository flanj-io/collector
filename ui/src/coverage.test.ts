import { describe, it, expect } from 'vitest';
import { callCoverage, notCheckedTitle, NOT_CHECKED_LABEL, type CoverageSpec } from './coverage';

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

  it('outbound: an UNSCOPED spec validates every outbound call (config spec_path with no peer_host)', () => {
    const specs = [providerSpec(undefined)];
    expect(callCoverage(out('api.acme.test'), specs)).toBe('checked');
    expect(callCoverage(out('anything.else'), specs)).toBe('checked');
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

  it('mcp: checked only once a SNAPSHOT exists for that host', () => {
    expect(callCoverage(mcp('mcp.acme.test'), [mcpSpec('mcp.acme.test')])).toBe('checked');
    // Traffic seen but no tools/list yet — nothing to validate against.
    expect(callCoverage(mcp('mcp.acme.test'), [])).toBe('not-checked');
    // Another server's snapshot must not cover this one.
    expect(callCoverage(mcp('mcp.other.test'), [mcpSpec('mcp.acme.test')])).toBe('not-checked');
  });

  it('mcp is not covered by an unscoped REST spec, and vice versa', () => {
    expect(callCoverage(mcp('mcp.acme.test'), [providerSpec(undefined)])).toBe('not-checked');
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
