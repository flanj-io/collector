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
    const h = headlineFor({ liveFindings: [], calls: [rest(false), rest(false)], integration: ACME });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
    // Pre-traffic honesty survives: the integration fragment still rides along
    // once a REST call has been observed under it, and is empty rather than a
    // fallback slug when health has none.
    expect(h.integration).toBe(ACME);
    expect(headlineFor({ liveFindings: [], calls: [rest(false)] }).integration).toBe('');
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

describe('headlineFor — evidence is per edge (the second QA walk)', () => {
  it('the repro: an unvalidated REST provider beside a validated MCP server is NEUTRAL, not green', () => {
    // Fresh stack, no contract. POST /__drive {target:"acme"} then {target:"mcp"},
    // reload: the MCP tool calls were validated against the server's tools/list,
    // the acme calls against nothing — and this line read "No drift detected on
    // integration acme-payments" in green, one line above "0 of 1 provider
    // checked against a contract · 1 MCP server self-reports theirs".
    const h = headlineFor({
      liveFindings: [],
      calls: [rest(false), rest(false), mcp(true), mcp(true)],
      integration: ACME
    });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
    // acme WAS observed on a REST edge, so the fragment still names it — under
    // the neutral line, where "nothing validated yet on acme-payments" is true.
    expect(h.integration).toBe(ACME);
  });

  it('MCP evidence never earns the REST all-clear, however much of it there is', () => {
    const h = headlineFor({ liveFindings: [], calls: many(50, () => mcp(true)), integration: ACME });
    expect(h.tone).toBe('neutral');
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
  });

  it('one validated REST call beside any number of MCP calls: the all-clear is earned', () => {
    const h = headlineFor({ liveFindings: [], calls: [mcp(true), mcp(false), rest(true)], integration: ACME });
    expect(h.you).toBe(NO_DRIFT_DETECTED);
    expect(h.tone).toBe('ok');
  });

  it('isRestCall: HTTP rows carry no transport, and only `mcp` is the other edge', () => {
    expect(isRestCall({})).toBe(true);
    expect(isRestCall({ transport: undefined })).toBe(true);
    expect(isRestCall({ transport: 'mcp' })).toBe(false);
  });
});

describe('the `on integration <slug>` fragment names only an integration seen on a REST edge', () => {
  it('MCP traffic alone does not surface the config slug', () => {
    // /api/health emits the slug from static config (flanjui.integration_id)
    // the moment ANY outbound edge exists — an MCP edge included. This install
    // has never made a REST call, so naming a REST integration under the line
    // is the pre-traffic fabrication the v1 spec forbids.
    const h = headlineFor({ liveFindings: [], calls: [mcp(true), mcp(false)], integration: ACME });
    expect(h.tone).toBe('neutral');
    expect(h.integration).toBe('');
  });

  it('a captured REST call under the slug is enough — validated or not', () => {
    expect(headlineFor({ liveFindings: [], calls: [rest(false)], integration: ACME }).integration).toBe(ACME);
    expect(headlineFor({ liveFindings: [], calls: [rest(true)], integration: ACME }).integration).toBe(ACME);
  });

  it('a live finding under the slug is enough, even with its call evicted', () => {
    const h = headlineFor({ liveFindings: [drift('POST /v1/charges')], calls: [], integration: ACME });
    expect(h.tone).toBe('drift');
    expect(h.integration).toBe(ACME);
  });

  it('a REST call under a DIFFERENT integration lends the slug nothing', () => {
    // The all-clear is earned (a REST call was validated), but not under the
    // slug health names — so the line is green and unscoped, never green
    // "on integration acme-payments".
    const h = headlineFor({ liveFindings: [], calls: [rest(true, 'globex-fx')], integration: ACME });
    expect(h.tone).toBe('ok');
    expect(h.integration).toBe('');
  });

  it('no slug from health (pre-traffic) → empty, never a fallback', () => {
    expect(headlineFor({ liveFindings: [], calls: [rest(true)] }).integration).toBe('');
    expect(headlineFor({ liveFindings: [], calls: [rest(true)], integration: '' }).integration).toBe('');
  });

  it('the MCP line reads the same zero state, in clause position', () => {
    // One constant drives both lines, so A6's wording ruling moves both.
    expect(NOTHING_VALIDATED_YET_CLAUSE).toBe('nothing validated yet.');
  });
});

it('the all-clear is scoped to the integration the fragment names', () => {
  // acme captured but never validated; another SDK's integration validated in
  // the same window. The fragment names acme, so acme's evidence decides: neutral.
  const h = headlineFor({
    liveFindings: [],
    calls: [
      { integration: 'acme-payments', validated: false },
      { integration: 'other-svc', validated: true }
    ],
    integration: 'acme-payments'
  } as never);
  expect(h.tone).toBe('neutral');
  expect(h.you).toBe(NOTHING_VALIDATED_YET);
  expect(h.integration).toBe('acme-payments');
});
