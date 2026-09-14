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
const LIVE = { kind: 'live-vs-spec', integration: 'acme-payments', peer_host: HOST };
const MCP = { kind: 'definition_change', integration: 'acme-tools' };

describe('providerNameForFinding', () => {
  it('a version diff takes the Edges panel name for the contract’s host, never a humanized host slug', () => {
    const name = providerNameForFinding(VERSION_DIFF, {
      healthIntegration: 'acme-payments',
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

  it('call-evidenced findings keep the SDK integration slug, humanized — the relay’s own rule', () => {
    expect(providerNameForFinding(LIVE, { contracts: [CONTRACT], edges: [] })).toBe('Acme Payments');
    // e2e pins `New thread with Acme Tools` for the MCP server (mcp.spec.ts).
    expect(providerNameForFinding(MCP, { contracts: [], edges: [] })).toBe('Acme Tools');
  });

  it('the configured provider_display_name still wins for the integration it names', () => {
    const ctx = { providerDisplayName: 'Acme (configured)', healthIntegration: 'acme-payments', contracts: [CONTRACT], edges: [] };
    expect(providerNameForFinding(LIVE, ctx)).toBe('Acme (configured)');
    // …and not for a version diff keyed by a different id — that one resolves through its contract.
    expect(providerNameForFinding(VERSION_DIFF, ctx)).toBe('Acme Payments API');
    // With no integration on /api/health the configured name applies to every finding (as before).
    expect(providerNameForFinding(VERSION_DIFF, { ...ctx, healthIntegration: undefined })).toBe('Acme (configured)');
  });

  it('humanize is a slug helper: dots are not word boundaries', () => {
    expect(humanize('acme-payments')).toBe('Acme Payments');
    expect(humanize('api.acme.test')).toBe('Api.acme.test');
  });
});
