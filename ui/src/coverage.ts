// Contract COVERAGE — was a call validated against anything at all?
//
// This is deliberately separate from DRIFT. Two different facts share the word
// "contract" in this UI and conflating them is what shipped a false assurance:
//   - coverage  — did the drift processor validate this call?           (the CALL)
//   - drift     — did that validation find the call departing? (also the CALL)
// A call to a host with no loaded spec was captured and NEVER validated, so it
// can be neither "conforming" nor "drifted". It previously rendered
// "conforming", which told an operator their traffic was checked when nothing
// had looked at it.
//
// ─── The verdict is a FACT on the call (2026-09-07) ──────────────────────
//
// `RedactedCall.validated` is the drift processor's own statement, stamped on
// the record where validation runs (CONTRACTS §2 `flanj.validated`, §3):
//   clean          → validated, nothing found               → checked
//   drifted        → validated, departed from the contract  → checked (+ drifted)
//   not-validated  → the processor saw the call and explicitly could not
//                    judge it; `validated_reason` names the gate → not checked
//   unknown        → the record reached the store carrying no verdict — an
//                    older front, or a pipeline running no drift processor
//                                                            → not checked
//   absent         → a row stored before verdicts were recorded (the column's
//                    migration default). ONLY here does this module fall back
//                    to its old guess, below.
//
// Why a fact and not an inference. Until 2026-09-07 this module DECIDED
// `checked` from the contract list: a document bound to the call's host, later
// refined to a document bound before the call was captured (`spec.loaded_at <=
// call.captured_at`). Both are facts about the STORE. Validation happens in
// the drift PROCESSOR, whose spec cache learns of an upload later than the
// store does — an announced refresh floors at five seconds, a tiered front
// polls a ten-second ticker, a front with the wrong store_pod_token never
// loads the document at all — so a drifting charge driven one to four seconds
// after an upload went through the processor unvalidated, produced no finding
// and no drifted flag, and this module read `checked` off the store's own
// timestamp: CONFORMING. Reproduced on sqlite, postgres (both pods) and tiered
// (both fronts), and PERMANENT on the mis-tokened front. The mirror had also
// diverged from processor.go twice before that. A verdict only the validating
// process can give is now stamped by it, and this module reads it.
//
// ─── The legacy mirror (pre-migration rows ONLY) ─────────────────────────
//
// Rows stored before the `validated` column existed carry no verdict, and no
// one will ever stamp one. For those — and ONLY those, `validated` absent —
// the old rules still answer, so an upgrade does not flip an install's whole
// history to `not checked`:
//   server (inbound)  → validated iff a `self` contract is loaded (no host scoping)
//   client (outbound) → validated iff an UPLOADED provider contract is bound to
//                       this call's peer_host AND was bound before the call was
//                       captured (the temporal gate of 2026-09-02). An unbound
//                       row validates NOTHING
//   mcp               → validated iff a snapshot exists for that host, the
//                       CALLED TOOL declares an `outputSchema` in it, the
//                       result was not an error, and the snapshot arrived first
//   internal          → never validated by design (metadata-only, no bodies)
// It is a mirror of what processor.go USED to do, frozen. Do not extend it, and
// never route a stamped call into it: a stamped call that lands there is a bug
// in the switch above, not a reason to widen the fallback.

import { noOutputContractNote } from './mcp';

/** The minimum shape this module needs from a call row. */
export interface CoverageCall {
  peer_host?: string;
  direction?: string;
  edge_class?: string;
  transport?: string;
  /** For the not-routable / status-undeclared / media-type-undeclared
   *  sentences: which call, and which response, the document did not describe. */
  method?: string;
  route?: string;
  status_code?: number;
  response_content_type?: string;
  /** MCP only: the tool this call invoked, used for the per-tool schema check. */
  mcp_tool_name?: string;
  /** MCP only: which server's snapshot to look the tool up in. */
  integration?: string;
  /** MCP only: the result was an execution failure. Error output is not
   *  contract evidence — the processor skips these, so nothing validated them. */
  mcp_is_error?: boolean;
  /** RFC3339, from the store. Half of the legacy temporal gate — a call captured
   *  before a contract was bound cannot have been validated against it. */
  captured_at?: string;
  /** The drift processor's verdict on THIS call (CONTRACTS §3): `clean`,
   *  `drifted`, `not-validated`, `unknown`. Absent on a pre-migration row. */
  validated?: string;
  /** The gate that stopped validation, when `validated` is `not-validated`. */
  validated_reason?: string;
}

