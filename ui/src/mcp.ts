// Pure helpers for the v0.5 MCP surfaces (spec §4.D). Every user-facing string
// here is VERBATIM deck copy ("v0.5 MCP surfaces — UX copy deck") with the
// placeholders filled in. No DOM, no fetch — unit-tested with vitest.
//
// Flaggability (spec §1/§6 evidence rule, AMENDED qfix2-2026-08-26 —
// ux-design-v2 §2.7; enforced server-side by the relay):
//   output_mismatch                    → flaggable (has a source call)
//   definition_change, EVERY class     → flaggable, and CALL-LESS: no call is
//     shared, because the evidence is the provider's own published definitions,
//     before and after. The control is never born disabled.
//   stale_client                       → local notice, NO flag control anywhere,
//     ever. Consumer-side; it fails the evidence rule.
// Nothing auto-flags: a finding only leaves this collector when a human presses
// the control.

import { requestIdsLine } from './threads';
import type { Correlation, Finding, RedactedCall } from './types';

// ─── Kind / class helpers ────────────────────────────────────────────────────

export const MCP_FINDING_KINDS = ['output_mismatch', 'definition_change', 'stale_client'] as const;

export function isMcpFinding(f: Pick<Finding, 'kind'>): boolean {
  return (MCP_FINDING_KINDS as readonly string[]).includes(f.kind);
}

export function isMcpCall(c: Pick<RedactedCall, 'transport'> | null | undefined): boolean {
  return !!c && c.transport === 'mcp';
}

/** Called tool name of an MCP call (the route slot carries "/<tool>" as a fallback). */
export function toolNameOf(c: Pick<RedactedCall, 'mcp_tool_name' | 'route'>): string {
  return c.mcp_tool_name || (c.route || '').replace(/^\/+/, '');
}

export type DefinitionClass = 'BREAKING' | 'NON-BREAKING' | 'DESCRIPTION';

/** Class badge of a definition_change finding, from severity + rule. */
export function definitionClass(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): DefinitionClass | '' {
  if (f.kind !== 'definition_change') return '';
  if (f.rule === 'description-changed') return 'DESCRIPTION';
  return f.severity === 'breaking' ? 'BREAKING' : 'NON-BREAKING';
}

/**
 * Local-only items: never a flag control, anywhere (spec §6 evidence rule).
 * Since qfix2-2026-08-26 this is stale_client and ONLY stale_client — a
 * DESCRIPTION definition change is now flaggable, so it is no longer a local
 * notice and no longer appears in the Overview "Local notices" band (whose own
 * sub-line promises that nothing in it can be flagged).
 */
export function isLocalNotice(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  return f.kind === 'stale_client';
}

/** A DESCRIPTION-class definition change — the one finding class that carries
 *  the mute-risk guard line on the flag sheet (§2.7.4). */
export function isDescriptionChange(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  return definitionClass(f) === 'DESCRIPTION';
}

/** Cross-org flaggable kinds (the relay enforces the same rule server-side). */
export function isFlaggableMcp(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  return isMcpFinding(f) && !isLocalNotice(f);
}

// ─── Badge tiers + local acknowledge (qfix-2026-08-25) ───────────────────────
// Two-tier taxonomy: red = breaking-severity (act), amber = informational
// (review) = NON-BREAKING + DESCRIPTION. Severity decides the tier, never the
// protocol. Acknowledge is LOCAL ONLY (wire key `ack`): it clears an
// informational finding out of the amber counts on this collector — nothing is
// sent to the control plane, and it is never a path to flagging.

/** Red tier: breaking-severity findings, all sources (REST live-vs-spec
 *  BREAKING + MCP BREAKING incl. output_mismatch). */
export function isBreakingFinding(f: Pick<Finding, 'severity'>): boolean {
  return f.severity === 'breaking';
}

/** Ackable: informational definition changes only — DESCRIPTION or
 *  NON-BREAKING. BREAKING rows are never ackable (resolved by a fix or a
 *  thread, not muted); stale_client keeps no control at all. The relay
 *  enforces the same rule server-side (403 not_ackable). */
export function isAckable(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  const cls = definitionClass(f);
  return cls === 'DESCRIPTION' || cls === 'NON-BREAKING';
}

/**
 * The evidence version an acknowledgement on this finding binds to: the AFTER
 * snapshot hash for a definition_change, empty for every other kind (mirrors
 * ackEvidenceVersion in extension/flanjui/acks.go).
 */
