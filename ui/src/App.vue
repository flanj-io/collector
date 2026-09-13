<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { ApiError, apiGet, apiPost, openThreadInNewTab } from './api';
import ConnectPanel from './ConnectPanel.vue';
import ContractUploader from './ContractUploader.vue';
import FlagSheet from './FlagSheet.vue';
import ThreadsTab from './ThreadsTab.vue';
import {
  THREADS_NOT_CONNECTED_NOTICE,
  THREAD_STATE_UNKNOWN,
  cannotListThreads,
  chipLabel,
  needsCollectorAddress,
  timeAgo,
  type ConnectState,
  type ThreadListResponse,
  type ThreadRow
} from './threads';
import { hashForTab, hashForThread, routeFromHash, type Tab } from './route';
import {
  ADD_CONTRACT,
  CONTRACT_OVER_CAP_TAG,
  MCP_SELF_REPORTS,
  NO_CONTRACT_ROW,
  NO_CONTRACT_SECTION,
  REPLACE_CONTRACT,
  UPLOADER_DISCARD_CONFIRM,
  VERSION_DIFF_NO_CALL,
  contractOverCap,
  contractOverCapLine,
  contractMeta,
  contractOrigin,
  provenanceWord,
  findingBelongsToContract,
  contractsByHost,
  edgeContractLine,
  humanize,
  isEvidenceFor,
  providerNameForFinding,
  rollCall,
  uncoveredHeading,
  uncoveredProviders,
  providerContractsEmptyText
} from './contracts';
import {
  THEME_FLIP_NOTICE_KEY,
  applyTheme,
  hasStoredThemeChoice,
  loadThemePref,
  saveThemePref,
  shouldShowThemeFlipNotice,
  type ThemePref
} from './theme';
import {
  ACK_LABEL,
  ACK_TITLE,
  JSONRPC_ID_TITLE,
  LOCAL_NOTICES_TITLE,
  MCP_BADGE_TOOLTIP,
  MCP_ERROR_TOOLTIP,
  MCP_NO_SPEC_NEEDED,
  MCP_TOOL_CHIP,
  UNDO_LABEL,
  UNDO_TITLE,
  ackedLine,
  afterColLabel,
  beforeColLabel,
  breakingChipLabel,
  breakingCountTitle,
  defChangeDetail,
  defChangeNoCallSub,
  definitionClass,
  descriptionChipLabel,
  descriptionChipTitle,
  descriptionCountTitle,
  informationalChipLabel,
  informationalChipTitle,
  informationalCountTitle,
  isAckable,
  isAcked,
  isBreakingFinding,
  isLocalNotice,
  isMcpCall,
  isMcpFinding,
  localNoticesSubFor,
  mcpBadgeLabel,
  mcpContractMeta,
  mcpHeadline,
  mcpStatusLabel,
  methodFacetOf,
  noOutputContractNote,
  noticeLine,
  parseToolRows,
  snapshotTimes,
  statusFilterMatches,
  toolContractLabel,
  toolNameOf,
  type McpToolRow
} from './mcp';
import {
  CANCEL_LABEL,
  REMOVE_NAME_LABEL,
  RETRY_LABEL,
  SAVE_ERROR,
  SAVE_LABEL,
  SUGGEST_TOO_LONG,
  beginEdit,
  cancelEdit,
  editorClosed,
  placeholderFor,
  renameLabel,
  saveFailed,
  saveStart,
  suggestLabelFor,
  suggestTooLong,
  toggleSuggest,
  typeDraft,
  START_THREAD_LABEL,
  type EdgeNameEdit
} from './edge-names';
import {
  callCoverageDetail,
  notCheckedTitle,
  validatedCallsMeta,
  NOT_CHECKED_LABEL,
  SINCE_LOAD,
  SINCE_SNAPSHOT,
  SINCE_UPLOAD,
  type Coverage,
  type CoverageVerdict
} from './coverage';
import { headlineFor } from './headline';
import { isoStamp } from './time';
import type { Correlation, Finding, FlagResult, Health, RedactedCall } from './types';

interface Edge {
  peer_host: string;
  direction: 'client' | 'server';
  role: 'consumer' | 'provider';
  class: string;
  first_seen: string;
  last_seen: string;
  call_count: number;
  drift_count: number;
  rpm?: number;
  /** v1p1 edge naming (outbound rows resolve a name; inbound stay auto). */
  registrable_domain?: string;
  display_name?: string;
  name_source?: string;
}
interface SpecInfo {
  integration: string;
  // "provider" = a contract we consume (validates outbound calls);
  // "self" = the contract WE publish (validates inbound responses).
  role?: 'provider' | 'self';
  peer_host?: string;
  format: string;
  title?: string;
  version?: string;
  docs_url?: string;
  endpoints?: number;
  loaded_at: string;
  /** "external" | "internal" | "local-process" — mirrors the call field. The
   *  only fact separating two servers that publish the same name; a
   *  local-process (stdio) server has no edge row to look it up from. */
  edge_class?: string;
  /** How this contract got here: "upload" (the UI), "config" (a mounted
   *  self_spec_path) or "observed" (an MCP tools/list). The card's provenance
   *  word tracks it, and only an uploaded contract offers Replace / Remove.
   *  Absent on rows written before provenance was recorded. */
  source?: string;
  /** The version this contract replaced, when it replaced one. */
  prev_version?: string;
}

const health = ref<Health | null>(null);
const findings = ref<Finding[]>([]);
const calls = ref<RedactedCall[]>([]);
const edges = ref<Edge[]>([]);
const contracts = ref<SpecInfo[]>([]);
// True once /api/contracts has answered OK — older collectors lack the endpoint,
// and only a real answer lets us assert "no contract loaded" per provider.
const contractsKnown = ref(false);
/** The relay itself was unreachable — no response to quote. Shared by the poll
 *  banner and the per-finding ack error so one failure never wears two names. */
const COLLECTOR_UNREACHABLE = 'Could not reach this collector.';
const loadError = ref('');
// ─── Connect + threads (v0.1a) ───────────────────────────────────────────
// Connect state comes from the relay (`GET /api/connect`, refreshed from the CP);
// polled every 5s while a confirmation is pending (or the Settings tab / a Flag
// sheet is open) and on focus, so "Create thread" unlocks the moment the
// contact clicks the confirmation. Threads come from `GET /api/threads` — one
// relayed call to the control plane's §5.5a list, most-recently-active first,
// with the collector's local join fields merged on — and feed both the Threads
// tab and the finding chips.
const connect = ref<ConnectState | null>(null);
const threads = ref<ThreadRow[]>([]);
const threadsLoaded = ref(false);
const threadsError = ref('');
// The relay's line when it cannot list at all (not connected / no control plane
// configured) — shown instead of an empty state, never as an error.
const threadsNotice = ref('');
// threadsKnown: the thread list has been ANSWERED at least once (a list, or the
// relay saying it cannot list). Until then a finding's thread state is unknown,
// and unknown must not render as "no thread" — that would offer Create thread
// for a thread that already exists. A later failure never clears it, and never
// clears the rows either: a stale chip beats a wrong one.
const threadsKnown = ref(false);
const threadsTotal = ref(0);
const threadsHasMore = ref(false);
const sheetFinding = ref<Finding | null>(null);
/** v1 phase 4 — the edge "Start a thread" was pressed on. Mutually exclusive
 *  with `sheetFinding`: the sheet reads QUESTION mode off the missing finding. */
const sheetEdge = ref<{ host: string; domain: string; name: string } | null>(null);
const sheetOpen = computed(() => sheetFinding.value !== null || sheetEdge.value !== null);
const highlightThreadId = ref<string | null>(null);
// `#contracts/<finding_id>` deep link IN (the control plane's findings index
// links here): the Contracts tab opens with that finding's row highlighted and
// scrolled into view — the mirror of the Threads tab's `#threads/<id>`.
const highlightFindingId = ref<string | null>(null);
/** The uncovered-provider row the Edges panel routed to, so the operator lands
 *  on the row they clicked rather than the top of a collapsed section. */
const highlightUncoveredHost = ref<string | null>(null);
const connectBannerDismissed = ref(localStorage.getItem('flanj.connect.banner.dismissed') === '1');
// Post-Connect nudge (v0.1b): Connected but no collector address yet — email
// links can't deep-link back here. One dismissible line on the Connect panel
// and the Threads tab; the dismissal is remembered (shared by both).
const addressNudgeDismissed = ref(localStorage.getItem('flanj.address.nudge.dismissed') === '1');
const focusAddressTick = ref(0);

const connectStatus = computed(() => connect.value?.status ?? 'disconnected');
const connectPill = computed(() => {
  switch (connectStatus.value) {
    case 'connected':
      return 'Connected';
    case 'pending':
      return 'Confirm your contact';
    default:
      return 'Not connected';
  }
});
/** The CP dashboard link, emitted by /api/connect ONLY while Connected and
 *  only when the collector holds an address a BROWSER can open. When absent the
 *  pill stays a button into Settings — the door is never offered to a control
 *  plane this collector has no identity at, nor to one this browser cannot
 *  reach (the collector's own in-network address is not a link for us). */
const dashboardUrl = computed(() => connect.value?.dashboard_url || '');

const threadsByFinding = computed(() => {
  const m: Record<string, ThreadRow> = {};
  for (const t of threads.value) m[t.finding_id] = t;
  return m;
});
const consumerName = computed(() => connect.value?.consumer_display_name || health.value?.consumer_display_name || 'Your organization');
// Header org pill: the org identity ONLY — when no display name is configured
// or Connected, no pill renders. Never the integration slug (a spec-scoping
// label, not an identity; it stays on the Overview headline + its Contracts card).
const orgPillName = computed(() => connect.value?.consumer_display_name || health.value?.consumer_display_name || '');
// The sheet header's address line: where THIS page is served from. Read once —
// the origin cannot change under a mounted app.
const uiHost = window.location.host;
// Hex-bolt tones (Blueprint). A bolt is a mark, so the class binds the bare
// family colour plus its -bolt ring: green only for a reached verdict, red for
// drift, the accent for an attention state, steel for a neutral one.
const pillTone = computed(() =>
  connectStatus.value === 'connected' ? 'tone-ok' : connectStatus.value === 'pending' ? 'tone-accent' : 'tone-info'
);
function toneClass(tone: 'drift' | 'ok' | 'neutral'): string {
  return tone === 'drift' ? 'tone-breaking' : tone === 'ok' ? 'tone-ok' : 'tone-info';
}
/** The bolt inside a severity chip follows the chip's tier; a DESCRIPTION
 *  (wording) change and the info tier are both steel. */
function badgeTone(f: Finding): string {
  if (f.kind === 'definition_change') {
    const c = definitionClass(f);
    return c === 'BREAKING' ? 'tone-breaking' : c === 'NON-BREAKING' ? 'tone-warning' : 'tone-info';
  }
  return f.severity === 'breaking' ? 'tone-breaking' : f.severity === 'warning' ? 'tone-warning' : 'tone-info';
}
/** A thread chip lifts to the accent when the turn is ours to act on. */
function chipAttention(row: ThreadRow): boolean {
  return row.summary?.turn === 'fix_reported' || row.summary?.turn === 'replied_while_closed';
}

// One in-flight `GET /api/connect` at a time. loadThreads now waits on the
// connect answer before deciding whether the list is askable at all, and mount
// and focus each call both — without the dedupe that would be two identical
// requests for one page load.
let connectInFlight: Promise<void> | null = null;

function loadConnect(): Promise<void> {
  if (!connectInFlight) {
    connectInFlight = apiGet<ConnectState>('/api/connect')
      .then((s) => {
        connect.value = s;
      })
      .catch(() => {
        /* keep the last known state; the health poll still reports the store's view */
      })
      .finally(() => {
        connectInFlight = null;
      });
  }
  return connectInFlight;
}

// The list is the control plane's (CONTRACTS-CP §5.5a), relayed by the
// collector and joined to its local records. There is no local enumeration to
// fall back on any more, so a failure is an honest error state — never a stale
// list — and the 5s poll is the retry. The relay's own message (the deck's
// "Couldn't reach the control plane.") is shown when it sent one.
async function loadThreads() {
  // Do not ask for a list this collector cannot produce. `GET /api/threads`
  // answers 412 not_connected for exactly the state `/api/connect` reports as
  // `disconnected` (threads.ts cannotListThreads), and the browser logs every
  // 4xx as "Failed to load resource" — a line no JS can suppress, repeated on
  // every 15s tick, for a designed state the tab already renders correctly.
  // While disconnected we refresh CONNECT instead (a 200, and it keeps the
  // header pill and this decision fresh) and show the relay's own line.
  // Connect state we have not seen yet is not disconnected: we wait for the
  // answer rather than flash the notice at a connected collector.
  if (cannotListThreads(connect.value) || connect.value === null) await loadConnect();
  if (cannotListThreads(connect.value)) {
    threads.value = [];
    threadsTotal.value = 0;
    threadsHasMore.value = false;
    threadsNotice.value = THREADS_NOT_CONNECTED_NOTICE;
    threadsError.value = '';
    threadsKnown.value = true;
    threadsLoaded.value = true;
    return;
  }
  try {
    const out = await apiGet<ThreadListResponse>('/api/threads');
    threads.value = out?.threads || [];
    threadsTotal.value = out?.total ?? threads.value.length;
    threadsHasMore.value = !!out?.has_more;
    threadsNotice.value = '';
    threadsError.value = '';
    threadsKnown.value = true;
  } catch (e) {
    const code = e instanceof ApiError ? e.code : '';
    if (code === 'not_connected' || code === 'cp_not_configured') {
      // Not a failure: the relay is telling us it cannot list, and why. There
      // are no threads to chip either — creating one needs the same key.
      threads.value = [];
      threadsTotal.value = 0;
      threadsHasMore.value = false;
      threadsNotice.value = (e as ApiError).message;
      threadsError.value = '';
      threadsKnown.value = true;
    } else {
      // Keep the last known rows: a failed poll must never un-chip a finding.
      threadsError.value = e instanceof ApiError ? e.message : 'Could not load threads from this collector.';
    }
  } finally {
    threadsLoaded.value = true;
  }
}

function onConnectUpdated(s: ConnectState) {
  connect.value = s;
  if (s.status !== 'connected') loadConnect();
}

function dismissConnectBanner() {
  connectBannerDismissed.value = true;
  localStorage.setItem('flanj.connect.banner.dismissed', '1');
}

const showAddressNudge = computed(
  () => !!health.value?.cp_configured && needsCollectorAddress(connect.value) && !addressNudgeDismissed.value
);

function dismissAddressNudge() {
  addressNudgeDismissed.value = true;
  localStorage.setItem('flanj.address.nudge.dismissed', '1');
}

// "Add address" from the Threads tab: jump to Settings with the address field focused.
function addCollectorAddress() {
  setTab('settings');
  focusAddressTick.value++;
}

// The Threads tab's not-connected notice routes here, the same way the Contracts
// banner does. setTab writes `#settings`, so a reload lands on the same tab and
// Back returns to Threads.
function goToSettings() {
  setTab('settings');
}

// Provider name shown on the sheet and sent on the flag: the configured
// provider_display_name for the observed integration, else the SDK's
// integration id humanized (the same rule the relay applies server-side) —
// except a version-diff, whose id is the CONTRACT's host-derived key and
// resolves through the Edges panel's name for that host (contracts.ts
// providerNameForFinding; QA 2026-09-14).
function providerNameFor(f: Finding): string {
  return providerNameForFinding(f, {
    providerDisplayName: health.value?.provider_display_name,
    healthIntegration: health.value?.integration,
    contracts: contracts.value,
    edges: edges.value
  });
}

function openSheet(f: Finding) {
  sheetEdge.value = null;
  sheetFinding.value = f;
  if (connectStatus.value !== 'connected') loadConnect();
}

/**
 * "Start a thread" on an outbound edge row (v1 phase 4): the SAME sheet, minus
 * the evidence block. Connect-gated identically — the sheet shows the inline
 * Connect prompt and unlocks the moment the confirmation click lands, which is
 * why this starts the same poll the flag path does.
 */
function openEdgeSheet(e: Edge) {
  sheetFinding.value = null;
  sheetEdge.value = {
    host: e.peer_host,
    domain: e.registrable_domain || e.peer_host,
    // The name the row the operator clicked is rendering — never a humanized
    // host standing in for one that was never given.
    name: e.display_name || e.peer_host
  };
  if (connectStatus.value !== 'connected') loadConnect();
}

function closeSheet() {
  sheetFinding.value = null;
  sheetEdge.value = null;
}

function onThreadCreated(_r: FlagResult) {
  loadThreads();
  refresh();
}

const openingThread = ref<string | null>(null);
const chipError = ref<Record<string, string>>({});

// "View thread" on a finding chip = the owner handoff in a new tab (same as the
// success state); "Threads ›" deep-links to the row for Close / Reopen / Replace.
async function openChipThread(threadId: string) {
  openingThread.value = threadId;
  chipError.value = { ...chipError.value, [threadId]: '' };
  try {
    const out = await openThreadInNewTab(threadId);
    if (!out.opened) chipError.value = { ...chipError.value, [threadId]: 'Your browser blocked the new tab — use View thread on the Threads tab.' };
  } catch (e) {
    chipError.value = { ...chipError.value, [threadId]: e instanceof ApiError ? e.message : "Couldn't reach the control plane." };
  } finally {
    openingThread.value = null;
  }
}

// Tab selection is the single source of truth in BOTH directions (route.ts):
// every tab change goes through here and writes the hash the tab owns, and the
// hash drives the tab (applyHash, below). Launch-week item 10: the tab buttons
// used to write nothing while "Add address" and "Threads ›" did, so the URL
// drifted from the screen — Threads → Add address (`#settings`) → Overview
// (nothing) → reload landed on Settings, and Back moved the URL but not the tab.
//
// A change PUSHES a history entry, so Back returns to the previous tab: every
// navigation in the app today is a user gesture. `replace` is for a redirect
// the user did not ask for, so Back never lands on a state that redirects
// again. `hash` carries a deep-link form (`#threads/<id>`) in place of the bare
// tab hash. Nothing else in the app writes location.hash.
function setTab(t: Tab, opts: { hash?: string; replace?: boolean } = {}) {
  tab.value = t;
  const want = opts.hash ?? hashForTab(t);
  if (window.location.hash === want) return;
  if (opts.replace) history.replaceState(null, '', want);
  else history.pushState(null, '', want);
}

function goToThread(threadId: string) {
  highlightThreadId.value = threadId;
  setTab('threads', { hash: hashForThread(threadId) });
}

