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
  /** 'external' | 'internal' | 'local-process' — mirrors the call field. The
   *  only fact that separates two servers publishing the same name. */
  edge_class?: string;
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

/**
 * The document cap, in bytes — `maxDocBytes` in
 * `extension/flanjui/contracts_upload.go`, byte for byte.
 *
 * The uploader checks the picked file against this BEFORE it reads or sends
 * anything, because the server's own refusal is not reliably deliverable. The
 * relay answers 413 through `http.MaxBytesReader`, which half-closes and gives
 * the client about half a second to notice; a browser still streaming a 20 MB
 * body past that sees a connection reset instead, `apiPost` rejects with a
 * network error rather than an `ApiError`, and the uploader falls back to
 * "Couldn't read that document." — a parse verdict for a size problem, which is
 * the wrong-diagnosis class the 413 existed to end.
 */
export const MAX_CONTRACT_BYTES = 8 * 1024 * 1024;

/**
 * The relay's own sentence for an oversized document
 * (`extension/flanjui/messages.go` → `msgContractTooLarge`), mirrored so the
 * local refusal above and the server's 413 read identically — the operator must
 * not be able to tell which end answered. Keep the two byte-identical;
 * `TestContractTooLargeMirrorInSync` fails if they drift.
 */
export const CONTRACT_TOO_LARGE =
  "That document is larger than 8 MB. Contracts this size are usually a bundle — upload the API's own document.";

/** True when a picked file is past the cap the server enforces. */
export function contractFileTooLarge(size: number): boolean {
  return size > MAX_CONTRACT_BYTES;
}

/**
 * Asked before one uploader is swapped for another.
 *
 * There is exactly one uploader open at a time, mounted on whichever row opened
 * it, so clicking Add contract or Replace anywhere else unmounted the first —
 * taking the document it had read and the confirm step on screen with it,
 * without a word. The operator's own click caused it, which is what made it
 * read as the app losing their work rather than as a choice they made.
 */
export const UPLOADER_DISCARD_CONFIRM =
  'You have a contract waiting to be confirmed. Opening another discards it — continue?';

/**
 * Why a version-diff row carries no Flag control.
 *
 * It used to read "Informational — spec-version findings have no failing call
 * to share", which contradicted the row it sat under: the model assigns these
 * findings `severity: breaking` (CONTRACTS §4, oasdiff Level=ERR), the badge
 * beside this line says `breaking`, and the red tab pill counts them. Calling
 * the same row informational in the footer told the operator the pill above was
 * wrong.
 *
 * What is actually true is the SECOND half: there is no failing call, because
 * the change was found by comparing two documents. So say that, and nothing
 * about severity.
 */
export const VERSION_DIFF_NO_CALL =
  'Found by comparing this contract with the version it replaced — there’s no failing call to attach, so it can’t be flagged from here.';

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
 *  "loaded", and an MCP snapshot was observed, not put there by anyone.
 *
 *  `source` is the store's word and it wins. The format and role branches are
 *  FALLBACKS for rows with no recorded provenance, and they must stay behind
 *  `source`: while the format branch ran first, every observed snapshot was
 *  stored and served as `config` and this card — the one place that would
 *  have shown it — said "observed" regardless (2026-09-07). */
