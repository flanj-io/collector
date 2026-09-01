import { describe, it, expect } from 'vitest';
import {
  contractMeta,
  contractsByHost,
  edgeContractLine,
  isEvidenceFor,
  hostLooksRoutable,
  bindingChecks,
  hasBindingWarning,
  endpointCount,
  provenanceWord,
  rollCall,
  serversLine,
  uncoveredHeading,
  uncoveredProviders,
  ROLL_CALL_ZERO,
  NO_CONTRACT_ROW,
  NO_CONTRACT_SECTION,
  type ContractSpec
} from './contracts';

const NOW = Date.parse('2026-08-31T12:00:00Z');

const uploaded = (peer_host: string, over: Partial<ContractSpec> = {}): ContractSpec => ({
  integration: peer_host.replace(/\./g, '-'),
  role: 'provider',
  format: 'openapi',
  peer_host,
  source: 'upload',
  version: '1.0.0',
  endpoints: 4,
  loaded_at: '2026-08-19T12:00:00Z',
  ...over
});

const edge = (peer_host: string) => ({ peer_host, direction: 'client' });

describe('provenance', () => {
  it('the word tracks the SOURCE, so which contract is live reads on sight', () => {
    expect(provenanceWord(uploaded('api.acme.test'))).toBe('uploaded');
    expect(provenanceWord({ integration: 'self', role: 'self', source: 'config' })).toBe('loaded');
    expect(provenanceWord({ integration: 'mcp-acme', format: 'mcp' })).toBe('observed');
  });

  it('falls back sensibly for rows written before provenance was recorded', () => {
    // A provider contract can only have been uploaded; a self contract can only
    // have come from config. Guessing wrong here would put the wrong word on a
    // card, which is the one thing this line exists to get right.
    expect(provenanceWord({ integration: 'acme', role: 'provider' })).toBe('uploaded');
    expect(provenanceWord({ integration: 'self', role: 'self' })).toBe('loaded');
  });
});

describe('endpointCount', () => {
  it('pluralises — the REST branch used to render "1 endpoints"', () => {
    expect(endpointCount(1)).toBe('1 endpoint');
    expect(endpointCount(4)).toBe('4 endpoints');
    expect(endpointCount(0)).toBe('0 endpoints');
    expect(endpointCount(undefined)).toBe('0 endpoints');
  });
});

describe('contractMeta', () => {
  it('reads as endpoints · version · provenance and recency', () => {
    expect(contractMeta(uploaded('api.acme.test'), NOW)).toBe('4 endpoints · v1.0.0 · uploaded 12d ago');
  });

  it('names the version it replaced, so a replace is legible without an archive', () => {
    expect(
      contractMeta(uploaded('api.acme.test', { version: '2.1.0', prev_version: '1.0.0', loaded_at: '2026-08-31T10:00:00Z' }), NOW)
    ).toBe('4 endpoints · v2.1.0 · uploaded 2h ago · replaced v1.0.0');
  });

  it('never says anything about age beyond how long ago it was', () => {
    // v1 ships relative time and NOTHING else: no threshold, no amber, no nag.
    // Contract age must never read as a defect — that severity class belongs to
    // what the PROVIDER did.
    const ancient = contractMeta(uploaded('api.acme.test', { loaded_at: '2025-01-01T00:00:00Z' }), NOW);
    for (const banned of ['stale', 'out of date', 'outdated', 'new version', 'missing', 'uncovered', 'gap']) {
      expect(ancient.toLowerCase()).not.toContain(banned);
    }
  });
});

describe('edgeContractLine', () => {
  it('is the muted meta line under the host on an Edges row', () => {
    expect(edgeContractLine(uploaded('api.acme.test'), NOW)).toBe('contract v1.0.0 · uploaded 12d ago');
  });

  it('drops the version when the document declares none', () => {
    expect(edgeContractLine(uploaded('api.acme.test', { version: '' }), NOW)).toBe('contract · uploaded 12d ago');
  });
});