// The other direction: the hash → the tab, on load and on every hashchange
// (Back / Forward between the entries setTab pushed fire it, and so does a
// pasted deep link). An empty, unknown or token fragment (`#k=…`) is not a tab:
// it shows the default and is left exactly as it is — never rewritten, never
// pushed — so a stale copy of the CP's thread-link token can never be laundered
// into this page's history.
function applyHash() {
  const r = routeFromHash(window.location.hash);
  if (r.threadId) highlightThreadId.value = r.threadId;
  if (r.findingId) highlightFindingId.value = r.findingId;
  tab.value = r.tab;
}

// Scroll the deep-linked finding row into view once findings have loaded and
// rendered (same pattern as the Threads tab's highlight scroll).
watch(
  () => [highlightFindingId.value, findings.value.length],
  () => {
    if (!highlightFindingId.value) return;
    nextTick(() => document.getElementById('finding-' + highlightFindingId.value)?.scrollIntoView({ block: 'center' }));
  },
  { immediate: true }
);

const tab = ref<Tab>('overview');
const expanded = ref<Record<string, boolean>>({});

// ─── Appearance (Settings): Light / Dark, default LIGHT ───────────────────
// ux-design-v2 §3: the collector matches the thread page — light by default,
// dark opt-in, NO System option. Persisted as `flanj.theme` and applied as
// data-flanj-theme on <html>; the dark palette lives under [data-flanj-theme="dark"] only
// and the prefers-color-scheme media query is gone from the stylesheet.
const themePref = ref<ThemePref>(loadThemePref());
function setTheme(pref: ThemePref) {
  themePref.value = pref;
  saveThemePref(pref);
  applyTheme(pref);
  // Choosing a theme retires the flip notice for good — it has nothing left to
  // tell you once you have used the control it points at.
  themeChoiceStored.value = true;
  themeNoticeDismissed.value = true;
}

// The one-time light-default notice (§3.4). BOTH gates: this browser never
// chose a theme (i.e. it was on the deleted System setting) AND the collector
// reports it held data before this upgrade — so a fresh install never sees it,
// and a dark-OS user who wakes up to a light UI is told why exactly once.
const themeChoiceStored = ref(hasStoredThemeChoice());
const themeNoticeDismissed = ref(localStorage.getItem(THEME_FLIP_NOTICE_KEY) === '1');
const showThemeFlipNotice = computed(() =>
  shouldShowThemeFlipNotice({
    storedChoice: themeChoiceStored.value,
    heldPriorData: health.value?.held_prior_data === true,
    dismissed: themeNoticeDismissed.value
  })
);
function dismissThemeFlipNotice() {
  themeNoticeDismissed.value = true;
  localStorage.setItem(THEME_FLIP_NOTICE_KEY, '1');
}
function openAppearance() {
  dismissThemeFlipNotice();
  setTab('settings');
}

// Live-tail stream state: polling always lands in `calls`, but while the user
// inspects a call (or hits pause) the table renders a frozen snapshot so rows
// stop moving under the cursor. New arrivals accumulate behind a "N new calls"
// bar until the user resumes — same semantics as Datadog Live Tail.
const manualPause = ref(false);
const frozenCalls = ref<RedactedCall[]>([]);

// Traffic filters (client-side over the visible window).
const q = ref('');
const fMethod = ref('');
const fStatus = ref('');
const fContract = ref('');
const fDirection = ref('');
const fPeer = ref('');
const hideHealth = ref(false);

const callsById = computed(() => {
  const m: Record<string, RedactedCall> = {};
  for (const c of calls.value) m[c.id] = c;
  return m;
});

// Stable row order: the store returns ORDER BY last_seen DESC, which re-sorts the
// rows on every 5s poll and moves buttons out from under the cursor. Sort
// client-side by registrable domain (then host) so a row keeps its place.
const byDomain = (a: Edge, b: Edge) =>
  (a.registrable_domain || a.peer_host).localeCompare(b.registrable_domain || b.peer_host) ||
  a.peer_host.localeCompare(b.peer_host);
const outboundEdges = computed(() => edges.value.filter((e) => e.direction === 'client').slice().sort(byDomain));
const inboundEdges  = computed(() => edges.value.filter((e) => e.direction === 'server').slice().sort(byDomain));

// ─── One host → name index, shared by every surface that shows a host ─────
// Owner ruling 2026-08-31: display names substitute for raw hosts everywhere a
// host is shown — Traffic (the calls table + the counterparty facet) and the
// Contracts provider cards, not only the Edges panel. They all read THIS
// index, built from the edges list already loaded for the Edges panel: one
// resolution path, no second lookup, and never a control-plane request on a

// A drift is per CALL. The signature is per-endpoint — that is how findings
// DEDUP — but "one finding per endpoint" never meant "every call on that
// endpoint drifted", and reading it that way relabelled conforming calls, and
// calls captured before the drift existed. The store now records it on the
// call (model.RedactedCall.Drifted); see isDrifted.

/** Contract COVERAGE for a row — see ui/src/coverage.ts. Kept distinct from
 *  DRIFT: a call to a host with no loaded contract was captured and never
 *  validated, so it is neither conforming nor drifted. */
/** How many captured calls this card's contract has actually validated.
 *  A contract can be loaded and have checked NOTHING — no traffic yet, or a
 *  host-scoped spec on an edge that has been quiet. Claiming "conforming" in
 *  that state asserts a clean bill of health nothing performed. An UNSCOPED
 *  provider spec (config `spec_path` with no `peer_host`) validates every
 *  outbound call, so it counts them all. */
function cardValidatedCalls(p: ContractCard): number {
  const card = { peerHost: p.peerHost, isSelf: p.spec?.role === 'self' };
  let n = 0;
  for (const c of calls.value) {
    // `checked` now carries the temporal gate (ui/src/coverage.ts): a call
    // captured BEFORE this contract was bound is not evidence for it, however
    // well the hosts match. Uploading a document used to flip six already
    // captured calls to validated with no new traffic at all.
    if (!isValidated(c)) continue;
    if (!isEvidenceFor(c, card)) continue;
    n++;
  }
  return n;
}

/**
 * Is this card's stored document past the contract channel's 8 MB cap?
 *
 * Gated on `serves_fronts`, and that gate is the whole reason this reads a
 * health field at all. The cap is a property of the FRONT hop: a single pod's
 * drift processor reads the same row in-process, crosses no boundary, applies
 * no cap, and validates against the document exactly as it always has. Showing
 * "too large to serve" there would warn about something that works, which is
 * the class of claim this surface exists to stop making. On the store pod of a
 * tiered deployment the same row genuinely cannot reach any front.
 *
 * Absent `serves_fronts` (an older collector) reads as no fronts — the
 * pre-tiered default, and the one that claims nothing.
 */
function cardOverCap(p: ContractCard): boolean {
  return !!health.value?.serves_fronts && !!p.spec && contractOverCap(p.spec);
}

/** The evidence line under a card: `validated 0 calls since upload`. Always
 *  rendered, because ZERO is the state that used to render CONFORMING and it
 *  is the only symptom a wrong host binding ever produces. */
function cardEvidenceMeta(p: ContractCard): string {
  const word = p.spec ? provenanceWord(p.spec) : 'uploaded';
  const since = word === 'observed' ? SINCE_SNAPSHOT : word === 'loaded' ? SINCE_LOAD : SINCE_UPLOAD;
  return validatedCallsMeta(cardValidatedCalls(p), since);
}

function coverageVerdictOf(c: RedactedCall): CoverageVerdict {
  // MCP coverage is per TOOL, not per server: only a tool publishing an
  // outputSchema, answering without isError, can have its result validated
  // (drift/mcp.go). Hand the parsed snapshot rows in so the chip cannot claim a
  // check the processor never runs.
  const tools: Record<string, McpToolRow[]> = {};
  for (const [integration, entry] of Object.entries(mcpTools.value)) tools[integration] = entry.rows;
  return callCoverageDetail(c, contracts.value, tools);
}

function coverageOf(c: RedactedCall): Coverage {
  return coverageVerdictOf(c).coverage;
}

/** Did a contract actually check THIS call? The one per-call validated fact
 *  every evidence count reads — the Overview headline, each MCP server's own
 *  line, the contract cards' `validated N calls`. Today it is the coverage
 *  mirror's `checked` verdict (ui/src/coverage.ts, residuals and all); when
 *  the server-side stamp lands it replaces THIS body and nothing else. */
function isValidated(c: RedactedCall): boolean {
  return coverageOf(c) === 'checked';
}

/** The `not checked` tooltip for THIS row, named after the actual cause — a
 *  tool that declares no outputSchema is not a missing upload, and MCP
 *  contracts are never uploaded at all. */
function notCheckedTitleOf(c: RedactedCall): string {
  return notCheckedTitle(coverageVerdictOf(c).reason ?? 'no-contract', c);
}

/**
 * Did THIS call drift?
 *
 * Read off the call's own `drifted` flag, which the store sets when the call
 * produced a finding saying the call departed from a contract — live-vs-spec on
 * REST, output_mismatch on MCP — on every occurrence, not just the first.
 *
 * It used to ask "has this ENDPOINT ever drifted?" (a set built from findings),
 * so ONE drifting charge marked every call on `POST /v1/charges` as drifted:
 * the conforming ones, and the ones captured before the drift existed. Same
 * false-assurance class as CONFORMING-with-no-evidence, pointing the other way.
 *
 * MCP kept that fallback — a set keyed by (integration, tool) — for one wrong
 * reason: the comment said the snapshot detector does not stamp calls, which is
 * true of definition_change and NOT of output_mismatch, a per-call finding that
 * has carried a source_call_id all along. The store simply was not marking it.
 * It does now, so MCP reads the same per-call fact as REST and the tool-level
 * guess is gone: no more relabelling a tool's whole history from one mismatch,
 * and no more DRIFTED on an isError result the processor never judged.
 *
 * The processor's own stamp (`validated: 'drifted'`, 2026-09-07) is the same
 * fact from the other end of the pipeline — the store sets `drifted` from it on
 * insert — and is read here too, so the row cannot lag the verdict by the one
 * finding record that follows the call in its batch.
 */
function isDrifted(c: RedactedCall): boolean {
  return c.drifted === true || c.validated === 'drifted';
}

const hasExpanded = computed(() => Object.values(expanded.value).some(Boolean));
const streamPaused = computed(() => manualPause.value || hasExpanded.value);
const displayCalls = computed(() => (streamPaused.value ? frozenCalls.value : calls.value));

const pendingCount = computed(() => {
  if (!streamPaused.value) return 0;
  const seen = new Set(frozenCalls.value.map((c) => c.id));
  return calls.value.filter((c) => !seen.has(c.id)).length;
});

// Snapshot the list the user is currently looking at, synchronously, before
// the state change that pauses the stream — so the clicked row cannot shift
// even if a poll lands on the same tick.
function ensureFrozen() {
  if (!streamPaused.value) frozenCalls.value = calls.value;
}

function togglePause() {
  if (streamPaused.value) {
    resumeLive();
  } else {
    ensureFrozen();
    manualPause.value = true;
  }
}

function resumeLive() {
  manualPause.value = false;
  expanded.value = {};
}

const HEALTH_RE = /(^|\/)(_?health[a-z-]*|livez?|readyz?|ping)(\/|$|\?)/i;

// Method facets: MCP rows list (and match) as TOOL, never as `tools/call`.
const methodOptions = computed(() =>
  Array.from(new Set(calls.value.map((c) => methodFacetOf(c)))).sort()
);

const peerOptions = computed(() =>
  Array.from(new Set(calls.value.map((c) => c.peer_host).filter(Boolean) as string[])).sort()
);

// Hosts observed as internal same-team edges: their Traffic rows are
// metadata-only (bodies never captured) and carry the INTERNAL chip; the
// counterparty facet names them `<host> · internal`.
const internalPeers = computed(() => {
  const s = new Set<string>();
  for (const c of calls.value) if (c.edge_class === 'internal' && c.peer_host) s.add(c.peer_host);
  return s;
});

const filtersActive = computed(
  () =>
    !!(
      q.value.trim() ||
      fMethod.value ||
      fStatus.value ||
      fContract.value ||
      fDirection.value ||
      fPeer.value ||
      hideHealth.value
    )
);

