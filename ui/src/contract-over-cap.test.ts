import { describe, it, expect } from 'vitest';
import {
  CONTRACT_OVER_CAP_TAG,
  MAX_CONTRACT_BYTES,
  NO_CONTRACT_ROW,
  contractOverCap,
  contractOverCapBy,
  contractOverCapLine,
  contractSizeLabel,
  type ContractSpec
} from './contracts';

const mcpRow = (over: Partial<ContractSpec> = {}): ContractSpec => ({
  integration: 'acme-tools',
  role: 'provider',
  format: 'mcp',
  peer_host: 'mcp.acme.test',
  source: 'observed',
  title: 'acme-tools-mcp',
  endpoints: 12,
  loaded_at: '2026-09-08T10:00:00Z',
  ...over
});

describe('the document-cap row state', () => {
  // The gap this closes: an oversized row was listed like any other — heading,
  // badge, tool rows, a contract that looks complete — while every call on that
  // edge was stamped not-validated on a front that never managed to read it.
  it('recognises a row past the cap', () => {
    expect(contractOverCap(mcpRow({ doc_bytes: MAX_CONTRACT_BYTES + 1 }))).toBe(true);
  });

  // Inclusive, like both refusals in the collector. An off-by-one here would
  // put a working contract into a failure state.
  it('leaves a row exactly at the cap alone', () => {
    expect(contractOverCap(mcpRow({ doc_bytes: MAX_CONTRACT_BYTES }))).toBe(false);
  });

  // A collector that predates doc_bytes reports no size. Guessing "over" from a
  // missing number would invent a fault; the transfer-time refusals still
  // answer for that case.
  it('never reads an unmeasured row as over the cap', () => {
    expect(contractOverCap(mcpRow())).toBe(false);
    expect(contractOverCap(mcpRow({ doc_bytes: 0 }))).toBe(false);
  });

  it('states the size, the overage, and both branches of what a front does', () => {
    const line = contractOverCapLine(mcpRow({ doc_bytes: MAX_CONTRACT_BYTES + 1.4 * 1024 * 1024 }));
    expect(line).toContain('9.4 MB');
    expect(line).toContain('1.4 MB over the 8 MB cap');
    // Both branches, because this surface genuinely cannot know which front
    // holds a baseline — that lives in another process and differs per front.
    expect(line).toContain('keeps whatever baseline it already had');
    expect(line).toContain('nothing validates them');
  });

  // The closing clause deliberately matches the no-contract row's, because on a
  // front with no baseline that is exactly the state the calls are in.
  it('closes in the same words as the no-contract row', () => {
    const line = contractOverCapLine(mcpRow({ doc_bytes: MAX_CONTRACT_BYTES + 1 }));
    expect(NO_CONTRACT_ROW).toContain('captured');
    expect(line).toContain('captured, and nothing validates them');
  });

  it('names an MCP row a snapshot and an uploaded one a document', () => {
    const bytes = MAX_CONTRACT_BYTES + 2 * 1024 * 1024;
    expect(contractOverCapLine(mcpRow({ doc_bytes: bytes }))).toMatch(/^This tools\/list snapshot/);
    expect(contractOverCapLine(mcpRow({ format: 'openapi', doc_bytes: bytes }))).toMatch(/^This document/);
  });

  // "0.0 MB over the 8 MB cap" reads like a rounding artifact, not a limit.
  it('says a hair over the cap in words, not in a rounded zero', () => {
    expect(contractOverCapBy(MAX_CONTRACT_BYTES + 200)).toBe('just over the 8 MB cap');
    expect(contractOverCapBy(MAX_CONTRACT_BYTES + 1024 * 1024)).toBe('1.0 MB over the 8 MB cap');
    expect(contractOverCapBy(MAX_CONTRACT_BYTES)).toBe('');
  });

  it('labels sizes the way an operator compares them', () => {
    expect(contractSizeLabel(9 * 1024 * 1024)).toBe('9.0 MB');
    expect(contractSizeLabel(4 * 1024)).toBe('4 KB');
  });

  // The banned vocabulary at the top of contracts.ts asserts something about
  // the PROVIDER's behaviour. This state is a fact about a document in the
  // operator's own store and about a limit they can act on, so none of it may
  // appear — and neither may a nag about the file's age.
  it('claims nothing about the provider', () => {
    const line = contractOverCapLine(mcpRow({ doc_bytes: MAX_CONTRACT_BYTES + 1 }));
    for (const banned of ['missing', 'uncovered', 'unprotected', 'gap', 'stale', 'out of date', 'outdated']) {
      expect(line.toLowerCase()).not.toContain(banned);
    }
    expect(CONTRACT_OVER_CAP_TAG).toBe('too large to serve');
  });
});
