// The Overview system status — ONE status for the whole collector, and the
// "Not validated" list of everything that status does not cover.
//
// This replaces two things that used to speak separately: the REST headline
// (`headlineFor`, deleted — ui/src/headline.ts) and the per-server MCP lines
// with their own clean-line fold (`foldMcpOverviewLines`, deleted —
// ui/src/mcp.ts). Both told the truth about their own edge and nothing about
// the other, so a REST provider with no contract could sit unmentioned next
// to a green REST all-clear, and an MCP output mismatch could sit unmentioned
// next to it too. Drift anywhere now means the STATUS is drift; the all-clear
// needs at least one checked call anywhere and no drift anywhere; and every
// surface nothing has checked yet is named once, under a disclosure of its
// own, instead of by omission.
//
// `systemStatus` takes ALREADY-DERIVED evidence — REST findings, MCP drift
// lines (from `mcpHeadline`, ui/src/mcp.ts), how much was checked, and the
// items list (from `notValidatedItems` below) — and decides the title, tone
// and lines. It does not walk calls itself: that walk is `notValidatedItems`'s
// job, and the two are kept separate so each is testable on its own terms.

import { NOTHING_VALIDATED_YET, NO_DRIFT_DETECTED, type HeadlineTone } from './headline';
import {
  callCoverageDetail,
  type CoverageCall,
  type CoverageSpec,
  type McpToolCoverage,
  type NotCheckedReason
} from './coverage';

export type StatusTone = HeadlineTone;

/* ── systemStatus ──────────────────────────────────────────────────────── */

/** One REST provider's drift, already grouped by host — see `groupRestFindings`. */
export interface RestFindingInput {
  /** Provider display name, fallback to host — the caller's job (contracts.ts). */
  name: string;
  host: string;
  endpoint: string;
  occurrence_count?: number;
  first_seen?: string;
  detected_at?: string;
}

export interface RestDriftLine {
  name: string;
  host: string;
  text: string;
}

/** RFC3339 timestamps, earliest first, '' when none parse. */
function earliest(times: readonly (string | undefined)[]): string {
  const sorted = times.filter((t): t is string => !!t).sort();
  return sorted[0] || '';
}

/**
 * Group LIVE REST findings by provider host into one status line each — the
 * REST half of "Drift detected in N places". Mirrors the MCP per-server line
 * (`mcpHeadline`, ui/src/mcp.ts): one line, one host, every endpoint it hit.
 */
export function groupRestFindings(findings: readonly RestFindingInput[], fmtTime: (iso: string) => string): RestDriftLine[] {
  const byHost = new Map<string, RestFindingInput[]>();
  for (const f of findings) {
    const list = byHost.get(f.host) || [];
    list.push(f);
    byHost.set(f.host, list);
  }
  const out: RestDriftLine[] = [];
  for (const [host, group] of byHost) {
    const endpoints = Array.from(new Set(group.map((f) => f.endpoint)));
    const calls = group.reduce((n, f) => n + (f.occurrence_count || 1), 0);
    const since = earliest(group.map((f) => f.first_seen || f.detected_at));
    const n = group.length;
    out.push({
      name: group[0].name,
      host,
      text:
        `${group[0].name} · ${host} — ${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')} — ` +
        `${calls} call${calls === 1 ? '' : 's'} since ${fmtTime(since)}.`
    });
  }
  return out;
}

/** One MCP server's drift line, already rendered (`mcpHeadline`). */
export interface McpDriftLine {
  /** The server's display name, for the title when this is the only place. */
  name: string;
  text: string;
  /** The contract row's integration id — how the caller matches this line
   *  back to the surface it is about (checkedCounts, ui/App.vue), since two
   *  servers can share a display name. */
  integration: string;
}

/**
 * Surfaces with at least one CHECKED call, already excluding any surface that
 * has a line of its own in `restLines`/`mcpLines` — a REST provider or MCP
 * server that drifted is never "the rest of what was checked" (ui/App.vue
 * checkedCounts does the exclusion, against those same lines). `inbound`
 * counts distinct checked peers, not a flag.
 */
export interface CheckedCounts {
  restProviders: number;
  inbound: number;
  mcpServers: number;
}

export interface StatusNote {
  text: string;
  tool: string;
}

export interface SystemStatusInput {
  restLines: readonly RestDriftLine[];
  mcpLines: readonly McpDriftLine[];
  checked: CheckedCounts;
  /** DESCRIPTION-only changes — never move the tone, rendered as quiet notes. */
  notes: readonly StatusNote[];
  /** How many rows `notValidatedItems` produced — decides whether the button shows. */
  itemCount: number;
}