const filteredCalls = computed(() => {
  const tokens = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const include = tokens.filter((t) => !t.startsWith('-'));
  const exclude = tokens.filter((t) => t.startsWith('-') && t.length > 1).map((t) => t.slice(1));
  return displayCalls.value.filter((c) => {
    if (fMethod.value && methodFacetOf(c) !== fMethod.value) return false;
    // Status: HTTP rows by status class; MCP rows are ok/error from isError
    // ('err' matches mcp_is_error, '2xx' matches ok — see statusFilterMatches).
    if (!statusFilterMatches(fStatus.value, c)) return false;
    if (fContract.value === 'drifted' && !isDrifted(c)) return false;
    // `conforming` now means validated-and-clean: a row nothing checked is not
    // conforming, and used to be counted as such.
    if (fContract.value === 'conforming' && (isDrifted(c) || coverageOf(c) !== 'checked')) return false;
    if (fContract.value === 'not-checked' && coverageOf(c) !== 'not-checked') return false;
    if (fDirection.value && c.direction !== fDirection.value) return false;
    if (fPeer.value && c.peer_host !== fPeer.value) return false;
    if (hideHealth.value && HEALTH_RE.test(c.route || c.url || '')) return false;
    if (include.length || exclude.length) {
      const hay = [
        c.method,
        c.route,
        c.url,
        String(c.status_code),
        c.integration,
        c.peer_host,
        c.direction === 'server' ? 'inbound' : c.direction === 'client' ? 'outbound' : '',
        c.correlation?.request_id,
        c.correlation?.idempotency_key,
        c.correlation?.trace_id,
        c.transport === 'mcp' ? 'mcp' : '',
        c.mcp_tool_name,
        c.correlation?.client_request_id
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      for (const t of include) if (!hay.includes(t)) return false;
      for (const t of exclude) if (hay.includes(t)) return false;
    }
    return true;
  });
});

function clearFilters() {
  q.value = '';
  fMethod.value = '';
  fStatus.value = '';
  fContract.value = '';
  fDirection.value = '';
  fPeer.value = '';
  hideHealth.value = false;
}

function dirLabel(d?: string): string {
  return d === 'server' ? 'in' : d === 'client' ? 'out' : '—';
}

function fmtRPM(rpm?: number): string {
  if (!rpm) return '0';
  return rpm >= 10 ? String(Math.round(rpm)) : rpm.toFixed(1).replace(/\.0$/, '');
}

function specHref(s: SpecInfo): string {
  return '/api/contracts/spec?integration=' + encodeURIComponent(s.integration);
}

// Contract cards. Self = the contract WE publish (validates our inbound
// responses). Providers = contracts that EXIST, then providers only known from
// findings (an older collector without /api/contracts).
//
// A card is NOT fabricated for every uncovered edge any more. That loop
// produced ~40 near-identical empty cards on a 50-provider estate, each with a
// chip saying nothing was loaded and a paragraph of config scolding — a wall
// that buried the handful of real contracts inside it. Uncovered providers get
// ONE ROW EACH in a collapsed section instead (uncoveredHosts below), with the
// uploader opening in place. A card materialises when a contract does.
interface ContractCard {
  key: string;
  name: string;
  peerHost: string;
  spec: SpecInfo | null;
  findings: Finding[];
}

const contractCards = computed<{ self: ContractCard[]; mcpServers: ContractCard[]; providers: ContractCard[] }>(() => {
  // Findings are attributed to a contract by HOST first, integration second —
  // see findingBelongsToContract. An uploaded contract's integration is derived
  // from its host while a finding's comes from the call, so an
  // integration-only join split one provider into two cards.
  const unclaimed = [...liveFindings.value, ...versionDiffFindings.value, ...mcpContractFindings.value];
  const hostOfCall = (id: string) => callsById.value[id]?.peer_host;
  const claim = (spec: SpecInfo): Finding[] => {
    const mine: Finding[] = [];
    for (let i = unclaimed.length - 1; i >= 0; i--) {
      if (findingBelongsToContract(unclaimed[i], spec, hostOfCall)) mine.unshift(...unclaimed.splice(i, 1));
    }
    return mine;
  };
  const self: ContractCard[] = [];
  // MCP servers are their own KIND, not providers with an odd format: nobody
  // uploaded them, Replace/Remove do not apply, and the providers section's own
  // sub-line ("the contracts your providers publish — your outbound calls
  // validated against them") is already false for them. A section whose
  // sub-line is untrue for some of its cards is the wrong section.
  const mcpServers: ContractCard[] = [];
  const providers: ContractCard[] = [];
  for (const s of contracts.value) {
    const card: ContractCard = {
      // The spec's own `info.title` wins: it is the provider's own words for the
      // contract, read out of the document the operator loaded — declared, and
      // inspectable in this very tab. Edge display names are deliberately NOT
      // consulted here (owner ruling 2026-08-31: naming stays on the Edges
      // panel; Traffic and Contracts keep domains). `peerHost` renders beside
      // whichever name wins, so the host is never replaced.
      key: 'spec-' + s.integration,
      name: s.title || humanize(s.integration) || s.peer_host || (s.role === 'self' ? 'Your API' : 'Provider'),
      peerHost: s.peer_host || '',
      spec: s,
      findings: claim(s)
    };
    if (s.role === 'self') self.push(card);
    else if (s.format === 'mcp') mcpServers.push(card);
    else providers.push(card);
  }
  // Anything still unclaimed belongs to no contract we hold — an older
  // collector's findings, or a provider whose contract was removed. It keeps
  // its own card, because dropping a finding on the floor is worse than an
  // imperfect heading.
  const leftover = new Map<string, Finding[]>();
  for (const f of unclaimed) {
    const list = leftover.get(f.integration) || [];
    list.push(f);
    leftover.set(f.integration, list);
  }
  for (const [integration, fs] of leftover) {
    providers.push({ key: 'find-' + integration, name: humanize(integration) || integration, peerHost: '', spec: null, findings: fs });
  }
  return { self, mcpServers, providers };
});

// Providers with traffic and no contract — one compact row each, collapsed by
// default. Only asserted once /api/contracts has actually answered: an older
// collector cannot say whether a contract is loaded, and guessing "none" there
// would invent work that may already be done.
const uncoveredHosts = computed(() =>
  contractsKnown.value ? uncoveredProviders(outboundEdges.value, contracts.value, mcpHosts.value) : []
);
const uncoveredOpen = ref(false);

// The Edges roll call: one line under the outbound group's heading, counted
// positive, below a fully rendered graph. Nothing is gated on it.
const outboundRollCall = computed(() =>
  contractsKnown.value ? rollCall(outboundEdges.value, contracts.value, mcpHosts.value) : ''
);

// Provider contracts indexed by the host each is bound to — the Edges row's
// meta line, and what the uploader consults to know it is replacing.
const contractByHost = computed(() => contractsByHost(contracts.value));

// ─── The uploader ────────────────────────────────────────────────────────
// State lives in ContractUploader.vue; App owns only WHICH row has it open and
// the one-line after-state it reports back. Only one can be open at a time —
// there is one mutation, so there is one confirm flow.
const uploadFor = ref('');
const uploadNotice = ref('');
const uploadError = ref('');
/** Whether the open uploader is holding work — reported by the child, because
 *  the document it has read and the confirm step it is showing are its state,
 *  not App's. */
const uploaderDirty = ref(false);

function openUploader(host: string) {
  const next = host || '*';
  // One uploader, mounted on the row that opened it — so opening another
  // UNMOUNTS this one and its in-progress confirm goes with it. Ask first;
  // Remove already asks, and this discards work the operator did by hand.
  if (uploadFor.value && uploadFor.value !== next && uploaderDirty.value) {
    if (!window.confirm(UPLOADER_DISCARD_CONFIRM)) return;
  }
  uploadFor.value = next;
  uploaderDirty.value = false;
  uploadNotice.value = '';
  uploadError.value = '';
}

function closeUploader() {
  uploadFor.value = '';
  uploaderDirty.value = false;
}

/**
 * The Edges panel's route into this tab. It is a ROUTE, not a second uploader:
 * one mutation, one confirm flow, one binding model, and one surface that can
 * render the after-state.
 *
 * A host with a contract lands on its card; a host without one expands the
 * collapsed section and highlights its row. Either way the uploader that opens
 * already knows the host, which is the whole ergonomic prize for routing here.
 */
function goToContracts(host: string) {
  setTab('contract');
  if (contractByHost.value.has(host)) {
    highlightUncoveredHost.value = null;
    nextTick(() => document.getElementById('contract-' + host)?.scrollIntoView({ block: 'center' }));
    return;
  }
  uncoveredOpen.value = true;
  highlightUncoveredHost.value = host;
  openUploader(host);
  nextTick(() => document.getElementById('uncovered-' + host)?.scrollIntoView({ block: 'center' }));
}

async function onUploaded(notice: string) {
  closeUploader();
  uploadNotice.value = notice;
  await refreshContracts();
}

/** Only an UPLOADED contract offers Replace and Remove. A config-loaded one
 *  would be back at the next start, and a button that undoes itself is worse
 *  than no button. */
function isUploaded(spec: SpecInfo | null): boolean {
  return !!spec && spec.format !== 'mcp' && spec.role !== 'self' && (spec.source ?? 'upload') === 'upload';
}

async function removeContract(integration: string, host: string) {
  if (!window.confirm(`Remove the contract for ${host || integration}? Its calls will be captured but not validated.`)) return;
  try {
    await apiPost('/api/contracts/remove', { integration });
    uploadNotice.value = '';
    await refreshContracts();
  } catch (e) {
    uploadError.value = e instanceof ApiError ? e.message : 'Couldn’t remove the contract.';
  }
}

// The Contracts tab's two sections, rendered by one shared card template. The
// self section only renders once /api/contracts has actually answered (older
// collectors can't say whether a self contract is loaded).
const cardGroups = computed(() => [
  {
    key: 'self',
    title: 'Your contract',
    sub: 'the API you publish — your inbound responses validated against it',
    cards: contractCards.value.self,
    emptyText: contractsKnown.value
      ? // NOT "before your consumers do" — we cannot, and saying so was a lie of
        // exactly the kind this surface exists to stop. The self contract validates
        // INBOUND calls (processor.go: direction == "server"), i.e. responses ALREADY
        // SENT; the SDK captures after the fact, so the consumer received the drifting
        // response first, every time. What this actually buys is the SOURCE of the
        // news: your own traffic instead of someone else's bug report.
        'No self contract loaded. Point flanjdrift.self_spec_path at the OpenAPI document you publish and your own responses get checked against it — so your drift reaches you from your traffic, not from a consumer.'
      : ''
  },
  {
    key: 'mcp',
    title: `MCP servers${contractCards.value.mcpServers.length ? ` (${contractCards.value.mcpServers.length})` : ''}`,
    sub: 'each server publishes its own contract on tools/list — nothing to upload, nothing to remove',
    cards: contractCards.value.mcpServers,
    emptyText: ''
  },
  {
    key: 'providers',
    // Counted like the MCP section: at 50 the number IS the information, and a
    // count on one section but not its neighbour reads as an oversight.
    title: `Provider contracts${contractCards.value.providers.length ? ` (${contractCards.value.providers.length})` : ''}`,
    sub: 'the contracts your providers publish — your outbound calls validated against them',
    cards: contractCards.value.providers,
    emptyText: providerContractsEmptyText(uncoveredHosts.value.length)
  }
]);

const liveFindings = computed(() => findings.value.filter((f) => f.kind === 'live-vs-spec'));

// Version diffs: the breaking changes between an uploaded contract and the one
// it replaced. Call-less by construction (the drift is in the two documents),
// which is why they are their OWN list and not folded into liveFindings — that
// one feeds the Overview headline, which counts LIVE drift only and must stay
// that way.
//
// They were in the Finding type union and produced by the upload path, and
// reached no surface at all: the tab built its rows from live-vs-spec + the two
// MCP kinds, so the notice announced "N breaking changes against the version it
// replaced" over a tab showing none of them, and the control plane's
// `#contracts/<id>` deep link landed on an anchor that did not exist.
const versionDiffFindings = computed(() => findings.value.filter((f) => f.kind === 'version-diff'));

// ─── MCP (v0.5 Step D) ───────────────────────────────────────────────────
// The MCP contract surface is SELF-DELIVERING: the server's observed
// tools/list arrives as a spec_infos row (format "mcp"), so the Contracts tab
// lists the server (title = serverInfo.name) and this UI needs no spec file.
const mcpContracts = computed(() => contracts.value.filter((s) => s.format === 'mcp'));

const mcpFindings = computed(() => findings.value.filter((f) => isMcpFinding(f)));

// Per-server tool rows, parsed from the stored tools/list snapshot document
// (refetched only when the contract's loaded_at moves).
const mcpTools = ref<Record<string, { key: string; rows: McpToolRow[] }>>({});
async function loadMcpTools() {
  for (const s of mcpContracts.value) {
    const key = s.integration + '|' + s.loaded_at;
    if (mcpTools.value[s.integration]?.key === key) continue;
    try {
      const resp = await fetch(specHref(s));
      if (!resp.ok) continue;
      mcpTools.value = { ...mcpTools.value, [s.integration]: { key, rows: parseToolRows(await resp.text()) } };
    } catch {
      /* keep the last parsed rows */
    }
  }
}

function mcpToolRows(integration: string): McpToolRow[] {
  return mcpTools.value[integration]?.rows || [];
}

/** serverInfo.name for an integration (spec title), else the integration id. */
function mcpServerName(integration: string): string {
  const s = mcpContracts.value.find((c) => c.integration === integration);
  return s?.title || integration;
}

// Hosts that are MCP edges: known from mcp contracts and from observed MCP
// calls — drives the transport badge on the Edges overview.
const mcpHosts = computed(() => {
  const hosts = new Set<string>();
  for (const s of mcpContracts.value) if (s.peer_host) hosts.add(s.peer_host);
  for (const c of calls.value) if (c.transport === 'mcp' && c.peer_host) hosts.add(c.peer_host);
  return hosts;
});

// Per-server MCP health headline (deck §2): output mismatch → definition
// change (breaking, no calls affected yet) → nothing validated yet (neutral)
// → clean. Three tones, like the REST line above it.
const mcpOverview = computed(() =>
  mcpContracts.value.map((s) => ({
    key: s.integration,
    headline: mcpHeadline(
      // Same origin rule as the Contracts card, from the same function — so the
      // two surfaces cannot drift apart and render two identical health lines
      // for two different servers again.
      { name: s.title || s.integration, version: s.version, origin: contractOrigin(s) },
      mcpFindings.value.filter((f) => f.integration === s.integration),
      humanTime,
      // Evidence for THIS server only: its own validated tool calls. Zero is
      // the neutral state — a snapshot that has judged nothing is not an
      // all-clear, however complete the Contracts card beside it looks.
      calls.value.filter((c) => isMcpCall(c) && c.integration === s.integration && isValidated(c)).length
    )
  }))
);

// Local notices (deck §2): stale_client ONLY since qfix2-2026-08-26. A
// DESCRIPTION definition change is now flaggable, so it cannot sit under a band
// whose sub-line promises "Nothing here can be flagged" — it lives on the
// Contracts tab with a Flag control, like every other definition change.
// Visible to you only; these items NEVER carry a flag control.
//
// Nor an acknowledged state: ackable() (extension/flanjui/acks.go) requires
// kind=definition_change, so a stale_client finding can never be acknowledged
// and the band carries no acked rendering. The band's items used to be able to
// be acked back when DESCRIPTION lived here.
const localNotices = computed(() =>
  mcpFindings.value
    .filter((f) => isLocalNotice(f))
    .map((f) => ({ id: f.id, line: noticeLine(f, mcpServerName(f.integration)) }))
);
// The band's sub-line: named only while every notice points at ONE provider;
// notices spanning several providers fall back to the neutral copy.
const localNoticesProviders = computed(() =>
  mcpFindings.value.filter((f) => isLocalNotice(f)).map((f) => providerNameFor(f))
);

// MCP contract findings shown on the Contracts tab: output_mismatch +
// definition_change (stale_client stays a Health-band notice only).
const mcpContractFindings = computed(() =>
  mcpFindings.value.filter((f) => f.kind === 'output_mismatch' || f.kind === 'definition_change')
);

// Contracts tab pills (two-tier): red = breaking-severity rows (all sources —
// severity decides the tier, never the protocol); amber = informational rows
// (NON-BREAKING + DESCRIPTION) not yet acknowledged. Invariant: red + amber +
// acknowledged = the rows listed on the tab.
const contractTabRows = computed(() => [...liveFindings.value, ...versionDiffFindings.value, ...mcpContractFindings.value]);
const contractBreakingCount = computed(() => contractTabRows.value.filter((f) => isBreakingFinding(f)).length);
const contractInfoCount = computed(
  () => contractTabRows.value.filter((f) => !isBreakingFinding(f) && !isAcked(f)).length
);
// Every un-acked informational row is a DESCRIPTION change: the pill keeps its
// class and its count (e2e reads `.tab-count.warn`) but wears the steel
// outline, not the copper fill — a wording change is not a warning.
const contractDescOnly = computed(
  () =>
    contractInfoCount.value > 0 &&
    contractTabRows.value.every((f) => isBreakingFinding(f) || isAcked(f) || definitionClass(f) === 'DESCRIPTION')
);

// Per-card chip counts — the same taxonomy as the tab pills, so the sum of
// card chips always equals the pills.
function cardBreakingCount(p: ContractCard): number {
  return p.findings.filter((f) => isBreakingFinding(f)).length;
}
function cardInfoCount(p: ContractCard): number {
  return p.findings.filter((f) => !isBreakingFinding(f) && !isAcked(f)).length;
}
// The card splits its informational chip by class — copper `N NON-BREAKING`
// for schema changes, steel `N DESCRIPTION` for wording — so each class wears
// one vocabulary from the tab through the card to the row badge. Their sum is
// still the tab pill's number.
function cardNonBreakingCount(p: ContractCard): number {
  return p.findings.filter((f) => !isBreakingFinding(f) && !isAcked(f) && definitionClass(f) !== 'DESCRIPTION').length;
}
function cardDescriptionCount(p: ContractCard): number {
  return p.findings.filter((f) => !isBreakingFinding(f) && !isAcked(f) && definitionClass(f) === 'DESCRIPTION').length;
}
function cardNonBreakingTitle(p: ContractCard): string {
  return informationalChipTitle(cardNonBreakingCount(p), 0);
}

// ─── Local acknowledge (qfix-2026-08-25) ─────────────────────────────────
// POST /api/findings/{id}/ack|unack — local-only (nothing is sent to the CP);
// the refreshed /api/findings join carries acked/acked_at back.
const ackBusy = ref<Record<string, boolean>>({});
const ackError = ref<Record<string, string>>({});
async function setAck(f: Finding, ack: boolean) {
  ackBusy.value = { ...ackBusy.value, [f.id]: true };
  ackError.value = { ...ackError.value, [f.id]: '' };
  try {
    await apiPost(`/api/findings/${encodeURIComponent(f.id)}/${ack ? 'ack' : 'unack'}`);
    await refresh();
  } catch (e) {
    ackError.value = { ...ackError.value, [f.id]: e instanceof ApiError ? e.message : COLLECTOR_UNREACHABLE };
  } finally {
    ackBusy.value = { ...ackBusy.value, [f.id]: false };
  }
}

// The finding the sheet is open for rides with its representative source call
// (MCP server identity + JSON-RPC id live on the call).
const sheetCall = computed(() =>
  sheetFinding.value?.source_call_id ? callsById.value[sheetFinding.value.source_call_id] || null : null
);

// Headline counts LIVE drift only — spec-version diffs are informational and
// intentionally excluded from the divergence status. The wording, the neutral
// zero state and the reason it exists all live in ui/src/headline.ts, where
// vitest can see them: this line used to assert `No drift detected` on an
// install where nothing had ever been validated.
//
// It gets the window's calls, each with its per-call validated fact, NOT a
// count: it counted every validated call in the window here, MCP tool calls
// included, and spent an MCP server's evidence on the REST provider it names
// beneath. Which calls are evidence for THIS line — the REST ones — is decided
// in headline.ts, beside the tests that pin it.
const headline = computed(() =>
  headlineFor({
    liveFindings: liveFindings.value,
    calls: calls.value.map((c) => ({ transport: c.transport, integration: c.integration, validated: isValidated(c) })),
    integration: health.value?.integration
  })
);

// ─── Edge naming (v1p1): the inline rename editor ─────────────────────────
// One editor at a time; all transitions live in edge-names.ts (vitest-covered).
// The 5s refresh repaints the rows but NEVER the open editor — the draft lives
// here, not on the row (the connect-form poll-clobber discipline).
const nameEdit = ref<EdgeNameEdit | null>(null);
// Post-save notices/errors keyed by registrable domain: the partial-success
// line ("Name saved. The suggestion didn't reach…") and the Remove-name error.
const nameNotice = ref<Record<string, string>>({});
const removeNameError = ref<Record<string, string>>({});
const removeNameBusy = ref<Record<string, boolean>>({});
// The editor's input: focused on open so Escape works on the first press and a
// mouse user does not have to click twice. The input is declared inside the
// outbound-rows v-for, so Vue compiles the ref with `ref_for` and stores the
// mounted elements as an ARRAY (at most one — the v-if allows a single open
// editor); normalize on read rather than calling .focus() on the array.
const renameInput = ref<HTMLInputElement | HTMLInputElement[] | null>(null);

function focusRenameInput() {
  const el = renameInput.value;
  (Array.isArray(el) ? el[0] : el)?.focus();
}

function edgeDomain(e: Edge): string {
  return e.registrable_domain || e.peer_host;
}

function startRename(e: Edge) {
  nameEdit.value = beginEdit(e);
  nextTick(focusRenameInput);
  nameNotice.value = { ...nameNotice.value, [edgeDomain(e)]: '' };
}

function cancelRename() {
  // Escape and the Cancel button both land here: inert while a save is in
  // flight (cancelEdit is identity-while-busy — the button is disabled then,
  // and the Escape key gets the same guard).
  nameEdit.value = cancelEdit(nameEdit.value);
}

function onRenameInput(ev: globalThis.Event) {
  if (!nameEdit.value) return;
  nameEdit.value = typeDraft(nameEdit.value, (ev.target as HTMLInputElement).value);
}

function onSuggestToggle(ev: globalThis.Event) {
  if (!nameEdit.value) return;
  nameEdit.value = toggleSuggest(nameEdit.value, (ev.target as HTMLInputElement).checked);
}

async function saveRename() {
  if (!nameEdit.value || nameEdit.value.busy) return;
  // The directory cap pre-check: Save is blocked while the suggest box is
  // ticked and the name exceeds 64 chars (SUGGEST_TOO_LONG shows inline).
  if (suggestTooLong(nameEdit.value)) return;
  const s = (nameEdit.value = saveStart(nameEdit.value));
  try {
    const out = await apiPost<{ saved: boolean; suggested: boolean; message?: string }>('/api/edges/name', {
      host: s.host,
      name: s.draft,
      suggest: s.suggest
    });
    if (out.message) nameNotice.value = { ...nameNotice.value, [s.domain]: out.message };
    nameEdit.value = editorClosed();
    refresh();
  } catch {
    if (nameEdit.value) nameEdit.value = saveFailed(nameEdit.value);
  }
}

async function removeName(e: Edge) {
  const domain = edgeDomain(e);
  removeNameBusy.value = { ...removeNameBusy.value, [domain]: true };
  removeNameError.value = { ...removeNameError.value, [domain]: '' };
  try {
    await apiPost('/api/edges/name', { host: e.peer_host, name: '', suggest: false });
    refresh();
  } catch {
    removeNameError.value = { ...removeNameError.value, [domain]: SAVE_ERROR };
  } finally {
    removeNameBusy.value = { ...removeNameBusy.value, [domain]: false };
  }
}

/**
 * The 5s poll — through apiGet, so a failed response can never be mistaken for
 * data.
 *
 * These four used to be `fetch(...).then(r => r.json())`, which does not look
 * at `r.ok`: a 500 with an error body RESOLVED, and the error object was
 * assigned straight into `health`. `cp_configured` then read `undefined` and
 * the header grew a `control plane not configured` pill, Overview said no edges
 * had been discovered and Traffic said no calls had been captured — a store
 * outage rendered as a fresh install, and the page invited the operator to fix
 * configuration that was working. Observed on the postgres lane with the
 * database stopped; only the Threads tab (which already used the api helpers)
 * told the truth.
 *
 * apiGet throws ApiError on a non-ok response, and Promise.all rejects on the
 * first of them, so NOTHING is assigned unless all four succeeded. Last-known
 * data stays on screen — stale beats invented — and the thrown message lands in
 * the existing "Failed to load" banner.
 */
async function refresh() {
  try {
    const [h, f, c, e] = await Promise.all([
      apiGet<Health>('/api/health'),
      apiGet<{ findings?: Finding[] }>('/api/findings'),
      apiGet<{ calls?: RedactedCall[] }>('/api/calls'),
      apiGet<{ edges?: Edge[] }>('/api/edges')
    ]);
    health.value = h;
    findings.value = f.findings || [];
    calls.value = c.calls || [];
    edges.value = e.edges || [];
    loadError.value = '';
  } catch (e) {
    // The relay's own one-sentence message when it answered, and the deck's
    // line when it did not answer at all. `String(e)` put a raw
    // `TypeError: Failed to fetch` in front of the operator (QA walk,
    // 2026-09-02) — the same string the ack path already refuses to show.
    loadError.value = e instanceof ApiError ? e.message : COLLECTOR_UNREACHABLE;
  }
  await refreshContracts();
}

// Newer endpoint — a collector predating /api/contracts must not fail the page.
// Called on every poll AND straight after an upload or a removal, so the card
// the operator just produced is on screen before the next tick.
async function refreshContracts() {
  try {
    const resp = await fetch('/api/contracts');
    if (resp.ok) {
      contracts.value = (await resp.json()).contracts || [];
      contractsKnown.value = true;
      // MCP contracts carry their tool list in the stored snapshot document —
      // fetch it (only when a snapshot moved) for the per-tool rows.
      loadMcpTools();
    }
  } catch {
    /* keep last known contracts */
  }
}

function correlationFor(f: Finding): Correlation | null {
  if (!f.source_call_id) return null;
  return callsById.value[f.source_call_id]?.correlation ?? null;
}

// Pretty-print a captured body: it is a redacted JSON string; parse+re-indent
// for readability, fall back to the raw text (e.g. non-JSON or truncated).
function prettyBody(raw: string): string {
  if (!raw) return '';
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

// One timestamp format on the surface (src/time.ts): the captured column and
// a finding's snapshot labels used to render the same instant two ways.
function humanTime(iso: string): string {
  return isoStamp(iso);
}
/** The two snapshot instants of a definition_change, in the surface format. */
function snapshotStamps(detail?: string): { from: string; to: string } {
  const t = snapshotTimes(detail);
  return { from: isoStamp(t.from), to: isoStamp(t.to) };
}

function toggle(id: string) {
  if (!expanded.value[id]) ensureFrozen();
  expanded.value = { ...expanded.value, [id]: !expanded.value[id] };
}

function headerRows(h?: Record<string, string>): [string, string][] {
  if (!h) return [];
  return Object.entries(h);
}

let timer: number | undefined;
let connectTimer: number | undefined;
let threadsTimer: number | undefined;

function connectPollWanted(): boolean {
  return connectStatus.value === 'pending' || tab.value === 'settings' || sheetOpen.value;
}

function onFocus() {
  if (document.visibilityState === 'hidden') return;
  loadConnect();
  if (tab.value === 'threads' || tab.value === 'contract') loadThreads();
}

onMounted(() => {
  applyHash();
  refresh();
  loadConnect();
  loadThreads();
  timer = window.setInterval(refresh, 5000);
  connectTimer = window.setInterval(() => {
    if (connectPollWanted()) loadConnect();
  }, 5000);
  threadsTimer = window.setInterval(() => {
    if (document.visibilityState !== 'hidden' && (tab.value === 'threads' || tab.value === 'contract')) loadThreads();
  }, 15000);
  window.addEventListener('focus', onFocus);
  document.addEventListener('visibilitychange', onFocus);
  window.addEventListener('hashchange', applyHash);
});
onUnmounted(() => {
  timer && window.clearInterval(timer);
  connectTimer && window.clearInterval(connectTimer);
  threadsTimer && window.clearInterval(threadsTimer);
  window.removeEventListener('focus', onFocus);
  document.removeEventListener('visibilitychange', onFocus);
  window.removeEventListener('hashchange', applyHash);
});

watch(tab, (t) => {
  if (t === 'threads' || t === 'contract') loadThreads();
  if (t === 'settings') loadConnect();
});
</script>

<template>
  <div class="page">
    <!-- The hex bolt: ONE inline <symbol> per document. Every bolt on the page
         is `<svg class="hx [sm] tone-*"><use href="#hxbolt"/></svg>`; the tone
         class binds currentColor (inner hex) and --l (outer ring) to a token
         family, so no bolt carries an inline style. -->
    <svg class="hx-defs" aria-hidden="true" focusable="false">
      <symbol id="hxbolt" viewBox="0 0 24 24">
        <polygon class="hx-outer" points="22,12 17,20.7 7,20.7 2,12 7,3.3 17,3.3" stroke-width="1.5" />
        <polygon class="hx-inner" points="16.5,12 14.25,15.9 9.75,15.9 7.5,12 9.75,8.1 14.25,8.1" stroke-width="1.5" />
      </symbol>
    </svg>
    <header class="topbar">
      <!-- The mark is docs/design/flanj-mark-mono.svg inlined (currentColor, so it
           themes with the ink); the wordmark is text, never an image. -->
      <div class="brand">
        <svg class="brand-mark" viewBox="0 0 512 512" fill="none" aria-hidden="true" focusable="false">
          <path d="M15 376.5H106M106 376.5V346.5H166.5M106 376.5V407H166.5M166.5 346.5V407M166.5 346.5V286H106V226H166.5V165.5M166.5 407V435.5H227V256.25V77H166.5V105.5M15 136H106M106 136V165.5H166.5M106 136V105.5H166.5M166.5 165.5V105.5M498 136H407M407 136V165.5H346.5M407 136V105.5H346.5M346.5 165.5V105.5M346.5 165.5V226H407V286H346.5V346.5M346.5 105.5V77H286V136M498 376.5H407M407 376.5V346.5H346.5M407 376.5V407H346.5M346.5 346.5V407M346.5 407V435.5H286V376.5M286 136H227M286 136V376.5M286 376.5H227" stroke="currentColor" stroke-width="15" stroke-linecap="round" stroke-linejoin="round" />
          <path d="M198.5 407.5V256M317 41H198.5V256M198.5 256H133M316.5 104.5V256M198 471H316.5V256M316.5 256H382" stroke="currentColor" stroke-width="26" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        <span class="brand-name">Flanj</span><span class="brand-product">Collector</span>
        <!-- Where this UI is served from and which build serves it — the sheet's
             own address line, in mono. -->
        <span v-if="health?.collector_version" class="brand-addr">{{ uiHost }} · {{ health.collector_version }}</span>
      </div>
      <div class="meta" v-if="health">
        <!-- Org identity only — never the integration slug (it scopes a spec,
             not this org; it lives on the Overview headline + its Contracts card). -->
        <span v-if="orgPillName" class="pill pill-name" title="Your organization — shown to the provider on every thread.">{{ orgPillName }}</span>
        <span v-if="!health.cp_configured" class="pill warn">control plane not configured</span>
        <!-- Connected: the pill is the one door out to the control plane. The
             LABEL stays the status ("Connected") — a status indicator that hides
             its state on hover would trade a fact for a hint, and there is no
             hover at all on touch — while the tooltip and the ↗ carry the
             destination. Not connected / pending: unchanged, it still opens
             Settings so the next step is the one you need. -->
        <a
          v-else-if="connectStatus === 'connected' && dashboardUrl"
          class="pill pill-btn ok pill-link"
          :href="dashboardUrl"
          target="_blank"
          rel="noopener noreferrer"
          title="Go to your Flanj dashboard"
        >
          <svg class="hx sm tone-ok" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>{{ connectPill }}<span class="pill-out" aria-hidden="true">↗</span>
        </a>
        <button
          v-else
          type="button"
          class="pill pill-btn"
          :class="{ ok: connectStatus === 'connected', warn: connectStatus === 'pending' }"
          title="Connect settings"
          @click="setTab('settings')"
        >
          <svg class="hx sm" :class="pillTone" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>{{ connectPill }}
        </button>
      </div>
    </header>

    <p v-if="loadError" class="error banner">Failed to load: {{ loadError }}</p>

    <!-- One-time light-default notice (ux-design-v2 §3.4). Reuses the shipped
         dismissible-banner component — no new component, no modal, no
         interstitial. Above the tab strip so it shows on whichever tab is
         opened first, exactly once per browser. -->
    <div v-if="showThemeFlipNotice" class="connect-banner info theme-flip-banner">
      <span>Flanj is light by default now. Dark is in Settings → Appearance.</span>
      <span class="connect-banner-actions">
        <button type="button" class="btn small" @click="openAppearance">Open Appearance</button>
        <button type="button" class="btn ghost small" aria-label="Dismiss" @click="dismissThemeFlipNotice">Dismiss</button>
      </span>
    </div>

    <nav class="tabs" role="tablist">
      <button role="tab" :aria-selected="tab === 'overview'" :class="{ active: tab === 'overview' }" @click="setTab('overview')">
        Overview
      </button>
      <button role="tab" :aria-selected="tab === 'traffic'" :class="{ active: tab === 'traffic' }" @click="setTab('traffic')">
        Traffic
      </button>
      <button role="tab" :aria-selected="tab === 'contract'" :class="{ active: tab === 'contract' }" @click="setTab('contract')">
        Contracts
        <!-- red = act (breaking) · copper = review (informational, un-acked) -->
        <span v-if="contractBreakingCount" class="tab-count bad" :title="breakingCountTitle(contractBreakingCount)">{{ contractBreakingCount }}</span>
        <span v-if="contractInfoCount" class="tab-count warn" :class="{ desc: contractDescOnly }" :title="contractDescOnly ? descriptionCountTitle(contractInfoCount) : informationalCountTitle(contractInfoCount)">{{ contractInfoCount }}</span>
      </button>
      <button role="tab" :aria-selected="tab === 'threads'" :class="{ active: tab === 'threads' }" @click="setTab('threads')">
        Threads
        <span v-if="threads.length" class="tab-count">{{ threads.length }}</span>
      </button>
      <button role="tab" class="tab-right" :aria-selected="tab === 'settings'" :class="{ active: tab === 'settings' }" @click="setTab('settings')">
        Settings
        <span v-if="health?.cp_configured && connectStatus !== 'connected'" class="tab-dot" :class="connectStatus"></span>
      </button>
    </nav>

    <!-- ───────────────────────── OVERVIEW ───────────────────────── -->
    <div v-show="tab === 'overview'" class="panel">
      <!-- Three tones, not two: `neutral` is the install where nothing has been
           validated yet, and it must read as neither the green all-clear nor
           the red drift banner (ui/src/headline.ts). -->
      <section class="headline" :class="headline.tone">
        <svg class="hx" :class="toneClass(headline.tone)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>
        <div>
          <!-- Observed state only — the collector does not measure provider
               health. No "You:" prefix (Blueprint): the subline carries scope. -->
          <div class="hl-you">
            <strong>{{ headline.you }}</strong>
          </div>
          <!-- Pre-traffic honesty: no integration observed on a REST edge yet →
               the fragment is simply absent (no replacement copy). An MCP edge
               alone does not count — the slug is a REST integration's name, and
               the MCP server has its own line below (ui/src/headline.ts). -->
          <div v-if="headline.integration" class="hl-sub">observed here, on integration <code>{{ headline.integration }}</code></div>
        </div>
      </section>

      <!-- MCP servers (v0.5): one headline per observed server (deck §2), on
           the same three tones as the REST line: a server whose snapshot has
           validated nothing yet is neutral, not green (ui/src/mcp.ts). -->
      <section
        v-for="m in mcpOverview"
        :key="'mcp-hl-' + m.key"
        class="headline mcp-headline"
        :class="m.headline.tone"
      >
        <svg class="hx" :class="toneClass(m.headline.tone)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>
        <!-- The deck's sentence stays whole (`Server: … You: …`): its clause is
             pinned lowercase by e2e headline-fresh, so it is the one line that
             keeps its pivot. A description-only change names itself in the
             clause but never takes the drift tone (ui/src/mcp.ts). -->
        <div class="hl-you">
          <span class="mcp-badge" :title="MCP_BADGE_TOOLTIP">MCP</span>
          <strong>{{ m.headline.text }}</strong>
        </div>
      </section>

      <!-- Local notices band (deck §2): stale-client items ONLY since
           qfix2-2026-08-26 — description-only changes moved to the Contracts
           tab when they became flaggable. Visible to you only; NO flag control
           here, ever, and nothing here is ackable either. -->
      <section v-if="localNotices.length" class="local-notices">
        <div class="ln-head">
          <span class="ln-title">{{ LOCAL_NOTICES_TITLE }}</span>
          <span class="ln-sub">{{ localNoticesSubFor(localNoticesProviders) }}</span>
        </div>
        <ul class="ln-list">
          <li v-for="n in localNotices" :key="n.id" class="ln-item">{{ n.line }}</li>
        </ul>
      </section>

      <section>
        <h2>
          Edges <small>discovered from traffic — external only</small>
        </h2>
        <p v-if="edges.length === 0" class="empty">
          No external edges discovered yet. Send some traffic through the SDK and your
          integration graph appears here automatically.
        </p>

        <div v-else class="edge-groups">
          <div class="edge-group">
            <h3 class="edge-title">
              <span class="dir-badge in">Inbound</span> consumer → you
              <small>this org is the provider</small>
            </h3>
            <p v-if="inboundEdges.length === 0" class="empty small">No inbound edges.</p>
            <div v-else class="edge-table">
              <!-- The evidence the kit's table carries: calls in the window, drifted
                   calls (red mono when any), last seen. The observed rate rides as a
                   muted suffix on the call count and only when it is non-zero — a
                   column of `0 /min` made a live install look dead. -->
              <div class="edge-head">
                <span>peer host</span><span>calls</span><span>drift</span><span>last seen</span>
              </div>
              <div v-for="e in inboundEdges" :key="'i-' + e.peer_host" class="edge-row" :class="{ drift: e.drift_count > 0 }">
                <span class="peer mono">
                  {{ e.peer_host }}
                  <span v-if="mcpHosts.has(e.peer_host)" class="mcp-badge" :title="MCP_BADGE_TOOLTIP">{{ mcpBadgeLabel(e.class) }}</span>
                </span>
                <span class="num calls">{{ e.call_count }}<span v-if="e.rpm" class="unit">· {{ fmtRPM(e.rpm) }}/min</span></span>
                <span class="num drift-n" :class="{ some: e.drift_count > 0 }">{{ e.drift_count }}</span>
                <span class="num seen">{{ timeAgo(e.last_seen) }}</span>
              </div>
            </div>
          </div>

          <div class="edge-group">
            <h3 class="edge-title">
              <span class="dir-badge out">Outbound</span> you → provider
              <small>this org is the consumer</small>
            </h3>
            <!-- The roll call. Counted POSITIVE, one line for the whole panel,
                 and it sits BELOW a fully rendered graph: the zero-config
                 install-to-graph moment is untouched and nothing is gated. -->
            <p v-if="outboundRollCall && outboundEdges.length" class="edge-rollcall">{{ outboundRollCall }}</p>
            <p v-if="outboundEdges.length === 0" class="empty small">No outbound edges.</p>
            <div v-else class="edge-table">
              <div class="edge-head named">
                <span>provider</span><span>calls</span><span>drift</span><span>last seen</span><span></span>
              </div>
              <!-- A NAME renders OVER the host, never instead of it — the registrable domain
                   stays visible (it is the identity; the name is decoration). An UNNAMED row
                   has no name to render over anything, so it shows the host once, plain, with
                   the `auto` badge — never a title-cased pseudo-name, never an empty state. -->
              <template v-for="e in outboundEdges" :key="'o-' + e.peer_host">
                <div class="edge-row named" :class="{ drift: e.drift_count > 0 }">
                  <span class="edge-name-cell">
                    <span class="edge-name-line">
                      <!-- Unnamed rows render the HOST ITSELF, once, in the name slot — never a
                           title-cased pseudo-name. humanize() is the integration-SLUG helper
                           (splits on -/_, never dots), so humanize('api.stripe.com') is
                           'Api.stripe.com': a mangled duplicate of the host line right below it.
                           The domain is the identity; when there is no name there is nothing to
                           render over it. -->
                      <span class="edge-name" :class="{ unnamed: !e.display_name }" :title="e.display_name || e.peer_host">{{ e.display_name || e.peer_host }}</span>
                      <span v-if="mcpHosts.has(e.peer_host)" class="mcp-badge" :title="MCP_BADGE_TOOLTIP">{{ mcpBadgeLabel(e.class) }}</span>
                    </span>
                    <span v-if="e.display_name" class="peer mono edge-host" :title="e.peer_host">{{ e.peer_host }}</span>
                    <!-- Coverage, in the SAME muted text channel that renders
                         the host — not the badge lane, not the actions cell.
                         Zero new chips: on ~40 rows a chip is a wall, and a
                         chip would be a label where a control does more work.
                         MCP rows get nothing new — their transport badge and
                         its tooltip already say "covered, self-delivering,
                         nothing for you to do". -->
                    <span v-if="!mcpHosts.has(e.peer_host) && contractsKnown" class="edge-contract">
                      <template v-if="contractByHost.get(e.peer_host)">
                        <button type="button" class="edge-contract-link" @click="goToContracts(e.peer_host)">
                          {{ edgeContractLine(contractByHost.get(e.peer_host)!) }}
                        </button>
                      </template>
                      <button v-else type="button" class="edge-contract-link add" @click="goToContracts(e.peer_host)">
                        {{ ADD_CONTRACT }}
                      </button>
                    </span>
                  </span>
                  <span class="num calls">{{ e.call_count }}<span v-if="e.rpm" class="unit">· {{ fmtRPM(e.rpm) }}/min</span></span>
                  <span class="num drift-n" :class="{ some: e.drift_count > 0 }">{{ e.drift_count }}</span>
                  <span class="num seen">{{ timeAgo(e.last_seen) }}</span>
                  <span class="edge-actions">
                    <!-- v1 phase 4. It sits FIRST because it is the only action
                         on this row that reaches the other org; Rename is
                         local housekeeping beside it. -->
                    <button type="button" class="btn ghost small" @click="openEdgeSheet(e)">{{ START_THREAD_LABEL }}</button>
                    <button type="button" class="btn ghost small" @click="startRename(e)">{{ renameLabel(e.name_source) }}</button>
                    <button
                      v-if="e.name_source === 'user'"
                      type="button"
                      class="btn ghost small"
                      :disabled="removeNameBusy[edgeDomain(e)]"
                      @click="removeName(e)"
                    >{{ REMOVE_NAME_LABEL }}</button>
                  </span>
                </div>
                <div v-if="nameEdit && nameEdit.host === e.peer_host" class="edge-rename">
                  <input
                    ref="renameInput"
                    class="edge-rename-input"
                    type="text"
                    :placeholder="placeholderFor(nameEdit.domain)"
                    :value="nameEdit.draft"
                    :disabled="nameEdit.busy"
                    @input="onRenameInput"
                    @keydown.enter.prevent="saveRename"
                    @keydown.esc.prevent="cancelRename"
                  />
                  <!-- The opt-in: per mapping, default UNCHECKED, names the egress plainly. -->
                  <label class="edge-suggest">
                    <input type="checkbox" :checked="nameEdit.suggest" :disabled="nameEdit.busy" @change="onSuggestToggle" />
                    <span>{{ suggestLabelFor(nameEdit.domain) }}</span>
                  </label>
                  <!-- The directory-cap pre-check: shown only while the box is
                       ticked AND the name exceeds 64 chars; Save is blocked,
                       unticking (or shortening) saves fine. -->
                  <p v-if="suggestTooLong(nameEdit)" class="edge-name-note">{{ SUGGEST_TOO_LONG }}</p>
                  <div class="edge-rename-actions">
                    <button type="button" class="btn primary small" :disabled="nameEdit.busy || suggestTooLong(nameEdit)" @click="saveRename">{{ SAVE_LABEL }}</button>
                    <button type="button" class="btn ghost small" :disabled="nameEdit.busy" @click="cancelRename">{{ CANCEL_LABEL }}</button>
                    <span v-if="nameEdit.error" class="error small-err">
                      {{ nameEdit.error }}
                      <button type="button" class="btn ghost small" @click="saveRename">{{ RETRY_LABEL }}</button>
                    </span>
                  </div>
                </div>
                <p v-if="nameNotice[edgeDomain(e)]" class="edge-name-note">{{ nameNotice[edgeDomain(e)] }}</p>
                <p v-if="removeNameError[edgeDomain(e)]" class="edge-name-note error small-err">
                  {{ removeNameError[edgeDomain(e)] }}
                  <button type="button" class="btn ghost small" @click="removeName(e)">{{ RETRY_LABEL }}</button>
                </p>
              </template>
            </div>
          </div>
        </div>
        <p class="hint">
          Internal same-team edges (RFC1918 / cluster-local / single-label hosts) are
          classified out and never surfaced or body-captured.
        </p>
      </section>

      <p class="hint">
        Collector <code>{{ health?.collector_version }}</code> · captures inbound &amp; outbound ·
        redacted at source · UI serves on localhost only. Open <strong>Contracts</strong> to review
        drift, <strong>Traffic</strong> to browse your captured calls.
      </p>
    </div>

    <!-- ───────────────────────── CONTRACTS ───────────────────────── -->
    <div v-show="tab === 'contract'" class="panel">
      <div v-if="health?.cp_configured && connectStatus !== 'connected' && !connectBannerDismissed" class="connect-banner">
        <span>
          <strong>{{ connectStatus === 'pending' ? 'Confirm your contact' : 'Not connected' }}</strong> —
          thread links need a Connected collector. Viewing your own traffic and findings never does.
        </span>
        <span class="connect-banner-actions">
          <button type="button" class="btn small" @click="setTab('settings')">{{ connectStatus === 'pending' ? 'Check status' : 'Connect' }}</button>
          <button type="button" class="btn ghost small" aria-label="Dismiss" @click="dismissConnectBanner">Dismiss</button>
        </span>
      </div>
      <p v-if="uploadNotice" class="upload-notice">{{ uploadNotice }}</p>
      <p v-if="uploadError" class="upload-notice error">{{ uploadError }}</p>

      <section v-for="g in cardGroups" v-show="g.cards.length || g.emptyText" :key="g.key">
        <h2>
          {{ g.title }}
          <small>{{ g.sub }}</small>
        </h2>
        <p v-if="g.cards.length === 0" class="empty">{{ g.emptyText }}</p>

        <article
          v-for="p in g.cards"
          :id="p.peerHost ? 'contract-' + p.peerHost : undefined"
          :key="p.key"
          class="provider"
          :class="{ self: g.key === 'self' }"
        >
          <div class="prov-head">
            <!-- `<name> · <origin>`, ALWAYS — not only on collision. Two MCP
                 servers can publish the SAME serverInfo.name (the live stack
                 does), and the origin is the only thing that separates them.
                 Collision-conditional would change a card's shape when an
                 unrelated second server appears, and at fifty cards nobody can
                 see whether a title is unique — so a bare name could never be
                 trusted to mean "the only one".
                 The separate mono host chip is GONE: the host is in the heading
                 now, and rendering it twice in two type sizes said nothing. -->
            <span class="prov-name">{{ p.name }}</span>
            <span v-if="p.spec && contractOrigin(p.spec)" class="prov-origin mono">· {{ contractOrigin(p.spec) }}</span>
            <span v-if="p.spec" class="fmt-badge">{{ p.spec.format === 'mcp' ? mcpBadgeLabel(p.spec.edge_class) : p.spec.format }}</span>
            <span v-if="p.spec?.version" class="prov-ver">v{{ p.spec.version }}</span>
            <!-- The integration slug lives here (it scopes THIS contract), not in the header.
                 On EVERY card that has one: the two MCP servers share a name (`acme-tools-mcp`), so
                 the slug is what tells `acme-tools` from `acme-tools-stdio`. -->
            <span v-if="p.spec?.integration" class="prov-integration mono">integration: {{ p.spec.integration }}</span>
            <span class="prov-status">
              <!-- FIRST in the lane: it explains the chips beside it. A card
                   whose document no front can read shows "no calls validated
                   yet" forever, and without this the operator reads that as a
                   quiet edge rather than as a channel that is refusing. -->
              <span v-if="cardOverCap(p)" class="tag warn">{{ CONTRACT_OVER_CAP_TAG }}</span>
              <!-- Tier-split chips — same taxonomy as the tab pills, so the sums always agree. -->
              <span v-if="cardBreakingCount(p)" class="tag drift">{{ breakingChipLabel(cardBreakingCount(p)) }}</span>
              <span v-if="cardNonBreakingCount(p)" class="tag warn" :title="cardNonBreakingTitle(p)">{{ informationalChipLabel(cardNonBreakingCount(p)) }}</span>
              <!-- A wording change is not a warning: steel outline, the row badge's own word. -->
              <span v-if="cardDescriptionCount(p)" class="tag desc" :title="descriptionChipTitle(cardDescriptionCount(p))">{{ descriptionChipLabel(cardDescriptionCount(p)) }}</span>
              <span
                v-if="!cardBreakingCount(p) && !cardInfoCount(p) && p.spec && cardValidatedCalls(p)"
                class="tag ok"
              >conforming</span>
              <!-- Loaded, but nothing has run against it yet: "conforming" would
                   be a clean bill of health nobody performed. -->
              <span
                v-else-if="!cardBreakingCount(p) && !cardInfoCount(p) && p.spec"
                class="tag none"
              >no calls validated yet</span>
              <span v-else-if="!cardBreakingCount(p) && !cardInfoCount(p)" class="tag none">no contract loaded</span>
            </span>
          </div>

          <!-- What the chip above means, in the muted text channel the card
               already uses for a per-row explanation (NO_CONTRACT_ROW sits in
               the same slot). Names the size, the overage, and both branches of
               what a front does with it — this surface cannot know which front
               holds a baseline, and guessing would be a statement. -->
          <p v-if="cardOverCap(p) && p.spec" class="prov-oversize">{{ contractOverCapLine(p.spec) }}</p>

          <ContractUploader
            v-if="uploadFor === p.peerHost && p.peerHost"
            :host="p.peerHost"
            @uploaded="onUploaded"
            @cancel="closeUploader"
            @dirty="uploaderDirty = $event"
          />

          <div v-if="p.spec" class="prov-links">
            <a class="doc-link" :href="specHref(p.spec)" target="_blank" rel="noopener">
              {{ p.spec.format === 'mcp' ? 'View tools/list snapshot' : 'View OpenAPI spec' }}
            </a>
            <a v-if="p.spec.docs_url" class="doc-link" :href="p.spec.docs_url" target="_blank" rel="noopener">
              API docs ↗
            </a>
            <template v-if="p.spec.format === 'mcp'">
              <span class="prov-meta">{{ mcpContractMeta(p.spec.endpoints || 0, humanTime(p.spec.loaded_at)) }}</span>
            </template>
            <template v-else>
              <!-- Provenance + recency, relative, with the absolute time on
                   hover. `Replace` sits immediately beside the date on purpose:
                   the affordance next to it is what stops a date from reading
                   as a nag. -->
              <span class="prov-meta" :title="humanTime(p.spec.loaded_at)">{{ contractMeta(p.spec) }}</span>
              <template v-if="isUploaded(p.spec)">
                <button type="button" class="btn ghost small" @click="openUploader(p.peerHost)">{{ REPLACE_CONTRACT }}</button>
                <button type="button" class="btn ghost small" @click="removeContract(p.spec.integration, p.peerHost)">Remove</button>
              </template>
            </template>
            <!-- How much evidence is actually behind the chip above. ZERO is
                 the point: it is the state that used to read CONFORMING, and a
                 contract bound to the wrong host has no other symptom at all —
                 no error, no finding, a card that looks finished. -->
            <span class="prov-meta evidence">{{ cardEvidenceMeta(p) }}</span>
          </div>
          <p v-else-if="mcpHosts.has(p.peerHost)" class="prov-nospec">{{ MCP_NO_SPEC_NEEDED }}</p>
          <p v-else class="prov-nospec">{{ NO_CONTRACT_ROW }}</p>

          <!-- MCP per-tool rows (deck §3): the server's tools ARE the contract surface. -->
          <div v-if="p.spec?.format === 'mcp' && mcpToolRows(p.spec.integration).length" class="tool-rows">
            <div v-for="t in mcpToolRows(p.spec.integration)" :key="t.name" class="tool-row">
              <div class="tool-line">
                <span class="tool-name mono">{{ t.name }}</span>
                <span class="tool-tag" :class="{ partial: !t.hasOutputSchema }">{{ toolContractLabel(t.hasOutputSchema) }}</span>
              </div>
              <p v-if="!t.hasOutputSchema" class="tool-note">
                {{ noOutputContractNote(p.spec.title || p.spec.integration, t.name) }}
              </p>
            </div>
          </div>

          <!-- Acked rows stay in place, dimmed — evidence is never hidden.
               The id is the `#contracts/<finding_id>` deep-link anchor: the
               control plane's findings index lands on this exact row. -->
          <article v-for="f in p.findings" :id="'finding-' + f.id" :key="f.id" class="finding nested" :class="{ acked: isAcked(f), highlight: f.id === highlightFindingId }">
            <div class="finding-head">
              <!-- definition_change rows carry the classifier's class badge (deck §3):
                   BREAKING red · NON-BREAKING copper · DESCRIPTION steel — each
                   badge matches the tab pill and the card chip that count it. -->
              <span
                v-if="f.kind === 'definition_change'"
                class="badge"
                :class="{ breaking: definitionClass(f) === 'BREAKING', warning: definitionClass(f) === 'NON-BREAKING', description: definitionClass(f) === 'DESCRIPTION' }"
              ><svg class="hx sm" :class="badgeTone(f)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>{{ definitionClass(f) }}<svg class="hx sm" :class="badgeTone(f)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg></span>
              <span v-else class="badge" :class="f.severity"><svg class="hx sm" :class="badgeTone(f)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>{{ f.severity }}<svg class="hx sm" :class="badgeTone(f)" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg></span>
              <span class="endpoint">{{ f.endpoint }}</span>
              <span class="rule">{{ f.rule }}</span>
              <!-- Call counts belong to call-evidenced kinds only. A
                   definition_change and a version-diff are both found by
                   comparing two documents, so "1 call" would be a fabricated
                   count of evidence that does not exist. -->
              <template v-if="f.kind !== 'definition_change' && f.kind !== 'version-diff'">
                <span v-if="f.occurrence_count && f.occurrence_count > 1" class="occ" title="calls carrying this same drift">
                  ×{{ f.occurrence_count }} calls
                </span>
                <span v-else class="occ single">1 call</span>
              </template>
            </div>
            <!-- definition_change: their tools/list at T1 vs at T2 (deck §3).
                 DESCRIPTION rows render the diff PLAIN — a wording change is
                 not a severity diff. -->
            <!-- The snapshot labels carry a digest and a timestamp — machine
                 identifiers, so `.snap` keeps the eyebrow's case (no uppercase). -->
            <div v-if="f.kind === 'definition_change'" class="drift-row two" :class="{ plain: definitionClass(f) === 'DESCRIPTION' }">
              <div class="col">
                <div class="k snap">{{ beforeColLabel(f.spec_version_from || '', snapshotStamps(f.detail).from) }}</div>
                <div class="v expected">{{ f.expected }}</div>
              </div>
              <div class="arrow">≠</div>
              <div class="col">
                <div class="k snap">{{ afterColLabel(f.spec_version_to || '', snapshotStamps(f.detail).to) }}</div>
                <div class="v actual">{{ f.actual }}</div>
              </div>
            </div>
            <!-- version-diff: the contract you replaced vs the one you uploaded.
                 `expected`/`actual` already ARE the two versions, so only the
                 labels change — neither side is "live", and there is no
                 location, because the change is in the documents. The `detail`
                 paragraph below names the field the rule fired on. Plain ink on
                 both sides: neither document is a verdict, and green is spent
                 on reached verdicts only. -->
            <div v-else-if="f.kind === 'version-diff'" class="drift-row two plain">
              <div class="col">
                <div class="k">replaced</div>
                <div class="v expected">{{ f.expected }}</div>
              </div>
              <div class="arrow">≠</div>
              <div class="col">
                <div class="k">uploaded</div>
                <div class="v actual">{{ f.actual }}</div>
              </div>
            </div>
            <div v-else class="drift-row">
              <div class="col">
                <div class="k">{{ f.kind === 'output_mismatch' ? 'declared (their outputSchema)' : 'expected (per spec)' }}</div>
                <div class="v expected">{{ f.expected }}</div>
              </div>
              <div class="arrow">≠</div>
              <div class="col">
                <div class="k">{{ f.kind === 'output_mismatch' ? 'got (structuredContent)' : 'actual (live)' }}</div>
                <div class="v actual">{{ f.actual }}</div>
              </div>
              <div class="col loc">
                <div class="k">location</div>
                <div class="v mono">{{ f.location }}</div>
              </div>
            </div>
            <p class="detail" v-if="f.kind === 'definition_change'">
              {{ defChangeDetail(snapshotStamps(f.detail).from, snapshotStamps(f.detail).to, providerNameFor(f)) }}
            </p>
            <p class="detail" v-else-if="f.detail">{{ f.detail }}</p>

            <div class="corr" v-if="correlationFor(f)">
              <span class="corr-title">correlation keys</span>
              <span v-if="correlationFor(f)!.request_id" class="corr-k">
                request-id <code>{{ correlationFor(f)!.request_id }}</code>
              </span>
              <span v-if="correlationFor(f)!.client_request_id" class="corr-k" :title="JSONRPC_ID_TITLE">
                {{ JSONRPC_ID_TITLE }} <code>{{ correlationFor(f)!.client_request_id }}</code>
              </span>
              <span v-if="correlationFor(f)!.idempotency_key" class="corr-k">
                idempotency-key <code>{{ correlationFor(f)!.idempotency_key }}</code>
              </span>
              <span v-if="correlationFor(f)!.trace_id" class="corr-k">
                trace-id <code>{{ correlationFor(f)!.trace_id }}</code>
              </span>
            </div>

            <div class="actions">
              <template v-if="threadsByFinding[f.id]">
                <span class="chip" :class="{ attention: chipAttention(threadsByFinding[f.id]) }">
                  <svg class="hx sm" :class="chipAttention(threadsByFinding[f.id]) ? 'tone-accent' : 'tone-info'" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>{{ chipLabel(threadsByFinding[f.id]) }}
                </span>
                <button type="button" class="btn small" :disabled="openingThread === threadsByFinding[f.id].thread_id" @click="openChipThread(threadsByFinding[f.id].thread_id)">
                  {{ openingThread === threadsByFinding[f.id].thread_id ? 'Opening…' : 'View thread' }}
                </button>
                <button type="button" class="btn ghost small" @click="goToThread(threadsByFinding[f.id].thread_id)">Threads ›</button>
                <span v-if="chipError[threadsByFinding[f.id].thread_id]" class="error small-err">{{ chipError[threadsByFinding[f.id].thread_id] }}</span>
              </template>
              <!-- Acknowledged (DESCRIPTION / NON-BREAKING only): the footer swaps
                   to the acked line + Undo. Local-only; never touches the CP. -->
              <template v-else-if="isAcked(f)">
                <span class="hint-inline">{{ ackedLine(timeAgo(f.acked_at)) }}</span>
                <button type="button" class="btn ghost small" :disabled="ackBusy[f.id]" :title="UNDO_TITLE" @click="setAck(f, false)">{{ UNDO_LABEL }}</button>
                <span v-if="ackError[f.id]" class="error small-err">{{ ackError[f.id] }}</span>
              </template>
              <!-- Evidence rule (v0.5 §6): local notices NEVER carry a flag control.
                   stale_client only — and it never reaches the Contracts tab
                   (mcpContractFindings excludes it), so this branch is a GUARD,
                   not a surface: it renders nothing, and its whole job is to
                   swallow a stale_client row before any Flag control below can
                   claim it. Deliberately empty — do not give it content. -->
              <template v-else-if="isLocalNotice(f)"><!-- no control, by design --></template>
              <!-- Thread state comes from the control plane and nowhere else.
                   Until the list has been answered once, this finding may well
                   already be in a thread — offering Create thread would be a
                   claim we cannot make. Say what we don't know instead. -->
              <template v-else-if="!threadsKnown"><span class="hint-inline">{{ THREAD_STATE_UNKNOWN }}</span></template>
              <!-- definition_change, EVERY class incl. DESCRIPTION (ux-design-v2
                   §2.7): flaggable and CALL-LESS. The control is never born
                   disabled — the relay lifted 400 finding_has_no_call for this
                   kind. `Flag this` keeps primary styling; the shared hint says
                   what stands in for the call. -->
              <template v-else-if="f.kind === 'definition_change'">
                <button type="button" class="btn primary flag" @click="openSheet(f)">Flag this</button>
                <span class="hint-inline">{{ defChangeNoCallSub(providerNameFor(f)) }}</span>
                <button v-if="isAckable(f)" type="button" class="btn ghost small" :disabled="ackBusy[f.id]" :title="ACK_TITLE" @click="setAck(f, true)">{{ ACK_LABEL }}</button>
                <span v-if="ackError[f.id]" class="error small-err">{{ ackError[f.id] }}</span>
              </template>
              <!-- The !isLocalNotice guards are redundant with the branch above
                   and deliberately so: a stale_client row must NEVER reach a
                   Flag control, and one guard is one edit away from being lost.
                   The trailing hint is likewise a fallback no row reaches today
                   — the trailing hint is where a CALL-LESS kind lands, and
                   since the version diffs joined contractTabRows that is a real
                   surface, not a fallback: a version-diff has no source call by
                   construction, so every one of its rows renders it. -->
              <!-- v1p4: a finding with NO source call is flaggable now — the
                   relay stopped answering 400 finding_has_no_call for every
                   kind, so a version diff (which has no call by construction)
                   gets the same control as everything else. The hint stays
                   beside it and says what the thread carries instead; it used
                   to say the row "can't be flagged from here", which is the
                   sentence this phase exists to delete. -->
              <template v-else-if="!isLocalNotice(f)">
                <button type="button" class="btn primary flag" @click="openSheet(f)">Flag this</button>
                <span v-if="!f.source_call_id" class="hint-inline">{{ VERSION_DIFF_NO_CALL }}</span>
              </template>
            </div>
          </article>
        </article>
      </section>

      <!-- Providers with traffic and no contract. ONE ROW EACH, collapsed by
           default — a card apiece was ~40 near-identical empty cards on a
           50-provider estate, which buried the real contracts above it. A card
           materialises when a contract does; until then the row IS the fix. -->
      <section v-if="uncoveredHosts.length" class="uncovered">
        <h2>
          <button type="button" class="uncovered-toggle" :aria-expanded="uncoveredOpen" @click="uncoveredOpen = !uncoveredOpen">
            <span class="chev" :class="{ open: uncoveredOpen }">▸</span>
            {{ uncoveredHeading(uncoveredHosts.length) }}
          </button>
        </h2>
        <!-- Said ONCE for the section, not once per row. Rendered at 28 real
             rows the identical sentence repeated 28 times was its own wall —
             the thing this section exists to replace — and it is a property of
             the group anyway, not of any particular provider. -->
        <p v-show="uncoveredOpen" class="uncovered-lede">{{ NO_CONTRACT_SECTION }}</p>
        <div v-show="uncoveredOpen" class="uncovered-rows">
          <div
            v-for="host in uncoveredHosts"
            :id="'uncovered-' + host"
            :key="host"
            class="uncovered-row"
            :class="{ highlight: host === highlightUncoveredHost }"
          >
            <div class="uncovered-line">
              <span class="mono uncovered-host">{{ host }}</span>
              <button
                v-if="uploadFor !== host"
                type="button"
                class="btn ghost small"
                @click="openUploader(host)"
              >{{ ADD_CONTRACT }}</button>
            </div>
            <ContractUploader
              v-if="uploadFor === host"
              :host="host"
              @uploaded="onUploaded"
              @cancel="closeUploader"
              @dirty="uploaderDirty = $event"
            />
          </div>
        </div>
      </section>

      <!-- Pre-traffic upload: on a fresh install there are no edges at all, so
           there is nothing to click Add contract ON. Naming the host by hand is
           the only route in, and it must exist from the first paint. -->
      <section class="pretraffic">
        <h2>Add a contract <small>for a provider you haven’t sent traffic to yet</small></h2>
        <button v-if="uploadFor !== '*'" type="button" class="btn" @click="openUploader('')">{{ ADD_CONTRACT }}</button>
        <ContractUploader v-else host="" @uploaded="onUploaded" @cancel="closeUploader" @dirty="uploaderDirty = $event" />
      </section>
    </div>

    <!-- ───────────────────────── THREADS ───────────────────────── -->
    <div v-show="tab === 'threads'" class="panel">
      <div v-if="showAddressNudge" class="connect-banner info">
        <span>Reply notification emails can link straight back to the thread here. Add this collector's address to turn that on.</span>
        <span class="connect-banner-actions">
          <button type="button" class="btn small" @click="addCollectorAddress">Add address</button>
          <button type="button" class="btn ghost small" aria-label="Dismiss" @click="dismissAddressNudge">Dismiss</button>
        </span>
      </div>
      <ThreadsTab
        :rows="threads"
        :loaded="threadsLoaded"
        :highlight-id="highlightThreadId"
        :load-error="threadsError"
        :notice="threadsNotice"
        :total="threadsTotal"
        :has-more="threadsHasMore"
        :dashboard-url="dashboardUrl"
        @connect="goToSettings"
      />
    </div>

    <!-- ───────────────────────── SETTINGS ───────────────────────── -->
    <div v-show="tab === 'settings'" class="panel settings-grid">
      <section>
        <h2>Settings <small>this collector · {{ health?.collector_version }}</small></h2>
        <p v-if="health && !health.cp_configured" class="empty">
          The control plane is not configured on this collector (set <code>cp_base_url</code> and <code>cp_deploy_token</code>). Local capture, detection and this UI work without it.
        </p>
        <ConnectPanel
          v-else
          :state="connect"
          :default-org="health?.consumer_display_name"
          :address-nudge-dismissed="addressNudgeDismissed"
          :focus-address-tick="focusAddressTick"
          @update:state="onConnectUpdated"
          @dismiss-address-nudge="dismissAddressNudge"
        />
      </section>
      <section>
        <h2>Appearance</h2>
        <div class="theme-field">
          <span class="theme-label">Theme</span>
          <!-- Exactly two segments (ux-design-v2 §3.2). The System segment and
               the OS-setting helper line beside it are DELETED, not re-worded —
               the replacement states the two consequences that matter: what the
               default is, and that the choice is per-browser. -->
          <div class="seg" role="group" aria-label="Theme">
            <button type="button" :class="{ active: themePref === 'light' }" :aria-pressed="themePref === 'light'" @click="setTheme('light')">Light</button>
            <button type="button" :class="{ active: themePref === 'dark' }" :aria-pressed="themePref === 'dark'" @click="setTheme('dark')">Dark</button>
          </div>
          <span class="theme-help">Light by default. Dark is remembered on this browser only.</span>
        </div>
      </section>
    </div>

    <!-- ───────────────────────── TRAFFIC ───────────────────────── -->
    <div v-show="tab === 'traffic'" class="panel">
      <section>
        <h2>
          Traffic <small>recent captured calls — redacted at source, read-only</small>
        </h2>
        <p v-if="displayCalls.length === 0 && !filtersActive" class="empty">No calls captured yet.</p>

        <template v-else>
          <div class="tr-toolbar">
            <input
              v-model="q"
              type="search"
              class="tr-search"
              placeholder="Search route, status, correlation…  (-token excludes)"
              aria-label="Search calls"
            />
            <select v-model="fDirection" class="tr-select" aria-label="Filter by direction">
              <option value="">direction: all</option>
              <option value="server">inbound</option>
              <option value="client">outbound</option>
            </select>
            <select v-model="fPeer" class="tr-select" aria-label="Filter by counterparty">
              <option value="">counterparty: all</option>
              <option v-for="p in peerOptions" :key="p" :value="p">{{ internalPeers.has(p) ? p + ' · internal' : p }}</option>
            </select>
            <select v-model="fMethod" class="tr-select" aria-label="Filter by method">
              <option value="">method: all</option>
              <option v-for="m in methodOptions" :key="m" :value="m">{{ m }}</option>
            </select>
            <select v-model="fStatus" class="tr-select" aria-label="Filter by status">
              <option value="">status: all</option>
              <option value="2xx">2xx</option>
              <option value="3xx">3xx</option>
              <option value="4xx">4xx</option>
              <option value="5xx">5xx</option>
              <option value="err">4xx + 5xx</option>
            </select>
            <select v-model="fContract" class="tr-select" aria-label="Filter by contract">
              <option value="">contract: all</option>
              <option value="drifted">drifted</option>
              <option value="conforming">conforming</option>
              <option value="not-checked">contract: not checked</option>
            </select>
            <label class="tr-chk"><input v-model="hideHealth" type="checkbox" /> hide health checks</label>
            <button v-if="filtersActive" class="tr-clear" @click="clearFilters">clear</button>
            <span class="tr-count">
              {{ filteredCalls.length }}<template v-if="filteredCalls.length !== displayCalls.length"> / {{ displayCalls.length }}</template> calls
            </span>
            <button
              class="live-btn"
              :class="{ paused: streamPaused }"
              :title="streamPaused ? 'Resume live updates' : 'Pause live updates'"
              @click="togglePause"
            >
              <span class="live-dot"></span>
              {{ streamPaused ? (manualPause ? 'Paused' : 'Paused — inspecting') : 'Live' }}
            </button>
          </div>

          <button v-if="pendingCount > 0" class="pending-bar" @click="resumeLive">
            ▲ {{ pendingCount }} new call{{ pendingCount === 1 ? '' : 's' }} — resume live
          </button>

        <div class="traffic">
          <div class="tr-head">
            <span class="c-when">captured</span>
            <span class="c-call">call</span>
            <span class="c-peer">counterparty</span>
            <span class="c-status">status</span>
            <span class="c-corr">correlation</span>
            <span class="c-mark">contract</span>
          </div>

          <p v-if="filteredCalls.length === 0" class="empty tr-nomatch">
            No calls match the current filters.
            <button class="tr-clear" @click="clearFilters">Clear filters</button>
          </p>

          <template v-for="c in filteredCalls" :key="c.id">
            <!-- A row that opens the call's detail is a control: in the tab
                 order, a button to assistive tech, Enter / Space toggle it like
                 the click does, and it draws the token focus ring. -->
            <div
              class="tr-row"
              :class="{ drift: isDrifted(c), open: expanded[c.id] }"
              role="button"
              tabindex="0"
              :aria-expanded="!!expanded[c.id]"
              @click="toggle(c.id)"
              @keydown.enter.prevent="toggle(c.id)"
              @keydown.space.prevent="toggle(c.id)"
            >
              <span class="c-when" :title="c.captured_at">
                <span class="chev">{{ expanded[c.id] ? '▾' : '▸' }}</span>
                {{ humanTime(c.captured_at) }}
              </span>
              <span class="c-call">
                <!-- MCP tool calls: the tool rides the method/path slot (deck §4). -->
                <template v-if="c.transport === 'mcp'">
                  <span class="method tool" :title="MCP_BADGE_TOOLTIP">{{ MCP_TOOL_CHIP }}</span>
                  <span class="route mono">{{ toolNameOf(c) }}</span>
                </template>
                <template v-else>
                  <span class="method" :class="c.method.toLowerCase()">{{ c.method }}</span>
                  <span class="route mono">{{ c.route || c.url }}</span>
                </template>
              </span>
              <span class="c-peer" :title="c.peer_addr ? 'peer address ' + c.peer_addr : undefined">
                <span class="dir-chip" :class="c.direction === 'server' ? 'in' : 'out'">{{ dirLabel(c.direction) }}</span>
                <span class="peer-host mono">{{ c.peer_host || c.peer_addr || '—' }}</span>
              </span>
              <span class="c-status">
                <!-- MCP: ok / error from isError — an execution failure, not contract drift. -->
                <span
                  v-if="c.transport === 'mcp'"
                  class="status-code"
                  :class="{ err: c.mcp_is_error }"
                  :title="c.mcp_is_error ? MCP_ERROR_TOOLTIP : undefined"
                >{{ mcpStatusLabel(c) }}</span>
                <span v-else class="status-code" :class="{ err: c.status_code >= 400 }">{{ c.status_code }}</span>
              </span>
              <span class="c-corr mono">
                <span v-if="c.correlation?.request_id" title="request-id">{{ c.correlation.request_id }}</span>
                <span v-if="c.correlation?.client_request_id" :title="JSONRPC_ID_TITLE">{{ c.correlation.client_request_id }}</span>
                <span v-if="c.correlation?.idempotency_key" class="dim" title="idempotency-key">
                  {{ c.correlation.idempotency_key }}
                </span>
                <span v-if="!c.correlation?.request_id && !c.correlation?.client_request_id && !c.correlation?.idempotency_key" class="dim">—</span>
              </span>
              <span class="c-mark">
                <!-- Internal same-team rows are metadata-only: nothing was
                     validated, so no contract-status claim — an honest chip instead. -->
                <span
                  v-if="c.edge_class === 'internal'"
                  class="tag none"
                  title="Internal same-team call — metadata only. Bodies are never captured or shared. Shown so the traffic log is complete."
                >internal</span>
                <span v-else-if="isDrifted(c)" class="tag drift">drifted</span>
                <!-- Nothing validated this call: no contract is bound to its edge.
                     It is neither conforming nor drifted, and saying "conforming"
                     here told operators their traffic was checked when nothing had
                     looked at it. Same honest-chip treatment as `internal`. -->
                <span
                  v-else-if="coverageOf(c) === 'not-checked'"
                  class="tag none"
                  :title="notCheckedTitleOf(c)"
                >{{ NOT_CHECKED_LABEL }}</span>
                <span v-else class="tag ok">conforming</span>
              </span>
            </div>

            <div v-if="expanded[c.id]" class="tr-detail" :key="c.id + '-d'">
              <div class="meta-line">
                <span v-if="c.duration_ms != null">{{ c.duration_ms }} ms</span>
                <span class="dim">·</span>
                <span>
                  {{ c.direction === 'server' ? 'inbound from' : 'outbound to' }}
                  <code>{{ c.peer_host || 'unknown' }}</code>
                  <span v-if="c.peer_addr && c.peer_addr !== c.peer_host" class="dim">
                    ({{ c.peer_addr }})
                  </span>
                </span>
                <span class="dim">·</span>
                <span>integration <code>{{ c.integration }}</code></span>
                <span class="dim">·</span>
                <span v-if="c.redaction?.applied" class="redacted-tag">
                  redacted<template v-if="c.redaction.patterns?.length"> · {{ c.redaction.patterns.join(', ') }}</template>
                </span>
                <span v-else class="dim">no redaction fired</span>
              </div>

              <div class="reqres">
                <div class="rr-col">
                  <div class="rr-title">Request</div>
                  <div class="rr-sub" v-if="headerRows(c.request_headers).length">headers</div>
                  <table v-if="headerRows(c.request_headers).length" class="hdrs">
                    <tr v-for="[k, v] in headerRows(c.request_headers)" :key="k">
                      <td class="hk">{{ k }}</td>
                      <td class="hv mono">{{ v }}</td>
                    </tr>
                  </table>
                  <div class="rr-sub">body</div>
                  <pre class="body">{{ prettyBody(c.request_body) || '(empty)' }}</pre>
                </div>

                <div class="rr-col">
                  <div class="rr-title">Response</div>
                  <div class="rr-sub" v-if="headerRows(c.response_headers).length">headers</div>
                  <table v-if="headerRows(c.response_headers).length" class="hdrs">
                    <tr v-for="[k, v] in headerRows(c.response_headers)" :key="k">
                      <td class="hk">{{ k }}</td>
                      <td class="hv mono">{{ v }}</td>
                    </tr>
                  </table>
                  <div class="rr-sub">body</div>
                  <pre class="body">{{ prettyBody(c.response_body) || '(empty)' }}</pre>
                </div>
              </div>
            </div>
          </template>
        </div>
        </template>
      </section>
    </div>
    <FlagSheet
      v-if="sheetFinding"
      :key="sheetFinding.id"
      :finding="sheetFinding"
      :correlation="correlationFor(sheetFinding)"
      :call="sheetCall"
      :provider="providerNameFor(sheetFinding)"
      :consumer="consumerName"
      :connect="connect"
      :default-org="health?.consumer_display_name"
      @close="closeSheet"
      @created="onThreadCreated"
      @update:connect="onConnectUpdated"
    />
    <!-- QUESTION mode: no finding, no call, no correlation. The sheet branches
         on the missing finding, so nothing here may pass one. -->
    <FlagSheet
      v-else-if="sheetEdge"
      :key="'edge-' + sheetEdge.host"
      :edge="sheetEdge"
      :provider="sheetEdge.name"
      :consumer="consumerName"
      :connect="connect"
      :default-org="health?.consumer_display_name"
      @close="closeSheet"
      @created="onThreadCreated"
      @update:connect="onConnectUpdated"
    />
    <footer class="foot">
      <span>Flanj Collector · ELv2</span>
      <span>Redacted at source · outbound only · UI on localhost</span>
    </footer>
  </div>
</template>

<style>
/* Blueprint collector kit (docs/design/kits/collector), bound straight to the
   canonical tokens. Palette: NONE of it lives here. `src/tokens.css` is the
   vendored copy of docs/design/tokens.css and is imported ahead of this block in
   main.ts. This file holds layout and component rules only — a hex literal
   below is a bug, and so is a custom property whose name the canonical file
   already defines (src/tokens.test.ts scans for both). The kit's alias layer
   (--bg, --panel, --cu …) and its font-name literals are dropped: every class
   reads var(--ground), var(--surface), var(--accent), var(--f-mono) … directly.

   The sheet: one 2px --rule frame around a 24px grid-paper ground. The header,
   the tab strip and the footer are surface bands ruled off from the paper;
   every panel, card and table sits on it in a 2px frame. Row separators, chip
   outlines and cell dividers are the 1.5px hairline; the severity accent on a
   row is a 4px inset stripe, on a headline card the 6px left rule.

   Green is the canonical --ok family and it is spent on REACHED VERDICTS only:
   `conforming`, `No drift detected`, a Connected state, the expected side of a
   drift row. A state that is merely positive-looking (a thread chip, the live
   tail, an inbound direction) never borrows it, because `.headline.neutral`
   below depends on green meaning "validated and clean" and nothing else.

   Severity is the product-fixed triad and nothing else may borrow it:
   --sev-breaking (red) / --sev-warning (copper) / --sev-info (steel). Blueprint
   makes the warning tier the SAME copper as the accent on purpose, so hue alone
   can never say "warning": every --sev-warning* use below renders a finding
   tier that carries a label or an outline (badge, tag, tab count, binding
   checklist, over-cap line), and every attention state that is not a finding
   (pending pill, connect banner, thread chip, fix-reported status) uses
   --accent*. Text takes the -ink role of its family; text at or below 14px
   never uses --ink-faint (the muted text role here is --ink-soft).

   Theme (ux-design-v2 §3.3): LIGHT is the base, dark applies under
   [data-flanj-theme="dark"] ONLY, and there is no OS-following state —
   index.html stamps data-flanj-theme="light" so tokens.css's
   prefers-color-scheme block never fires here. Corners are square (--radius:
   0) as a brand decision. Motion is the lift on hover (--lift + the hard offset
   shadow) and colour fades; prefers-reduced-motion keeps only the fades. */
* { box-sizing: border-box; }
html { background: var(--ground); }
body { margin: 0; background: var(--ground); color: var(--ink); font: 14px/1.5 var(--f-sans); }
.page {
  max-width: 1140px; margin: 24px auto;
  border: var(--border-w) solid var(--rule); border-radius: var(--radius);
  color: var(--ink); background-color: var(--ground);
  background-image: linear-gradient(var(--grid-line) 1px, transparent 1px), linear-gradient(90deg, var(--grid-line) 1px, transparent 1px);
  background-size: var(--grid-size-dense) var(--grid-size-dense);
}
.mono { font-family: var(--f-mono); }
code { font-family: var(--f-mono); }

/* The hex bolt. One inline <symbol id="hxbolt"> sits at the top of the
   template; every use is `<svg class="hx [sm] tone-*"><use href="#hxbolt"/>`.
   currentColor fills the inner hex, --l the outer ring, --stroke the outline;
   the tone classes bind all three to a token family, so no bolt carries an
   inline style. A bolt is a mark, not text, so it takes the bare family colour. */
.hx-defs { position: absolute; width: 0; height: 0; overflow: hidden; }
.hx { width: 14px; height: 14px; flex: none; overflow: visible; --stroke: var(--ink); --l: var(--sev-info-bolt); color: var(--sev-info); }
.hx.sm { width: 9px; height: 9px; }
.hx-outer { fill: var(--l); stroke: var(--stroke); }
.hx-inner { fill: currentColor; stroke: var(--stroke); }
.hx.tone-ok { color: var(--ok); --l: var(--ok-bolt); }
.hx.tone-breaking { color: var(--sev-breaking); --l: var(--sev-breaking-bolt); }
.hx.tone-warning { color: var(--sev-warning); --l: var(--sev-warning-bolt); }
.hx.tone-accent { color: var(--accent); --l: var(--accent-bolt); }
.hx.tone-info { color: var(--sev-info); --l: var(--sev-info-bolt); }

/* Sheet header: inline mono mark + wordmark + product, the address line in
   mono, then the org pill and the connect pill. */
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; padding: 12px 18px; border-bottom: var(--border-w) solid var(--rule); background: var(--surface); }
.brand { display: inline-flex; align-items: center; gap: 10px; flex-wrap: wrap; min-width: 0; font-weight: 700; letter-spacing: -0.01em; font-size: 16px; color: var(--ink); }
.brand-mark { width: 30px; height: 30px; flex: none; }
.brand-product { color: var(--ink-soft); font-weight: 500; }
.brand-addr { font-family: var(--f-mono); font-size: 11px; font-weight: 400; letter-spacing: 0.06em; color: var(--ink-soft); margin-left: 8px; padding-left: 12px; border-left: var(--border-w-hair) solid var(--rule); }
.meta { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
/* Pills are mono micro-labels: uppercase at 10.5px, a hairline outline. */
.pill { display: inline-flex; align-items: center; gap: 7px; font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; padding: 4px 9px; border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); background: var(--surface); }
/* Attention states (not configured, pending) are the accent, outlined and
   labelled — the warning tier belongs to findings only. */
.pill.warn { color: var(--accent-ink); border-color: var(--accent-ink); }
/* A pill that carries a NAME someone typed (the org pill) keeps that name's own
   case: the uppercase mono treatment is for labels. "CustomerX" is how the
   provider sees it on every thread, and e2e reads it verbatim. */
.pill-name { text-transform: none; letter-spacing: 0.02em; }
/* Connected is a reached state: green, with the green bolt leading it. */
.pill.ok { color: var(--ok-ink); border-color: var(--ok); }
.pill-btn { cursor: pointer; transition: color var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease); }
.pill-btn:hover { color: var(--ink); }
.pill-link { text-decoration: none; }
.pill-link:hover { text-decoration: underline; }
.pill-out { font-size: 0.9em; opacity: 0.75; }