export function ackEvidenceVersion(f: Pick<Finding, 'kind' | 'spec_version_to'>): string {
  return f.kind === 'definition_change' ? f.spec_version_to || '' : '';
}

/**
 * Acknowledged on this collector (from the read-API join).
 *
 * The collector applies the evidence-version rule server-side; this repeats it
 * client-side on purpose (ux-design-v2 §2.8, §7 risk 2 — the highest-severity
 * item in the slice). A SECOND definition change on the same tool and field has
 * the IDENTICAL signature, so a signature-only ack would render it silently
 * pre-acknowledged and a breaking change could sit unseen. Two independent
 * checks means one of them failing cannot hide a new change.
 */
export function isAcked(
  f: Pick<Finding, 'kind' | 'acked' | 'acked_evidence_version' | 'spec_version_to'>
): boolean {
  if (f.acked !== true) return false;
  return (f.acked_evidence_version || '') === ackEvidenceVersion(f);
}

export const ACK_LABEL = 'Acknowledge';
export const ACK_TITLE = 'Local only — clears it from the counts on this collector. Nothing is sent anywhere.';
export const UNDO_LABEL = 'Undo';
export const UNDO_TITLE = 'Puts it back in the count.';

/** Acked footer line: `Acknowledged 5m ago.` */
export function ackedLine(relative: string): string {
  return `Acknowledged ${relative}.`;
}

/** Red tab-pill title: `7 breaking findings`. */
export function breakingCountTitle(n: number): string {
  return `${n} breaking finding${n === 1 ? '' : 's'}`;
}

/** Amber tab-pill title: `2 non-breaking — acknowledge to clear`. */
export function informationalCountTitle(n: number): string {
  return `${n} non-breaking — acknowledge to clear`;
}

/** Red card chip: `1 BREAKING`. */
export function breakingChipLabel(n: number): string {
  return `${n} BREAKING`;
}

/** Amber card chip: `2 NON-BREAKING`. */
export function informationalChipLabel(n: number): string {
  return `${n} NON-BREAKING`;
}

/** Amber card chip title: `1 non-breaking change · 1 description change`. */
export function informationalChipTitle(nonBreaking: number, description: number): string {
  const parts: string[] = [];
  if (nonBreaking > 0) parts.push(`${nonBreaking} non-breaking change${nonBreaking === 1 ? '' : 's'}`);
  if (description > 0) parts.push(`${description} description change${description === 1 ? '' : 's'}`);
  return parts.join(' · ');
}

// ─── Deck §1 — Edges ─────────────────────────────────────────────────────────

export const MCP_BADGE_TOOLTIP = 'An MCP server — its tools/list is the contract.';

export function mcpBadgeLabel(edgeClass?: string): string {
  return edgeClass === 'local-process' ? 'MCP · stdio' : 'MCP';
}

// ─── Deck §2 — Health (Overview) ─────────────────────────────────────────────

export interface McpServerRef {
  /** serverInfo.name — NOT unique: two servers can publish the same one. */
  name: string;
  version?: string;
  /** Where this server is: its host, or `stdio` for a local process. Two
   *  servers sharing a name rendered two IDENTICAL health lines without it. */
  origin?: string;
}

/**
 * `Server: <name> v<version> · <origin>. You: `
 *
 * The origin goes after the version and before the `. You: ` pivot, so all four
 * headline branches inherit it from here and none needs editing — and so this
 * surface can never drift from the Contracts card, which builds its heading the
 * same way.
 */
function serverLead(s: McpServerRef): string {
  const version = s.version ? ' v' + s.version : '';
  const origin = s.origin ? ' · ' + s.origin : '';
  return `Server: ${s.name}${version}${origin}. You: `;
}

/**
 * The per-server MCP health headline. Priority: output mismatch (drift) →
 * breaking definition change (no calls affected yet) → description change →
 * clean.
 *
 * The DESCRIPTION clause exists because qfix2-2026-08-26 moved description
 * changes OUT of the Overview "Local notices" band (they are flaggable now, and
 * that band promises nothing in it can be flagged). Without a clause here a
 * server whose only drift is a wording change would report "no drift detected"
 * in GREEN on Overview while the Contracts tab showed an amber row with a
 * primary `Flag this` — the tab and the headline contradicting each other
 * (ux-design-v2 §7 risk 3: that band must never render green). Description
 * drift is not an alarm, so the line says plainly what did and did not change;
 * it is simply not `ok`.
 */