export interface SystemStatus {
  tone: StatusTone;
  title: string;
  lines: readonly (RestDriftLine | McpDriftLine)[];
  /** The `Checked: …` / `No drift in the rest…` / neutral sub-line. */
  sub: string;
  /** `observed here, by collector …` is App.vue's own line (it owns the
   *  collector's identity) and is not part of this module. */
  notes: readonly StatusNote[];
  showButton: boolean;
}

function checkedTally(c: CheckedCounts): string[] {
  const parts: string[] = [];
  if (c.restProviders > 0) parts.push(`${c.restProviders} REST provider${c.restProviders === 1 ? '' : 's'}`);
  if (c.inbound > 0) parts.push(`${c.inbound} inbound consumer${c.inbound === 1 ? '' : 's'}`);
  if (c.mcpServers > 0) parts.push(`${c.mcpServers} MCP server${c.mcpServers === 1 ? '' : 's'}`);
  return parts;
}

/**
 * The truth table (ui/src/status.test.ts pins every row):
 *
 *   drift anywhere | ≥1 checked call | items | status                | tone
 *   yes            | any             | any   | Drift detected …      | drift
 *   no             | yes             | any   | No drift detected     | ok
 *   no             | no              | any   | Nothing validated yet | neutral
 *
 * A finding is evidence in itself — drift wins even with zero checked calls
 * in the window (the drifting call can be evicted while its finding
 * outlives it). The button shows whenever `itemCount > 0`, in every tone.
 */
export function systemStatus(input: SystemStatusInput): SystemStatus {
  const places = input.restLines.length + input.mcpLines.length;
  const showButton = input.itemCount > 0;

  if (places > 0) {
    const title =
      places === 1
        ? `Drift detected on ${input.restLines[0]?.name ?? input.mcpLines[0]?.name}`
        : `Drift detected in ${places} places`;
    // `input.checked` already excludes every surface named in `lines` above
    // (the caller built it that way) — a drifting REST provider or MCP
    // server can never be double-counted as part of the clean rest. When
    // that leaves nothing, `checkedTally` returns an empty list and the
    // sentence disappears instead of naming the very things that drifted.
    const rest = checkedTally(input.checked);
    const sub = rest.length ? `No drift in the rest of what was checked: ${rest.join(' · ')}.` : '';
    return {
      tone: 'drift',
      title,
      lines: [...input.restLines, ...input.mcpLines],
      sub,
      notes: input.notes,
      showButton
    };
  }

  const anyChecked = input.checked.restProviders + input.checked.inbound + input.checked.mcpServers > 0;
  if (anyChecked) {
    const tally = checkedTally(input.checked).join(' · ');
    const tail = showButton ? 'What nothing checked is under Not validated.' : 'That is everything this collector saw.';
    return {
      tone: 'ok',
      title: NO_DRIFT_DETECTED,
      lines: [],
      sub: `Checked: ${tally}. ${tail}`,
      notes: input.notes,
      showButton
    };
  }

  return {
    tone: 'neutral',
    title: NOTHING_VALIDATED_YET,
    lines: [],
    sub: showButton
      ? 'Calls are being captured, but none has been checked against a contract yet.'
      : '',
    notes: input.notes,
    showButton
  };
}

/* ── notValidatedItems ─────────────────────────────────────────────────── */

export type NotValidatedKind =
  | 'rest-no-contract'
  | 'rest-contract-gap'
  | 'rest-contract-not-reached'
  | 'inbound-no-self'
  | 'mcp-no-output-schema'
  | 'mcp-nothing-checked'
  | 'no-verdict';

export type NotValidatedGroup = 'rest' | 'inbound' | 'mcp';

export interface NotValidatedAction {
  label: string;
}

export interface NotValidatedItem {
  kind: NotValidatedKind;
  group: NotValidatedGroup;
  /** Stable within a render: host for REST/inbound, integration for MCP. */
  key: string;
  name: string;
  host?: string;
  why: string;
  action?: NotValidatedAction;
}

export const ADD_REST_CONTRACT_ACTION = 'Add REST contract';
export const SHOW_THESE_CALLS_ACTION = 'Show these calls';
export const HOW_TO_ADD_IT_ACTION = 'How to add it';
export const VIEW_TOOLS_ACTION = 'View tools';

