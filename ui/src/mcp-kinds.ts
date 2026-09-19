// The MCP finding kinds, in a module of their own so that contracts.ts can
// tell an MCP finding from a REST one without importing mcp.ts (which imports
// contracts.ts — a cycle whose top-level reads would hit the TDZ at runtime).
export const MCP_FINDING_KINDS = [
  'output_mismatch',
  'definition_change',
  'stale_client',
  'value_change',
  'input_rejection',
] as const;

export function isMcpFindingKind(kind: string | undefined): boolean {
  return !!kind && (MCP_FINDING_KINDS as readonly string[]).includes(kind);
}