/* Banners that sit between the header and the tab strip. */
.banner { margin: 12px 18px 0; padding: 10px 14px; border: var(--border-w) solid var(--sev-breaking); border-left-width: var(--border-w-stripe-lg); border-radius: var(--radius); background: var(--surface); font-size: 13.5px; }
.error { color: var(--sev-breaking-ink); }
/* The one-time theme-flip notice sits above the tab strip, not inside a tab. */
.theme-flip-banner { margin: 12px 18px 0; }

/* Tabs: mono uppercase on a surface band; the active tab draws a 2px ink
   underline over the band's rule with the copper hairline just beneath it. */
.tabs { display: flex; flex-wrap: wrap; margin: 0; padding: 0 18px; border-bottom: var(--border-w) solid var(--rule); background: var(--surface); }
.tabs button { position: relative; background: none; border: 0; border-bottom: var(--border-w) solid transparent; margin-bottom: calc(-1 * var(--border-w)); cursor: pointer; font: 500 11.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-soft); padding: 12px 16px; display: inline-flex; align-items: center; gap: 8px; transition: color var(--dur-fast) var(--ease); }
.tabs button:hover { color: var(--ink); }
.tabs button.active { color: var(--ink); border-bottom-color: var(--ink); }
.tabs button.active::before { content: ''; position: absolute; left: 0; right: 0; bottom: calc(-1 * var(--border-w)); height: var(--border-w-hair); background: var(--accent); }
.tab-right { margin-left: auto; }
/* Square count chips: red = breaking (act), copper = review — each carries its
   number, so the two filled chips never rely on hue alone. */