export const YOUR_API_NAME = 'Your API';
export const SELF_SPEC_PATH_HINT = 'flanjdrift.self_spec_path';

/** Not listed row by row: single per-call skips on a surface that otherwise
 *  checked calls fine. One footnote on the panel says so instead. */
export const SKIPPED_PER_CALL_FOOTNOTE =
  'Not listed: single calls that were skipped for their own reason, such as a tool result marked as an ' +
  'error, or a call made before the server’s tools/list arrived. The Traffic row of each one says why.';

export interface NotValidatedNames {
  /** Display name for a REST provider host — fallback to the host itself. */
  restName(host: string): string;
  /** The MCP server this integration id names, and where it runs. */
  mcpServer(integration: string): { name: string; version?: string; origin?: string; host?: string };
}

const GAP_REASONS: ReadonlySet<NotCheckedReason> = new Set([
  'not-routable',
  'status-undeclared',
  'media-type-undeclared',
  'body-not-decodable',
  'validator-error',
  'response-header-missing',
  'no-schema',
  'unspecified'
]);

const MCP_SKIP_PHRASE: Partial<Record<NotCheckedReason, string>> = {
  'error-result': 'returned an error result',
  'task-handle': 'returned a Tasks handle, not the result',
  'result-not-json': 'carried no JSON result to check',
  'input-required': 'asked for more input',
  'tool-not-listed': 'called a tool the server no longer lists'
};

interface RestBucket {
  reasons: Map<NotCheckedReason, number>;
  notChecked: number;
  checked: number;
  sample?: { method?: string; route?: string };
}

interface McpBucket {
  checked: number;
  schemaless: Map<string, number>;
  skipReasons: Map<NotCheckedReason, number>;
  noVerdict: number;
}

function bestRestKind(reasons: ReadonlySet<NotCheckedReason>): NotValidatedKind {
  for (const r of reasons) if (GAP_REASONS.has(r)) return 'rest-contract-gap';
  if (reasons.has('contract-not-reached')) return 'rest-contract-not-reached';
  if (reasons.has('no-verdict') && reasons.size === 1) return 'no-verdict';
  return 'rest-no-contract';
}

/**
 * Every surface this collector captured but could not check — REST providers
 * with no contract or a gap in the one they have, your own inbound API with
 * no self contract loaded, and MCP servers whose calls a schema-less tool or
 * a run of skips left unjudged. One row per surface (`kind` + `key`): a
 * provider with a bound contract and gap calls gives one row, not two, and a
 * host serving both REST and MCP gives one REST row and one MCP row — two
 * different things to fix.
 *
 * Single per-call skips (an isError result, a call before tools/list) are
 * NOT their own row when the surface has other checked calls — the count is
 * what nothing checked, not everything that was ever skipped for its own
 * reason. `SKIPPED_PER_CALL_FOOTNOTE` says so once, on the panel.
 */