describe('contractsByHost', () => {
  it('indexes provider contracts by the host each is bound to', () => {
    const byHost = contractsByHost([uploaded('api.acme.test'), uploaded('api.globex.test')]);
    expect([...byHost.keys()].sort()).toEqual(['api.acme.test', 'api.globex.test']);
  });

  it('skips self contracts, MCP snapshots and UNBOUND rows', () => {
    // An unbound row validates nothing (binding is mandatory at upload), so
    // counting it as coverage would mark every provider checked while the
    // processor checks none of them.
    const byHost = contractsByHost([
      { integration: 'self', role: 'self', peer_host: 'api.self.test' },
      { integration: 'mcp-acme', format: 'mcp', peer_host: 'mcp.acme.test' },
      { integration: 'unbound', role: 'provider', format: 'openapi' }
    ]);
    expect(byHost.size).toBe(0);
  });
});

describe('uncoveredProviders', () => {
  const mcp = new Set(['mcp.acme.test']);

  it('lists outbound providers with no contract, once each', () => {
    const edges = [edge('api.acme.test'), edge('api.globex.test'), edge('api.globex.test')];
    expect(uncoveredProviders(edges, [uploaded('api.acme.test')], mcp)).toEqual(['api.globex.test']);
  });

  it('never lists an MCP server — its tools/list IS the contract', () => {
    // Listing one would invent a job that does not exist and send the operator
    // looking for a document no vendor publishes.
    expect(uncoveredProviders([edge('mcp.acme.test')], [], mcp)).toEqual([]);
  });

  it('ignores inbound edges — coverage there is the self-contract story', () => {
    expect(uncoveredProviders([{ peer_host: 'api.consumer.test', direction: 'server' }], [], mcp)).toEqual([]);
  });
});

describe('rollCall', () => {
  const mcp = new Set(['mcp.acme.test']);

  it('counts positive and names the MCP servers separately', () => {
    const edges = [edge('api.acme.test'), edge('api.globex.test'), edge('api.initech.test'), edge('mcp.acme.test')];
    expect(rollCall(edges, [uploaded('api.acme.test')], mcp)).toBe(
      '1 of 3 providers checked against a contract · 1 MCP server self-reports theirs'
    );
  });

  it('the zero state points at the fix instead of naming a deficiency', () => {
    expect(rollCall([edge('api.acme.test')], [], new Set())).toBe(ROLL_CALL_ZERO);
    expect(ROLL_CALL_ZERO).toContain('upload one');
  });

  it('an MCP-only estate is not a zero state — those servers ARE covered', () => {
    expect(rollCall([edge('mcp.acme.test')], [], mcp)).toBe('1 MCP server self-reports theirs');
  });

  it('agrees with itself in the singular', () => {
    expect(rollCall([edge('api.acme.test')], [uploaded('api.acme.test')], new Set())).toBe(
      '1 of 1 provider checked against a contract'
    );
  });

  it('never counts an unbound contract as coverage', () => {
    const edges = [edge('api.acme.test')];
    const unbound: ContractSpec = { integration: 'unbound', role: 'provider', format: 'openapi' };
    expect(rollCall(edges, [unbound], new Set())).toBe(ROLL_CALL_ZERO);
  });
});

describe('serversLine', () => {
  it('corroborates a match', () => {
    expect(serversLine(['api.globex.test'], 'api.globex.test')).toBe(
      'This spec’s servers: list api.globex.test — matches this edge.'
    );
  });

  it('warns on a mismatch WITHOUT refusing — gateways and staging hosts are normal', () => {
    expect(serversLine(['api.example.com'], 'api.globex.test')).toBe(
      'This spec’s servers: list api.example.com. You’re binding it to api.globex.test.'
    );
  });

  it('says nothing when the document declares no servers', () => {
    expect(serversLine([], 'api.globex.test')).toBe('');
  });
});

describe('uncoveredHeading', () => {
  it('carries the count so the section is legible while collapsed', () => {
    expect(uncoveredHeading(9)).toBe('Providers with no contract (9)');
  });
});

