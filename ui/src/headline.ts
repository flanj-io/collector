// The zero-state copy and the REST/MCP edge split, shared by the Overview
// system status (ui/src/status.ts) and the per-server MCP line (ui/src/mcp.ts).
//
// This module used to own the whole REST headline computation (`headlineFor`).
// The Overview moved to ONE status for the whole collector (ui/src/status.ts
// `systemStatus`) — drift anywhere makes the status drift, and the REST-only
// all-clear this module used to compute could render green beside an MCP
// drift line it knew nothing about. `headlineFor` is gone; its one caller is
// now `systemStatus`, which reads REST and MCP evidence together. What is
// still true regardless of how many edges exist — the two zero-state strings,
// the three-tone type, and the REST/MCP split test — stays here, close to the
// tests that pin it.
//
// The WORDING of the neutral zero state is still open, not whether the state
// exists. It ships as one constant so a wording change is a one-line change
// here and nowhere else.

/** The neutral zero state: nothing has been checked against a contract yet. */
export const NOTHING_VALIDATED_YET = 'Nothing validated yet';

/** The all-clear, which costs at least one validated call somewhere. */
export const NO_DRIFT_DETECTED = 'No drift detected';

/**
 * `neutral` is a third state, not a shade of the other two. Rendering it green
 * asserts an all-clear nothing earned; rendering it red accuses a provider of
 * something on evidence that does not exist either.
 */
export type HeadlineTone = 'ok' | 'drift' | 'neutral';

/** The minimum a call needs to be weighed as REST evidence. */
export interface HeadlineCall {
  /**
   * `mcp` on an MCP tool call; absent on HTTP rows. Decides WHICH surface the
   * call is evidence for — the REST status, or its own MCP server's line.
   */
  transport?: string;
}

/**
 * Is this call on a REST edge, one of the edges the system status folds in
 * directly (rather than through a per-server MCP line)? An MCP tool call is
 * evidence for its own server and must never be double-counted here, however
 * validated it is. HTTP rows carry no transport at all, so the question is
 * "not MCP", not "is HTTP".
 */
export function isRestCall(c: Pick<HeadlineCall, 'transport'>): boolean {
  return c.transport !== 'mcp';
}
