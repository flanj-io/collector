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
  /** 'external' | 'internal' | 'local-process' | 'unknown' — mirrors the call
   *  field. The only fact that separates two servers publishing the same name.
   *  `unknown` (Python SDK): an MCP server whose transport the SDK never saw. */
  edge_class?: string;
  /** How the contract got here: 'upload' | 'fetched' | 'config' | 'observed'. */
  source?: string;
  /** Where a 'fetched' contract came from. Empty for every other source.
   *  Evidence, not a handle — nothing re-reads it, and nothing re-fetches. */
  source_url?: string;
  /** How the client launched a stdio MCP server: a JSON array STRING,
   *  `[command, ...args]`. Absent on every other row. Local display only. */
  server_command?: string;
  /** The version this one replaced, when it replaced one. */
  prev_version?: string;
  /** When the document this one replaced was loaded. Set on EVERY replace, so
   *  it — not prev_version, which is empty when the replaced document declared
   *  no version — is what says a replace happened. */
  prev_loaded_at?: string;
  /** The stored document's size in bytes, measured by the store at list time.
   *  Absent (or 0) on a collector that predates the field. */
  doc_bytes?: number;
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
/**
 * The MCP contrast, stated as a COMPARISON — the canonical paragraph in the
 * vault's `positioning-2026-09.md` §5, which names this empty state as its
 * highest-leverage single placement: it is where a new operator stands when
 * they wonder what to do next.
 *
 * The power is in the contrast, not the convenience. "Nobody publishes an
 * accurate OpenAPI spec" is the strongest practical objection to the REST half,
 * and it does not apply to MCP at all.
 *
 * THE URL CLAUSE IS RESTORED HERE, and this is the commit that earns it.
 * §5 ends "REST providers need a spec: paste a URL, or upload one", and the doc
 * says in the same breath: restore the clause in the same commit that ships the
 * fetch, and not before. PR #91 shipped it WITHOUT the clause because the
 * collector could not then fetch anything, so the verbatim string would have
 * been a false claim on the one surface whose whole argument is that it does
 * not make them. Ruling R5's fetch ships in this commit
 * (`extension/flanjui/contracts_fetch.go`), so the claim is now true, and
 * `contracts.test.ts` asserts the clause is present rather than absent.
 *
 * If the fetch route is ever removed, this string goes back to the #91 wording
 * in the same commit. The rule is the doc's and it cuts both ways.
 */
export const MCP_NEEDS_NO_SETUP =
  'MCP servers need nothing here — their baseline arrived with the traffic, because tools/list is the contract. A REST provider needs a spec somebody published: paste its URL, or upload the document.';
export const ROLL_CALL_ZERO =
  'No providers checked against a contract yet — upload one to start drift detection on it.';
export const UPLOAD_STAYS_LOCAL = 'Stays on this collector. Uploaded contracts are never sent to Flanj.';
/**
 * The fetch's own privacy line, and the replacement for UPLOAD_NO_URL_FETCH
 * ("this collector never fetches on your behalf"), which was true until this
 * commit and is now retired rather than softened — a line that is no longer
 * true does not get to stay in a gentler form.
 *
 * What it must say, because this is the one moment the operator decides whether
 * to point their collector at somebody else's host: WHO makes the request (this
 * collector, from inside their network, not Flanj), and WHERE the document
 * lands (here, same as an upload). Both halves matter — the second is the
 * promise Settings makes about Connect, and a fetch would look like a breach of
 * it if this line were quiet about it.
 */
export const FETCH_STAYS_LOCAL =
  'This collector makes the request, from your network. The document is stored here, like an upload — Flanj never sees it.';
/** Said once, at the fetch field: nothing re-reads the URL after the bind. The
 *  operator's reasonable assumption about a URL is that it is a subscription,
 *  and it is not one. */
export const FETCH_ONCE_ONLY =
  'Fetched once, now. Nothing re-checks the URL later — replace the contract when you want a newer document.';
export const FETCH_PROMPT = 'Paste the URL of the provider’s OpenAPI document.';
export const FETCH_ACTION = 'Fetch';
export const FETCH_TAB_URL = 'From a URL';
export const FETCH_TAB_FILE = 'From a file';

/**
 * The probe's copy. Every string here is careful to describe an OFFER: the
 * control "looks for" a spec, the results are "found", and a human binds. None
 * of them may ever be rewritten to imply the collector set something up.
 */
export const PROBE_ACTION = 'Look for a published spec';
/** Said above the results, every time, including when there is exactly one.
 *  A single confident-looking result is the case most likely to be taken on
 *  trust, so this is where the sentence is needed most. */