/** The minimum shape this module needs from a loaded contract (spec_infos). */
export interface CoverageSpec {
  role?: 'provider' | 'self';
  peer_host?: string;
  format?: string;
  /** RFC3339, from the store. The other half of the legacy temporal gate: the
   *  moment this contract became readable by the drift processor. */
  loaded_at?: string;
}

export type Coverage = 'internal' | 'checked' | 'not-checked';

/**
 * WHY a call was not checked. One chip, several causes, and they are not
 * interchangeable: the chip is only actionable if it names the actual gap.
 *
 * Most of these are the processor's own words (CONTRACTS §2
 * `flanj.validated.reason`), passed through. Two are this module's: the
 * processor's `no-contract` is split by whether a contract for the edge is
 * bound NOW — because "No contract uploaded" is false, and unactionable, when
 * the operator is looking at the card that says one is — and `no-verdict` is
 * the record that carries no verdict at all.
 */
export type NotCheckedReason =
  /** Nothing is bound to this call's edge (and nothing was when it went through). */
  | 'no-contract'
  /** A contract for the edge is bound NOW, but had not reached the drift
   *  processor when this call went through — the upload was seconds old, or
   *  the front cannot read the store pod. */
  | 'contract-not-reached'
  /** The record carries no verdict: an older front, or no drift processor. */
  | 'no-verdict'
  /** The bound document does not describe this call (method + path). */
  | 'not-routable'
  /** REST: the document routes the call but declares no response for this status. */
  | 'status-undeclared'
  /** REST: the status is declared, but not with this media type (problem+json
   *  under a contract that declares application/json). */
  | 'media-type-undeclared'
  /** REST: the response body could not be decoded as its declared media type. */
  | 'body-not-decodable'
  /** The validator refused the call for a reason the collector does not classify. */
  | 'validator-error'
  /** REST: the document requires a response header the captured call does not carry. */
  | 'response-header-missing'
  /** REST: nothing to compare — a HEAD/redirect status the validator skips, a
   *  response with no body content, or a media type declared without a schema. */
  | 'no-schema'
  /** MCP: the current tools/list does not declare the called tool. */
  | 'tool-not-listed'
  /** MCP: resultType input_required — a mid-flight exchange. */
  | 'input-required'
  /** MCP: the called tool declares no `outputSchema`, so its result is unjudgeable. */
  | 'no-output-contract'
  /** MCP: the result was an execution failure — error output, not contract evidence. */
  | 'error-result'
  /** MCP: the result was a Tasks handle, an envelope with no payload. */
  | 'task-handle'
  /** MCP: no complete JSON structuredContent to check. */
  | 'result-not-json'
  /** The processor did not validate the call and gave a reason this UI does not know. */
  | 'unspecified';

/** A coverage answer with its cause. `reason` is set iff `not-checked`. */
export interface CoverageVerdict {
  coverage: Coverage;
  reason?: NotCheckedReason;
}

/** One tool from a server's tools/list snapshot (ui/src/mcp.ts parseToolRows). */
export interface McpToolCoverage {
  name: string;
  hasOutputSchema: boolean;
}

/** The Traffic chip for an unvalidated call, and its filter value. */
export const NOT_CHECKED_LABEL = 'not checked';

/** The `error-result` tooltip. Deliberately echoes MCP_ERROR_TOOLTIP on the
 *  status chip beside it — the row already says isError is an execution failure
 *  and not contract drift; this says what follows for the contract verdict. */
export const ERROR_RESULT_NOT_CHECKED =
  'The tool returned isError — an execution failure, not contract evidence. ' +
  'Nothing validated this result against the declared outputSchema.';

/** The `no-verdict` tooltip: the record reached the store with no verdict. */
export const NO_VERDICT_NOT_CHECKED =
  'The collector that captured this call recorded no verdict — it predates per-call ' +
  'verdicts, or its pipeline runs no drift processor. Captured, not validated.';

