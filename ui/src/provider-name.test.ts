// QA 2026-09-14: the flag sheet for a version-diff finding was titled
// `New thread with Api Acme Test` — humanize('api-acme-test'), the host slug an
// uploaded contract is keyed by — while the card above it read `Acme Payments
// API`. The sheet's title is what gets pasted to the other organization, so it
// must resolve the same way the Edges panel and the Contracts card do.
import { describe, it, expect } from 'vitest';
import { humanize, providerNameForFinding } from './contracts';

const HOST = 'api.acme.test';
const CONTRACT = { integration: 'api-acme-test', role: 'provider' as const, peer_host: HOST, format: 'openapi', title: 'Acme Payments API', version: '2.0.0' };
const VERSION_DIFF = { kind: 'version-diff', integration: 'api-acme-test' };
const LIVE = { kind: 'live-vs-spec', integration: 'api-acme-test', peer_host: HOST };
const MCP_HOST = 'mcp.acme.test';
const MCP = { kind: 'definition_change', integration: 'mcp-acme-test', peer_host: MCP_HOST };
const MCP_CATALOGUE = { integration: 'mcp-acme-test', role: 'provider' as const, peer_host: MCP_HOST, format: 'mcp', title: 'acme-tools-mcp', version: '1.0.0' };

describe('providerNameForFinding', () => {
  it('a version diff takes the Edges panel name for the contract’s host, never a humanized host slug', () => {
    const name = providerNameForFinding(VERSION_DIFF, {
      contracts: [CONTRACT],
      edges: [{ peer_host: HOST, display_name: 'Acme Payments' }]
    });
    expect(name).toBe('Acme Payments');
    expect(name).not.toBe('Api Acme Test');
  });

  it('…then the contract’s own title, then the host itself — the identity is never invented', () => {
    expect(providerNameForFinding(VERSION_DIFF, { contracts: [CONTRACT], edges: [{ peer_host: HOST }] })).toBe('Acme Payments API');
    expect(providerNameForFinding(VERSION_DIFF, { contracts: [{ ...CONTRACT, title: undefined }], edges: [] })).toBe(HOST);
    // The contract is gone (removed after the diff was stored): the raw id,
    // not a title-cased pseudo-name.
    expect(providerNameForFinding(VERSION_DIFF, { contracts: [], edges: [] })).toBe('api-acme-test');
  });

  it('a call-evidenced finding takes the Edges panel name for its host, else the HOST ITSELF — never a humanized key', () => {
    // The collector derives every key now, so `humanize()` here produced
    // `Api Acme Test` / `Mcp Acme Test`: title-cased pseudo-companies pasted to
    // the other organization (the composed lane, 2026-09-20).
    expect(providerNameForFinding(LIVE, { contracts: [CONTRACT], edges: [{ peer_host: HOST, display_name: 'Acme Payments' }] })).toBe('Acme Payments');
    expect(providerNameForFinding(LIVE, { contracts: [CONTRACT], edges: [] })).toBe(HOST);
    expect(providerNameForFinding(LIVE, { contracts: [CONTRACT], edges: [] })).not.toBe('Api Acme Test');
    // An MCP server: its host, not its catalogue's self-reported title, and
    // never the humanized key. For a stdio server the host IS that
    // self-reported name, so one rule covers both transports.
    expect(providerNameForFinding(MCP, { contracts: [MCP_CATALOGUE], edges: [] })).toBe(MCP_HOST);
    expect(providerNameForFinding(MCP, { contracts: [MCP_CATALOGUE], edges: [] })).not.toBe('Mcp Acme Test');
    expect(providerNameForFinding({ kind: 'definition_change', integration: 'acme-tools-mcp', peer_host: 'acme-tools-mcp' }, { contracts: [], edges: [] })).toBe('acme-tools-mcp');
    // The host is unknown (an older row, no contract): the raw key, not prose.
    expect(providerNameForFinding({ kind: 'output_mismatch', integration: 'mcp-acme-test' }, { contracts: [], edges: [] })).toBe('mcp-acme-test');
  });

  it('the configured provider_display_name wins for a call-evidenced REST finding — and only that kind', () => {
    const ctx = { providerDisplayName: 'Acme (configured)', contracts: [CONTRACT], edges: [] };
    expect(providerNameForFinding(LIVE, ctx)).toBe('Acme (configured)');
    // …not for a version diff — that one resolves through its contract.
    expect(providerNameForFinding(VERSION_DIFF, ctx)).toBe('Acme Payments API');
    // …and never for an MCP finding: the server names itself. The config `integration_id` used to
    // be the scope; with it gone (2026-09-14) the kind is, and the composed lane caught the
    // regression where the configured REST name leaked onto the MCP flag sheet.
    expect(providerNameForFinding(MCP, ctx)).toBe(MCP_HOST);
    expect(providerNameForFinding({ kind: 'output_mismatch', integration: 'mcp-acme-test', peer_host: MCP_HOST }, ctx)).toBe(MCP_HOST);
  });

  it('humanize is a slug helper: dots are not word boundaries', () => {
    expect(humanize('acme-payments')).toBe('Acme Payments');
    expect(humanize('api.acme.test')).toBe('Api.acme.test');
  });
});