export function provenanceWord(spec: ContractSpec): string {
  if (spec.source === 'observed') return 'observed';
  if (spec.source === 'config') return 'loaded';
  if (spec.source === 'upload') return 'uploaded';
  // Fallbacks, for rows written before provenance was recorded: an MCP
  // snapshot can only have been observed, a provider contract can only have
  // been uploaded, and a self contract can only have come from config.
  if (spec.format === 'mcp') return 'observed';
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
  for (const host of hosts) {
    if (!mcpHosts.has(host) && covered.has(host)) checked++;
  }
  const rest = [...hosts].filter((h) => !mcpHosts.has(h)).length;

  // MCP servers are counted from the CONTRACTS, not from the edges.
  //
  // Counting them among outbound edge hosts structurally could not see a stdio
  // server: it is `local-process`, and GET /api/edges is external-only. So
  // Overview said "1 MCP server self-reports theirs" while the Contracts tab
  // showed two cards — the same disagreement between two surfaces that this
  // whole roll call exists to prevent. The `N of M providers` clause keeps its
  // edge-based count, which is right: a REST provider with no traffic is
  // genuinely not on the roll call yet.
  const mcp = specs.filter((s) => s.format === 'mcp' && s.role !== 'self').length;

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

/* ── Binding confidence ────────────────────────────────────────────────── */

/**
 * Does this look like a host traffic could actually carry?
 *
 * NOT a validity check, and deliberately not a refusal: `localhost` and
 * single-label internal service names are real peer hosts and must stay
 * bindable. But a bare word like `sad` is almost always a typo, and binding a
 * contract to it validates nothing FOREVER while the card shows a loaded
 * contract — the exact silent failure mandatory binding exists to prevent.
 *
 * So this drives a warning, never a block.
 */
export function hostLooksRoutable(host: string): boolean {
  const h = host.trim().toLowerCase();
  if (!h) return false;
  // Dotted name (api.acme.test), IPv4, or a known bare host.
  if (h.includes('.')) return true;
  return h === 'localhost';
}

/** One thing worth checking before an upload binds. */
export interface BindingCheck {
  /** 'ok' reassures; 'warn' is a reason to look again — never a refusal. */
  level: 'ok' | 'warn';
  text: string;
}

/**
 * The confirm step's checklist: everything knowable about whether this binding
 * is the one the operator meant.
 *
 * Every warn is survivable — proxy, gateway and staging hosts legitimately
 * mismatch `servers:`. The point is that the operator SEES the signals together
 * instead of one whispered line, because a typo'd host trips both at once and
 * that pattern is unmistakable.
 *
 * Traffic state is deliberately NOT here — see bindingTiming.
 */
export function bindingChecks(host: string, servers: readonly string[]): BindingCheck[] {
  const checks: BindingCheck[] = [];

  if (!hostLooksRoutable(host)) {
    checks.push({
      level: 'warn',
      text: `“${host}” doesn’t look like a host your traffic would carry — check for a typo.`
    });
  }

  if (servers.length) {
    checks.push(
      servers.some((s) => s.toLowerCase() === host.trim().toLowerCase())
        ? { level: 'ok', text: `This spec’s servers: list ${servers.join(', ')} — matches this edge.` }
        : {
            level: 'warn',
            text: `This spec’s servers: list ${servers.join(', ')}. You’re binding it to ${host}.`
          }
    );
  }

  // Traffic state is INFORMATIONAL, never a check. Neither answer is a problem:
  // calls already flowing is the normal case and needs no remark at all, and a
  // host with no traffic yet is a legitimate pre-traffic upload — the whole
  // state of a fresh install. Scoring either as something to weigh made the
  // list cry wolf, which is how a real warning gets ignored.
  return checks;
}

/** The one-line note under the checks: what happens next, stated plainly. Not a
 *  check, because there is nothing here to get wrong. */
export function bindingTiming(host: string, hasTraffic: boolean): string {
  return hasTraffic
    ? `Calls to ${host} are already being captured — validation starts on the next one.`
    : noTrafficYet(host);
}

/** True when anything on the checklist wants a second look. */
export function hasBindingWarning(checks: readonly BindingCheck[]): boolean {
  return checks.some((c) => c.level === 'warn');
}

/* ── Identity ──────────────────────────────────────────────────────────── */

/**
 * WHERE this contract's counterparty is: the host for a network transport, the
 * literal word `stdio` for a local process.
 *
 * Exists because two MCP servers can publish the same `serverInfo.name` — the
 * live stack has exactly that — and the name is the only thing the card
 * rendered at heading weight. The integration slug was supposed to be the
 * tiebreak, but `acme-tools` vs `acme-tools-stdio` differ by a trailing suffix
 * on a muted 0.78rem mono string, which is where a reader compares rather than
 * distinguishes.
 */
export function contractOrigin(spec: ContractSpec): string {
  // The SELF contract describes THIS org's own API. It has no counterparty, so
  // an origin would be inventing one — it keeps a bare title.
  if (spec.role === 'self') return '';
  if (spec.edge_class === 'local-process') return 'stdio';
  return spec.peer_host || spec.integration || '';
}

/**
 * The card heading: `<name> · <origin>`, ALWAYS — not only on collision.
 *
 * Collision-conditional would change a card's shape when an unrelated second
 * server appears, and at fifty cards nobody can see whether a title is unique,
 * so a bare name could never be trusted to mean "the only one".
 */
export function contractHeading(spec: ContractSpec): string {
  const name = spec.title || spec.integration;
  const origin = contractOrigin(spec);
  return origin ? `${name} · ${origin}` : name;
}

/* ── Joining findings to the contract that produced them ───────────────── */

/** The minimum a finding needs to be attributed to a contract. */
export interface AttributableFinding {
  integration: string;
  source_call_id?: string | null;
  /** The host of the finding's source call, pinned server-side by
   *  GET /api/findings. Present whenever the store still holds that call —
   *  which is whenever a finding exists, since a finding pins its evidence. */
  peer_host?: string;
}

/**
 * Does this finding belong to this contract?
 *
 * HOST FIRST, integration second — and the order is the whole point.
 *
 * An uploaded contract's integration id is DERIVED from the host it binds to
 * (`api.acme.test` → `api-acme-test`), because the operator is never asked for
 * one. A finding's integration comes from the CALL, stamped by the SDK
 * (`acme-payments`). Those two are unrelated strings for the same provider, so
 * an integration-only join split one provider into two cards: the contract card
 * claiming CONFORMING, and beside it a second card carrying the BREAKING
 * finding under "No contract for this provider" — denying the contract while
 * rendering a verdict only that contract could produce.
 *
 * The host is what they genuinely share. A finding reaches it through its
 * source call. Integration stays as the fallback for CALL-LESS findings
 * (version-diff on replace, MCP definition_change), which have no call to
 * resolve a host from and DO carry the contract's own integration.
 *
 * THE HOST COMES FROM THE ROW, not from the calls page. `finding.peer_host` is
 * decorated server-side from the pinned source call; `hostOfCall` reads
 * GET /api/calls, which returns the 200 NEWEST rows. source_call_id is frozen
 * at the first occurrence, so under the lookup alone the join died as soon as
 * the evidence call aged out of that page — minutes of ordinary traffic — and
 * the provider split back into two cards (postgres lane, 2026-09-02). The
 * lookup stays as the fallback for a row from a collector that predates the
 * field.
 */
export function findingBelongsToContract(
  finding: AttributableFinding,
  spec: ContractSpec,
  hostOfCall: (callId: string) => string | undefined
): boolean {
  const callHost =
    finding.peer_host || (finding.source_call_id ? hostOfCall(finding.source_call_id) : undefined);
  if (callHost && spec.peer_host) return callHost === spec.peer_host;
  return finding.integration === spec.integration;
}

/**
 * The "Provider contracts" empty state, which has TWO branches because the
 * instruction differs.
 *
 * BUG (postgres-lane QA walk, 2026-09-01): the single string ended "— or send
 * traffic through the SDK to discover providers first", and stayed on screen
 * after discovery had happened, directly above a section headed "Providers with
 * no contract (2)". The page was instructing a step the operator had already
 * completed, one line above the proof they had completed it.
 *
 * Once providers ARE discovered the only remaining action is the upload, so the
 * copy names it and points at the list below instead of asking for traffic.
 */
export function providerContractsEmptyText(uncoveredCount: number): string {
  if (uncoveredCount > 0) {
    return `No provider contracts yet. Upload a document for any of the ${uncoveredCount} ${
      uncoveredCount === 1 ? 'provider' : 'providers'
    } below to start validating your calls to ${uncoveredCount === 1 ? 'it' : 'them'}.`;
  }
  return 'No provider contracts yet. Upload a provider’s OpenAPI document to start validating your calls to it — or send traffic through the SDK to discover providers first.';
}