/** Tooltip for the `not checked` chip, one string per CAUSE. */
export function notCheckedTitle(reason: NotCheckedReason, call: CoverageCall = {}): string {
  const host = call.peer_host ? ` for ${call.peer_host}` : '';
  const tool = call.mcp_tool_name || 'this tool';
  switch (reason) {
    case 'contract-not-reached':
      return (
        `A contract${host} is bound now, but had not reached the drift processor when this call went ` +
        'through — it was captured, not validated. A contract validates only the calls that follow its ' +
        'arrival; a tiered front that cannot read the store pod never receives it.'
      );
    case 'no-verdict':
      return NO_VERDICT_NOT_CHECKED;
    case 'not-routable': {
      const which = call.method && call.route ? ` ${call.method} ${call.route}` : ' this call';
      return `The bound contract${host} does not describe${which}, so nothing validated it.`;
    }
    case 'status-undeclared': {
      const which = call.method && call.route ? ` ${call.method} ${call.route}` : ' this call';
      const status = call.status_code ? `status ${call.status_code}` : 'this status';
      return `The bound contract${host} describes${which} but declares no response for ${status} — nothing was compared to a schema.`;
    }
    case 'media-type-undeclared': {
      const which = call.method && call.route ? ` ${call.method} ${call.route}` : ' this call';
      const status = call.status_code ? `status ${call.status_code}` : 'this status';
      const ct = call.response_content_type || 'this media type';
      return `The bound contract${host} describes${which} and ${status}, but not with ${ct} — nothing was compared to a schema.`;
    }
    case 'body-not-decodable':
      return `The response body could not be decoded as ${call.response_content_type || 'its declared media type'}, so nothing was compared to the contract.`;
    case 'validator-error':
      return 'The validator could not judge this response, so nothing was compared to the contract — captured, not validated.';
    case 'response-header-missing':
      return `The bound contract${host} requires a response header this call does not carry, so the validator stopped before the body — nothing was compared to a schema.`;
    case 'no-schema':
      return `The bound contract${host} declares this response without a body schema (or it is a status the validator never judges), so there was nothing to compare — captured, not validated.`;
    case 'tool-not-listed':
      return `The server's current tools/list does not declare ${tool}, so its result could not be checked — see the stale_client notice.`;
    case 'input-required':
      return 'The server asked for more input (resultType input_required) — a mid-flight exchange, not contract evidence.';
    case 'no-output-contract':
      // The honest string the Contracts tab already shows for this very tool.
      return noOutputContractNote(call.integration || call.peer_host || 'this server', tool);
    case 'error-result':
      return ERROR_RESULT_NOT_CHECKED;
    case 'task-handle':
      return "The result was a Tasks handle, not the tool's output — nothing to validate against the outputSchema.";
    case 'result-not-json':
      return `The result carried no complete JSON structuredContent, so nothing was checked against ${tool}'s outputSchema.`;
    case 'unspecified':
      return 'The drift processor did not validate this call — it was captured, not validated.';
    default:
      return `No contract uploaded${host} — this call was captured, not validated.`;
  }
}

const CHECKED: CoverageVerdict = { coverage: 'checked' };
const INTERNAL: CoverageVerdict = { coverage: 'internal' };
const NOT_CHECKED: CoverageVerdict = { coverage: 'not-checked', reason: 'no-contract' };
const notChecked = (reason: NotCheckedReason): CoverageVerdict => ({ coverage: 'not-checked', reason });

/**
 * Coverage for one call, given every loaded contract.
 *
 * `internal` is returned as its own state rather than folded into
 * `not-checked`: an internal edge is metadata-only BY DESIGN (no bodies are
 * captured, so there is nothing to validate and nothing missing), while an
 * external call with no contract is a real gap in what the operator can see.
 * Rendering them the same would turn a deliberate policy into an apparent hole.
 */
export function callCoverageDetail(
  call: CoverageCall,
  specs: readonly CoverageSpec[],
  mcpTools: Readonly<Record<string, readonly McpToolCoverage[]>> = {}
): CoverageVerdict {
  if (call.edge_class === 'internal') return INTERNAL;

  switch (call.validated) {
    case 'clean':
    case 'drifted':
      // The processor ran validation. Whether a contract is still bound, or
      // when it was bound, is beside the point — the check happened.
      return CHECKED;
    case 'not-validated':
      return notChecked(serverReason(call, specs));
    case 'unknown':
      return notChecked('no-verdict');
    case undefined:
    case '':
      // A pre-migration row: no verdict was ever recorded for it. The one
      // place the legacy mirror still answers.
      return legacyVerdict(call, specs, mcpTools);
    default:
      // A verdict word this UI does not know (a newer processor). Never
      // `checked` on a word we cannot read.
      return notChecked('unspecified');
  }
}

/** The state alone, for the many callers that only filter or count on it. */
export function callCoverage(
  call: CoverageCall,
  specs: readonly CoverageSpec[],
  mcpTools: Readonly<Record<string, readonly McpToolCoverage[]>> = {}
): Coverage {
  return callCoverageDetail(call, specs, mcpTools).coverage;
}