describe('isEvidenceFor', () => {
  const selfCard = { peerHost: '', isSelf: true };
  const acmeCard = { peerHost: 'api.acme.test', isSelf: false };
  const outbound = (peer_host: string) => ({ peer_host, direction: 'client' });
  const inbound = (peer_host: string) => ({ peer_host, direction: 'server' });

  it('a SELF card is evidenced only by INBOUND calls', () => {
    // The bug this exists to stop: a self card carries no peer_host, so a rule
    // that only filters by host counts every outbound call validated against
    // somebody ELSE'S contract — and reports the org's own API conforming on
    // evidence nobody gathered about it. Seen live on the mock stack.
    expect(isEvidenceFor(inbound('api.consumer-a.test'), selfCard)).toBe(true);
    expect(isEvidenceFor(outbound('api.acme.test'), selfCard)).toBe(false);
    expect(isEvidenceFor(outbound('api.globex.test'), selfCard)).toBe(false);
  });

  it('a PROVIDER card is evidenced only by outbound calls to the host it is bound to', () => {
    expect(isEvidenceFor(outbound('api.acme.test'), acmeCard)).toBe(true);
    expect(isEvidenceFor(outbound('api.globex.test'), acmeCard)).toBe(false);
    expect(isEvidenceFor(inbound('api.acme.test'), acmeCard)).toBe(false);
  });

  it('an unbound provider card can be evidenced by nothing at all', () => {
    // Nothing can be attributed to a card with no host, and attributing
    // everything would be the self-card bug again.
    expect(isEvidenceFor(outbound('api.acme.test'), { peerHost: '', isSelf: false })).toBe(false);
  });
});

describe('the two no-contract strings', () => {
  it('the section one is plural, because it heads a group', () => {
    // It started as the row string and read "this provider" over 28 rows.
    expect(NO_CONTRACT_SECTION).toContain('These providers');
    expect(NO_CONTRACT_ROW).toContain('this provider');
  });

  it('neither one names a deficiency in banned words', () => {
    for (const s of [NO_CONTRACT_ROW, NO_CONTRACT_SECTION]) {
      for (const banned of ['missing', 'uncovered', 'unprotected', 'gap', 'stale']) {
        expect(s.toLowerCase()).not.toContain(banned);
      }
    }
  });
});

describe('hostLooksRoutable', () => {
  it('accepts what traffic actually carries', () => {
    for (const h of ['api.acme.test', 'api-eu.acme.test', 'localhost', '10.0.0.7', 'acme.test']) {
      expect(hostLooksRoutable(h), h).toBe(true);
    }
  });

  it('flags a bare word — the `sad` case', () => {
    // Found by Idan in the live uploader: a bare word was accepted, and a
    // contract bound to it validates NOTHING forever while the card shows a
    // loaded contract. That is the silent failure mandatory binding exists to
    // prevent, so it has to be visible at the moment of binding.
    for (const h of ['sad', 'acme', 'todo', '']) {
      expect(hostLooksRoutable(h), h).toBe(false);
    }
  });

  it('never refuses `localhost` or an internal single-label host by shape alone', () => {
    // A refusal here would break real deployments — this drives a warning only.
    expect(hostLooksRoutable('localhost')).toBe(true);
  });
});

describe('bindingChecks', () => {
  it('a typo trips every signal at once — which is the pattern worth seeing', () => {
    const checks = bindingChecks('sad', ['api.acme.test'], false);
    expect(checks.every((c) => c.level === 'warn')).toBe(true);
    expect(hasBindingWarning(checks)).toBe(true);
    expect(checks[0].text).toContain('typo');
  });

  it('the good case reassures instead of staying silent', () => {
    const checks = bindingChecks('api.acme.test', ['api.acme.test'], true);
    expect(checks.every((c) => c.level === 'ok')).toBe(true);
    expect(hasBindingWarning(checks)).toBe(false);
  });

  it('a gateway host warns on servers but stays bindable — warn, never block', () => {
    const checks = bindingChecks('api-gateway.internal.test', ['api.acme.test'], true);
    expect(hasBindingWarning(checks)).toBe(true);
    // The host itself is fine and traffic exists; only the servers line objects.
    expect(checks.filter((c) => c.level === 'warn')).toHaveLength(1);
  });

  it('says nothing about servers when the document declares none', () => {
    const checks = bindingChecks('api.acme.test', [], true);
    expect(checks.some((c) => c.text.includes('servers:'))).toBe(false);
  });

  it('pre-traffic upload warns but is legitimate — a fresh install has no edges', () => {
    const checks = bindingChecks('api.acme.test', ['api.acme.test'], false);
    expect(hasBindingWarning(checks)).toBe(true);
    expect(checks.find((c) => c.level === 'warn')!.text).toContain('starts validating when traffic arrives');
  });
});