export function mcpHeadline(
  s: McpServerRef,
  findings: Finding[],
  fmtTime: (iso: string) => string
): { text: string; ok: boolean } {
  const mismatches = findings.filter((f) => f.kind === 'output_mismatch');
  if (mismatches.length > 0) {
    const tools = Array.from(new Set(mismatches.map((f) => f.endpoint))).join(', ');
    const calls = mismatches.reduce((n, f) => n + (f.occurrence_count || 1), 0);
    const since = mismatches
      .map((f) => f.first_seen || f.detected_at || '')
      .filter(Boolean)
      .sort()[0];
    return {
      text: serverLead(s) + `output mismatch on ${tools} — ${calls} call${calls === 1 ? '' : 's'} since ${fmtTime(since || '')}.`,
      ok: false
    };
  }
  const breaking = findings.filter((f) => f.kind === 'definition_change' && definitionClass(f) === 'BREAKING');
  if (breaking.length > 0) {
    const tools = Array.from(new Set(breaking.map((f) => f.endpoint))).join(', ');
    return { text: serverLead(s) + `definition change on ${tools} — breaking, no calls affected yet.`, ok: false };
  }
  const described = findings.filter((f) => f.kind === 'definition_change' && definitionClass(f) === 'DESCRIPTION');
  if (described.length > 0) {
    const tools = Array.from(new Set(described.map((f) => f.endpoint))).join(', ');
    return { text: serverLead(s) + `definition change on ${tools} — description only, no schema change.`, ok: false };
  }
  return { text: serverLead(s) + 'no drift detected.', ok: true };
}

/** Local notices band (deck §2): title + sub. Items carry no Flag control, ever. */
export const LOCAL_NOTICES_TITLE = 'Local notices';

export function localNoticesSub(provider: string): string {
  return `Visible to you only. Nothing here can be flagged — these aren't evidence against ${provider}.`;
}

/**
 * The band's sub-line for the providers its notices actually span: one
 * provider → named; several (or none we can name) → the neutral fallback, so
 * one org's notices are never blamed on another.
 */
export function localNoticesSubFor(providers: string[]): string {
  const named = Array.from(new Set(providers.filter(Boolean)));
  return localNoticesSub(named.length === 1 ? named[0] : 'the provider');
}

/**
 * One local-notice line. stale_client only since qfix2-2026-08-26: the
 * DESCRIPTION line used to live here and closed by calling itself a local note,
 * which the policy change made false. It is deleted rather than re-worded — a
 * description change now belongs on the Contracts tab, with a flag control.
 */
export function noticeLine(f: Finding, server: string): string {
  if (f.rule === 'tool-not-listed') {
    return `Your agent still calls ${f.endpoint} — ${server} no longer lists it. Update your client.`;
  }
  const path = f.field_path ? '$.' + f.field_path : f.location || '';
  return `Your agent's arguments to ${f.endpoint} no longer match the current inputSchema at ${path}. Update your client.`;
}

// ─── Deck §3 — Contracts ─────────────────────────────────────────────────────

export const MCP_NO_SPEC_NEEDED = 'No spec file needed — the server publishes its own contract on tools/list.';

export function mcpContractMeta(toolCount: number, updated: string): string {
  return `${toolCount} tool${toolCount === 1 ? '' : 's'} · contract observed from tools/list · updated ${updated}`;
}

/** Per-tool row label from the tool's declared schemas. */
export function toolContractLabel(hasOutputSchema: boolean): string {
  return hasOutputSchema ? 'input + output contract' : 'input contract only';
}

export function noOutputContractNote(server: string, tool: string): string {
  return `No output contract declared — ${server} doesn't say what ${tool} returns, so output drift on this tool can't be checked.`;
}

/** A tool row parsed out of the stored tools/list snapshot document. */
export interface McpToolRow {
  name: string;
  hasOutputSchema: boolean;
}

/** Parse the raw snapshot document ({"tools":[…]}) into per-tool rows. */
export function parseToolRows(rawSnapshot: string): McpToolRow[] {
  try {
    const doc = JSON.parse(rawSnapshot);
    const tools = Array.isArray(doc?.tools) ? doc.tools : [];
    return tools
      .filter((t: any) => t && typeof t.name === 'string' && t.name)
      .map((t: any) => ({ name: t.name as string, hasOutputSchema: t.outputSchema != null }));
  } catch {
    return [];
  }
}