.tab-count { font: 500 10px/1.4 var(--f-mono); letter-spacing: 0; text-transform: none; padding: 1px 6px; min-width: 20px; text-align: center; border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); background: var(--surface); }
.tab-count.bad { background: var(--sev-breaking); border-color: var(--sev-breaking); color: var(--sev-breaking-contrast); }
.tab-count.warn { background: var(--sev-warning); border-color: var(--sev-warning); color: var(--sev-warning-contrast); }
/* Every row in the count is a DESCRIPTION change: the steel outline the row
   badge wears, not the warning fill — a wording change is not a warning. */
.tab-count.warn.desc { background: var(--surface); border-color: var(--ink-soft); color: var(--ink-soft); }
.tab-dot { width: 8px; height: 8px; border-radius: var(--radius); background: var(--accent); display: inline-block; }
.tab-dot.disconnected { background: var(--ink-soft); }

/* Panels and section headings. A heading is a mono eyebrow with its scope in
   sentence-case sans beside it. */
.panel { padding: 20px 18px 24px; }
.panel > * + section:not(.headline) { margin-top: 22px; }
h2 { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 0 0 12px; padding: 0; border: 0; font: 500 11px/1.5 var(--f-mono); letter-spacing: 0.12em; text-transform: uppercase; color: var(--ink-soft); }
h2 small { font: 400 12.5px/1.5 var(--f-sans); letter-spacing: 0.04em; text-transform: none; color: var(--ink-soft); margin: 0; }
.empty { color: var(--ink-soft); }
.empty.small { font-size: 13px; }
.hint { color: var(--ink-soft); font-size: 12.5px; margin: 16px 0 0; }
.hint-inline { color: var(--ink-soft); font-size: 12.5px; }
.small-err { font-size: 12.5px; }

