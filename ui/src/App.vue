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
  isEvidenceFor,
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
// provider_display_name for the observed integration, else a humanized id
// (the same rule the relay applies server-side).
function providerNameFor(f: Finding): string {
  if (health.value?.provider_display_name && (!health.value.integration || f.integration === health.value.integration)) {
    return health.value.provider_display_name;
  }
  return humanize(f.integration) || f.integration || 'the provider';
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
// data-theme on <html>; the dark palette lives under [data-theme="dark"] only
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

function humanize(id: string): string {
  return id
    .split(/[-_]+/)
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join(' ');
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

// Per-card chip counts — the same taxonomy as the tab pills, so the sum of
// card chips always equals the pills.
function cardBreakingCount(p: ContractCard): number {
  return p.findings.filter((f) => isBreakingFinding(f)).length;
}
function cardInfoCount(p: ContractCard): number {
  return p.findings.filter((f) => !isBreakingFinding(f) && !isAcked(f)).length;
}
function cardInfoTitle(p: ContractCard): string {
  const info = p.findings.filter((f) => !isBreakingFinding(f) && !isAcked(f));
  const description = info.filter((f) => definitionClass(f) === 'DESCRIPTION').length;
  return informationalChipTitle(info.length - description, description);
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

function humanTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  });
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
    <header class="topbar">
      <div class="brand">Flanj<span>Collector</span></div>
      <div class="meta" v-if="health">
        <!-- Org identity only — never the integration slug (it scopes a spec,
             not this org; it lives on the Overview headline + its Contracts card). -->
        <span v-if="orgPillName" class="pill" title="Your organization — shown to the provider on every thread.">{{ orgPillName }}</span>
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
          {{ connectPill }}<span class="pill-out" aria-hidden="true">↗</span>
        </a>
        <button
          v-else
          type="button"
          class="pill pill-btn"
          :class="{ ok: connectStatus === 'connected', warn: connectStatus === 'pending' }"
          title="Connect settings"
          @click="setTab('settings')"
        >
          {{ connectPill }}
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
        <!-- red = act (breaking) · amber = review (informational, un-acked) -->
        <span v-if="contractBreakingCount" class="tab-count bad" :title="breakingCountTitle(contractBreakingCount)">{{ contractBreakingCount }}</span>
        <span v-if="contractInfoCount" class="tab-count warn" :title="informationalCountTitle(contractInfoCount)">{{ contractInfoCount }}</span>
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
    <div v-show="tab === 'overview'">
      <!-- Three tones, not two: `neutral` is the install where nothing has been
           validated yet, and it must read as neither the green all-clear nor
           the red drift banner (ui/src/headline.ts). -->
      <section class="headline" :class="headline.tone">
        <!-- Observed state only — the collector does not measure provider health. -->
        <div class="hl-you">
          You: <strong>{{ headline.you }}</strong>
        </div>
        <!-- Pre-traffic honesty: no integration observed on a REST edge yet →
             the fragment is simply absent (no replacement copy). An MCP edge
             alone does not count — the slug is a REST integration's name, and
             the MCP server has its own line below (ui/src/headline.ts). -->
        <div v-if="headline.integration" class="hl-sub">on integration <code>{{ headline.integration }}</code></div>
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
              <div class="edge-head">
                <span>peer host</span><span>observed RPM</span>
              </div>
              <div v-for="e in inboundEdges" :key="'i-' + e.peer_host" class="edge-row" :class="{ drift: e.drift_count > 0 }">
                <span class="peer mono">
                  {{ e.peer_host }}
                  <span v-if="mcpHosts.has(e.peer_host)" class="mcp-badge" :title="MCP_BADGE_TOOLTIP">{{ mcpBadgeLabel(e.class) }}</span>
                </span>
                <span class="num">{{ fmtRPM(e.rpm) }}<span class="unit">/min</span></span>
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
                <span>provider</span><span>observed RPM</span><span></span>
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
                  <span class="num">{{ fmtRPM(e.rpm) }}<span class="unit">/min</span></span>
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
    <div v-show="tab === 'contract'">
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
              <span v-if="cardInfoCount(p)" class="tag warn" :title="cardInfoTitle(p)">{{ informationalChipLabel(cardInfoCount(p)) }}</span>
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
                   BREAKING red filled · NON-BREAKING amber filled · DESCRIPTION amber outline —
                   each badge matches the tab pill that counts it. -->
              <span
                v-if="f.kind === 'definition_change'"
                class="badge"
                :class="{ breaking: definitionClass(f) === 'BREAKING', warning: definitionClass(f) === 'NON-BREAKING', description: definitionClass(f) === 'DESCRIPTION' }"
              >{{ definitionClass(f) }}</span>
              <span v-else class="badge" :class="f.severity">{{ f.severity }}</span>
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
            <div v-if="f.kind === 'definition_change'" class="drift-row" :class="{ plain: definitionClass(f) === 'DESCRIPTION' }">
              <div class="col">
                <div class="k">{{ beforeColLabel(f.spec_version_from || '', snapshotTimes(f.detail).from) }}</div>
                <div class="v expected">{{ f.expected }}</div>
              </div>
              <div class="arrow">≠</div>
              <div class="col">
                <div class="k">{{ afterColLabel(f.spec_version_to || '', snapshotTimes(f.detail).to) }}</div>
                <div class="v actual">{{ f.actual }}</div>
              </div>
            </div>
            <!-- version-diff: the contract you replaced vs the one you uploaded.
                 `expected`/`actual` already ARE the two versions, so only the
                 labels change — neither side is "live", and there is no
                 location, because the change is in the documents. The `detail`
                 paragraph below names the field the rule fired on. -->
            <div v-else-if="f.kind === 'version-diff'" class="drift-row">
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
              {{ defChangeDetail(snapshotTimes(f.detail).from, snapshotTimes(f.detail).to, providerNameFor(f)) }}
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
                <span class="chip" :class="{ attention: threadsByFinding[f.id].summary?.turn === 'fix_reported' || threadsByFinding[f.id].summary?.turn === 'replied_while_closed' }">
                  {{ chipLabel(threadsByFinding[f.id]) }}
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
    <div v-show="tab === 'threads'">
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
        @connect="goToSettings"
      />
    </div>

    <!-- ───────────────────────── SETTINGS ───────────────────────── -->
    <div v-show="tab === 'settings'">
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
    <div v-show="tab === 'traffic'">
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
            <div
              class="tr-row"
              :class="{ drift: isDrifted(c), open: expanded[c.id] }"
              @click="toggle(c.id)"
            >
              <span class="c-when">
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
  </div>
</template>

<style>
/* Palette: NONE of it lives here any more. `src/tokens.css` is the vendored copy
   of the canonical Flanj token layer (docs/design/tokens.css) and is imported
   ahead of this block in main.ts; `src/tokens-pending.css` carries the one
   family the canonical set does not yet define (see its header). This file
   holds layout and component rules only — a hex literal appearing below is a
   bug, not a style choice.

   Theme (ux-design-v2 §3.3) is unchanged by the token adoption: LIGHT is the
   base, dark applies under [data-theme="dark"] ONLY, and there is no
   OS-following state. tokens.css does ship a `prefers-color-scheme` block for
   surfaces whose toggle is optional — index.html stamps data-theme="light" on
   <html> so it never fires here, exactly the escape hatch tokens.css documents.

   Severity is the product-fixed triad and nothing else may borrow it:
   --sev-breaking (red) / --sev-warning (yellow) / --sev-info (neutral), each
   paired with its own -wash / -contrast / -edge role. --accent is NEVER
   semantic. Corners are square (--radius: 0) as a brand decision. */
* { box-sizing: border-box; }
body { margin: 0; background: var(--ground); color: var(--ink); font: 15px/1.5 var(--font-sans); }
.page { max-width: 1040px; margin: 0 auto; padding: 1.5rem 1.25rem 4rem; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.brand { font-weight: 700; letter-spacing: -0.02em; font-size: 1.2rem; }
.brand span { color: var(--ink-soft); font-weight: 500; margin-left: 0.35rem; }
.meta { display: flex; gap: 0.5rem; flex-wrap: wrap; }
.pill { background: var(--surface-sunk); border: 1px solid var(--rule); color: var(--ink-soft); border-radius: var(--radius); padding: 0.15rem 0.6rem; font-size: 0.8rem; }
.pill.warn { color: var(--sev-warning-ink); border-color: var(--sev-warning-ink); }
.banner { margin: 1rem 0 0; }

/* Tabs */
.tabs { display: flex; gap: 0.25rem; margin: 1.35rem 0 0.5rem; border-bottom: 1px solid var(--rule); }
.tabs button { background: transparent; border: 0; border-bottom: 2px solid transparent; color: var(--ink-soft); font: inherit; font-weight: 600; padding: 0.55rem 0.9rem; cursor: pointer; display: inline-flex; align-items: center; gap: 0.45rem; margin-bottom: -1px; }
.tabs button:hover { color: var(--ink); }
.tabs button.active { color: var(--ink); border-bottom-color: var(--accent-ink); }
.tab-count { background: var(--surface-sunk); border: 1px solid var(--rule); color: var(--ink-soft); border-radius: var(--radius); font-size: 0.72rem; font-weight: 700; padding: 0.02rem 0.4rem; min-width: 1.2rem; text-align: center; }
.tab-count.bad { background: var(--sev-breaking); border-color: var(--sev-breaking); color: var(--sev-breaking-contrast); }
.tab-count.warn { background: var(--sev-warning); border-color: var(--sev-warning); color: var(--sev-warning-contrast); }

/* Health */
.headline { margin: 1rem 0; padding: 1rem 1.15rem; border-radius: var(--radius); border: 1px solid var(--rule); background: var(--surface); }
.headline.drift { border-color: var(--sev-breaking); background: var(--sev-breaking-wash); }
.hl-you { font-size: 1.15rem; }
.hl-sub { color: var(--ink-soft); font-size: 0.85rem; margin-top: 0.3rem; }
.headline.drift .hl-you strong { color: var(--sev-breaking); }
.headline.ok .hl-you strong { color: var(--verified-ink); }
/* Neutral: nothing has been validated yet. Deliberately uncoloured — the two
   coloured tones are verdicts, and this state has not reached one. */
.headline.neutral .hl-you strong { color: var(--ink-soft); font-weight: 600; }
.hint { color: var(--ink-soft); font-size: 0.88rem; margin-top: 1rem; }

/* Shared */
h2 { font-size: 1rem; margin: 1.25rem 0 0.75rem; border-bottom: 1px solid var(--rule); padding-bottom: 0.4rem; }
h2 small { color: var(--ink-soft); font-weight: 400; margin-left: 0.5rem; }
.empty { color: var(--ink-soft); }
.mono { font-family: var(--font-mono); }
code { font-family: var(--font-mono); }

/* Contract findings */
.finding { background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.finding-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.badge { text-transform: uppercase; font-size: 0.68rem; letter-spacing: 0.05em; padding: 0.15rem 0.45rem; border-radius: var(--radius); font-weight: 700; }
.badge.breaking { background: var(--sev-breaking); color: var(--sev-breaking-contrast); }
.badge.warning { background: var(--sev-warning); color: var(--sev-warning-contrast); }
/* INFO is the neutral tier of the product-fixed triad. It used to be filled with
   --accent; accent is never semantic, and a blue "info" badge sitting beside a red
   and a yellow one read as a fourth severity. --sev-info IS the neutral tier. */
.badge.info { background: var(--sev-info); color: var(--sev-info-contrast); }
/* DESCRIPTION: amber OUTLINE, never a fill — the tier a description change maps
   to in the canonical vocabulary is `warning` ("the wording an agent steers on
   changed"), and the outline keeps it distinguishable from a filled WARNING badge
   by shape as well as hue. */
.badge.description { background: transparent; color: var(--sev-warning-ink); border: 1px solid var(--sev-warning-ink); }
.endpoint { font-weight: 600; }
.rule { color: var(--ink-soft); font-family: var(--font-mono); font-size: 0.85rem; }
.drift-row { display: flex; align-items: stretch; gap: 0.75rem; margin-top: 0.85rem; flex-wrap: wrap; }
.col { flex: 1; min-width: 140px; background: var(--surface-sunk); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.5rem 0.65rem; }
.col .k { font-size: 0.72rem; color: var(--ink-soft); text-transform: uppercase; letter-spacing: 0.04em; }
.col .v { margin-top: 0.2rem; font-family: var(--font-mono); word-break: break-word; }
.v.expected { color: var(--verified-ink); }
.v.actual { color: var(--sev-breaking); }
/* DESCRIPTION rows: a wording change is not a severity diff — plain ink. */
.drift-row.plain .v.expected, .drift-row.plain .v.actual { color: var(--ink); }
.arrow { align-self: center; color: var(--ink-soft); font-size: 1.2rem; }
.detail { color: var(--ink-soft); margin: 0.75rem 0 0; }
.corr { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-top: 0.85rem; padding-top: 0.75rem; border-top: 1px dashed var(--rule); }
.corr-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--ink-soft); }
.corr-k { font-size: 0.82rem; color: var(--ink-soft); }
.corr-k code { color: var(--accent-ink); background: var(--surface-sunk); padding: 0.1rem 0.35rem; border-radius: var(--radius); }
.actions { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; margin-top: 0.9rem; }
/* Buttons (shared by the Connect panel, Flag sheet and Threads tab) */
.btn { background: var(--surface-sunk); color: var(--ink); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.42rem 0.85rem; font: inherit; font-size: 0.88rem; font-weight: 600; cursor: pointer; }
.btn:hover { border-color: var(--ink-soft); }
.btn.primary { background: var(--accent); color: var(--accent-contrast); border-color: var(--accent); }
.btn.primary:hover { filter: brightness(1.08); }
.btn.ghost { background: transparent; color: var(--ink-soft); }
.btn.ghost:hover { color: var(--ink); }
.btn.small { padding: 0.28rem 0.65rem; font-size: 0.8rem; }
.btn.attention { color: var(--sev-warning-ink); border-color: var(--sev-warning-ink); }
.btn:disabled { opacity: 0.6; cursor: default; }
.chip { display: inline-flex; align-items: center; font-size: 0.8rem; font-weight: 600; color: var(--verified-ink); border: 1px solid var(--verified-ink); border-radius: var(--radius); padding: 0.15rem 0.6rem; }
.chip.attention { color: var(--sev-warning-ink); border-color: var(--sev-warning-ink); }
.hint-inline { color: var(--ink-soft); font-size: 0.82rem; }
.small-err { font-size: 0.82rem; }
.pill-btn { cursor: pointer; font: inherit; font-size: 0.8rem; }
.pill.ok { color: var(--verified-ink); border-color: var(--verified-ink); }
.tab-right { margin-left: auto; }
.tab-dot { width: 8px; height: 8px; border-radius: var(--radius); background: var(--sev-warning); display: inline-block; }
.tab-dot.disconnected { background: var(--ink-soft); }
.connect-banner { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; flex-wrap: wrap; margin: 0.75rem 0 0; padding: 0.6rem 0.9rem; border: 1px solid var(--sev-warning); border-radius: var(--radius); background: var(--surface); font-size: 0.88rem; }
.connect-banner-actions { display: flex; gap: 0.5rem; }
.connect-banner.info { border-color: var(--rule); color: var(--ink-soft); }
/* The one-time theme-flip notice sits above the tab strip, not inside a tab. */
.theme-flip-banner { margin-top: 1rem; }
.error { color: var(--sev-breaking); }

/* Traffic toolbar: search + facet filters + live/pause control */
.tr-toolbar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; padding: 0.6rem 0; background: var(--ground); }
.tr-search { flex: 1 1 240px; min-width: 180px; background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 0.88rem; padding: 0.4rem 0.7rem; }
.tr-search::placeholder { color: var(--ink-soft); }
.tr-search:focus, .tr-select:focus { outline: none; border-color: var(--accent); }
.tr-select { background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 0.82rem; padding: 0.38rem 0.5rem; }
.tr-chk { display: inline-flex; align-items: center; gap: 0.35rem; color: var(--ink-soft); font-size: 0.82rem; cursor: pointer; white-space: nowrap; user-select: none; }
.tr-chk input { accent-color: var(--accent-ink); }
.tr-clear { background: transparent; border: 1px solid var(--rule); border-radius: var(--radius); color: var(--ink-soft); font: inherit; font-size: 0.8rem; padding: 0.3rem 0.6rem; cursor: pointer; }
.tr-clear:hover { color: var(--ink); border-color: var(--ink-soft); }
.tr-count { color: var(--ink-soft); font-size: 0.8rem; font-variant-numeric: tabular-nums; white-space: nowrap; margin-left: auto; }
.live-btn { display: inline-flex; align-items: center; gap: 0.4rem; background: var(--surface); border: 1px solid var(--verified-ink); border-radius: var(--radius); color: var(--verified-ink); font: inherit; font-size: 0.8rem; font-weight: 700; padding: 0.3rem 0.75rem; cursor: pointer; white-space: nowrap; }
.live-dot { width: 8px; height: 8px; border-radius: var(--radius); background: var(--verified); animation: live-pulse 1.6s ease-in-out infinite; }
.live-btn.paused { border-color: var(--sev-warning-ink); color: var(--sev-warning-ink); }
.live-btn.paused .live-dot { background: var(--sev-warning); animation: none; }
@keyframes live-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }
.pending-bar { display: block; width: 100%; background: var(--surface-sunk); border: 1px solid var(--accent-ink); border-radius: var(--radius); color: var(--accent-ink); font: inherit; font-size: 0.82rem; font-weight: 700; padding: 0.45rem 0.75rem; margin: 0 0 0.5rem; cursor: pointer; text-align: center; }
.pending-bar:hover { background: var(--surface); }
.tr-nomatch { display: flex; align-items: center; gap: 0.6rem; margin: 0; padding: 1rem; }