export function notValidatedItems(
  calls: readonly CoverageCall[],
  specs: readonly CoverageSpec[],
  mcpTools: Readonly<Record<string, readonly McpToolCoverage[]>>,
  names: NotValidatedNames
): NotValidatedItem[] {
  const rest = new Map<string, RestBucket>();
  const mcp = new Map<string, McpBucket>();
  let inboundNotChecked = 0;
  let inboundChecked = 0;

  for (const call of calls) {
    const verdict = callCoverageDetail(call, specs, mcpTools);
    if (verdict.coverage === 'internal') continue;

    if (call.transport === 'mcp') {
      const key = call.integration || '';
      const b = mcp.get(key) || { checked: 0, schemaless: new Map(), skipReasons: new Map(), noVerdict: 0 };
      if (verdict.coverage === 'checked') {
        b.checked++;
      } else if (verdict.reason === 'no-output-contract') {
        const tool = call.mcp_tool_name || 'this tool';
        b.schemaless.set(tool, (b.schemaless.get(tool) || 0) + 1);
      } else if (verdict.reason === 'no-verdict') {
        b.noVerdict++;
      } else if (verdict.reason) {
        b.skipReasons.set(verdict.reason, (b.skipReasons.get(verdict.reason) || 0) + 1);
      }
      mcp.set(key, b);
      continue;
    }

    if (call.direction === 'server') {
      if (verdict.coverage === 'checked') inboundChecked++;
      else if (verdict.coverage === 'not-checked') inboundNotChecked++;
      continue;
    }

    // Outbound REST.
    const host = call.peer_host || '';
    const b = rest.get(host) || { reasons: new Map<NotCheckedReason, number>(), notChecked: 0, checked: 0 };
    if (verdict.coverage === 'checked') {
      b.checked++;
    } else if (verdict.coverage === 'not-checked' && verdict.reason) {
      b.reasons.set(verdict.reason, (b.reasons.get(verdict.reason) || 0) + 1);
      b.notChecked++;
      if (!b.sample) b.sample = { method: call.method, route: call.route };
    }
    rest.set(host, b);
  }

  const items: NotValidatedItem[] = [];

  for (const [host, b] of rest) {
    if (b.notChecked === 0) continue;
    const kind = bestRestKind(new Set(b.reasons.keys()));
    const name = names.restName(host);
    const n = b.notChecked;
    const everyOther = b.checked > 0 ? ` Every other call to ${name} was checked.` : '';
    let why: string;
    let action: NotValidatedAction | undefined;
    switch (kind) {
      case 'rest-contract-not-reached':
        why = `A contract is bound but had not reached the drift check when these calls went through. ${n} call${n === 1 ? '' : 's'} not checked.${everyOther}`;
        break;
      case 'rest-contract-gap': {
        const which = b.sample?.method && b.sample?.route ? `${b.sample.method} ${b.sample.route}` : 'these calls';
        why = `Its contract does not describe ${which}. ${n} call${n === 1 ? '' : 's'} not checked.${everyOther}`;
        action = { label: SHOW_THESE_CALLS_ACTION };
        break;
      }
      case 'no-verdict':
        why = 'Captured by a collector that records no verdict — not checked.';
        break;
      default:
        why = `No contract uploaded. ${n} call${n === 1 ? '' : 's'} not checked.`;
        action = { label: ADD_REST_CONTRACT_ACTION };
    }
    items.push({ kind, group: 'rest', key: host, name, host, why, action });
  }

  if (inboundNotChecked > 0) {
    const everyOther = inboundChecked > 0 ? ` Every other call to your API was checked.` : '';
    items.push({
      kind: 'inbound-no-self',
      group: 'inbound',
      key: '__self__',
      name: YOUR_API_NAME,
      why:
        `No contract you publish is loaded. ${inboundNotChecked} call${inboundNotChecked === 1 ? '' : 's'} not checked. ` +
        `Set ${SELF_SPEC_PATH_HINT} to the OpenAPI document you publish.${everyOther}`,
      action: { label: HOW_TO_ADD_IT_ACTION }
    });
  }

  for (const [integration, b] of mcp) {
    const server = names.mcpServer(integration);
    if (b.schemaless.size > 0) {
      const tools = Array.from(b.schemaless.keys());
      const n = Array.from(b.schemaless.values()).reduce((a, c) => a + c, 0);
      const declareVerb = tools.length === 1 ? 'declares' : 'declare';
      const checkedNote =
        b.checked > 0
          ? // Checked-tool names are not tracked per tool here (mcpTools carries
            // the full row list, not which of them supplied `checked` calls);
            // the honest fallback still says calls elsewhere on this server did
            // get checked, without naming a tool this pass cannot attribute.
            'Other calls to this server were checked.'
          : 'Nothing on this server has been checked.';
      items.push({
        kind: 'mcp-no-output-schema',
        group: 'mcp',
        key: integration,
        name: `${server.name}${server.version ? ' v' + server.version : ''}`,
        host: server.host,
        why: `${tools.join(', ')} ${declareVerb} no output schema. ${n} call${n === 1 ? '' : 's'} not checked. ${checkedNote}`,
        action: { label: VIEW_TOOLS_ACTION }
      });
      continue;
    }
    const skipTotal = Array.from(b.skipReasons.values()).reduce((a, c) => a + c, 0) + b.noVerdict;
    if (b.checked === 0 && skipTotal > 0) {
      let dominant: NotCheckedReason | undefined;
      let max = -1;
      for (const [r, c] of b.skipReasons) if (c > max) (dominant = r), (max = c);
      const phrase = (dominant && MCP_SKIP_PHRASE[dominant]) || 'could not be checked yet';
      items.push({
        kind: 'mcp-nothing-checked',
        group: 'mcp',
        key: integration,
        name: `${server.name}${server.version ? ' v' + server.version : ''}`,
        host: server.host,
        why: `No call to this server has been checked yet: the calls so far ${phrase}.`
      });
    }
    // b.checked > 0 and only per-call skips otherwise: not its own row
    // (SKIPPED_PER_CALL_FOOTNOTE covers it).
  }

  return items;
}
