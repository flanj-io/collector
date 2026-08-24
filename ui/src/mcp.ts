// Pure helpers for the v0.5 MCP surfaces (spec §4.D). Every user-facing string
// here is VERBATIM deck copy ("v0.5 MCP surfaces — UX copy deck") with the
// placeholders filled in. No DOM, no fetch — unit-tested with vitest.
//
// Flaggability (spec §1/§6 evidence rule, enforced server-side by the relay):
//   output_mismatch                    → flaggable (has a source call)
//   definition_change BREAKING/NON-BR. → flaggable in principle, but call-less —
//     the relay/CP refuse a finding without a call (400 finding_has_no_call),
//     a KNOWN v0.5 limitation; the UI keeps the Flag control disabled with the
//     honest deck reason instead of pretending.
//   definition_change DESCRIPTION      → local note, NO flag control anywhere
//   stale_client                       → local notice, NO flag control anywhere

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

/** Local-only items: never a flag control, anywhere (spec §6 evidence rule). */
export function isLocalNotice(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  return f.kind === 'stale_client' || definitionClass(f) === 'DESCRIPTION';
}

/** Cross-org flaggable kinds (the relay enforces the same rule server-side). */
export function isFlaggableMcp(f: Pick<Finding, 'kind' | 'severity' | 'rule'>): boolean {
  return isMcpFinding(f) && !isLocalNotice(f);
}

// ─── Deck §1 — Edges ─────────────────────────────────────────────────────────

export const MCP_BADGE_TOOLTIP = 'An MCP server — its tools/list is the contract.';

export function mcpBadgeLabel(edgeClass?: string): string {
  return edgeClass === 'local-process' ? 'MCP · stdio' : 'MCP';
}

// ─── Deck §2 — Health (Overview) ─────────────────────────────────────────────

export interface McpServerRef {
  /** serverInfo.name */
  name: string;
  version?: string;
}

function serverLead(s: McpServerRef): string {
  return `Server: ${s.name}${s.version ? ' v' + s.version : ''}. You: `;
}

/**
 * The per-server MCP health headline. Priority: output mismatch (drift) →
 * breaking definition change (no calls affected yet) → clean.
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

/** One local-notice line (stale_client / DESCRIPTION-only definition change). */
export function noticeLine(f: Finding, server: string, provider: string): string {
  if (f.kind === 'stale_client') {
    if (f.rule === 'tool-not-listed') {
      return `Your agent still calls ${f.endpoint} — ${server} no longer lists it. Update your client.`;
    }
    const path = f.field_path ? '$.' + f.field_path : f.location || '';
    return `Your agent's arguments to ${f.endpoint} no longer match the current inputSchema at ${path}. Update your client.`;
  }
  return `Description changed on ${f.endpoint} — schema unchanged. This can change which tools your model picks. Wording is ${provider}'s to change, so this stays a local note.`;
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

export const LOCAL_NOTE_NOT_FLAGGABLE = 'Local note — not flaggable.';

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

/** Evidence line body (rendered after the "Evidence (1):" label). */
export function mcpEvidenceLine(f: Finding, server: string): string {
  if (f.kind === 'definition_change') {
    const cls = definitionClass(f);
    const t = snapshotTimes(f.detail);
    return `${f.endpoint} on ${server} — definition change (${cls}): ${f.rule}. Two tools/list snapshots, ${t.from} → ${t.to}.`;
  }
  return `${f.endpoint} on ${server} — output mismatch at ${pathOf(f)}: declared ${typeOf(f.expected)}, got ${typeOf(f.actual)}`;
}

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
export function mcpDisclosureLead(f: Pick<Finding, 'kind'>, tool: string): string {
  if (f.kind === 'definition_change') {
    return `The before/after fragments of ${tool}'s definition, the finding, the two snapshot hashes and observed-at times, the server name and version, your message, and`;
  }
  return "This redacted tool call and result, the finding, the tool's declared output schema, the JSON-RPC id, the tool and server name, your message, and";
}

export function mcpDisclosureTail(f: Pick<Finding, 'kind'>): string {
  return f.kind === 'definition_change' ? 'No call data is involved, so none leaves.' : 'Raw calls never leave.';
}

/** Prefilled, editable, optional message for the Flag sheet (deck §5). */
export function mcpDefaultMessage(f: Finding, fmtDate: (iso: string) => string): string {
  if (f.kind === 'definition_change') {
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