/**
 * The two snapshot timestamps of a definition_change finding, parsed from its
 * detail line ("… tools/list observed <t1> → <t2>." — produced by this
 * collector's own detector, so the shape is ours to rely on).
 */
export function snapshotTimes(detail?: string): { from: string; to: string } {
  const m = /tools\/list observed (\S+) → (\S+?)\.?$/.exec(detail || '');
  return m ? { from: m[1], to: m[2] } : { from: '', to: '' };
}

export function beforeColLabel(hash: string, time: string): string {
  return `before (snapshot ${hash || '—'} · ${time || '—'})`;
}

export function afterColLabel(hash: string, time: string): string {
  return `after (snapshot ${hash || '—'} · ${time || '—'})`;
}

/** definition_change row detail (deck §3). */
export function defChangeDetail(t1: string, t2: string, provider: string): string {
  return `Their tools/list at ${t1} vs at ${t2} — both ${provider}'s own words.`;
}

// ─── Deck §4 — Traffic ───────────────────────────────────────────────────────

export const MCP_TOOL_CHIP = 'TOOL';
export const MCP_ERROR_TOOLTIP = 'The server returned isError — an execution failure, not contract drift.';
export const JSONRPC_ID_TITLE = 'JSON-RPC id (client-generated)';

/** Status slot text of an MCP call row (from isError). */
export function mcpStatusLabel(c: Pick<RedactedCall, 'mcp_is_error'>): 'ok' | 'error' {
  return c.mcp_is_error ? 'error' : 'ok';
}

/**
 * The method-facet value of a call — what the method filter matches and what
 * its dropdown lists. MCP rows facet as the TOOL chip (their method slot),
 * never as the wire's `tools/call`, so an HTTP method filter excludes them.
 */
export function methodFacetOf(c: Pick<RedactedCall, 'transport' | 'method'>): string {
  return c.transport === 'mcp' ? MCP_TOOL_CHIP : (c.method || '').toUpperCase();
}

/**
 * Status-facet match for one call. HTTP rows match by status class ('err' =
 * ≥400). MCP rows carry NO HTTP status — they are ok/error from isError
 * (deck §4): 'err' matches an isError row, the ok bucket '2xx' matches an ok
 * row, and 3xx/4xx/5xx match nothing (an MCP error has no HTTP class to claim).
 */
export function statusFilterMatches(
  filter: string,
  c: Pick<RedactedCall, 'transport' | 'mcp_is_error' | 'status_code'>
): boolean {
  if (!filter) return true;
  if (c.transport === 'mcp') {
    if (filter === 'err') return !!c.mcp_is_error;
    if (filter === '2xx') return !c.mcp_is_error;
    return false;
  }
  if (filter === 'err') return c.status_code >= 400;
  return `${Math.floor(c.status_code / 100)}xx` === filter;
}

// ─── Deck §5 — Flag sheet (MCP) ──────────────────────────────────────────────

