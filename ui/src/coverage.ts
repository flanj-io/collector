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
// this must change with it, or the UI resumes lying. As of 2026-08-31:
//   server (inbound)  → validated iff a `self` contract is loaded (no host scoping)
//   client (outbound) → validated iff an UPLOADED provider contract is bound to
//                       this call's peer_host. Binding is mandatory at upload,
//                       so the host is the whole lookup — there is no longer an
//                       unscoped spec that validates every outbound call (the
//                       config `spec_path`/`peer_host` pair was removed from
//                       CONTRACTS §8 when contracts moved into the UI). A row
//                       with no peer_host therefore validates NOTHING, and must
//                       never be read as covering the call in front of it
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
//
// ─── The temporal gate (2026-09-02) ──────────────────────────────────────
//
// Every rule above answers a question about the EDGE, and the header comment
// says so in its second line — yet the answer was being rendered per CALL. So
// coverage travelled backwards in time: uploading a contract flipped calls
// captured MINUTES EARLIER from `not checked` to CONFORMING, with no new
// traffic, directly beneath a notice reading "Calls already captured aren't
// re-checked". Reproduced on all three tiers of the first-launch QA, including
// a call whose body carried a drifted `1200` that the bound document could not
// have routed, and the tiered shape where the store pod drawing this UI is not
// even the process that validates.
//
// Validation happens ONCE, in the drift processor, at the moment the call goes
// through it. A contract that arrived afterwards never saw the call. So a
// contract covers a call only from its own binding time forward, and the two
// facts needed to say that are already on the wire: `SpecInfo.loaded_at` and
// `RedactedCall.captured_at`.
//
// This is a MIRROR of processor.go, not the fact itself, and it is the second
// time this mirror has diverged. The durable fix is a per-call validated fact
// stamped server-side where validation actually happens, which retires this
// module's guesswork entirely — a new OTLP attribute, a store migration and a
// re-vendor. Until then, temporal-local and conservative.
//
// Two residuals, both accepted and both in the safe direction:
//   - the processor's spec cache refreshes within a minute of an upload, so a
//     call captured in that window can read `checked` while the processor had
//     not yet loaded the document. Sub-minute, and it shrinks to nothing the
//     moment the server-side stamp lands
//   - a REPLACED contract keeps only the current row's `loaded_at`, so calls
//     validated against the document it replaced read `not checked`. That
//     understates coverage rather than overstating it, which is the whole
//     point of this module

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
  /** RFC3339, from the store. Half of the temporal gate — a call captured
   *  before a contract was bound cannot have been validated against it. */
  captured_at?: string;
}

/** The minimum shape this module needs from a loaded contract (spec_infos). */
export interface CoverageSpec {
  role?: 'provider' | 'self';
  peer_host?: string;
  format?: string;
  /** RFC3339, from the store. The other half of the temporal gate: the moment
   *  this contract became readable by the drift processor. */
  loaded_at?: string;
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

/* ── The temporal gate ─────────────────────────────────────────────────── */

/** RFC3339 → epoch ms, or undefined when it is absent or unparseable. */
function epoch(t?: string): number | undefined {
  if (!t) return undefined;
  const n = Date.parse(t);
  return Number.isNaN(n) ? undefined : n;
}

/**
 * The earliest moment any of these contracts could have validated anything.
 *
 * `undefined` means the question cannot be answered from this data — a row
 * written before `loaded_at` was recorded. The gate is then not applied at all
 * rather than guessed at in either direction: it is an ADDITIONAL requirement
 * on top of the host match, and a missing timestamp leaves the pre-existing
 * host-match answer standing instead of inventing a new verdict from a blank.
 */
function earliestBinding(specs: readonly CoverageSpec[]): number | undefined {
  let earliest: number | undefined;
  for (const s of specs) {
    const t = epoch(s.loaded_at);
    if (t === undefined) return undefined;
    if (earliest === undefined || t < earliest) earliest = t;
  }
  return earliest;
}

/**
 * Was this call still in front of the collector when one of these contracts
 * was bound? Calls captured strictly earlier were never offered to it.
 */
function capturedAfterBinding(call: CoverageCall, matched: readonly CoverageSpec[]): boolean {
  const bound = earliestBinding(matched);
  const captured = epoch(call.captured_at);
  if (bound === undefined || captured === undefined) return true;
  return captured >= bound;
}

/** `checked` only when a contract matched AND it was bound before the call. */
function verdict(call: CoverageCall, matched: readonly CoverageSpec[]): Coverage {
  if (!matched.length) return 'not-checked';
  return capturedAfterBinding(call, matched) ? 'checked' : 'not-checked';
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
    const snapshots = specs.filter(
      (s) => s.format === 'mcp' && s.peer_host && s.peer_host === call.peer_host
    );
    if (!snapshots.length) return 'not-checked';
    // Per TOOL: only a tool that publishes an outputSchema can have its result
    // validated. Rows not loaded yet resolve conservatively — never claim a
    // check we cannot evidence.
    const rows = (call.integration && mcpTools[call.integration]) || [];
    const tool = rows.find((t) => t.name === call.mcp_tool_name);
    if (!tool?.hasOutputSchema) return 'not-checked';
    // And the snapshot has to have ARRIVED first. An MCP server's tools/list is
    // observed rather than uploaded, but it reaches the drift processor the
    // same way and just as late.
    return verdict(call, snapshots);
  }

  if (call.direction === 'server') {
    return verdict(
      call,
      specs.filter((s) => s.role === 'self')
    );
  }

  // Outbound. Uploaded contracts bind to exactly one host, so coverage is a
  // host match and nothing else. An unbound row (only reachable from a store
  // written before uploads existed) validates nothing and is not coverage.
  return verdict(
    call,
    specs.filter(
      (s) => s.role !== 'self' && s.format !== 'mcp' && !!s.peer_host && s.peer_host === call.peer_host
    )
  );
}

/* ── The evidence line, per card ───────────────────────────────────────── */

/**
 * How the "since" clause of the evidence line is anchored, per provenance.
 *
 * The count is meaningless without the moment it counts from, and that moment
 * has a different name depending on how the contract got here — a config-loaded
 * SELF contract was never uploaded by anyone, and an MCP tools/list arrived on
 * its own. Saying "since upload" on those two would be the small kind of lie
 * this file exists to stop telling.
 */
export const SINCE_UPLOAD = 'upload';
export const SINCE_LOAD = 'it loaded';
export const SINCE_SNAPSHOT = 'this snapshot';

/**
 * The card's evidence line: how many captured calls this contract has actually
 * validated.
 *
 * ZERO is the whole reason this line exists. A contract can be bound and have
 * checked nothing — no traffic yet, a quiet edge, or a host binding that is
 * simply wrong — and that last case has no other symptom at all: no error, no
 * finding, a card that looks complete. The chip beside it already refuses to
 * say "conforming" without evidence; this says how much evidence there is, so
 * "none" is visible rather than merely implied.
 */
export function validatedCallsMeta(n: number, since: string = SINCE_UPLOAD): string {
  return `validated ${n} ${n === 1 ? 'call' : 'calls'} since ${since}`;
}