/* Traffic table */
.traffic { border: 1px solid var(--rule); border-radius: var(--radius); overflow: hidden; background: var(--surface); }
.tr-head, .tr-row {
  display: grid;
  grid-template-columns: 1.15fr 1.9fr 1.35fr 0.55fr 1.4fr 0.9fr;
  gap: 0.65rem;
  align-items: center;
  padding: 0.55rem 0.9rem;
}
.tr-head { color: var(--ink-soft); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; border-bottom: 1px solid var(--rule); background: var(--surface-sunk); }
.tr-row { border-top: 1px solid var(--rule); cursor: pointer; font-size: 0.9rem; }
.tr-row:first-child { border-top: 0; }
.tr-row:hover { background: var(--surface-sunk); }
.tr-row.open { background: var(--surface-sunk); }
.tr-row.drift { box-shadow: inset 3px 0 0 var(--sev-breaking); }
.chev { color: var(--ink-soft); display: inline-block; width: 1rem; }
.c-when { color: var(--ink-soft); white-space: nowrap; }
.c-call { display: flex; align-items: center; gap: 0.5rem; min-width: 0; }
.method { font-weight: 700; font-size: 0.72rem; padding: 0.1rem 0.4rem; border-radius: var(--radius); background: var(--surface-sunk); border: 1px solid var(--rule); color: var(--ink-soft); text-transform: uppercase; }
/* Method chips are NEUTRAL. They used to borrow the palette — POST green, GET
   accent, DELETE the severity red — which put a `breaking` red on a DELETE that
   was behaving perfectly. Severity is its own triad and nothing else may spend
   it; the verb itself is the distinguisher, and it is already uppercase mono. */