export const PROBE_OFFER_ONLY =
  'Found at the usual paths — nothing is bound yet. Check it’s the right document, then fetch it.';
/** A miss is a RESULT, not a failure: most providers publish at none of these
 *  paths, and an empty panel with no sentence reads as a broken control. */
export const PROBE_NOTHING_FOUND =
  'Nothing at the usual paths. Most providers don’t publish one there — paste the URL if you know it, or upload the document.';
/** Editing the bound host throws away a staged fetch: the document was read and
 *  described against the old host, and binding it to a different one is the
 *  wrong binding this confirm step exists to prevent. */
export const REFETCH_AFTER_HOST_EDIT =
  'The host changed, so that fetch no longer applies. Fetch the document again for this host.';
/** Network-level fallbacks. The server has a sentence for every refusal it can
 *  state; these cover the case where no response arrived to carry one. */
export const FETCH_UNREACHABLE_FALLBACK = 'Couldn’t reach that URL from this collector.';
export const FETCH_BIND_FAILED_FALLBACK = 'Couldn’t bind that document. Nothing was changed.';
export const PROBE_FAILED_FALLBACK = 'Couldn’t look for a spec on that host just now.';

/**
 * Idan, 2026-09-19: a contract that declares no version SAYS so. Every version
 * segment used to be conditional and simply vanished, which reads the same as
 * "this line has no version slot". Empty and whitespace-only count as missing.
 */
export const VERSION_NOT_SPECIFIED = 'version not specified';

/** `v1.0.0`, or `version not specified`. */
export function versionLabel(version?: string): string {
  const v = (version || '').trim();
  return v ? `v${v}` : VERSION_NOT_SPECIFIED;
}

/** One offered candidate, described the way the confirm step describes a
 *  document: what it is, how big, and whether its own `servers:` corroborate
 *  the host — the single most useful "is this the right document?" signal, and
 *  the one an operator taking a suggestion on trust would otherwise skip. */
export function probeCandidateLine(c: {
  title?: string;
  version?: string;
  endpoints: number;
  servers_match: boolean;
}): string {
  const parts = [c.title || 'OpenAPI document', versionLabel(c.version)];
  parts.push(endpointCount(c.endpoints));
  parts.push(c.servers_match ? 'its servers list this host' : 'its servers don’t list this host');
  return parts.join(' · ');
}
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

/* ── A document the contract channel refuses to serve ──────────────────── */

/**
 * The row state for a stored contract past the 8 MB cap.
 *
 * Why the card needs a state at all. `MAX_CONTRACT_BYTES` is refused at BOTH
 * ends of the tiered contract channel — the store pod answers 413 and a front
 * refuses to read a prefix, because a document cut at the cap still parses, as
 * garbage, and would make the front report a PARSE error for a SIZE problem.
 * Neither refusal reaches this card. So the row was listed like any other:
 * heading, format badge, version, tool rows, a full-looking contract — while
 * every call on that edge was stamped `not-validated` with reason `no-contract`
 * on a front that had never managed to read it. Honest per call, and flatly
 * contradicted one panel over.
 *
 * Reachable by exactly one writer: an OBSERVED MCP `tools/list`, which nothing
 * caps on its way in. An upload is refused over the cap before it is stored,
 * and the self contract never crosses this hop.
 *
 * This is NOT a claim about the provider, so none of the banned vocabulary at
 * the top of this file applies to it and none is used: it is a fact about a
 * document sitting in the operator's own store, and about a limit the operator
 * can act on by splitting the server or trimming the catalogue.
 */

/** The chip, in the card's existing row-state vocabulary. */
export const CONTRACT_OVER_CAP_TAG = 'too large to serve';

/** True when this row's stored document is past the cap. Absent size (an older
 *  collector, which does not measure) reads as "not measured", never as over —
 *  the transfer-time refusals still answer for that case, and inventing a
 *  warning from a missing number is the wrong direction to guess in. */
export function contractOverCap(spec: ContractSpec): boolean {
  return (spec.doc_bytes ?? 0) > MAX_CONTRACT_BYTES;
}

