// Contract COVERAGE as the operator reads it — the roll call, the per-row
// meta line, and the provenance a card shows.
//
// Coverage answers "is a contract bound to this edge?" and lives here.
// `coverage.ts` answers "did this call run against one?" and lives there. The
// split is deliberate and every string below obeys it:
//
//   checked   — a contract is bound to this host  (a property of the EDGE)
//   validated — a call actually ran against it    (a property of the CALL)
//
// Banned outright from every string in this file: missing, uncovered,
// unprotected, gap, stale, out of date, outdated, new version available. They
// all assert something about the PROVIDER's behaviour from a fact about the
// operator's own file, and a localhost debugging tool has no business nagging.

import { timeAgo } from './threads';

/** The contract row shape this module reads (spec_infos, `GET /api/contracts`). */
export interface ContractSpec {
  integration: string;
  role?: 'provider' | 'self';
  peer_host?: string;
  format?: string;
  title?: string;
  version?: string;
  endpoints?: number;
  loaded_at?: string;
  /** How the contract got here: 'upload' | 'config' | 'observed'. */
  source?: string;
  /** The version this one replaced, when it replaced one. */
  prev_version?: string;
}

/** The minimum an edge row needs to be placed on the roll call. */
export interface CoverageEdge {
  peer_host: string;
  direction?: string;
}

/* ── Copy deck ─────────────────────────────────────────────────────────── */

export const ADD_CONTRACT = 'Add contract';
export const REPLACE_CONTRACT = 'Replace';
export const MCP_SELF_REPORTS = 'An MCP server — its tools/list is the contract.';
/** On a single provider's card. */
export const NO_CONTRACT_ROW =
  'No contract for this provider — its calls are captured, but nothing validates them.';
/** Heading the collapsed section, where it covers many providers at once. Said
 *  ONCE there: at 28 rows the per-row version was its own wall. */
export const NO_CONTRACT_SECTION =
  'These providers have traffic but no contract — their calls are captured, and nothing validates them.';
export const ROLL_CALL_ZERO =
  'No providers checked against a contract yet — upload one to start drift detection on it.';
export const UPLOAD_STAYS_LOCAL = 'Stays on this collector. Uploaded contracts are never sent to Flanj.';
export const UPLOAD_NO_URL_FETCH =
  'Spec lives at a URL? Download it and drop the file — this collector never fetches on your behalf.';
export const UPLOAD_PROMPT = 'Drop the provider’s OpenAPI document here, or choose a file.';
export const UPLOAD_FORMATS = 'JSON or YAML.';
export const UPLOAD_TAKES_EFFECT = 'Validating from now on. Calls already captured aren’t re-checked.';
export const BIND_ANYWAY = 'Bind anyway';

/** Pre-traffic upload is legitimate — on a fresh install there are no edges at
 *  all — so this explains rather than warns. */
export function noTrafficYet(host: string): string {
  return `No calls to ${host} yet — this contract starts validating when traffic arrives.`;
}

/** `servers:` CORROBORATES a binding and never decides it: proxy, gateway and
 *  staging hosts are legitimate and common, so a mismatch warns and the upload
 *  still goes through. */
export function serversLine(servers: readonly string[], host: string): string {
  if (!servers.length) return '';
  const list = servers.join(', ');
  return servers.some((s) => s.toLowerCase() === host.toLowerCase())
    ? `This spec’s servers: list ${list} — matches this edge.`
    : `This spec’s servers: list ${list}. You’re binding it to ${host}.`;
}

/* ── The card's provenance + recency line ──────────────────────────────── */

/** The provenance word tracks the SOURCE, so which contract is live is legible
 *  on sight: an uploaded document reads "uploaded", a config-loaded one keeps
 *  "loaded", and an MCP snapshot was observed, not put there by anyone. */
export function provenanceWord(spec: ContractSpec): string {
  if (spec.format === 'mcp') return 'observed';
  if (spec.source === 'config') return 'loaded';
  if (spec.source === 'upload') return 'uploaded';
  // Rows written before provenance was recorded: a provider contract can only
  // have been uploaded, and a self contract can only have come from config.
  return spec.role === 'self' ? 'loaded' : 'uploaded';
}

/** "1 endpoint" / "4 endpoints" — the REST branch used to say "1 endpoints". */
export function endpointCount(n?: number): string {
  const count = n ?? 0;
  return `${count} ${count === 1 ? 'endpoint' : 'endpoints'}`;
}

/**
 * The card's meta line: what this contract is, and when it got here.
 *
 * Relative time, deliberately, and NOTHING else about age. No threshold, no
 * amber, no dot, and contract age never feeds the tab's red/amber counters —
 * those count what the PROVIDER did, and "your file is old" does not belong in
 * the same severity class as "the provider changed something". The slot is
 * designed as provenance + recency rather than "upload date" so the deferred
 * control-plane freshness line is a copy swap here, not a re-layout.
 */