/* Headline cards: a 6px left rule carries the tone, a bolt leads the line.
   Three tones, not two — `neutral` is the install where nothing has been
   validated yet, and it must read as neither the green all-clear nor the red
   drift banner (ui/src/headline.ts). */
.headline { display: grid; grid-template-columns: auto 1fr; gap: 16px; align-items: start; margin: 0 0 14px; padding: 16px 18px; border: var(--border-w) solid var(--rule); border-left-width: var(--border-w-stripe-lg); border-radius: var(--radius); background: var(--surface); }
.headline.drift { border-left-color: var(--sev-breaking); }
.headline.ok { border-left-color: var(--ok); }
.headline.neutral { border-left-color: var(--rule); }
.headline > .hx { margin-top: 6px; }
.hl-you { font-size: 17px; font-weight: 600; letter-spacing: -0.01em; }
.headline.drift .hl-you strong { color: var(--sev-breaking-ink); }
.headline.ok .hl-you strong { color: var(--ok-ink); }
.headline.neutral .hl-you strong { color: var(--ink-soft); }
.hl-sub { color: var(--ink-soft); font-size: 12.5px; margin-top: 4px; }
.hl-sub code { background: var(--surface-sunk); padding: 1px 6px; }
/* The MCP line is the deck's whole sentence, so it steps down one size. */
.mcp-headline .hl-you { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 15px; }
.mcp-headline .mcp-badge { margin-left: 0; }