/** `type=integer` → `integer`; `type=string ("1200")` → `string`. */
export function typeOf(rendered: string): string {
  const m = /^type=([^ (]+)/.exec(rendered || '');
  return m ? m[1] : rendered || '';
}

/** `type=string ("1200")` → `"1200"`; falls back to the rendered text. */
export function valueOf(rendered: string): string {
  const m = /^type=[^(]*\((.*)\)$/.exec(rendered || '');
  return m ? m[1] : rendered || '';
}

function pathOf(f: Pick<Finding, 'field_path' | 'location'>): string {
  return f.field_path ? '$.' + f.field_path : f.location || '';
}

/**
 * The AFTER snapshot's observation time on a definition_change: the structured
 * field when the collector supplied it, else parsed out of the detail line.
 */
export function afterObservedAt(f: Pick<Finding, 'snapshot_observed_at' | 'detail'>): string {
  return f.snapshot_observed_at || snapshotTimes(f.detail).to;
}

/**
 * Evidence line body (rendered after the "Evidence (1):" label).
 *
 * DESCRIPTION gets its own variant (ux-design-v2 §2.7.4): the claim it makes is
 * "your own two published versions", not "a definition change of class X" —
 * which is what lets a subjective finding cross the org boundary honestly.
 */
export function mcpEvidenceLine(f: Finding, server: string, fmtDate: (iso: string) => string = (x) => x): string {
  if (f.kind === 'definition_change') {
    if (isDescriptionChange(f)) {
      return `${f.endpoint} — description changed in your tools/list on ${fmtDate(afterObservedAt(f))}. Both versions are your own published text.`;
    }
    const cls = definitionClass(f);
    const t = snapshotTimes(f.detail);
    return `${f.endpoint} on ${server} — definition change (${cls}): ${f.rule}. Two tools/list snapshots, ${t.from} → ${t.to}.`;
  }
  return `${f.endpoint} on ${server} — output mismatch at ${pathOf(f)}: declared ${typeOf(f.expected)}, got ${typeOf(f.actual)}`;
}

/**
 * The mute-risk guard (ux-design-v2 §2.7.4), rendered directly above the
 * primary button on the DESCRIPTION class ONLY. It stops a subjective finding
 * from landing at the provider as an accusation.
 */
export const FLAG_DESCRIPTION_GUARD = "This isn't a bug report — you're asking whether the change was intended.";

/** IDs line for an output_mismatch flag (the JSON-RPC id is client-generated). */
export function mcpIdsLine(provider: string): string {
  return `1 request ID will be shared. It's your client's JSON-RPC id — it shows up in ${provider}'s logs only if they log it.`;
}

/**
 * The IDs line for an MCP flag. The deck's JSON-RPC line is honest only while
 * the client-generated id is the SOLE correlation key; with mixed keys the
 * standard count line runs, plus a short honest note for the client-generated
 * one (never pitched as an id the provider issued).
 */
export function mcpIdsLineFor(c: Correlation | null | undefined, provider: string): string {
  const hasClientId = !!c?.client_request_id;
  const others = c ? [c.request_id, c.idempotency_key, c.trace_id].filter(Boolean).length : 0;
  if (hasClientId && others === 0) return mcpIdsLine(provider);
  const line = requestIdsLine(others + (hasClientId ? 1 : 0));
  if (!hasClientId) return line;
  return `${line} One of them is your client's own JSON-RPC id — in ${provider}'s logs only if they log it.`;
}

/** The sub-line under a call-less definition_change evidence (deck §5). */
export function defChangeNoCallSub(provider: string): string {
  return `No call is shared — the evidence is ${provider}'s own published definitions, before and after.`;
}

/**
 * "What leaves this collector" (deck §5) — the lead before the
 * `<C> · <E>` names; the tail after them.
 */
export function mcpDisclosureLead(f: Pick<Finding, 'kind' | 'severity' | 'rule'>, tool: string): string {
  if (f.kind === 'definition_change') {
    if (isDescriptionChange(f)) {
      return 'The two published descriptions, when each was observed, the endpoint, your message, and';
    }
    return `The before/after fragments of ${tool}'s definition, the finding, the two snapshot hashes and observed-at times, the server name and version, your message, and`;
  }
  return "This redacted tool call and result, the finding, the tool's declared output schema, the JSON-RPC id, the tool and server name, your message, and";
}

export function mcpDisclosureTail(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): string {
  if (f.kind !== 'definition_change') return 'Raw calls never leave.';
  return isDescriptionChange(f) ? 'Raw calls never leave.' : 'No call data is involved, so none leaves.';
}

/** Prefilled, editable, optional message for the Flag sheet (deck §5). */
export function mcpDefaultMessage(f: Finding, fmtDate: (iso: string) => string): string {
  if (f.kind === 'definition_change') {
    if (isDescriptionChange(f)) {
      return `Your tools/list description for ${f.endpoint} changed on ${fmtDate(afterObservedAt(f))}. The schema didn't change, but the wording did, and our agent picks tools from that text. Can you confirm the new wording is intended and stable?`;
    }
    const t = snapshotTimes(f.detail);
    return `Your tools/list changed ${f.endpoint} between ${fmtDate(t.from)} and ${fmtDate(t.to)} — ${f.rule}. Was this intentional? Anything we should migrate to?`;
  }
  const got = typeOf(f.actual);
  const article = /^[aeiou]/i.test(got) ? 'an' : 'a';
  const since = fmtDate(f.first_seen || f.detected_at || '');
  return `Seeing ${f.endpoint} return ${article} ${got} at ${pathOf(f)} since ${since} — your outputSchema says ${typeOf(f.expected)}. Can you confirm on your side?`;
}

/** How many request IDs an MCP flag will share (the client-generated JSON-RPC id). */
export function mcpCorrelationCount(c?: Correlation | null): number {
  if (!c) return 0;
  return [c.client_request_id, c.request_id, c.idempotency_key, c.trace_id].filter(Boolean).length;
}