/* ── The processor's reason → the chip's cause ────────────────────────── */

const PASSTHROUGH_REASONS: ReadonlySet<string> = new Set<NotCheckedReason>([
  'not-routable',
  'status-undeclared',
  'media-type-undeclared',
  'body-not-decodable',
  'validator-error',
  'response-header-missing',
  'no-schema',
  'tool-not-listed',
  'input-required',
  'no-output-contract',
  'error-result',
  'task-handle',
  'result-not-json'
]);

/**
 * The processor's `no-contract` covers every way its cache can hold nothing
 * for the edge: nothing uploaded, an upload it has not loaded yet, a front that
 * cannot read the store pod. From the operator's chair those are two different
 * sentences, and the contract list on this screen tells them apart.
 */
function serverReason(call: CoverageCall, specs: readonly CoverageSpec[]): NotCheckedReason {
  const reason = call.validated_reason ?? '';
  if (PASSTHROUGH_REASONS.has(reason)) return reason as NotCheckedReason;
  if (reason === 'no-contract' || reason === '') {
    return boundNow(call, specs).length ? 'contract-not-reached' : 'no-contract';
  }
  return 'unspecified';
}

/** The contracts that would validate this call's edge if they reached the
 *  processor — the same host match the legacy mirror uses. */
function boundNow(call: CoverageCall, specs: readonly CoverageSpec[]): CoverageSpec[] {
  if (call.transport === 'mcp') {
    return specs.filter((s) => s.format === 'mcp' && !!s.peer_host && s.peer_host === call.peer_host);
  }
  if (call.direction === 'server') return specs.filter((s) => s.role === 'self');
  return specs.filter(
    (s) => s.role !== 'self' && s.format !== 'mcp' && !!s.peer_host && s.peer_host === call.peer_host
  );
}

/* ── The legacy mirror — pre-migration rows only ──────────────────────── */

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
 * rather than guessed at in either direction.
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

/** Was this call still in front of the collector when one of these contracts
 *  was bound? Calls captured strictly earlier were never offered to it. */
function capturedAfterBinding(call: CoverageCall, matched: readonly CoverageSpec[]): boolean {
  const bound = earliestBinding(matched);
  const captured = epoch(call.captured_at);
  if (bound === undefined || captured === undefined) return true;
  return captured >= bound;
}

/** `checked` only when a contract matched AND it was bound before the call. */
function legacyGate(call: CoverageCall, matched: readonly CoverageSpec[]): CoverageVerdict {
  if (!matched.length) return NOT_CHECKED;
  return capturedAfterBinding(call, matched) ? CHECKED : NOT_CHECKED;
}

/**
 * What processor.go used to do, mirrored — for rows that carry no verdict and
 * never will. Frozen: see the header.
 */
function legacyVerdict(
  call: CoverageCall,
  specs: readonly CoverageSpec[],
  mcpTools: Readonly<Record<string, readonly McpToolCoverage[]>>
): CoverageVerdict {
  if (call.transport === 'mcp') {
    // Snapshots only — NOT the wider "hosts we have seen MCP traffic from".
    const snapshots = boundNow(call, specs);
    if (!snapshots.length) return NOT_CHECKED;
    // Per TOOL: only a tool that publishes an outputSchema can have its result
    // validated. Rows not loaded yet resolve conservatively.
    const rows = (call.integration && mcpTools[call.integration]) || [];
    const tool = rows.find((t) => t.name === call.mcp_tool_name);
    if (!tool?.hasOutputSchema) return notChecked('no-output-contract');
    // isError: error output, not the tool's payload; the processor skipped it.
    if (call.mcp_is_error) return notChecked('error-result');
    // And the snapshot has to have ARRIVED first.
    return legacyGate(call, snapshots);
  }
  // Inbound against the self contract; outbound against the host's upload.
  return legacyGate(call, boundNow(call, specs));
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
 * checked nothing — no traffic yet, a quiet edge, a host binding that is simply
 * wrong, or a front that cannot reach the store pod — and none of those has any
 * other symptom: no error, no finding, a card that looks complete. The chip
 * beside it already refuses to say "conforming" without evidence; this says
 * how much evidence there is, so "none" is visible rather than merely implied.
 */
export function validatedCallsMeta(n: number, since: string = SINCE_UPLOAD): string {
  return `validated ${n} ${n === 1 ? 'call' : 'calls'} since ${since}`;
}