/* Local notices band (v0.5): visible to you only, never a flag control. */
.local-notices { margin: 0 0 14px; padding: 14px 18px; border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.ln-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.ln-title { font-weight: 700; font-size: 13.5px; }
.ln-sub { color: var(--ink-soft); font-size: 12.5px; }
.ln-list { margin: 8px 0 0; padding-left: 18px; }
.ln-item { color: var(--ink-soft); font-size: 13.5px; margin-top: 4px; }

/* Buttons (shared by the Connect panel, Flag sheet, uploader and Threads tab):
   a 2px ink outline that lifts on hover under the hard offset shadow; primary
   is the ink fill; ghost keeps the rule outline in muted ink. */
.btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; background: var(--surface); color: var(--ink); border: var(--border-w) solid var(--ink); border-radius: var(--radius); padding: 6px 12px; font: 600 12.5px/1.5 var(--f-sans); cursor: pointer; transition: transform var(--dur) var(--ease-lift), box-shadow var(--dur) var(--ease-lift), color var(--dur-fast) var(--ease), background-color var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease); }
.btn:hover { transform: var(--lift); box-shadow: var(--shadow-lift); }
.btn:active { transform: none; box-shadow: none; }
.btn.primary { background: var(--ink); color: var(--ground); border-color: var(--ink); }
.btn.ghost { border-color: var(--rule); color: var(--ink-soft); }
.btn.ghost:hover { color: var(--ink); box-shadow: var(--shadow-lift-soft); }
.btn.small { padding: 4px 10px; font-size: 12px; }
.btn.attention { color: var(--accent-ink); border-color: var(--accent-ink); }
.btn:disabled { opacity: 0.5; cursor: default; transform: none; box-shadow: none; }
/* Focus: one ring for every control, from the token layer — the 2px ink outline
   at 2px offset. :focus-visible only, so a mouse click on a button draws
   nothing while keyboard focus always does; text fields draw it on every focus,
   which is what :focus-visible means for editable elements. Scoped SFCs repeat
   the two declarations for their own controls (src/tokens.test.ts lists them;
   the uploader's live in ContractUploader.vue). */
.btn:focus-visible, .tabs button:focus-visible, .seg button:focus-visible, .pill-btn:focus-visible,
.live-btn:focus-visible, .pending-bar:focus-visible, .tr-search:focus-visible, .tr-select:focus-visible, .tr-row:focus-visible,
.tr-clear:focus-visible, .tr-chk input:focus-visible, .doc-link:focus-visible, .edge-contract-link:focus-visible,
.pill-link:focus-visible, .uncovered-toggle:focus-visible, .edge-rename-input:focus-visible, .edge-suggest input:focus-visible {
  outline: var(--focus-ring); outline-offset: var(--focus-offset);
}
/* A thread chip is a state, not a verdict: muted ink with a steel bolt, and
   `attention` lifts it to the accent. */
.chip { display: inline-flex; align-items: center; gap: 8px; font: 500 11px/1.5 var(--f-mono); letter-spacing: 0.06em; color: var(--ink-soft); border: var(--border-w-hair) solid var(--ink-soft); border-radius: var(--radius); padding: 4px 9px; }
.chip.attention { color: var(--accent-ink); border-color: var(--accent-ink); }
/* Connect banners inside a panel: the accent frames an attention state; `info`
   is a plain framed note. */
.connect-banner { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin: 0 0 12px; padding: 10px 14px; border: var(--border-w) solid var(--accent); border-left-width: var(--border-w-stripe-lg); border-radius: var(--radius); background: var(--surface); font-size: 13.5px; }
.connect-banner-actions { display: flex; gap: 8px; }
.connect-banner.info { border-color: var(--rule); color: var(--ink-soft); }

/* Traffic toolbar: a framed band — search + facet filters + the live control.
   Sticky, so the filters stay in reach while the tail scrolls (wide layouts
   only — at phone width it is a third of the viewport, so it scrolls away). */
.tr-toolbar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 0 0 12px; padding: 10px 12px; border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.tr-search { flex: 1 1 220px; min-width: 160px; background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 13px; padding: 7px 10px; transition: border-color var(--dur-fast) var(--ease); }
.tr-search::placeholder { color: var(--ink-soft); }
.tr-search:focus, .tr-select:focus { border-color: var(--ink); }
.tr-select { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 12.5px; padding: 7px 8px; }
.tr-chk { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-soft); font-size: 12.5px; cursor: pointer; white-space: nowrap; user-select: none; }
.tr-chk input { accent-color: var(--ink); }
.tr-clear { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); font: 600 12px/1.5 var(--f-sans); padding: 4px 10px; cursor: pointer; transition: color var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease); }
.tr-clear:hover { color: var(--ink); border-color: var(--ink); }
.tr-count { font-family: var(--f-mono); font-size: 11px; letter-spacing: 0.06em; color: var(--ink-soft); font-variant-numeric: tabular-nums; white-space: nowrap; margin-left: auto; }
/* Live / paused is a stream state, not a verdict and not a warning: the accent
   carries `live` (a pulsing hex), muted ink carries `paused`, and the label
   says which — green is reserved for reached verdicts. */
.live-btn { display: inline-flex; align-items: center; gap: 8px; background: var(--surface); border: var(--border-w) solid var(--accent-ink); border-radius: var(--radius); color: var(--accent-ink); font: 600 11px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; padding: 6px 11px; cursor: pointer; white-space: nowrap; transition: color var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease); }
.live-dot { width: 8px; height: 8px; background: var(--accent); clip-path: polygon(25% 0, 75% 0, 100% 50%, 75% 100%, 25% 100%, 0 50%); animation: live-pulse 1.6s ease-in-out infinite; }
.live-btn.paused { border-color: var(--ink-soft); color: var(--ink-soft); }
.live-btn.paused .live-dot { background: var(--ink-soft); animation: none; }
@keyframes live-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.2; } }
.pending-bar { display: block; width: 100%; background: var(--surface); border: var(--border-w) solid var(--accent-ink); border-radius: var(--radius); color: var(--accent-ink); font: 600 12.5px/1.5 var(--f-sans); padding: 8px 12px; margin: 0 0 12px; cursor: pointer; text-align: center; transition: transform var(--dur) var(--ease-lift), box-shadow var(--dur) var(--ease-lift); }
.pending-bar:hover { transform: var(--lift); box-shadow: var(--shadow-lift-soft); }
.tr-nomatch { display: flex; align-items: center; gap: 10px; margin: 0; padding: 16px 14px; }

/* Traffic table: a framed table; mono uppercase heads on the sunk surface,
   hairline row separators, the drift stripe inset on the row. */
.traffic { border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.tr-head, .tr-row {
  display: grid;
  grid-template-columns: 1.15fr 1.9fr 1.35fr 0.55fr 1.4fr 0.9fr;
  gap: 10px;
  align-items: center;
  padding: 10px 14px;
}
.tr-head { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-soft); border-bottom: var(--border-w) solid var(--rule); background: var(--surface-sunk); }
.tr-row { border-top: var(--border-w-hair) solid var(--rule-soft); cursor: pointer; font-size: 13.5px; transition: background-color var(--dur-fast) var(--ease); }
.tr-head + .tr-row, .tr-head + .tr-nomatch + .tr-row { border-top: 0; }
.tr-row:hover { background: var(--surface-sunk); }
.tr-row.open { background: var(--surface-sunk); }
.tr-row.drift { box-shadow: inset var(--border-w-stripe) 0 0 var(--sev-breaking); }
.chev { color: var(--ink-soft); display: inline-block; width: 16px; }
/* Per-cell rules are scoped to ROWS: the head's cells share the same class
   names and must keep the head's one register (mono 10.5px, --ink-soft). */
.tr-row .c-when { color: var(--ink-soft); white-space: nowrap; font-family: var(--f-mono); font-size: 12.5px; }
.tr-row .c-call { display: flex; align-items: center; gap: 8px; min-width: 0; }
/* Method chips are NEUTRAL mono micro-labels. They used to borrow the palette —
   POST green, GET accent, DELETE the severity red — which put a `breaking` red
   on a DELETE that was behaving perfectly. The verb is the distinguisher; the
   MCP TOOL chip takes the ink outline. */
.method { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; padding: 2px 7px; border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); white-space: nowrap; }
.method.tool { color: var(--ink); border-color: var(--ink); }
.route { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.status-code { font-family: var(--f-mono); font-size: 13px; }
.status-code.err { color: var(--sev-breaking-ink); }
.tr-row .c-corr { display: flex; flex-direction: column; font-size: 12px; color: var(--ink); min-width: 0; }
.tr-row .c-corr span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tr-row .c-corr .dim { color: var(--ink-soft); }
/* Contract marks: `conforming` is the only green — a reached verdict. `drifted`
   is the red fill, `not checked` / `internal` the muted outline. */
.tag { display: inline-block; white-space: nowrap; font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; padding: 2px 7px; border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); }
.tag.ok { color: var(--ok-ink); border-color: var(--ok); }
.tag.drift { background: var(--sev-breaking); border-color: var(--sev-breaking); color: var(--sev-breaking-contrast); }
.tag.warn { background: var(--sev-warning); border-color: var(--sev-warning); color: var(--sev-warning-contrast); }
/* `N DESCRIPTION`: the row badge's steel outline — one vocabulary per class. */
.tag.desc { color: var(--ink-soft); border-color: var(--ink-soft); }
.tag.none { color: var(--ink-soft); border-color: var(--rule); }

/* Traffic counterparty cell. Direction is a fact, not a verdict: `out` is the
   accent, `in` is muted ink, and the uppercase label tells them apart. */
.tr-row .c-peer { display: flex; align-items: center; gap: 8px; min-width: 0; }
.dir-chip { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; border: var(--border-w-hair) solid currentColor; border-radius: var(--radius); padding: 1px 6px; flex: none; }
.dir-chip.out { color: var(--accent-ink); }
.dir-chip.in { color: var(--ink-soft); }
.peer-host { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 12.5px; }

/* Expanded call: headers and bodies on the sunk surface, framed. */
.tr-detail { border-top: var(--border-w-hair) solid var(--rule-soft); background: var(--surface-sunk); padding: 14px 14px 18px; }
.meta-line { color: var(--ink-soft); font-size: 12.5px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; }
/* Dimming is done with the palette, never opacity: --ink-soft keeps its
   contrast on the sunk surface, a half-opacity --ink-soft did not (2.2:1). */
.meta-line .dim { color: var(--ink-soft); }
/* `redacted · patterns` is a fact about the row, stated in body ink at weight —
   not a warning, and not a link. */
