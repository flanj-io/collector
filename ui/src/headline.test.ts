import { describe, it, expect } from 'vitest';
import {
  headlineFor,
  isRestCall,
  NOTHING_VALIDATED_YET,
  NOTHING_VALIDATED_YET_CLAUSE,
  NO_DRIFT_DETECTED,
  type HeadlineCall
} from './headline';

const ACME = 'acme-payments';
const drift = (endpoint: string, integration = ACME) => ({ endpoint, integration });
/** A REST provider call — HTTP rows carry no transport at all. */
const rest = (validated: boolean, integration = ACME): HeadlineCall => ({ integration, validated });
/** An MCP tool call — evidence for its server's own headline, never this one. */
const mcp = (validated: boolean, integration = 'acme-tools'): HeadlineCall => ({
  transport: 'mcp',
  integration,
  validated
});
const many = (n: number, make: () => HeadlineCall) => Array.from({ length: n }, make);

describe('headlineFor — the all-clear has to be earned', () => {
  it('zero calls and zero contracts: neutral, not green (fresh install)', () => {
    // The first-launch walk hit this on every tier: a store with no traffic and
    // no contract announced "No drift detected" before anything existed to look at.
    const h = headlineFor({ liveFindings: [], calls: [] });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
  });

  it('calls captured but nothing validated them: still neutral', () => {
    // "0 of 2 providers checked against a contract" rendered directly BELOW the
    // green headline. Same install, two surfaces, opposite claims.
    const h = headlineFor({ liveFindings: [], calls: [rest(false), rest(false)] });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
    // The line carries no `on integration` scope any more (the config slug is
    // gone, 2026-09-14): the collector name App.vue renders is not a claim
    // about traffic, so nothing here can be a pre-traffic fabrication.
    expect(h).toEqual({ you: NOTHING_VALIDATED_YET, tone: 'neutral' });
  });

  it('a vendor hard down validates nothing, so it cannot read as an all-clear', () => {
    // Nothing reached the provider at all; the old line called that clean.
    expect(headlineFor({ liveFindings: [], calls: [] }).tone).not.toBe('ok');
  });

  it('zero findings WITH at least one validated call: the green all-clear', () => {
    const h = headlineFor({ liveFindings: [], calls: [rest(true)] });
    expect(h.you).toBe(NO_DRIFT_DETECTED);
    expect(h.tone).toBe('ok');
  });

  it('findings win over everything, and keep their exact wording', () => {
    const four = many(4, () => rest(true));
    const one = headlineFor({ liveFindings: [drift('POST /v1/charges')], calls: four });
    expect(one.you).toBe('1 contract drift finding on POST /v1/charges');
    expect(one.tone).toBe('drift');

    const two = headlineFor({
      liveFindings: [drift('POST /v1/charges'), drift('GET /v1/balance')],
      calls: four
    });
    expect(two.you).toBe('2 contract drift findings on POST /v1/charges, GET /v1/balance');
    expect(two.tone).toBe('drift');
  });

  it('repeated endpoints are named once', () => {
    const h = headlineFor({
      liveFindings: [drift('POST /v1/charges'), drift('POST /v1/charges')],
      calls: many(2, () => rest(true))
    });
    expect(h.you).toBe('2 contract drift findings on POST /v1/charges');
  });

  it('a finding is itself evidence: it reports drift even with no validated call in the window', () => {
    // The drifting call can be evicted by the rolling window while its finding
    // survives. Falling back to neutral there would HIDE a live drift.
    const h = headlineFor({ liveFindings: [drift('POST /v1/charges')], calls: [] });
    expect(h.tone).toBe('drift');
  });

  it('the neutral tone is a third state, never a shade of the other two', () => {
    const tones = [
      headlineFor({ liveFindings: [], calls: [] }).tone,
      headlineFor({ liveFindings: [], calls: [rest(true)] }).tone,
      headlineFor({ liveFindings: [drift('GET /x')], calls: [rest(true)] }).tone
    ];
    expect(tones).toEqual(['neutral', 'ok', 'drift']);
  });
});

describe('headlineFor — evidence is per edge (the second exploratory pass)', () => {
  it('the repro: an unvalidated REST provider beside a validated MCP server is NEUTRAL, not green', () => {
    // Fresh stack, no contract. POST /__drive {target:"acme"} then {target:"mcp"},
    // reload: the MCP tool calls were validated against the server's tools/list,
    // the acme calls against nothing — and this line read "No drift detected on
    // integration acme-payments" in green, one line above "0 of 1 provider
    // checked against a contract · 1 MCP server self-reports theirs".
    const h = headlineFor({
      liveFindings: [],
      calls: [rest(false), rest(false), mcp(true), mcp(true)]
    });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
  });

  it('MCP evidence never earns the REST all-clear, however much of it there is', () => {
    const h = headlineFor({ liveFindings: [], calls: many(50, () => mcp(true)) });
    expect(h.tone).toBe('neutral');
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
  });

  it('one validated REST call beside any number of MCP calls: the all-clear is earned', () => {
    const h = headlineFor({ liveFindings: [], calls: [mcp(true), mcp(false), rest(true)] });
    expect(h.you).toBe(NO_DRIFT_DETECTED);
    expect(h.tone).toBe('ok');
  });

  it('a validated REST call under ANY integration earns it — the line is unscoped since the config slug went (2026-09-14)', () => {
    const h = headlineFor({ liveFindings: [], calls: [rest(true, 'globex-fx')] });
    expect(h.tone).toBe('ok');
    expect(h).not.toHaveProperty('integration');
  });

  it('isRestCall: HTTP rows carry no transport, and only `mcp` is the other edge', () => {
    expect(isRestCall({})).toBe(true);
    expect(isRestCall({ transport: undefined })).toBe(true);
    expect(isRestCall({ transport: 'mcp' })).toBe(false);
  });
});


describe('the headline is BREAKING only — a deprecation never turns it red', () => {
  const deprecation = (endpoint: string) => ({ endpoint, integration: ACME, severity: 'warning' });
  const breaking = (endpoint: string) => ({ endpoint, integration: ACME, severity: 'breaking' });

  it('a deprecation warning alone leaves the all-clear standing', () => {
    // The provider is giving notice about a surface that still works. The
    // finding lists on the Contracts tab in the warning tier; the Overview's
    // one alarm is not spent on it.
    const h = headlineFor({
      liveFindings: [deprecation('POST /v1/legacy-charges')],
      calls: [rest(true)]
    });
    expect(h.tone).toBe('ok');
    expect(h.you).toBe(NO_DRIFT_DETECTED);
  });

  it('a deprecation does not manufacture an all-clear either', () => {
    // No validated call: still neutral, not green. The warning is not evidence
    // that anything was checked.
    const h = headlineFor({ liveFindings: [deprecation('POST /v1/legacy-charges')], calls: [] });
    expect(h.tone).toBe('neutral');
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
  });

  it('a breaking finding beside a deprecation counts once, and reds the line', () => {
    const h = headlineFor({
      liveFindings: [breaking('POST /v1/charges'), deprecation('POST /v1/legacy-charges')],
      calls: [rest(true)]
    });
    expect(h.tone).toBe('drift');
    expect(h.you).toContain('1 contract drift finding');
    expect(h.you).toContain('POST /v1/charges');
    expect(h.you).not.toContain('legacy-charges');
  });

  it('a finding with no severity still counts — every older one was breaking', () => {
    const h = headlineFor({ liveFindings: [drift('POST /v1/charges')], calls: [rest(true)] });
    expect(h.tone).toBe('drift');
  });
});
