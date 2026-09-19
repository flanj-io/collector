import { describe, it, expect } from 'vitest';
// The WHOLE deck, for the two sweeps that assert a claim appears nowhere in it.
// A per-constant list would pass the day somebody adds the forbidden sentence
// to a new constant, which is exactly the day it needs to fail.
import * as deck from './contracts';
import {
  contractMeta,
  contractsByHost,
  edgeContractLine,
  isEvidenceFor,
  hostLooksRoutable,
  bindingChecks,
  bindingTiming,
  contractHeading,
  contractOrigin,
  findingBelongsToContract,
  providerContractsEmptyText,
  FETCH_ONCE_ONLY,
  FETCH_PROMPT,
  FETCH_STAYS_LOCAL,
  MCP_NEEDS_NO_SETUP,
  PROBE_ACTION,
  PROBE_NOTHING_FOUND,
  PROBE_OFFER_ONLY,
  fetchedSourceForThread,
  fetchedSourceLine,
  probeCandidateLine,
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
    expect(provenanceWord({ integration: 'mcp-acme', format: 'mcp', source: 'observed' })).toBe('observed');
  });

  it('trusts the stored source ahead of the format', () => {
    // The format branch used to run first, so this card said "observed" while
    // the store — and every filter, client and conflict check reading it —
    // held `config` for every snapshot (2026-09-07). The stored word wins, so
    // a wrong one is visible on the one card that can show it.
    expect(provenanceWord({ integration: 'mcp-acme', format: 'mcp', source: 'config' })).toBe('loaded');
  });

  it('falls back sensibly for rows written before provenance was recorded', () => {
    // No source at all: an MCP snapshot can only have been observed, a
    // provider contract can only have been uploaded, and a self contract can
    // only have come from config. Guessing wrong here would put the wrong word
    // on a card, which is the one thing this line exists to get right.
    expect(provenanceWord({ integration: 'mcp-acme', format: 'mcp' })).toBe('observed');
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

  // Idan, 2026-09-19: a contract with no version SAYS so. The segment used to
  // vanish, which read the same as "this line has no version slot".
  it('says "version not specified" when the document declares no version', () => {
    expect(contractMeta(uploaded('api.acme.test', { version: undefined }), NOW))
      .toBe('4 endpoints · version not specified · uploaded 12d ago');
    expect(contractMeta(uploaded('api.acme.test', { version: '' }), NOW))
      .toBe('4 endpoints · version not specified · uploaded 12d ago');
  });

  it('treats a whitespace-only version as no version', () => {
    expect(contractMeta(uploaded('api.acme.test', { version: '  ' }), NOW))
      .toBe('4 endpoints · version not specified · uploaded 12d ago');
  });

  it('says so on a replace whose NEW document declares no version', () => {
    expect(
      contractMeta(uploaded('api.acme.test', { version: '', prev_version: '1.0.0', loaded_at: '2026-08-31T10:00:00Z' }), NOW)
    ).toBe('4 endpoints · version not specified · uploaded 2h ago · replaced v1.0.0');
  });

  it('still names the replace when the REPLACED document declared no version', () => {
    // prev_loaded_at is what marks a replace; prev_version is empty both when
    // nothing was replaced and when the replaced document had no version.
    const replaced = { version: '2.1.0', prev_loaded_at: '2026-08-20T10:00:00Z', loaded_at: '2026-08-31T10:00:00Z' };
    expect(contractMeta(uploaded('api.acme.test', replaced), NOW))
      .toBe('4 endpoints · v2.1.0 · uploaded 2h ago · replaced (version not specified)');
    expect(contractMeta(uploaded('api.acme.test', { ...replaced, prev_version: ' ' }), NOW))
      .toBe('4 endpoints · v2.1.0 · uploaded 2h ago · replaced (version not specified)');
  });

  it('adds no replace segment when nothing was replaced', () => {
    expect(contractMeta(uploaded('api.acme.test'), NOW)).not.toContain('replaced');
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

  // Idan, 2026-09-19: the version used to drop out (`contract · uploaded 12d ago`).
  it('says "version not specified" when the document declares none', () => {
    expect(edgeContractLine(uploaded('api.acme.test', { version: '' }), NOW))
      .toBe('contract · version not specified · uploaded 12d ago');
    expect(edgeContractLine(uploaded('api.acme.test', { version: undefined }), NOW))
      .toBe('contract · version not specified · uploaded 12d ago');
  });

  it('treats a whitespace-only version as none', () => {
    expect(edgeContractLine(uploaded('api.acme.test', { version: ' \t' }), NOW))
      .toBe('contract · version not specified · uploaded 12d ago');
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

  const mcpContract: ContractSpec = {
    integration: 'acme-tools', format: 'mcp', peer_host: 'mcp.acme.test', title: 'acme-tools-mcp'
  };

  it('counts positive and names the MCP servers separately', () => {
    const edges = [edge('api.acme.test'), edge('api.globex.test'), edge('api.initech.test'), edge('mcp.acme.test')];
    // The MCP clause counts CONTRACTS now, so the server needs a contract row —
    // which is the point: a stdio server has a contract and no edge.
    expect(rollCall(edges, [uploaded('api.acme.test'), mcpContract], mcp)).toBe(
      '1 of 3 providers checked against a contract · 1 MCP server self-reports theirs'
    );
  });

  it('the zero state points at the fix instead of naming a deficiency', () => {
    expect(rollCall([edge('api.acme.test')], [], new Set())).toBe(ROLL_CALL_ZERO);
    expect(ROLL_CALL_ZERO).toContain('upload one');
  });

  it('a search-learned catalog is the same server, not a second one (brief §3.5)', () => {
    const searched: ContractSpec = { ...mcpContract, integration: 'acme-tools:search', source: 'search_result' };
    expect(rollCall([edge('mcp.acme.test')], [mcpContract, searched], mcp)).toBe('1 MCP server self-reports theirs');
    expect(provenanceWord(searched)).toBe('searched');
  });

  it('an MCP-only estate is not a zero state — those servers ARE covered', () => {
    expect(rollCall([edge('mcp.acme.test')], [mcpContract], mcp)).toBe('1 MCP server self-reports theirs');
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
    const checks = bindingChecks('sad', ['api.acme.test']);
    expect(checks.every((c) => c.level === 'warn')).toBe(true);
    expect(hasBindingWarning(checks)).toBe(true);
    expect(checks[0].text).toContain('typo');
  });

  it('the good case reassures instead of staying silent', () => {
    const checks = bindingChecks('api.acme.test', ['api.acme.test']);
    expect(checks.every((c) => c.level === 'ok')).toBe(true);
    expect(hasBindingWarning(checks)).toBe(false);
  });

  it('a gateway host warns on servers but stays bindable — warn, never block', () => {
    const checks = bindingChecks('api-gateway.internal.test', ['api.acme.test']);
    expect(hasBindingWarning(checks)).toBe(true);
    expect(checks.filter((c) => c.level === 'warn')).toHaveLength(1);
  });

  it('says nothing about servers when the document declares none', () => {
    const checks = bindingChecks('api.acme.test', []);
    expect(checks.some((c) => c.text.includes('servers:'))).toBe(false);
  });

  it('NEVER scores traffic state — neither answer is a problem to weigh', () => {
    // Pre-traffic upload is the whole state of a fresh install, and traffic
    // already flowing is the normal case. Scoring either made the list cry
    // wolf, which is how a real warning gets ignored.
    for (const host of ['api.acme.test', 'nowhere.test']) {
      for (const c of bindingChecks(host, ['api.acme.test'])) {
        expect(c.text).not.toContain('validation starts');
        expect(c.text).not.toContain('starts validating');
      }
    }
  });
});

describe('bindingTiming', () => {
  it('states what happens next, as information rather than a verdict', () => {
    expect(bindingTiming('api.acme.test', true)).toContain('validation starts on the next one');
    expect(bindingTiming('api.acme.test', false)).toContain('starts validating when traffic arrives');
  });
});

describe('contract identity', () => {
  // The live stack runs two MCP servers publishing the SAME serverInfo.name.
  // The owner asked "why do I see 2 acme-tools-mcp in Contracts?" — they are
  // two real servers whose only distinguishing fact was never rendered at a
  // weight anyone reads.
  const http: ContractSpec = {
    integration: 'acme-tools', title: 'acme-tools-mcp', format: 'mcp',
    peer_host: 'mcp.acme.test', edge_class: 'external'
  };
  const stdio: ContractSpec = {
    integration: 'acme-tools-stdio', title: 'acme-tools-mcp', format: 'mcp',
    peer_host: 'acme-tools-mcp', edge_class: 'local-process'
  };

  it('two servers with the SAME name get different headings', () => {
    expect(contractHeading(http)).toBe('acme-tools-mcp · mcp.acme.test');
    expect(contractHeading(stdio)).toBe('acme-tools-mcp · stdio');
    expect(contractHeading(http)).not.toBe(contractHeading(stdio));
  });

  it('a local process reads as stdio, never as its pseudo-host', () => {
    // `acme-tools-mcp` is the stdio server's peer_host — rendering it would
    // print the name twice and say nothing about the transport.
    expect(contractOrigin(stdio)).toBe('stdio');
  });

  it('an uploaded REST contract reads as its bound host', () => {
    expect(contractHeading({
      integration: 'api-acme-test', title: 'Acme Payments API', format: 'openapi',
      peer_host: 'api.acme.test', edge_class: 'external'
    })).toBe('Acme Payments API · api.acme.test');
  });

  it('the self contract keeps a bare title — it has no counterparty', () => {
    expect(contractHeading({ integration: 'self', role: 'self', title: 'Our Public API' })).toBe('Our Public API');
  });
});

describe('rollCall counts MCP from contracts, not edges', () => {
  // REGRESSION: Overview said "1 MCP server self-reports theirs" while the
  // Contracts tab showed TWO MCP cards. A stdio server is `local-process` and
  // GET /api/edges is external-only, so counting MCP among outbound edge hosts
  // could never see it. Two surfaces disagreeing is the exact failure the roll
  // call exists to prevent.
  const httpMcp: ContractSpec = { integration: 'acme-tools', format: 'mcp', peer_host: 'mcp.acme.test', title: 'acme-tools-mcp' };
  const stdioMcp: ContractSpec = { integration: 'acme-tools-stdio', format: 'mcp', peer_host: 'acme-tools-mcp', edge_class: 'local-process', title: 'acme-tools-mcp' };
  const rest = uploaded('api.acme.test');
  const edges = [
    { peer_host: 'api.acme.test', direction: 'client' },
    { peer_host: 'api.globex.test', direction: 'client' },
    { peer_host: 'mcp.acme.test', direction: 'client' }
  ];

  it('counts the stdio server, which has no edge at all', () => {
    expect(rollCall(edges, [rest, httpMcp, stdioMcp], new Set(['mcp.acme.test']))).toBe(
      '1 of 2 providers checked against a contract · 2 MCP servers self-report theirs'
    );
  });

  it('the provider clause still counts EDGES — a provider with no traffic is not on the roll call', () => {
    // api.initech.test has a contract but no edge; it must not inflate the total.
    const extra = uploaded('api.initech.test');
    expect(rollCall(edges, [rest, extra, httpMcp], new Set(['mcp.acme.test']))).toContain('1 of 2 providers');
  });
});

/** No call resolves — the state of the calls page after the window turns over. */
const evictedAll = (): string | undefined => undefined;

describe('findingBelongsToContract', () => {
  // REGRESSION, found by the blind QA walk on the sqlite lane: one provider
  // rendered as TWO cards — the uploaded contract showing CONFORMING, and
  // beside it a second card carrying the BREAKING finding under "No contract
  // for this provider". The card denied the contract while rendering a verdict
  // only that contract could produce.
  //
  // Cause: an uploaded contract's integration is DERIVED from its host
  // (api-acme-test) because the operator is never asked for one, while a
  // finding's integration comes from the CALL (acme-payments). Unrelated
  // strings for the same provider.
  const contract: ContractSpec = {
    integration: 'api-acme-test', role: 'provider', format: 'openapi', peer_host: 'api.acme.test'
  };
  const hostOf = (id: string) => (id === 'call_1' ? 'api.acme.test' : undefined);

  it('joins on the HOST the two genuinely share, not the ids they do not', () => {
    expect(findingBelongsToContract(
      { integration: 'acme-payments', source_call_id: 'call_1' }, contract, hostOf
    )).toBe(true);
  });

  it('a finding from another host never lands on this card', () => {
    const other = (id: string) => (id === 'call_x' ? 'api.globex.test' : undefined);
    expect(findingBelongsToContract(
      { integration: 'acme-payments', source_call_id: 'call_x' }, contract, other
    )).toBe(false);
  });

  it('a CALL-LESS finding falls back to integration — it has no host to resolve', () => {
    // version-diff on replace, and MCP definition_change, carry the contract's
    // OWN integration and no source call.
    expect(findingBelongsToContract(
      { integration: 'api-acme-test', source_call_id: null }, contract, hostOf
    )).toBe(true);
    expect(findingBelongsToContract(
      { integration: 'somebody-else', source_call_id: null }, contract, hostOf
    )).toBe(false);
  });

  it('an evicted source call falls back to integration rather than vanishing', () => {
    // The rolling window evicts unpinned calls; a finding must not silently
    // detach from its card because its evidence aged out.
    expect(findingBelongsToContract(
      { integration: 'api-acme-test', source_call_id: 'gone' }, contract, hostOf
    )).toBe(true);
  });

  // REGRESSION, postgres lane 2026-09-02: the SAME provider split into two
  // cards AGAIN — contract + CONFORMING pill above, "No contract for this
  // provider" + the BREAKING finding below — minutes after ordinary traffic.
  //
  // The host-first join was right and still failed by construction. The
  // finding's source_call_id is frozen at the FIRST occurrence, and the browser
  // resolved it through GET /api/calls, which returns only the 200 newest rows:
  // once that call aged off the page hostOfCall answered undefined, the join
  // fell through to integration — an SDK slug (acme-payments) against a slug
  // derived from the host (api-acme-test) — and the finding detached.
  //
  // So the host now rides on the finding row itself, decorated server-side from
  // the pinned call the store still holds. The case the fix exists for is
  // exactly the one the callsById lookup cannot answer.
  describe('when the source call has aged out of the calls page', () => {
    const evicted = () => undefined; // callsById knows nothing about it

    it('still pairs with the contract for its host', () => {
      expect(findingBelongsToContract(
        { integration: 'acme-payments', source_call_id: 'call_1', peer_host: 'api.acme.test' },
        contract,
        evicted
      )).toBe(true);
    });

    it('does not pair with a contract for a different host', () => {
      expect(findingBelongsToContract(
        { integration: 'acme-payments', source_call_id: 'call_1', peer_host: 'api.globex.test' },
        contract,
        evicted
      )).toBe(false);
    });

    it('without the field it is the old bug — the fallback cannot save it', () => {
      // Pinned deliberately: this is what a row from a collector predating the
      // decoration does, and it is why the field had to exist. An
      // integration-only fallback cannot join acme-payments to api-acme-test.
      expect(findingBelongsToContract(
        { integration: 'acme-payments', source_call_id: 'call_1' }, contract, evicted
      )).toBe(false);
    });
  });

  it('the row wins over the calls page — they cannot disagree, and the row is the pinned one', () => {
    // hostOfCall would answer for this id, but the server resolved the call the
    // finding actually points at. One source of truth, and it is the store's.
    expect(findingBelongsToContract(
      { integration: 'acme-payments', source_call_id: 'call_1', peer_host: 'api.globex.test' },
      contract,
      hostOf
    )).toBe(false);
  });

  it('a call-less finding with no host still falls back to integration', () => {
    expect(findingBelongsToContract(
      { integration: 'api-acme-test', source_call_id: null }, contract, evictedAll
    )).toBe(true);
  });
});


describe('MCP_NEEDS_NO_SETUP — the Contracts empty state', () => {
  // The canonical paragraph in the vault's positioning-2026-09.md §5 ends
  // "REST providers need a spec: paste a URL, or upload one". PR #91 shipped
  // this string WITHOUT the URL clause and this test asserted its ABSENCE,
  // because the collector could not then fetch anything and the doc's own rule
  // was: restore the clause in the same commit that ships the fetch, and not
  // before.
  //
  // That commit is this one (ruling R5 — extension/flanjui/contracts_fetch.go),
  // so the assertion INVERTS: the clause must now be present. The premise
  // changed, the discipline did not — this copy may only ever promise what the
  // surface actually does.
  //
  // The pairing below is the real guard, and it is why both halves are checked
  // in one test: the claim and the capability travel together. Delete the fetch
  // route and this test must go back to asserting the absence, in the same
  // commit.
  it('offers the URL, because the collector now fetches one', () => {
    expect(MCP_NEEDS_NO_SETUP).toMatch(/paste its URL/);
    // …and the deck's own fetch copy exists, which is the capability half of
    // the claim. A string promising a URL with no fetch panel behind it is the
    // false claim #91 refused to ship.
    expect(FETCH_PROMPT).toMatch(/URL/);
    expect(FETCH_STAYS_LOCAL).toMatch(/This collector makes the request/);
  });

  // The line that used to say "this collector never fetches on your behalf" is
  // RETIRED, not softened: a claim that stopped being true does not get to
  // survive in a gentler form somewhere on the same tab.
  it('no longer promises anywhere that the collector never fetches', () => {
    for (const [name, value] of Object.entries(deck)) {
      if (typeof value !== 'string') continue;
      expect(value, `${name} still denies the fetch`).not.toMatch(/never fetches/i);
    }
  });

  it('states the contrast, not the convenience — both halves in one line', () => {
    expect(MCP_NEEDS_NO_SETUP).toMatch(/MCP servers need nothing here/);
    expect(MCP_NEEDS_NO_SETUP).toMatch(/tools\/list is the contract/);
    expect(MCP_NEEDS_NO_SETUP).toMatch(/REST provider needs a spec/);
  });
});

describe('the fetch is fetched-once, and says so', () => {
  // The operator's reasonable assumption about a URL is that it is a
  // subscription. It is NOT one — nothing re-reads it, by design: periodic
  // re-fetch is the control-plane registry (v2). A UI that let that assumption
  // stand would be selling the v2 feature for free and delivering nothing.
  it('promises no re-checking', () => {
    expect(FETCH_ONCE_ONLY).toMatch(/Fetched once/);
    expect(FETCH_ONCE_ONLY).toMatch(/Nothing re-checks/);
  });

  it('never implies a schedule', () => {
    for (const [name, value] of Object.entries(deck)) {
      if (typeof value !== 'string') continue;
      expect(value, `${name} implies a recurring fetch`)
        .not.toMatch(/\b(kept up to date|stays up to date|re-?fetch(es|ed)? (daily|hourly|nightly|automatically)|watches the url)\b/i);
    }
  });
});

describe('the probe OFFERS and never binds', () => {
  // The probe's whole risk is copy: a result that reads like a setup step is
  // one an operator takes on trust, and a wrong contract renders DRIFTED to a
  // stranger on their real provider. Every string it shows has to describe an
  // offer.
  it('names the control as looking, not finding or adding', () => {
    expect(PROBE_ACTION).toMatch(/Look for/);
    expect(PROBE_ACTION).not.toMatch(/\badd\b|\bbind\b|\bset up\b/i);
  });

  it('says nothing is bound yet, on the results themselves', () => {
    expect(PROBE_OFFER_ONLY).toMatch(/nothing is bound yet/i);
  });

  it('treats a miss as a result, with what to do next', () => {
    expect(PROBE_NOTHING_FOUND).toMatch(/Nothing at the usual paths/);
    expect(PROBE_NOTHING_FOUND).toMatch(/paste the URL|upload/i);
    // Never a verdict about the provider: "they don't publish a spec" is a
    // claim about somebody else's behaviour drawn from four failed GETs.
    expect(PROBE_NOTHING_FOUND).not.toMatch(/doesn.t publish (a|any) spec\b/i);
  });

  it('shows the corroboration on the offer, not after it is taken', () => {
    expect(probeCandidateLine({ title: 'Acme', version: '1.2.0', endpoints: 4, servers_match: true }))
      .toBe('Acme · v1.2.0 · 4 endpoints · its servers list this host');
    // Idan, 2026-09-19: no version reads "version not specified", never a gap.
    expect(probeCandidateLine({ endpoints: 1, servers_match: false }))
      .toBe('OpenAPI document · version not specified · 1 endpoint · its servers don’t list this host');
    expect(probeCandidateLine({ title: 'Acme', version: ' ', endpoints: 4, servers_match: true }))
      .toBe('Acme · version not specified · 4 endpoints · its servers list this host');
  });
});

describe('fetchedSourceLine / fetchedSourceForThread — the evidence sentence', () => {
  const fetched = {
    integration: 'api-acme-test',
    source: 'fetched',
    source_url: 'https://api.acme.test/openapi.json',
    loaded_at: '2026-09-17T10:00:00Z'
  };
  const NOW = Date.parse('2026-09-17T12:00:00Z');

  it('names the URL and when it was read', () => {
    const line = fetchedSourceLine(fetched, NOW);
    expect(line).toContain('https://api.acme.test/openapi.json');
    expect(line).toMatch(/Fetched from/);
  });

  // The whole point of the phase: an uploaded file's provenance is not
  // checkable by a stranger, so it gets NO line rather than a hedge. "Uploaded
  // from a file" would be filler dressed as provenance.
  it('is empty for every source that cannot be checked by the provider', () => {
    for (const source of ['upload', 'config', 'observed', undefined]) {
      expect(fetchedSourceLine({ ...fetched, source })).toBe('');
    }
    // A fetched row with no URL is a row that cannot support the claim.
    expect(fetchedSourceLine({ ...fetched, source_url: '' })).toBe('');
    expect(fetchedSourceLine(null)).toBe('');
  });

  it("the thread's version is written for the provider, in their terms", () => {
    const line = fetchedSourceForThread(fetched, () => '17 Sep');
    expect(line).toBe(
      'Checked against your published spec at https://api.acme.test/openapi.json, fetched 17 Sep.'
    );
  });

  // A thread is read days after it is written. A relative time silently
  // re-anchors to the READER's now, so "fetched 2 hours ago" becomes a lie the
  // moment the provider opens the link — on the one sentence whose entire job
  // is to be checkable.
  it('the thread version takes an absolute date, never a relative one', () => {
    const line = fetchedSourceForThread(fetched, (iso) => iso.slice(0, 10));
    expect(line).toContain('2026-09-17');
    expect(line).not.toMatch(/\bago\b/);
  });

  it('says nothing on the thread for an unfetched contract', () => {
    expect(fetchedSourceForThread({ ...fetched, source: 'upload' }, () => 'x')).toBe('');
  });
});

describe('providerContractsEmptyText', () => {
  // BUG (postgres-lane QA walk, 2026-09-01): the empty state told the operator
  // to "send traffic through the SDK to discover providers first" while the
  // section immediately below it read "Providers with no contract (2)" — the
  // page instructing a step one line above the proof it was already done.
  it('does not ask for discovery once providers are discovered', () => {
    const text = providerContractsEmptyText(2);
    expect(text).not.toMatch(/discover providers first/);
    expect(text).toContain('2 providers below');
  });

  it('asks for discovery only when nothing has been discovered', () => {
    expect(providerContractsEmptyText(0)).toMatch(/discover providers first/);
  });

  it('agrees in number with a single discovered provider', () => {
    const text = providerContractsEmptyText(1);
    expect(text).toContain('1 provider below');
    expect(text).not.toContain('providers below');
    expect(text).toContain('validating your calls to it.');
  });
});
