import { describe, it, expect } from 'vitest';
import { headlineFor, NOTHING_VALIDATED_YET, NO_DRIFT_DETECTED } from './headline';

const drift = (endpoint: string) => ({ endpoint });

describe('headlineFor — the all-clear has to be earned', () => {
  it('zero calls and zero contracts: neutral, not green (fresh install)', () => {
    // The first-launch walk hit this on every tier: a store with no traffic and
    // no contract announced "No drift detected" before anything existed to look at.
    const h = headlineFor({ liveFindings: [], validatedCalls: 0 });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
  });

  it('calls captured but nothing validated them: still neutral', () => {
    // "0 of 2 providers checked against a contract" rendered directly BELOW the
    // green headline. Same install, two surfaces, opposite claims.
    const h = headlineFor({ liveFindings: [], validatedCalls: 0, integration: 'acme-payments' });
    expect(h.you).toBe(NOTHING_VALIDATED_YET);
    expect(h.tone).toBe('neutral');
    // Pre-traffic honesty survives: the integration fragment still rides along
    // when health has one, and is empty rather than a fallback slug when it does not.
    expect(h.integration).toBe('acme-payments');
    expect(headlineFor({ liveFindings: [], validatedCalls: 0 }).integration).toBe('');
  });

  it('a vendor hard down validates nothing, so it cannot read as an all-clear', () => {
    // Nothing reached the provider at all; the old line called that clean.
    expect(headlineFor({ liveFindings: [], validatedCalls: 0 }).tone).not.toBe('ok');
  });

  it('zero findings WITH at least one validated call: the green all-clear', () => {
    const h = headlineFor({ liveFindings: [], validatedCalls: 1 });
    expect(h.you).toBe(NO_DRIFT_DETECTED);
    expect(h.tone).toBe('ok');
  });

  it('findings win over everything, and keep their exact wording', () => {
    const one = headlineFor({ liveFindings: [drift('POST /v1/charges')], validatedCalls: 4 });
    expect(one.you).toBe('1 contract drift finding on POST /v1/charges');
    expect(one.tone).toBe('drift');

    const two = headlineFor({
      liveFindings: [drift('POST /v1/charges'), drift('GET /v1/balance')],
      validatedCalls: 4
    });
    expect(two.you).toBe('2 contract drift findings on POST /v1/charges, GET /v1/balance');
    expect(two.tone).toBe('drift');
  });

  it('repeated endpoints are named once', () => {
    const h = headlineFor({ liveFindings: [drift('POST /v1/charges'), drift('POST /v1/charges')], validatedCalls: 2 });
    expect(h.you).toBe('2 contract drift findings on POST /v1/charges');
  });

  it('a finding is itself evidence: it reports drift even with no validated call in the window', () => {
    // The drifting call can be evicted by the rolling window while its finding
    // survives. Falling back to neutral there would HIDE a live drift.
    const h = headlineFor({ liveFindings: [drift('POST /v1/charges')], validatedCalls: 0 });
    expect(h.tone).toBe('drift');
  });

  it('the neutral tone is a third state, never a shade of the other two', () => {
    const tones = [
      headlineFor({ liveFindings: [], validatedCalls: 0 }).tone,
      headlineFor({ liveFindings: [], validatedCalls: 1 }).tone,
      headlineFor({ liveFindings: [drift('GET /x')], validatedCalls: 1 }).tone
    ];
    expect(tones).toEqual(['neutral', 'ok', 'drift']);
  });
});