export function contractMeta(spec: ContractSpec, now: number = Date.now()): string {
  const parts = [endpointCount(spec.endpoints)];
  if (spec.version) parts.push(`v${spec.version}`);
  parts.push(`${provenanceWord(spec)} ${timeAgo(spec.loaded_at, now)}`);
  if (spec.prev_version) parts.push(`replaced v${spec.prev_version}`);
  return parts.join(' · ');
}

/** The Edges row's third line, in the muted text channel under the host:
 *  `contract v1.0.0 · uploaded 12d ago`. */
export function edgeContractLine(spec: ContractSpec, now: number = Date.now()): string {
  const version = spec.version ? ` v${spec.version}` : '';
  return `contract${version} · ${provenanceWord(spec)} ${timeAgo(spec.loaded_at, now)}`;
}

/* ── The roll call ─────────────────────────────────────────────────────── */

/** Provider contracts indexed by the host each is bound to. Unbound rows are
 *  skipped: binding is mandatory at upload, and a row with no host validates
 *  nothing, so counting it as coverage would restate the lie this whole
 *  surface exists to kill. */
export function contractsByHost(specs: readonly ContractSpec[]): Map<string, ContractSpec> {
  const out = new Map<string, ContractSpec>();
  for (const s of specs) {
    if (s.role === 'self' || s.format === 'mcp' || !s.peer_host) continue;
    out.set(s.peer_host, s);
  }
  return out;
}

/** Outbound providers with no contract bound, in the order the edges arrived.
 *  MCP hosts are excluded — their tools/list IS the contract, so listing them
 *  as needing one would invent a job that does not exist. */
export function uncoveredProviders(
  edges: readonly CoverageEdge[],
  specs: readonly ContractSpec[],
  mcpHosts: ReadonlySet<string>
): string[] {
  const covered = contractsByHost(specs);
  const out: string[] = [];
  const seen = new Set<string>();
  for (const e of edges) {
    if (e.direction && e.direction !== 'client') continue;
    if (covered.has(e.peer_host) || mcpHosts.has(e.peer_host) || seen.has(e.peer_host)) continue;
    seen.add(e.peer_host);
    out.push(e.peer_host);
  }
  return out;
}

/**
 * The outbound group's sub-line — the roll call.
 *
 * Counted POSITIVE, never negative, one line for the whole panel, and it sits
 * below a fully rendered graph: the zero-config install-to-graph moment is
 * untouched and nothing is gated on it.
 */
export function rollCall(
  edges: readonly CoverageEdge[],
  specs: readonly ContractSpec[],
  mcpHosts: ReadonlySet<string>
): string {
  const outbound = edges.filter((e) => !e.direction || e.direction === 'client');
  const hosts = new Set(outbound.map((e) => e.peer_host));
  const covered = contractsByHost(specs);
  let checked = 0;
  let mcp = 0;
  for (const host of hosts) {
    if (mcpHosts.has(host)) mcp++;
    else if (covered.has(host)) checked++;
  }
  const rest = hosts.size - mcp;

  if (checked === 0 && mcp === 0) return ROLL_CALL_ZERO;

  const parts: string[] = [];
  if (rest > 0) {
    parts.push(`${checked} of ${rest} ${rest === 1 ? 'provider' : 'providers'} checked against a contract`);
  }
  if (mcp > 0) {
    parts.push(`${mcp} MCP ${mcp === 1 ? 'server self-reports theirs' : 'servers self-report theirs'}`);
  }
  return parts.join(' · ');
}

/** Providers with no contract (9) — the collapsed section's heading. */
export function uncoveredHeading(n: number): string {
  return `Providers with no contract (${n})`;
}

/* ── Evidence, per card ────────────────────────────────────────────────── */

/**
 * Does this validated call count as evidence for THIS card?
 *
 * The chip on a card says "conforming" only once at least one call actually ran
 * against that contract, and this is what "against that contract" means.
 * Getting it wrong reintroduces the exact lie the gate was added to kill, one
 * card over: a SELF card carries no peer_host, so a rule that only filters by
 * host counts every outbound call validated against somebody else's contract
 * and reports the org's own API conforming on evidence nobody gathered about
 * it.
 *
 * Mirrors processor/flanjdrift/processor.go:
 *   self card     ← INBOUND calls only (our responses, our own contract)
 *   provider card ← OUTBOUND calls to the host it is bound to
 */
export function isEvidenceFor(
  call: { peer_host?: string; direction?: string },
  card: { peerHost: string; isSelf: boolean }
): boolean {
  if (card.isSelf) return call.direction === 'server';
  if (!card.peerHost) return false;
  return call.direction !== 'server' && call.peer_host === card.peerHost;
}