.redacted-tag { color: var(--ink); font-weight: 600; }
.reqres { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.rr-col { min-width: 0; }
.rr-title { font-weight: 600; font-size: 13px; margin-bottom: 6px; }
.rr-sub { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); margin: 8px 0 4px; }
.hdrs { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.hdrs td { padding: 2px 6px; border-bottom: var(--border-w-hair) solid var(--rule-soft); vertical-align: top; }
.hk { color: var(--ink-soft); white-space: nowrap; width: 1%; }
.hv { color: var(--ink); word-break: break-all; }
pre.body { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 10px 12px; margin: 0; font-family: var(--f-mono); font-size: 12.5px; line-height: 1.45; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }

/* Contract cards (self + provider): a framed card whose heading row carries the
   name, origin, format chip, version and the verdict chips. */
.provider { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 16px 18px; margin-bottom: 14px; }
.provider.self { border-color: var(--accent); }
.prov-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.prov-name { font-weight: 700; font-size: 16px; letter-spacing: -0.01em; }
/* The origin sits IN the heading, muted — the fact that separates two servers
   sharing a name has to be where the eye already is. */
.prov-origin { color: var(--ink-soft); font-weight: 400; font-size: 13px; }
.fmt-badge { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--accent-ink); border: var(--border-w-hair) solid var(--accent-ink); border-radius: var(--radius); padding: 2px 7px; }
.prov-ver { color: var(--ink-soft); font-family: var(--f-mono); font-size: 12px; }
.prov-integration { color: var(--ink-soft); font-family: var(--f-mono); font-size: 12px; }
.prov-status { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.prov-links { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-top: 12px; }
.doc-link { color: var(--ink); font-size: 12px; font-weight: 600; text-decoration: none; border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 4px 10px; background: var(--surface); transition: transform var(--dur) var(--ease-lift), box-shadow var(--dur) var(--ease-lift), border-color var(--dur-fast) var(--ease); }
.doc-link:hover { transform: var(--lift); box-shadow: var(--shadow-lift-soft); border-color: var(--ink); }
.prov-meta { color: var(--ink-soft); font-size: 12.5px; }
/* The evidence count rides in the same muted channel as provenance — it is a
   fact about this card, not a warning, and zero must not be dressed as one. */
.prov-meta.evidence { margin-left: auto; font-family: var(--f-mono); font-size: 11.5px; letter-spacing: 0.02em; }
.prov-nospec { color: var(--ink-soft); font-size: 13.5px; margin: 10px 0 0; }
/* The document-cap line. Same slot and size as .prov-nospec — the same kind of
   sentence — but it stays on the warning tier: an over-cap document is a
   finding-class condition the operator acts on. Its own text is the label, and
   it takes the tier's edge rule as well as the ink role so it survives sharing
   a hue with the accent (copper is both, by design). */
.prov-oversize { color: var(--sev-warning-ink); font-size: 13.5px; margin: 10px 0 0; padding-left: 10px; border-left: var(--border-w-stripe) solid var(--sev-warning-edge); }

/* Findings: framed cards. Head row (bolted severity chip · endpoint · rule id ·
   ×N calls) → expected ≠ actual ≠ location in mono cells → detail →
   correlation → actions. Acknowledged rows dim in place; evidence is never
   hidden. */
.finding { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); margin-bottom: 14px; transition: border-color var(--dur) var(--ease), color var(--dur) var(--ease); }
.finding.nested { margin: 12px 0 0; }
/* Acknowledged: dimmed IN PLACE with the palette, never with opacity — the
   kit's `opacity: .55` put 12–13px evidence at 2.6:1, which hides it for
   low-vision readers while promising it is never hidden. Text drops to
   --ink-soft, the frame to --rule-soft, the chip and its bolts to steel; every
   pair stays at or above 4.5:1 in both schemes. */
.finding.acked { border-color: var(--rule-soft); }
.finding.acked .finding-head, .finding.acked .drift-row, .finding.acked .col { border-color: var(--rule-soft); }
.finding.acked .endpoint, .finding.acked .v, .finding.acked .v.expected, .finding.acked .v.actual,
.finding.acked .detail, .finding.acked .corr code { color: var(--ink-soft); }
.finding.acked .badge { color: var(--ink-soft); border-color: var(--rule); }
.finding.acked .badge .hx { color: var(--ink-soft); --l: var(--sev-info-bolt); }
/* The #contracts/<finding_id> deep-link target — same accent rule as the
   Threads tab's highlighted row. */
.finding.highlight { box-shadow: inset var(--border-w-stripe-lg) 0 0 var(--accent); }
.finding-head { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; padding: 14px 18px; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
/* The severity chip: mono uppercase between two bolts, a hairline outline in
   the tier's bare colour, text in its -ink role. Each tier uses its own family
   and the accent never carries one (src/tokens.test.ts pins all three). */
.badge { display: inline-flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 104px; font: 600 10.5px/1 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; padding: 4px 7px; border: var(--border-w-hair) solid currentColor; border-radius: var(--radius); }
.badge.breaking { color: var(--sev-breaking-ink); border-color: var(--sev-breaking); }
.badge.warning { color: var(--sev-warning-ink); border-color: var(--sev-warning); }
/* INFO is the neutral tier of the product-fixed triad. It used to be filled with
   --accent; accent is never semantic, and a blue "info" badge sitting beside a
   red and a copper one read as a fourth severity. --sev-info IS the neutral tier. */
.badge.info { color: var(--sev-info-ink); border-color: var(--sev-info); }
/* DESCRIPTION: a wording change is not a severity claim — muted ink, steel
   bolts, the same outline shape, so it is distinguishable from WARNING by
   colour AND by label. */
.badge.description { color: var(--ink-soft); border-color: var(--ink-soft); }
.endpoint { font-weight: 600; }
.rule { color: var(--ink-soft); font-family: var(--f-mono); font-size: 12px; }
/* The occurrence count is a number in mono, not a severity. */
.occ { color: var(--ink-soft); font-family: var(--f-mono); font-size: 12px; }
.drift-row { display: grid; grid-template-columns: 1fr auto 1fr 1fr; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
.drift-row.two { grid-template-columns: 1fr auto 1fr; }
.col { padding: 12px 18px; border-right: var(--border-w-hair) solid var(--rule-soft); min-width: 0; }
.col:last-child { border-right: 0; }
.col .k { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
/* A label carrying a digest and a timestamp is a machine identifier: the
   eyebrow's register, but its case and tracking are left alone. */
.col .k.snap { text-transform: none; letter-spacing: 0.02em; }
.col .v { margin-top: 4px; font-family: var(--f-mono); font-size: 13px; word-break: break-word; }
.v.expected { color: var(--ok-ink); }
.v.actual { color: var(--sev-breaking-ink); }
/* DESCRIPTION rows: a wording change is not a severity diff — plain ink. */
.drift-row.plain .v.expected, .drift-row.plain .v.actual { color: var(--ink); }
.arrow { display: flex; align-items: center; color: var(--ink-soft); font-size: 20px; padding: 0 14px; }
.detail { color: var(--ink-soft); font-size: 13.5px; margin: 0; padding: 12px 18px; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
.corr { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 18px; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
.corr-title { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
.corr-k { font-size: 12.5px; color: var(--ink-soft); }
.corr-k code { color: var(--ink); background: var(--surface-sunk); padding: 2px 6px; }
.actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 18px; }

/* MCP per-tool rows (v0.5): the server's tools ARE the contract surface. */
.mcp-badge { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--accent-ink); border: var(--border-w-hair) solid var(--accent-ink); border-radius: var(--radius); padding: 1px 6px; margin-left: 6px; vertical-align: middle; white-space: nowrap; }
.tool-rows { margin-top: 12px; border: var(--border-w) solid var(--rule); border-radius: var(--radius); }
.tool-row { padding: 8px 12px; border-top: var(--border-w-hair) solid var(--rule-soft); }
.tool-row:first-child { border-top: 0; }
.tool-line { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.tool-name { font-size: 13px; }
/* `input + output contract` is a reached state of the tool's contract: green
   outline; `input contract only` is the muted outline. */
.tool-tag { font-family: var(--f-mono); font-size: 11px; letter-spacing: 0.02em; color: var(--ok-ink); border: var(--border-w-hair) solid var(--ok); border-radius: var(--radius); padding: 1px 8px; }
.tool-tag.partial { color: var(--ink-soft); border-color: var(--rule); }
.tool-note { color: var(--ink-soft); font-size: 12.5px; margin: 4px 0 0; }

/* Edges overview: two framed groups stacked, each a framed table — the
   inbound / outbound split is a group rule, not two half-width boxes that
   squeezed every name and number. */
.edge-groups { display: grid; grid-template-columns: 1fr; gap: 14px; }
.edge-group { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 14px 16px 18px; }
.edge-title { font-size: 13.5px; font-weight: 600; margin: 0 0 12px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.edge-title small { color: var(--ink-soft); font-weight: 400; font-size: 12.5px; }
.dir-badge { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; padding: 2px 7px; border: var(--border-w-hair) solid currentColor; border-radius: var(--radius); }
.dir-badge.out { color: var(--accent-ink); }
.dir-badge.in { color: var(--ink-soft); }
.edge-table { border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.edge-head, .edge-row { display: grid; grid-template-columns: minmax(0, 2.4fr) max-content max-content max-content; gap: 14px; align-items: center; padding: 10px 14px; }
.edge-head { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-soft); background: var(--surface-sunk); border-bottom: var(--border-w) solid var(--rule); }
/* The column labels never wrap ("OBSERVED RPM" used to become the tallest thing
   in the header row). */
.edge-head span { white-space: nowrap; }
.edge-row { border-top: var(--border-w-hair) solid var(--rule-soft); font-size: 13.5px; transition: background-color var(--dur-fast) var(--ease); }
.edge-head + .edge-row { border-top: 0; }
.edge-row:hover { background: var(--surface-sunk); }
.edge-row.drift { box-shadow: inset var(--border-w-stripe) 0 0 var(--sev-breaking); }
.edge-row .peer { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; }
/* v1p1 edge naming: outbound rows carry name-over-host + actions. The name cell
   gets a real floor — a fractional track collapsed it to ~59px at every width. */
.edge-head.named, .edge-row.named { grid-template-columns: minmax(144px, 1fr) max-content max-content max-content auto; }
.edge-name-cell { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.edge-name-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.edge-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* An unnamed row IS its host: the same mono treatment the named row's host line
   gets, so the two row shapes read as one column. */
.edge-name.unnamed { color: var(--ink); font-weight: 400; font-family: var(--f-mono); font-size: 12.5px; }
.edge-host { color: var(--ink-soft); font-size: 12px; }
/* Contract coverage on an outbound row: the SAME muted text channel as
   .edge-host, one line below it — a text link, not a chip, so it is present
   when you look at a row and invisible when you scan the column. */
.edge-contract { font-size: 12px; line-height: 1.35; }
.edge-contract-link {
  background: none; border: 0; padding: 0; margin: 0;
  font: inherit; color: var(--ink-soft); cursor: pointer;
  text-align: left; text-decoration: none; transition: color var(--dur-fast) var(--ease);
}
.edge-contract-link:hover, .edge-contract-link:focus-visible { color: var(--ink); text-decoration: underline; }
/* `Add contract` stays muted: rendered at 30 rows an accent link marched down
   the column louder than the chip this design avoids. The dotted underline
   marks it as a control without spending colour on it. */
.edge-contract-link.add { border-bottom: 1px dotted var(--rule); }
.edge-contract-link.add:hover, .edge-contract-link.add:focus-visible { border-bottom-color: currentColor; text-decoration: none; }
.edge-rollcall { margin: -2px 0 8px; font-size: 12.5px; color: var(--ink-soft); }
.edge-actions { display: flex; gap: 6px; justify-content: flex-end; flex-wrap: wrap; }
.edge-rename { border-top: var(--border-w-hair) solid var(--rule-soft); background: var(--surface-sunk); padding: 10px 14px; display: flex; flex-direction: column; gap: 8px; }
.edge-rename-input { width: 100%; max-width: 416px; padding: 6px 8px; border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); color: var(--ink); font: inherit; font-size: 13px; }
.edge-rename-input:focus { border-color: var(--ink); }
.edge-suggest { display: flex; align-items: flex-start; gap: 6px; font-size: 12px; color: var(--ink-soft); }
.edge-suggest input { margin-top: 2px; accent-color: var(--ink); }
.edge-rename-actions { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.edge-name-note { margin: 0; padding: 6px 14px; font-size: 12px; color: var(--ink-soft); border-top: var(--border-w-hair) solid var(--rule-soft); }
.edge-row .num { text-align: right; font-family: var(--f-mono); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.edge-row .unit { color: var(--ink-soft); font-size: 11px; margin-left: 4px; }
/* Drifted calls: a number in red mono when there are any, muted at zero —
   the count is the label. Last seen rides the muted register. */
.edge-row .drift-n { color: var(--ink-soft); }
.edge-row .drift-n.some { color: var(--sev-breaking-ink); font-weight: 600; }
.edge-row .seen { color: var(--ink-soft); }

/* Providers with no contract — rows, not cards. Collapsed by default. */
.uncovered h2 { margin-bottom: 6px; }
.uncovered-toggle {
  background: none; border: 0; padding: 0; font: inherit; color: inherit; letter-spacing: inherit; text-transform: inherit;
  cursor: pointer; display: inline-flex; align-items: center; gap: 6px;
}
.uncovered-toggle .chev { display: inline-block; transition: transform var(--dur-fast) var(--ease); color: var(--ink-soft); font-size: 0.9em; width: auto; }
.uncovered-toggle .chev.open { transform: rotate(90deg); }
.uncovered-rows { border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.uncovered-row { padding: 8px 14px; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
.uncovered-row:last-child { border-bottom: 0; }
.uncovered-row.highlight { background: var(--surface-sunk); box-shadow: inset var(--border-w-stripe) 0 0 var(--accent); }
.uncovered-line { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.uncovered-host { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.uncovered-lede { margin: 0 0 8px; font-size: 12.5px; color: var(--ink-soft); }

/* The after-state: what happens NEXT, said once, above the cards. */
.upload-notice {
  margin: 0 0 12px; padding: 8px 12px; font-size: 13px;
  border: var(--border-w) solid var(--rule); border-left: var(--border-w-stripe-lg) solid var(--accent); background: var(--surface); color: var(--ink);
}
.upload-notice.error { border-left-color: var(--sev-breaking); }
.pretraffic h2 { margin-bottom: 8px; }

/* Settings: two framed cards side by side — Connect, and Appearance. */
.settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; align-items: start; }
.settings-grid > section, .settings-grid > * + section:not(.headline) { margin: 0; border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); padding: 16px 18px; }
.theme-field { display: grid; grid-template-columns: 130px 1fr; gap: 10px; align-items: center; }
.theme-label { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
.theme-help { grid-column: 1 / -1; color: var(--ink-soft); font-size: 12.5px; margin-top: 2px; }
/* The Light / Dark segmented control: a 2px ink frame, the pressed segment
   ink-filled so it never reads as a primary button or a warning chip. No
   overflow clip on the wrapper: the buttons' focus ring sits 2px outside their
   box, and radius is 0, so a clip would erase the ring and round nothing. */
.seg { display: inline-flex; justify-self: start; border: var(--border-w) solid var(--ink); border-radius: var(--radius); background: var(--surface); }
.seg button { background: none; border: 0; color: var(--ink-soft); font: 600 12.5px/1.5 var(--f-sans); padding: 6px 14px; cursor: pointer; transition: color var(--dur-fast) var(--ease), background-color var(--dur-fast) var(--ease); }
.seg button:hover { color: var(--ink); }
.seg button.active { background: var(--ink); color: var(--ground); }

/* The footer band: two mono micro-labels. */
.foot { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; padding: 10px 18px; border-top: var(--border-w) solid var(--rule); background: var(--surface); font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-soft); }

@media (max-width: 760px) {
  .settings-grid { grid-template-columns: 1fr; }
  .drift-row, .drift-row.two { grid-template-columns: 1fr; }
  .arrow { display: none; }
  .col { border-right: 0; border-bottom: var(--border-w-hair) solid var(--rule-soft); }
  .col:last-child { border-bottom: 0; }
}

@media (max-width: 720px) {
  .page { margin: 8px; }
  .topbar, .tabs, .foot { padding-left: 12px; padding-right: 12px; }
  .tabs button { padding: 10px 10px; }
  .panel { padding: 16px 12px 20px; }
  .banner, .theme-flip-banner { margin-left: 12px; margin-right: 12px; }
  /* The toolbar is a third of a phone viewport: it scrolls with the page. */
  .tr-toolbar { position: static; }
  .tr-head { display: none; }
  /* Each call is a stacked card: the route on its own line, whole (it is the
     cell that matters, so it wraps rather than ellipsizes); direction, host
     and status on the second; captured time (muted) and the contract chip on
     the third; correlation on its own line. Two columns only — a third
     max-content column (the time beside the status) squeezed the host to
     30px and wrapped it per character. Nothing sits in an unlabeled cell
     beside a stranger. */
  .tr-row {
    grid-template-columns: minmax(0, 1fr) max-content;
    grid-template-areas: "call call" "peer status" "when mark" "corr corr";
    gap: 6px 10px;
  }
  .tr-row .c-call { grid-area: call; flex-wrap: wrap; }
  .tr-row .route { white-space: normal; overflow: visible; text-overflow: clip; word-break: break-word; }
  .tr-row .c-peer { grid-area: peer; }
  /* The host is an identity: it wraps at natural breaks rather than ellipsizing. */
  .tr-row .peer-host { white-space: normal; overflow: visible; text-overflow: clip; overflow-wrap: anywhere; }
  .tr-row .c-when { grid-area: when; font-size: 11.5px; }
  .tr-row .c-status { grid-area: status; justify-self: end; }
  .tr-row .c-corr { grid-area: corr; }
  .tr-row .c-mark { grid-area: mark; justify-self: end; }
  .reqres { grid-template-columns: 1fr; }
  .edge-head, .edge-row { gap: 10px; }
  .edge-head.named, .edge-row.named { grid-template-columns: minmax(0, 1fr) max-content max-content max-content; }
  .edge-row.named .edge-actions { grid-column: 1 / -1; justify-content: flex-start; }
}

/* Reduced motion keeps the colour fades and drops everything that moves. */
@media (prefers-reduced-motion: reduce) {
  .btn:hover, .btn.ghost:hover, .doc-link:hover, .pending-bar:hover { transform: none; box-shadow: none; }
  .live-dot { animation: none; }
  .uncovered-toggle .chev { transition: none; }
  .finding { transition: none; }
}
</style>
