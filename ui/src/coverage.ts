// Contract COVERAGE — is a call validated against anything at all?
//
// This is deliberately separate from DRIFT. Two different facts share the word
// "contract" in this UI and conflating them is what shipped a false assurance:
//   - coverage  — is a contract bound to this call's edge? (a property of the EDGE)
//   - validated — did this call actually run against that contract? (the CALL)
// A call to a host with no loaded spec was captured and NEVER validated, so it
// can be neither "conforming" nor "drifted". It previously rendered
// "conforming", which told an operator their traffic was checked when nothing
// had looked at it.
//
// The rules below MIRROR processor/flanjdrift/processor.go — if that changes,
// this must change with it, or the UI resumes lying. As of v1p1-2026-08-31:
//   server (inbound)  → validated iff a `self` contract is loaded (no host scoping)
//   client (outbound) → validated iff a provider doc is loaded AND that spec is
//                       either unscoped (`peer_host` empty → validates EVERY
//                       outbound call) or its peer_host matches the call's
//   mcp               → validated iff a snapshot exists for that host AND the
//                       CALLED TOOL declares an `outputSchema` in it. A tool
//                       without one publishes nothing to check its result
//                       against, so its calls are never validated even though
//                       the server's tools/list is present (drift/mcp.go:220
//                       gates on `op.OutputSchema != nil`). Per-TOOL, not
//                       per-host — the mock's `list_transactions` omits it
//                       deliberately, and treating the whole server as covered
//                       would restate the very lie this module exists to kill
//   internal          → never validated by design (metadata-only, no bodies)

/** The minimum shape this module needs from a call row. */
export interface CoverageCall {
  peer_host?: string;
  direction?: string;
  edge_class?: string;
  transport?: string;
  /** MCP only: the tool this call invoked, used for the per-tool schema check. */
  mcp_tool_name?: string;
  /** MCP only: which server's snapshot to look the tool up in. */
  integration?: string;
}

/** The minimum shape this module needs from a loaded contract (spec_infos). */
export interface CoverageSpec {
  role?: 'provider' | 'self';
  peer_host?: string;
  format?: string;
}

export type Coverage = 'internal' | 'checked' | 'not-checked';

/** One tool from a server's tools/list snapshot (ui/src/mcp.ts parseToolRows). */
export interface McpToolCoverage {
  name: string;
  hasOutputSchema: boolean;
}

/** The Traffic chip for an unvalidated call, and its filter value. */
export const NOT_CHECKED_LABEL = 'not checked';

/** Tooltip for the `not checked` chip — names the host so it is actionable. */
export function notCheckedTitle(host?: string): string {
  const where = host ? ` for ${host}` : '';
  return `No contract uploaded${where} — this call was captured, not validated.`;
}

/**
 * Coverage for one call, given every loaded contract.
 *
 * `internal` is returned as its own state rather than folded into
 * `not-checked`: an internal edge is metadata-only BY DESIGN (no bodies are
 * captured, so there is nothing to validate and nothing missing), while an
 * external call with no contract is a real gap in what the operator can see.
 * Rendering them the same would turn a deliberate policy into an apparent hole.
 */
export function callCoverage(
  call: CoverageCall,
  specs: readonly CoverageSpec[],
  mcpTools: Readonly<Record<string, readonly McpToolCoverage[]>> = {}
): Coverage {
  if (call.edge_class === 'internal') return 'internal';

  if (call.transport === 'mcp') {
    // Snapshots only — NOT the wider "hosts we have seen MCP traffic from".
    // A server whose tools/list has not arrived yet has nothing to validate
    // against, and claiming otherwise would be the same lie in a new place.
    const snapshot = specs.some(
      (s) => s.format === 'mcp' && s.peer_host && s.peer_host === call.peer_host
    );
    if (!snapshot) return 'not-checked';
    // Per TOOL: only a tool that publishes an outputSchema can have its result
    // validated. Rows not loaded yet resolve conservatively — never claim a
    // check we cannot evidence.
    const rows = (call.integration && mcpTools[call.integration]) || [];
    const tool = rows.find((t) => t.name === call.mcp_tool_name);
    return tool?.hasOutputSchema ? 'checked' : 'not-checked';
  }

  if (call.direction === 'server') {
    return specs.some((s) => s.role === 'self') ? 'checked' : 'not-checked';
  }

  // Outbound. An unscoped provider spec validates EVERY outbound call, which is
  // the config-file `spec_path` without `peer_host` — still supported, so the
  // UI must not report those calls as unchecked.
  return specs.some(
    (s) =>
      s.role !== 'self' &&
      s.format !== 'mcp' &&
      (!s.peer_host || s.peer_host === call.peer_host)
  )
    ? 'checked'
    : 'not-checked';
}