/** `9.4 MB` / `812 KB` — sizes an operator compares at a glance. */
export function contractSizeLabel(bytes: number): string {
  if (bytes >= MAX_CONTRACT_BYTES / 8) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

/**
 * How far past the cap, as a phrase. Under a tenth of a megabyte rounds to
 * "0.0 MB over", which reads like a rounding artifact rather than a limit, so
 * that band says it in words instead.
 */
export function contractOverCapBy(bytes: number): string {
  const over = bytes - MAX_CONTRACT_BYTES;
  if (over <= 0) return '';
  if (over < 1024 * 1024 / 10) return 'just over the 8 MB cap';
  return `${(over / (1024 * 1024)).toFixed(1)} MB over the 8 MB cap`;
}

/**
 * The line under the heading: what the document is, and what it costs.
 *
 * The second sentence states BOTH branches, because this surface genuinely
 * cannot know which one is live. Each front keeps its own baseline in memory,
 * and whether a given front has one depends on when it started relative to when
 * the catalogue outgrew the cap — a fact that lives in a different process and
 * differs between fronts of the same deployment. Naming one branch would be a
 * guess rendered as a statement; naming both is the whole truth and is still
 * one sentence long.
 *
 * The closing clause is deliberately word-for-word the shape of
 * `NO_CONTRACT_ROW`, because on a front with no baseline that is exactly the
 * state the calls are in.
 */
export function contractOverCapLine(spec: ContractSpec): string {
  const bytes = spec.doc_bytes ?? 0;
  const what = spec.format === 'mcp' ? 'This tools/list snapshot' : 'This document';
  return (
    `${what} is ${contractSizeLabel(bytes)} — ${contractOverCapBy(bytes)}. ` +
    'Front collectors can’t read it, so each keeps whatever baseline it already had — ' +
    'on a front that has none, calls to this provider are captured, and nothing validates them.'
  );
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
  'Found by comparing this contract with the version it replaced — there’s no failing call to attach, so your message is what the thread carries.';

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
  // 'fetched' is the word the finding and the thread repeat verbatim, so it is
  // the one place all three surfaces agree on what happened.
  if (spec.source === 'fetched') return 'fetched';
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
  const parts = [endpointCount(spec.endpoints), versionLabel(spec.version)];
  parts.push(`${provenanceWord(spec)} ${timeAgo(spec.loaded_at, now)}`);
  const replaced = replacedSegment(spec);
  if (replaced) parts.push(replaced);
  return parts.join(' · ');
}

/** `replaced v1.0.0`; `replaced (version not specified)` when the replaced
 *  document declared none; empty when nothing was replaced. */
function replacedSegment(spec: ContractSpec): string {
  const prev = (spec.prev_version || '').trim();
  if (prev) return `replaced v${prev}`;
  if (spec.prev_loaded_at || spec.prev_version) return `replaced (${VERSION_NOT_SPECIFIED})`;
  return '';
}

/* ── The fetched source line — the point of the whole fetch phase ───────── */

/**
 * "your own published spec at <url>, fetched <when>".
 *
 * This is the SENTENCE the fetch exists to produce, and it is deliberately one
 * function used by all three surfaces — the contract card, the finding, and the
 * flagged thread (`defaultFlagMessage` in threads.ts). Three surfaces phrasing
 * one claim three ways is how a claim stops being checkable.
 *
 * Why it matters more than it looks: an uploaded file's provenance is "somebody
 * here had a file". A provider reading a flagged thread cannot verify that,
 * cannot date it, and cannot tell their own document from an edited copy. A URL
 * they serve, with the moment it was read, is a claim they can check against
 * what they are publishing right now — which is an EVIDENCE-RULE upgrade, not a
 * convenience (architecture.md §3.4, ruling R5).
 *
 * Returns '' for every other source, and the callers render nothing rather than
 * a hedge — an uploaded contract simply has no such line, and inventing one
 * ("uploaded from a file") would be filler dressed as provenance.
 */
export function fetchedSourceLine(spec: ContractSpec | null | undefined, now: number = Date.now()): string {
  if (!hasFetchedSource(spec)) return '';
  return `Fetched from ${spec!.source_url} ${timeAgo(spec!.loaded_at, now)}.`;
}

/**
 * Whether a contract HAS a fetched source — the predicate behind every
 * `v-if` that guards the line above.
 *
 * It exists so a template can ask the question without building the string and
 * throwing it away, and — the reason that matters — so the card, which renders
 * the URL as a real anchor the operator can click (a sentence in a `{{ }}` is
 * not clickable, and an unclickable URL is not a checkable one), asks the SAME
 * question as the surfaces that render the plain sentence. Two predicates is
 * how one surface ends up showing the line and another not.
 */
export function hasFetchedSource(spec: ContractSpec | null | undefined): boolean {
  return !!spec && spec.source === 'fetched' && !!spec.source_url;
}

/**
 * The same fact, written for a STRANGER — the provider reading the thread, who
 * does not know what a collector is and has never seen this UI.
 *
 * Second person and possessive ("your published spec"), because the entire
 * point is that THEY can check it: the document was read from a URL they serve.
 * An absolute date, not "3 days ago" — a thread is read days after it is
 * written, and a relative time silently re-anchors to the reader's now.
 */
export function fetchedSourceForThread(spec: ContractSpec | null | undefined, fmtDate: (iso: string) => string): string {
  if (!hasFetchedSource(spec)) return '';
  const when = fmtDate(spec!.loaded_at || '');
  return `Checked against your published spec at ${spec!.source_url}, fetched ${when}.`;
}

/** The Edges row's third line, in the muted text channel under the host:
 *  `contract v1.0.0 · uploaded 12d ago`, or
 *  `contract · version not specified · uploaded 12d ago`. */
export function edgeContractLine(spec: ContractSpec, now: number = Date.now()): string {
  const label = versionLabel(spec.version);
  const head = label === VERSION_NOT_SPECIFIED ? `contract · ${label}` : `contract ${label}`;
  return `${head} · ${provenanceWord(spec)} ${timeAgo(spec.loaded_at, now)}`;
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

/** The origin of a server whose transport the SDK never saw (edge class `unknown`). */
export const ORIGIN_UNKNOWN = 'location unknown';

/**
 * WHERE this contract's counterparty is: the host for a network transport, the
 * literal word `stdio` for a local process, and `location unknown` when the SDK
 * never saw the transport (edge class `unknown`, Python SDK) — its peer_host is
 * then the server's own serverInfo.name, and printing that as a place would
 * repeat the name and claim a location nobody observed.
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
  if (spec.edge_class === 'unknown') return ORIGIN_UNKNOWN;
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

/* ── The provider a finding is about ───────────────────────────────────── */

/** Title-case an INTEGRATION SLUG (`acme-payments` → `Acme Payments`): the same
 *  rule the relay applies server-side. Splits on `-` / `_` only, never on
 *  dots — it is for slugs, and a host run through it (`api.acme.test` →
 *  `Api.acme.test`) is a mangled pseudo-name, which is exactly what
 *  providerNameForFinding exists to prevent. */
export function humanize(id: string): string {
  return id
    .split(/[-_]+/)
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join(' ');
}

/** The edge row shape the provider lookup reads (`GET /api/edges`). */
export interface ProviderEdge {
  peer_host: string;
  display_name?: string;
}

/**
 * The name the flag sheet addresses and the flag sends for a finding — the
 * "New thread with <name>" title, the default message, the paste text.
 *
 * Two kinds of `integration` reach this from `GET /api/findings`:
 *
 *   - a call-evidenced finding (live-vs-spec, the MCP kinds) carries the SDK's
 *     own integration id (`acme-payments`, `acme-tools`) — a slug the operator
 *     chose, which humanizes honestly and which the relay humanizes the same
 *     way;
 *   - a version-diff carries the CONTRACT's id, and an uploaded contract is
 *     keyed by the host it was bound to (`api-acme-test` for `api.acme.test`) —
 *     a slug nobody chose. humanize() turned it into `Api Acme Test`, a
 *     title-cased pseudo-name the Edges panel already forbids (its unnamed rows
 *     show the host itself), and the sheet pasted it to the other organization
 *     while the card above read `Acme Payments API` (QA 2026-09-14).
 *
 * So a version-diff resolves through the contract it came from, to the host
 * that contract is bound to, and takes — in order — the Edges panel's display
 * name for that host, the contract's own title, then the host itself. Never a
 * humanized host slug. The configured `provider_display_name` still wins for
 * the integration it names, as before.
 */
export function providerNameForFinding(
  f: { kind: string; integration: string; peer_host?: string },
  ctx: {
    providerDisplayName?: string;
    contracts: readonly ContractSpec[];
    edges: readonly ProviderEdge[];
  }
): string {
  // The configured `provider_display_name` is a REST provider's name — the v0 one-provider shape —
  // and it names ONLY a call-evidenced REST finding. Never an MCP server, which names itself through
  // its tools/list (`New thread with Acme Tools`, pinned by e2e), and never a version diff, which
  // resolves through its contract below. Until 2026-09-14 the scope was the config `integration_id`
  // (the name applied to findings under that slug alone); with the key gone, the KIND is the scope.
  if (ctx.providerDisplayName && f.kind === 'live-vs-spec') {
    return ctx.providerDisplayName;
  }
  if (f.kind === 'version-diff') {
    const spec = ctx.contracts.find((s) => s.integration === f.integration);
    const host = spec?.peer_host || f.peer_host || '';
    const edge = host ? ctx.edges.find((e) => e.peer_host === host) : undefined;
    const named = edge?.display_name || spec?.title || host;
    if (named) return named;
    return f.integration || 'the provider';
  }
  return humanize(f.integration) || f.integration || 'the provider';
}