.route { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-code { font-family: var(--font-mono); }
.status-code.err { color: var(--sev-breaking); }
.c-corr { display: flex; flex-direction: column; font-size: 0.78rem; color: var(--accent-ink); min-width: 0; }
.c-corr span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-corr .dim { color: var(--ink-soft); }
.tag { font-size: 0.72rem; font-weight: 700; padding: 0.12rem 0.5rem; border-radius: var(--radius); text-transform: uppercase; letter-spacing: 0.03em; }
.tag.ok { color: var(--verified-ink); border: 1px solid var(--verified-ink); }
.tag.drift { background: var(--sev-breaking); color: var(--sev-breaking-contrast); }
.tag.warn { background: var(--sev-warning); color: var(--sev-warning-contrast); }
.tag.none { color: var(--ink-soft); border: 1px solid var(--rule); }

/* Traffic counterparty cell */
.c-peer { display: flex; align-items: center; gap: 0.45rem; min-width: 0; }
.dir-chip { font-size: 0.64rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; border-radius: var(--radius); padding: 0.08rem 0.35rem; flex: none; }
.dir-chip.out { color: var(--accent-ink); border: 1px solid var(--accent-ink); }
.dir-chip.in { color: var(--verified-ink); border: 1px solid var(--verified-ink); }
.peer-host { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 0.82rem; }
/* Named row: the host demotes to the same under-line treatment the Edges panel
   gives it — still there, still selectable, just no longer the headline. An
   UNNAMED row keeps the rule above untouched, i.e. renders exactly as before. */

/* Contract cards (self + provider) */
.provider { background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.provider.self { border-color: var(--accent); }
.prov-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.prov-name { font-weight: 700; font-size: 1.02rem; }
.prov-host { color: var(--ink-soft); font-size: 0.85rem; }
.fmt-badge { text-transform: uppercase; font-size: 0.66rem; letter-spacing: 0.05em; font-weight: 700; color: var(--accent-ink); border: 1px solid var(--accent-ink); border-radius: var(--radius); padding: 0.1rem 0.4rem; }
.prov-ver { color: var(--ink-soft); font-family: var(--font-mono); font-size: 0.85rem; }
.prov-status { margin-left: auto; }
.prov-links { display: flex; align-items: center; gap: 0.9rem; flex-wrap: wrap; margin-top: 0.65rem; }
.doc-link { color: var(--accent-ink); font-size: 0.85rem; text-decoration: none; border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.28rem 0.7rem; background: var(--surface-sunk); }
.doc-link:hover { border-color: var(--accent); }
.prov-meta { color: var(--ink-soft); font-size: 0.8rem; }
/* The evidence count rides in the same muted channel as provenance — it is a
   fact about this card, not a warning, and zero must not be dressed as one. */
.prov-meta.evidence { margin-left: auto; }
/* The origin sits IN the heading at the same size as the name, muted — the fact
   that separates two servers sharing a name has to be where the eye already is,
   not in a 0.78rem slug at the end of the row. */
.prov-origin { color: var(--ink-soft); font-weight: 400; }
.prov-nospec { color: var(--ink-soft); font-size: 0.88rem; margin: 0.6rem 0 0; }
/* The document-cap line. Same slot and same size as .prov-nospec — it is the
   same kind of sentence — in the amber TEXT role rather than the muted one,
   because this one names something the operator can act on. --sev-warning, not
   --sev-warning-edge: the fill is for chips, and --accent is never semantic. */
.prov-oversize { color: var(--sev-warning-ink); font-size: 0.88rem; margin: 0.6rem 0 0; }
.prov-integration { color: var(--ink-soft); font-size: 0.78rem; }
.finding.nested { background: var(--surface-sunk); margin: 0.75rem 0 0; }
/* Acked rows: dimmed in place (matches the disabled idiom); evidence stays visible. */
.finding.acked { opacity: 0.55; }
/* The #contracts/<finding_id> deep-link target — same accent bar as the
   Threads tab's highlighted row. */
.finding.highlight { box-shadow: inset 3px 0 0 var(--accent); }

.tr-detail { border-top: 1px dashed var(--rule); background: var(--ground); padding: 0.85rem 0.9rem 1.1rem; }
.meta-line { color: var(--ink-soft); font-size: 0.82rem; display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; margin-bottom: 0.75rem; }
.meta-line .dim { opacity: 0.5; }
.redacted-tag { color: var(--sev-warning-ink); }
.reqres { display: grid; grid-template-columns: 1fr 1fr; gap: 0.85rem; }
.rr-col { min-width: 0; }
.rr-title { font-weight: 700; font-size: 0.85rem; margin-bottom: 0.4rem; }
.rr-sub { color: var(--ink-soft); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.04em; margin: 0.55rem 0 0.25rem; }
.hdrs { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.hdrs td { padding: 0.12rem 0.4rem; border-bottom: 1px solid var(--rule); vertical-align: top; }
.hk { color: var(--ink-soft); white-space: nowrap; width: 1%; }
.hv { color: var(--ink); word-break: break-all; }
pre.body { background: var(--surface-sunk); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.6rem 0.75rem; margin: 0; font-family: var(--font-mono); font-size: 0.8rem; line-height: 1.45; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }

/* Edges overview */
.edge-groups { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.5rem; }
.edge-group { background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.9rem 1rem 1.1rem; }
.edge-title { font-size: 0.95rem; margin: 0 0 0.75rem; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.edge-title small { color: var(--ink-soft); font-weight: 400; }
.dir-badge { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 700; padding: 0.15rem 0.5rem; border-radius: var(--radius); }
.dir-badge.out { background: var(--accent); color: var(--accent-contrast); }
.dir-badge.in { background: var(--verified); color: var(--verified-contrast); }
.edge-table { border: 1px solid var(--rule); border-radius: var(--radius); overflow: hidden; }
.edge-head, .edge-row { display: grid; grid-template-columns: 2.4fr 1fr; gap: 0.5rem; align-items: center; padding: 0.4rem 0.7rem; }
.edge-head { color: var(--ink-soft); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em; background: var(--surface-sunk); border-bottom: 1px solid var(--rule); }
/* "OBSERVED RPM" wrapped to two lines at narrow widths and became the tallest
   thing in the header row — the column labels never wrap. */
.edge-head span { white-space: nowrap; }
.edge-row { border-top: 1px solid var(--rule); font-size: 0.85rem; }
.edge-row:first-child { border-top: 0; }
.edge-row.drift { box-shadow: inset 3px 0 0 var(--sev-breaking); }
.edge-row .peer { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.edge-row .role { color: var(--ink-soft); }
/* v1p1 edge naming: outbound rows carry name-over-host + actions. */
/* The name cell gets a real floor: a fractional track collapsed it to ~59px at
   every width, truncating a saved name to "Acme …" (a single clipped letter at
   900px). minmax(9rem, 1fr) + max-content RPM keeps the name readable. */
.edge-head.named, .edge-row.named { grid-template-columns: minmax(9rem, 1fr) max-content auto; }
.edge-name-cell { display: flex; flex-direction: column; gap: 0.1rem; min-width: 0; }
.edge-name-line { display: flex; align-items: center; gap: 0.4rem; min-width: 0; }
.edge-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* An unnamed row IS its host: same mono treatment the named row's host line gets, so the two
   row shapes read as one column and nothing looks like a mangled name. */
.edge-name.unnamed { color: var(--ink); font-weight: 400; font-family: var(--font-mono); font-size: 0.82rem; }
.edge-host { color: var(--ink-soft); font-size: 0.75rem; }
/* Contract coverage on an outbound row: the SAME muted text channel as
   .edge-host, one line below it. Not the badge lane and not the actions cell —
   the cell is `auto` inside minmax(9rem,1fr) max-content auto, a user-named row
   already carries two buttons, and a third is untested at 900px. A text link
   dodges the width fight entirely and does strictly more than a chip would. */
.edge-contract { font-size: 0.75rem; line-height: 1.35; }
.edge-contract-link {
  background: none; border: 0; padding: 0; margin: 0;
  font: inherit; color: var(--ink-soft); cursor: pointer;
  text-align: left; text-decoration: none;
}
.edge-contract-link:hover, .edge-contract-link:focus-visible { color: var(--ink); text-decoration: underline; }
/* `Add contract` stays in the SAME muted channel as everything else on this
   line — no accent, no fill, no border. Rendered at 30 real rows it was
   accent-coloured, and 27 blue links marching down the column was the `auto`
   wall verbatim: louder than the chip this design exists to avoid, and it
   inverted the signal, since the three covered rows read quieter than the
   uncovered ones. Muted, it is present when you look at a row and invisible
   when you scan the column, which is the whole point of putting it here rather
   than in the badge lane. The dotted underline is what marks it as a control
   without spending colour on it. */
.edge-contract-link.add { border-bottom: 1px dotted var(--rule); }
.edge-contract-link.add:hover, .edge-contract-link.add:focus-visible { border-bottom-color: currentColor; text-decoration: none; }
/* The roll call: one line under the group heading, above the rows. */
.edge-rollcall { margin: -0.15rem 0 0.55rem; font-size: 0.8rem; color: var(--ink-soft); }

/* Providers with no contract — rows, not cards. Collapsed by default. */
.uncovered h2 { margin-bottom: 0.4rem; }
.uncovered-toggle {
  background: none; border: 0; padding: 0; font: inherit; color: inherit;
  cursor: pointer; display: inline-flex; align-items: center; gap: 0.4rem;
}
.uncovered-toggle .chev { display: inline-block; transition: transform 120ms ease-out; color: var(--ink-soft); font-size: 0.8em; }
.uncovered-toggle .chev.open { transform: rotate(90deg); }
.uncovered-rows { border: 1px solid var(--rule); border-radius: var(--radius); overflow: hidden; }
.uncovered-row { padding: 0.55rem 0.7rem; border-bottom: 1px solid var(--rule); }
.uncovered-row:last-child { border-bottom: 0; }
.uncovered-row.highlight { background: var(--surface-sunk); }
.uncovered-line { display: flex; align-items: center; justify-content: space-between; gap: 0.6rem; }
.uncovered-host { font-size: 0.85rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.uncovered-lede { margin: 0 0 0.5rem; font-size: 0.82rem; color: var(--ink-soft); }

/* The after-state: what happens NEXT, said once, above the cards. */
.upload-notice {
  margin: 0 0 0.8rem; padding: 0.5rem 0.7rem; font-size: 0.85rem;
  border-left: 3px solid var(--accent); background: var(--surface-sunk); color: var(--ink);
}
.upload-notice.error { border-left-color: var(--sev-breaking); }

/* The uploader. Rendered inline on whichever row opened it — there is one
   mutation, so only one can be open at a time. */
.uploader { border: 1px dashed var(--rule); border-radius: var(--radius); padding: 0.7rem; margin-top: 0.5rem; background: var(--surface-sunk); }
.uploader-host { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--ink-soft); margin-bottom: 0.6rem; }
.uploader-host input {
  padding: 0.35rem 0.5rem; border: 1px solid var(--rule); border-radius: var(--radius);
  background: var(--surface); color: var(--ink); font-size: 0.85rem; max-width: 22rem;
}
.dropzone {
  border: 1px dashed var(--rule); border-radius: var(--radius); padding: 1.1rem 0.8rem;
  text-align: center; background: var(--surface);
}
.dropzone.dragging { border-color: var(--accent); background: var(--surface-sunk); }
.dz-prompt { margin: 0 0 0.15rem; font-size: 0.88rem; }
.dz-formats { margin: 0 0 0.6rem; font-size: 0.78rem; color: var(--ink-soft); }
/* The privacy line sits AT the picker, where the document is chosen — the one
   moment the operator is deciding whether to hand over a vendor's document. */
.uploader-privacy { margin: 0.55rem 0 0; font-size: 0.78rem; color: var(--ink); }
.uploader-note { margin: 0.2rem 0 0; font-size: 0.78rem; color: var(--ink-soft); }
.uploader-error { margin: 0.5rem 0 0; font-size: 0.82rem; color: var(--sev-breaking); }
.uploader-actions { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-top: 0.7rem; }
.confirm-facts { display: grid; gap: 0.25rem; margin: 0 0 0.5rem; }
.confirm-facts > div { display: flex; gap: 0.5rem; font-size: 0.85rem; }
.confirm-facts dt { color: var(--ink-soft); min-width: 6rem; }
.confirm-facts dd { margin: 0; }
/* The binding checklist. A `servers:` mismatch used to render in body ink —
   "warn, never block" means warn VISIBLY, and that was a whisper. Warnings get
   the warning colour and a marker; the button still says go. */
.binding-checks { list-style: none; margin: 0 0 0.5rem; padding: 0.5rem 0.6rem; display: grid; gap: 0.3rem; border-radius: var(--radius); background: var(--surface); border: 1px solid var(--rule); }
.binding-checks.warned { border-color: var(--sev-warning-edge); background: var(--sev-warning-wash); }
.binding-checks li { display: flex; gap: 0.45rem; align-items: flex-start; font-size: 0.8rem; line-height: 1.45; color: var(--ink-soft); }
.binding-checks li.warn { color: var(--ink); }
.binding-checks .chk { flex: none; width: 1em; text-align: center; font-weight: 700; color: var(--ink-soft); }
.binding-checks li.warn .chk { color: var(--sev-warning-ink); }
.confirm-host { margin: 0.1rem 0 0.6rem; }
.confirm-host input:disabled { opacity: 0.7; cursor: not-allowed; }
.host-hint, .host-locked { font-size: 0.72rem; color: var(--ink-soft); }
.host-awaiting { font-size: 0.75rem; color: var(--ink); }
.uploader-host.awaiting input { border-color: var(--accent); }
.confirm-timing { margin: 0 0 0.4rem; font-size: 0.78rem; color: var(--ink-soft); }
.host-hint code { font-size: 0.95em; }
.btn.warn { border-color: var(--sev-warning-ink); }
.pretraffic h2 { margin-bottom: 0.5rem; }
/* The deck's badge strings are lowercase ("named by you" · "config" ·
   "directory" · "auto") — uppercasing them made all four read as one shouted
   pill. Render the copy as written. */
.pill-link { text-decoration: none; display: inline-flex; align-items: center; gap: 0.3rem; }
.pill-link:hover { text-decoration: underline; }
.pill-out { font-size: 0.85em; opacity: 0.75; }
.edge-actions { display: flex; gap: 0.35rem; justify-content: flex-end; }
.edge-rename { border-top: 1px dashed var(--rule); background: var(--surface-sunk); padding: 0.6rem 0.7rem; display: flex; flex-direction: column; gap: 0.45rem; }
.edge-rename-input { width: 100%; max-width: 26rem; padding: 0.35rem 0.5rem; border: 1px solid var(--rule); border-radius: var(--radius); background: var(--surface); color: var(--ink); font-size: 0.85rem; }
.edge-suggest { display: flex; align-items: flex-start; gap: 0.4rem; font-size: 0.78rem; color: var(--ink-soft); }
.edge-suggest input { margin-top: 0.15rem; }
.edge-rename-actions { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; }
.edge-name-note { margin: 0; padding: 0.35rem 0.7rem; font-size: 0.78rem; color: var(--ink-soft); border-top: 1px dashed var(--rule); }
.edge-row .num { text-align: right; font-variant-numeric: tabular-nums; }
.edge-row .unit { color: var(--ink-soft); font-size: 0.72rem; margin-left: 0.12rem; }
.empty.small { font-size: 0.85rem; }
.occ { font-size: 0.72rem; color: var(--sev-warning-ink); font-weight: 700; border: 1px solid var(--sev-warning-ink); border-radius: var(--radius); padding: 0.05rem 0.45rem; }
.occ.single { color: var(--ink-soft); border-color: var(--rule); font-weight: 500; }

/* ─── MCP surfaces (v0.5) ─── */
.mcp-badge { font-size: 0.64rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; color: var(--accent-ink); border: 1px solid var(--accent-ink); border-radius: var(--radius); padding: 0.08rem 0.35rem; margin-left: 0.4rem; vertical-align: middle; white-space: nowrap; }
.mcp-headline { margin-top: -0.35rem; }
.mcp-headline .hl-you { font-size: 1rem; display: flex; align-items: center; gap: 0.55rem; }
.mcp-headline .mcp-badge { margin-left: 0; }
.local-notices { margin: 1rem 0; padding: 0.85rem 1.1rem; border-radius: var(--radius); border: 1px dashed var(--rule); background: var(--surface); }
.ln-head { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; }
.ln-title { font-weight: 700; font-size: 0.9rem; }
.ln-sub { color: var(--ink-soft); font-size: 0.82rem; }
.ln-list { margin: 0.55rem 0 0; padding-left: 1.1rem; }
.ln-item { color: var(--ink-soft); font-size: 0.88rem; margin-top: 0.25rem; }
.tool-rows { margin-top: 0.65rem; border: 1px solid var(--rule); border-radius: var(--radius); overflow: hidden; }
.tool-row { padding: 0.42rem 0.7rem; border-top: 1px solid var(--rule); }
.tool-row:first-child { border-top: 0; }
.tool-line { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.tool-name { font-size: 0.85rem; }
.tool-tag { font-size: 0.72rem; color: var(--verified-ink); border: 1px solid var(--verified-ink); border-radius: var(--radius); padding: 0.05rem 0.45rem; }
.tool-tag.partial { color: var(--ink-soft); border-color: var(--rule); }
.tool-note { color: var(--ink-soft); font-size: 0.8rem; margin: 0.3rem 0 0; }

/* Appearance (Settings) */
.theme-field { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; }
.theme-label { font-size: 0.88rem; font-weight: 600; }
.theme-help { color: var(--ink-soft); font-size: 0.82rem; }
.seg { display: inline-flex; border: 1px solid var(--rule); border-radius: var(--radius); overflow: hidden; }
.seg button { background: var(--surface); border: 0; border-left: 1px solid var(--rule); color: var(--ink-soft); font: inherit; font-size: 0.85rem; font-weight: 600; padding: 0.35rem 0.85rem; cursor: pointer; }
.seg button:first-child { border-left: 0; }
.seg button:hover { color: var(--ink); }
.seg button.active { background: var(--accent); color: var(--accent-contrast); }

/* The two edge tables need the full width well before the phone breakpoint —
   side by side they squeeze the name cell to a few characters. */
@media (max-width: 1024px) {
  .edge-groups { grid-template-columns: 1fr; }
}

@media (max-width: 720px) {
  .tr-head { display: none; }
  .tr-row { grid-template-columns: 1fr 1fr; grid-auto-rows: min-content; }
  .reqres { grid-template-columns: 1fr; }
}
</style>
