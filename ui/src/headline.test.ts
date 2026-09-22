import { describe, it, expect } from 'vitest';
import { isRestCall, NOTHING_VALIDATED_YET, NO_DRIFT_DETECTED } from './headline';

describe('headline.ts — shared zero-state copy and the REST/MCP split', () => {
  it('isRestCall: HTTP rows carry no transport, and only `mcp` is the other edge', () => {
    expect(isRestCall({})).toBe(true);
    expect(isRestCall({ transport: undefined })).toBe(true);
    expect(isRestCall({ transport: 'mcp' })).toBe(false);
  });

  it('the two zero-state strings are fixed, one place each', () => {
    expect(NOTHING_VALIDATED_YET).toBe('Nothing validated yet');
    expect(NO_DRIFT_DETECTED).toBe('No drift detected');
  });
});
