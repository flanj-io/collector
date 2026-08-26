<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { ApiError, apiGet, apiPost, openThreadInNewTab } from './api';
import ConnectPanel from './ConnectPanel.vue';
import FlagSheet from './FlagSheet.vue';
import ThreadsTab from './ThreadsTab.vue';
import { chipLabel, needsCollectorAddress, threadIdFromHash, timeAgo, type ConnectState, type ThreadRow } from './threads';
import { applyTheme, loadThemePref, saveThemePref, type ThemePref } from './theme';
import {
  ACK_LABEL,
  ACK_TITLE,
  JSONRPC_ID_TITLE,
  LOCAL_NOTE_NOT_FLAGGABLE,
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
}

type Tab = 'overview' | 'traffic' | 'contract' | 'threads' | 'settings';

const health = ref<Health | null>(null);
const findings = ref<Finding[]>([]);
const calls = ref<RedactedCall[]>([]);
const edges = ref<Edge[]>([]);
const contracts = ref<SpecInfo[]>([]);
// True once /api/contracts has answered OK — older collectors lack the endpoint,
// and only a real answer lets us assert "no contract loaded" per provider.
const contractsKnown = ref(false);
const loadError = ref('');
// ─── Connect + threads (v0.1a) ───────────────────────────────────────────
// Connect state comes from the relay (`GET /api/connect`, refreshed from the CP);
// polled every 5s while a confirmation is pending (or the Settings tab / a Flag
// sheet is open) and on focus, so "Create thread" unlocks the moment the
// contact clicks the confirmation. Threads come from `GET /api/threads` (every
// row with its CP summary) and feed both the Threads tab and the finding chips.
const connect = ref<ConnectState | null>(null);
const threads = ref<ThreadRow[]>([]);
const threadsLoaded = ref(false);
const threadsError = ref('');
const sheetFinding = ref<Finding | null>(null);
const highlightThreadId = ref<string | null>(null);
const connectBannerDismissed = ref(localStorage.getItem('vinifera.connect.banner.dismissed') === '1');
// Post-Connect nudge (v0.1b): Connected but no collector address yet — email
// links can't deep-link back here. One dismissible line on the Connect panel
// and the Threads tab; the dismissal is remembered (shared by both).
const addressNudgeDismissed = ref(localStorage.getItem('vinifera.address.nudge.dismissed') === '1');
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

async function loadConnect() {
  try {
    connect.value = await apiGet<ConnectState>('/api/connect');
  } catch {
    /* keep the last known state; the health poll still reports the store's view */
  }
}

async function loadThreads() {
  try {
    threads.value = (await apiGet<ThreadRow[]>('/api/threads')) || [];
    threadsError.value = '';
  } catch (e) {
    threadsError.value = 'Could not load threads from this collector.';
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
  localStorage.setItem('vinifera.connect.banner.dismissed', '1');
}

const showAddressNudge = computed(
  () => !!health.value?.cp_configured && needsCollectorAddress(connect.value) && !addressNudgeDismissed.value
);

function dismissAddressNudge() {
  addressNudgeDismissed.value = true;
  localStorage.setItem('vinifera.address.nudge.dismissed', '1');
}

// "Add address" from the Threads tab: jump to Settings with the address field focused.
function addCollectorAddress() {
  tab.value = 'settings';
  focusAddressTick.value++;
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
  sheetFinding.value = f;
  if (connectStatus.value !== 'connected') loadConnect();
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

function goToThread(threadId: string) {
  highlightThreadId.value = threadId;
  tab.value = 'threads';
  if (window.location.hash !== '#threads/' + threadId) history.replaceState(null, '', '#threads/' + encodeURIComponent(threadId));
}

function applyHash() {
  const id = threadIdFromHash(window.location.hash);
  if (id) {
    highlightThreadId.value = id;
    tab.value = 'threads';
  } else if (window.location.hash === '#threads') {
    tab.value = 'threads';
  } else if (window.location.hash === '#settings') {
    tab.value = 'settings';
  }
}

const tab = ref<Tab>('overview');
const expanded = ref<Record<string, boolean>>({});

// ─── Appearance (Settings): System / Light / Dark, default System ─────────
// Persisted as `vinifera.theme`; applied as data-theme on <html>. The System
// state carries no attribute — the CSS prefers-color-scheme media query tracks
// the OS live (no JS listener needed).
const themePref = ref<ThemePref>(loadThemePref());
function setTheme(pref: ThemePref) {
  themePref.value = pref;
  saveThemePref(pref);
  applyTheme(pref);
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

const outboundEdges = computed(() => edges.value.filter((e) => e.direction === 'client'));
const inboundEdges = computed(() => edges.value.filter((e) => e.direction === 'server'));

// Integration ids that label SELF-contract findings (our own API, inbound).
const selfIntegrations = computed(
  () => new Set(contracts.value.filter((s) => s.role === 'self').map((s) => s.integration))
);

// The drifted endpoints derived from live-vs-spec findings. A drift is
// per-endpoint, so every call on a drifted endpoint is itself drifted — not
// only the representative source call. Provider findings key by
// "<integration> <METHOD ROUTE>" and match outbound calls; self findings key by
// endpoint alone and match inbound calls (inbound calls carry the org's
// integration id, not the self contract's label).
const driftedEndpoints = computed(() => {
  const provider = new Set<string>();
  const self = new Set<string>();
  for (const f of findings.value) {
    if (f.kind !== 'live-vs-spec') continue;
    if (selfIntegrations.value.has(f.integration)) self.add(f.endpoint);
    else provider.add(`${f.integration} ${f.endpoint}`);
  }
  return { provider, self };
});

function isDrifted(c: RedactedCall): boolean {
  if (c.transport === 'mcp') return mcpDriftedTools.value.has(`${c.integration} ${toolNameOf(c)}`);
  if (c.direction === 'server') return driftedEndpoints.value.self.has(`${c.method} ${c.route}`);
  return driftedEndpoints.value.provider.has(`${c.integration} ${c.method} ${c.route}`);
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
    if (fContract.value === 'conforming' && isDrifted(c)) return false;
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
// responses). Providers = loaded provider contracts first, then providers only
// known from findings (older collector without /api/contracts), then discovered
// outbound edges with no contract loaded — those are only asserted when the
// collector actually answered /api/contracts.
interface ContractCard {
  key: string;
  name: string;
  peerHost: string;
  spec: SpecInfo | null;
  findings: Finding[];
}

const contractCards = computed<{ self: ContractCard[]; providers: ContractCard[] }>(() => {
  const byIntegration = new Map<string, Finding[]>();
  for (const f of [...liveFindings.value, ...mcpContractFindings.value]) {
    const list = byIntegration.get(f.integration) || [];
    list.push(f);
    byIntegration.set(f.integration, list);
  }
  const self: ContractCard[] = [];
  const providers: ContractCard[] = [];
  const coveredHosts = new Set<string>();
  for (const s of contracts.value) {
    const card: ContractCard = {
      key: 'spec-' + s.integration,
      name: s.title || humanize(s.integration) || s.peer_host || (s.role === 'self' ? 'Your API' : 'Provider'),
      peerHost: s.peer_host || '',
      spec: s,
      findings: byIntegration.get(s.integration) || []
    };
    byIntegration.delete(s.integration);
    if (s.role === 'self') {
      self.push(card);
    } else {
      providers.push(card);
      if (s.peer_host) coveredHosts.add(s.peer_host);
    }
  }
  for (const [integration, fs] of byIntegration) {
    providers.push({ key: 'find-' + integration, name: humanize(integration) || integration, peerHost: '', spec: null, findings: fs });
  }
  if (contractsKnown.value) {
    for (const e of outboundEdges.value) {
      if (!coveredHosts.has(e.peer_host)) {
        providers.push({ key: 'edge-' + e.peer_host, name: e.peer_host, peerHost: e.peer_host, spec: null, findings: [] });
      }
    }
  }
  return { self, providers };
});

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
      ? 'No self contract loaded. Point viniferadrift.self_spec_path at the OpenAPI document you publish to catch your own drift before your consumers do.'
      : ''
  },
  {
    key: 'providers',
    title: 'Provider contracts',
    sub: 'the contracts your providers publish — your outbound calls validated against them',
    cards: contractCards.value.providers,
    emptyText:
      'No provider contracts yet. Point viniferadrift.spec_path at a provider’s OpenAPI document, or send traffic through the SDK to discover providers.'
  }
]);

const liveFindings = computed(() => findings.value.filter((f) => f.kind === 'live-vs-spec'));

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
// change (breaking, no calls affected yet) → clean.
const mcpOverview = computed(() =>
  mcpContracts.value.map((s) => ({
    key: s.integration,
    headline: mcpHeadline(
      { name: s.title || s.integration, version: s.version },
      mcpFindings.value.filter((f) => f.integration === s.integration),
      humanTime
    )
  }))
);

// Local notices (deck §2): stale_client + DESCRIPTION-only definition changes.
// Visible to you only; these items NEVER carry a flag control. An acknowledged
// notice stays listed, dimmed with a trailing "Acknowledged" — one state, two
// surfaces (the Contracts row is the control; no control here).
const localNotices = computed(() =>
  mcpFindings.value
    .filter((f) => isLocalNotice(f))
    .map((f) => ({ id: f.id, line: noticeLine(f, mcpServerName(f.integration), providerNameFor(f)), acked: isAcked(f) }))
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
const contractTabRows = computed(() => [...liveFindings.value, ...mcpContractFindings.value]);
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
    ackError.value = { ...ackError.value, [f.id]: e instanceof ApiError ? e.message : 'Could not reach this collector.' };
  } finally {
    ackBusy.value = { ...ackBusy.value, [f.id]: false };
  }
}

// Drifted MCP tools: "<integration> <tool>" for every output_mismatch.
const mcpDriftedTools = computed(() => {
  const s = new Set<string>();
  for (const f of mcpFindings.value) if (f.kind === 'output_mismatch') s.add(`${f.integration} ${f.endpoint}`);
  return s;
});

// The finding the sheet is open for rides with its representative source call
// (MCP server identity + JSON-RPC id live on the call).
const sheetCall = computed(() =>
  sheetFinding.value?.source_call_id ? callsById.value[sheetFinding.value.source_call_id] || null : null
);

// Headline counts LIVE drift only — spec-version diffs are informational and
// intentionally excluded from the divergence status.
const headline = computed(() => {
  const n = liveFindings.value.length;
  const integration = health.value?.integration || 'provider';
  if (n === 0) return { you: 'No drift detected', ok: true, integration };
  const endpoints = Array.from(new Set(liveFindings.value.map((f) => f.endpoint)));
  return {
    you: `${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')}`,
    ok: false,
    integration
  };
});

async function refresh() {
  try {
    const [h, f, c, e] = await Promise.all([
      fetch('/api/health').then((r) => r.json()),
      fetch('/api/findings').then((r) => r.json()),
      fetch('/api/calls').then((r) => r.json()),
      fetch('/api/edges').then((r) => r.json())
    ]);
    health.value = h;
    findings.value = f.findings || [];
    calls.value = c.calls || [];
    edges.value = e.edges || [];
    loadError.value = '';
  } catch (e) {
    loadError.value = String(e);
  }
  // Newer endpoint — a collector predating /api/contracts must not fail the page.
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
  return connectStatus.value === 'pending' || tab.value === 'settings' || sheetFinding.value !== null;
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
  if (t !== 'threads' && threadIdFromHash(window.location.hash)) history.replaceState(null, '', window.location.pathname);
});
</script>

<template>
  <div class="page">
    <header class="topbar">
      <div class="brand">Vinifera<span>Collector</span></div>
      <div class="meta" v-if="health">
        <!-- Org identity only — never the integration slug (it scopes a spec,
             not this org; it lives on the Overview headline + its Contracts card). -->
        <span v-if="orgPillName" class="pill" title="Your organization — shown to the provider on every thread.">{{ orgPillName }}</span>
        <span v-if="!health.cp_configured" class="pill warn">control plane not configured</span>
        <button
          v-else
          type="button"
          class="pill pill-btn"
          :class="{ ok: connectStatus === 'connected', warn: connectStatus === 'pending' }"
          title="Connect settings"
          @click="tab = 'settings'"
        >
          {{ connectPill }}
        </button>
      </div>
    </header>

    <p v-if="loadError" class="error banner">Failed to load: {{ loadError }}</p>

    <nav class="tabs" role="tablist">
      <button role="tab" :class="{ active: tab === 'overview' }" @click="tab = 'overview'">
        Overview
      </button>
      <button role="tab" :class="{ active: tab === 'traffic' }" @click="tab = 'traffic'">
        Traffic
      </button>
      <button role="tab" :class="{ active: tab === 'contract' }" @click="tab = 'contract'">
        Contracts
        <!-- red = act (breaking) · amber = review (informational, un-acked) -->
        <span v-if="contractBreakingCount" class="tab-count bad" :title="breakingCountTitle(contractBreakingCount)">{{ contractBreakingCount }}</span>
        <span v-if="contractInfoCount" class="tab-count warn" :title="informationalCountTitle(contractInfoCount)">{{ contractInfoCount }}</span>
      </button>
      <button role="tab" :class="{ active: tab === 'threads' }" @click="tab = 'threads'">
        Threads
        <span v-if="threads.length" class="tab-count">{{ threads.length }}</span>
      </button>
      <button role="tab" class="tab-right" :class="{ active: tab === 'settings' }" @click="tab = 'settings'">
        Settings
        <span v-if="health?.cp_configured && connectStatus !== 'connected'" class="tab-dot" :class="connectStatus"></span>
      </button>
    </nav>

    <!-- ───────────────────────── OVERVIEW ───────────────────────── -->
    <div v-show="tab === 'overview'">
      <section class="headline" :class="{ ok: headline.ok, drift: !headline.ok }">
        <!-- Observed state only — the collector does not measure provider health. -->
        <div class="hl-you">
          You: <strong>{{ headline.you }}</strong>
        </div>
        <div class="hl-sub">on integration <code>{{ headline.integration }}</code></div>
      </section>

      <!-- MCP servers (v0.5): one headline per observed server (deck §2). -->
      <section
        v-for="m in mcpOverview"
        :key="'mcp-hl-' + m.key"
        class="headline mcp-headline"
        :class="{ ok: m.headline.ok, drift: !m.headline.ok }"
      >
        <div class="hl-you">
          <span class="mcp-badge" :title="MCP_BADGE_TOOLTIP">MCP</span>
          <strong>{{ m.headline.text }}</strong>
        </div>
      </section>

      <!-- Local notices band (deck §2): stale-client + description-only items.
           Visible to you only; NO flag control here, ever. -->
      <section v-if="localNotices.length" class="local-notices">
        <div class="ln-head">
          <span class="ln-title">{{ LOCAL_NOTICES_TITLE }}</span>
          <span class="ln-sub">{{ localNoticesSubFor(localNoticesProviders) }}</span>
        </div>
        <ul class="ln-list">
          <li v-for="n in localNotices" :key="n.id" class="ln-item" :class="{ acked: n.acked }">
            {{ n.line }}<span v-if="n.acked" class="ln-ackmark"> · Acknowledged</span>
          </li>
        </ul>
      </section>

      <section>
        <h2>
          Edges <small>discovered from traffic — external only, no targets configured</small>
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
            <p v-if="outboundEdges.length === 0" class="empty small">No outbound edges.</p>
            <div v-else class="edge-table">
              <div class="edge-head">
                <span>peer host</span><span>observed RPM</span>
              </div>
              <div v-for="e in outboundEdges" :key="'o-' + e.peer_host" class="edge-row" :class="{ drift: e.drift_count > 0 }">
                <span class="peer mono">
                  {{ e.peer_host }}
                  <span v-if="mcpHosts.has(e.peer_host)" class="mcp-badge" :title="MCP_BADGE_TOOLTIP">{{ mcpBadgeLabel(e.class) }}</span>
                </span>
                <span class="num">{{ fmtRPM(e.rpm) }}<span class="unit">/min</span></span>
              </div>
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
          <button type="button" class="btn small" @click="tab = 'settings'">{{ connectStatus === 'pending' ? 'Check status' : 'Connect' }}</button>
          <button type="button" class="btn ghost small" aria-label="Dismiss" @click="dismissConnectBanner">Dismiss</button>
        </span>
      </div>
      <section v-for="g in cardGroups" v-show="g.cards.length || g.emptyText" :key="g.key">
        <h2>
          {{ g.title }}
          <small>{{ g.sub }}</small>
        </h2>
        <p v-if="g.cards.length === 0" class="empty">{{ g.emptyText }}</p>

        <article v-for="p in g.cards" :key="p.key" class="provider" :class="{ self: g.key === 'self' }">
          <div class="prov-head">
            <span class="prov-name">{{ p.name }}</span>
            <span v-if="p.peerHost" class="prov-host mono">{{ p.peerHost }}</span>
            <span v-if="p.spec" class="fmt-badge">{{ p.spec.format }}</span>
            <span v-if="p.spec?.version" class="prov-ver">v{{ p.spec.version }}</span>
            <!-- The integration slug lives here (it scopes THIS contract), not in the header.
                 On EVERY card that has one: the two MCP servers share a name (`acme-tools-mcp`), so
                 the slug is what tells `acme-tools` from `acme-tools-stdio`. -->
            <span v-if="p.spec?.integration" class="prov-integration mono">integration: {{ p.spec.integration }}</span>
            <span class="prov-status">
              <!-- Tier-split chips — same taxonomy as the tab pills, so the sums always agree. -->
              <span v-if="cardBreakingCount(p)" class="tag drift">{{ breakingChipLabel(cardBreakingCount(p)) }}</span>
              <span v-if="cardInfoCount(p)" class="tag warn" :title="cardInfoTitle(p)">{{ informationalChipLabel(cardInfoCount(p)) }}</span>
              <span v-if="!cardBreakingCount(p) && !cardInfoCount(p) && p.spec" class="tag ok">conforming</span>
              <span v-else-if="!cardBreakingCount(p) && !cardInfoCount(p)" class="tag none">no contract loaded</span>
            </span>
          </div>

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
              <span v-if="p.spec.endpoints" class="prov-meta">{{ p.spec.endpoints }} endpoints</span>
              <span class="prov-meta">loaded {{ humanTime(p.spec.loaded_at) }}</span>
            </template>
          </div>
          <p v-else-if="mcpHosts.has(p.peerHost)" class="prov-nospec">{{ MCP_NO_SPEC_NEEDED }}</p>
          <p v-else class="prov-nospec">
            No spec loaded for this provider — point <code>viniferadrift.spec_path</code> at its
            OpenAPI document to validate live traffic against it.
          </p>

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

          <!-- Acked rows stay in place, dimmed — evidence is never hidden. -->
          <article v-for="f in p.findings" :key="f.id" class="finding nested" :class="{ acked: isAcked(f) }">
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
              <template v-if="f.kind !== 'definition_change'">
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
                   Acknowledge is ADDITIVE next to the pinned text — never a replacement. -->
              <template v-else-if="isLocalNotice(f)">
                <span class="hint-inline">{{ LOCAL_NOTE_NOT_FLAGGABLE }}</span>
                <button v-if="isAckable(f)" type="button" class="btn ghost small" :disabled="ackBusy[f.id]" :title="ACK_TITLE" @click="setAck(f, true)">{{ ACK_LABEL }}</button>
                <span v-if="ackError[f.id]" class="error small-err">{{ ackError[f.id] }}</span>
              </template>
              <!-- Flaggable definition_change: call-less — the flag POST refuses a
                   finding without a call (KNOWN v0.5 limitation), so the control
                   stays disabled with the honest reason instead of failing late.
                   NON-BREAKING rows render it as a disabled ghost (not a fake
                   primary) and gain Acknowledge. -->
              <template v-else-if="f.kind === 'definition_change'">
                <button
                  type="button"
                  class="btn"
                  :class="definitionClass(f) === 'NON-BREAKING' ? 'ghost' : 'primary flag'"
                  disabled
                  title="Flagging this needs a failing call — not available yet for definition changes."
                >Flag this</button>
                <span class="hint-inline">{{ defChangeNoCallSub(providerNameFor(f)) }}</span>
                <button v-if="isAckable(f)" type="button" class="btn ghost small" :disabled="ackBusy[f.id]" :title="ACK_TITLE" @click="setAck(f, true)">{{ ACK_LABEL }}</button>
                <span v-if="ackError[f.id]" class="error small-err">{{ ackError[f.id] }}</span>
              </template>
              <button v-else-if="f.source_call_id" type="button" class="btn primary flag" @click="openSheet(f)">Flag this</button>
              <span v-else class="hint-inline">Informational — spec-version findings have no failing call to share.</span>
            </div>
          </article>
        </article>
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
        :connected="connectStatus === 'connected'"
        :highlight-id="highlightThreadId"
        :load-error="threadsError"
        @refresh="loadThreads"
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
          <div class="seg" role="group" aria-label="Theme">
            <button type="button" :class="{ active: themePref === 'system' }" :aria-pressed="themePref === 'system'" @click="setTheme('system')">System</button>
            <button type="button" :class="{ active: themePref === 'light' }" :aria-pressed="themePref === 'light'" @click="setTheme('light')">Light</button>
            <button type="button" :class="{ active: themePref === 'dark' }" :aria-pressed="themePref === 'dark'" @click="setTheme('dark')">Dark</button>
          </div>
          <span class="theme-help">System follows your OS setting.</span>
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
      @close="sheetFinding = null"
      @created="onThreadCreated"
      @update:connect="onConnectUpdated"
    />
  </div>
</template>

<style>
/* Theme tokens. Dark is the base palette; light is applied for an explicit
   data-theme="light" AND (via prefers-color-scheme, guarded so an explicit
   dark choice wins) for the default System state on a light OS.
   --warn is the amber FILL; --warn-text is the amber TEXT/BORDER role (the
   fill fails contrast as text on light surfaces). Same split for green:
   --ok is the green FILL; --ok-text is the green TEXT/BORDER role — on light,
   #157f5f is only 4.38:1 on --panel2, so the ink is darkened to #116b50
   (5.7:1) while fills keep the palette value (ux-design §6.1: split fill vs
   ink tokens rather than nudging shared values). Light --warn-text is #8a5c00
   (not the table's #9a6700, which is 4.30:1 on --panel2 — fails 4.5:1).
   --on-* are the inks used on filled accent/danger/warn/ok surfaces. */
:root {
  color-scheme: dark;
  --bg: #0f1216;
  --panel: #171b21;
  --panel2: #1d232b;
  --ink: #e7ecf2;
  --muted: #8b97a7;
  --line: #2a323c;
  --accent: #6ea8fe;
  --danger: #ff6b6b;
  --danger-bg: #2a1618;
  --ok: #46d19e;
  --ok-text: #46d19e;
  --warn: #f4b740;
  --warn-text: #f4b740;
  --on-accent: #04122e;
  --on-danger: #200;
  --on-warn: #201800;
  --on-ok: #04231a;
}
:root[data-theme='light'] {
  color-scheme: light;
  --bg: #f6f8fa;
  --panel: #ffffff;
  --panel2: #eef1f5;
  --ink: #1a222c;
  --muted: #5b6878;
  --line: #d5dce4;
  --accent: #2f6fed;
  --danger: #c62f3d;
  --danger-bg: #fbe9ea;
  --ok: #157f5f;
  --ok-text: #116b50;
  --warn: #f4b740;
  --warn-text: #8a5c00;
  --on-accent: #ffffff;
  --on-danger: #ffffff;
  --on-warn: #201800;
  --on-ok: #ffffff;
}
/* System state on a light OS — same palette; an explicit dark choice wins. */
@media (prefers-color-scheme: light) {
  :root:not([data-theme='dark']) {
    color-scheme: light;
    --bg: #f6f8fa;
    --panel: #ffffff;
    --panel2: #eef1f5;
    --ink: #1a222c;
    --muted: #5b6878;
    --line: #d5dce4;
    --accent: #2f6fed;
    --danger: #c62f3d;
    --danger-bg: #fbe9ea;
    --ok: #157f5f;
    --ok-text: #116b50;
    --warn: #f4b740;
    --warn-text: #8a5c00;
    --on-accent: #ffffff;
    --on-danger: #ffffff;
    --on-warn: #201800;
    --on-ok: #ffffff;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 15px/1.5 system-ui, sans-serif; }
.page { max-width: 1040px; margin: 0 auto; padding: 1.5rem 1.25rem 4rem; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.brand { font-weight: 700; letter-spacing: -0.02em; font-size: 1.2rem; }
.brand span { color: var(--muted); font-weight: 500; margin-left: 0.35rem; }
.meta { display: flex; gap: 0.5rem; flex-wrap: wrap; }
.pill { background: var(--panel2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; padding: 0.15rem 0.6rem; font-size: 0.8rem; }
.pill.warn { color: var(--warn-text); border-color: var(--warn-text); }
.banner { margin: 1rem 0 0; }

/* Tabs */
.tabs { display: flex; gap: 0.25rem; margin: 1.35rem 0 0.5rem; border-bottom: 1px solid var(--line); }
.tabs button { background: transparent; border: 0; border-bottom: 2px solid transparent; color: var(--muted); font: inherit; font-weight: 600; padding: 0.55rem 0.9rem; cursor: pointer; display: inline-flex; align-items: center; gap: 0.45rem; margin-bottom: -1px; }
.tabs button:hover { color: var(--ink); }
.tabs button.active { color: var(--ink); border-bottom-color: var(--accent); }
.tab-count { background: var(--panel2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; font-size: 0.72rem; font-weight: 700; padding: 0.02rem 0.4rem; min-width: 1.2rem; text-align: center; }
.tab-count.bad { background: var(--danger); border-color: var(--danger); color: var(--on-danger); }
.tab-count.warn { background: var(--warn); border-color: var(--warn); color: var(--on-warn); }

/* Health */
.headline { margin: 1rem 0; padding: 1rem 1.15rem; border-radius: 12px; border: 1px solid var(--line); background: var(--panel); }
.headline.drift { border-color: var(--danger); background: var(--danger-bg); }
.hl-you { font-size: 1.15rem; }
.hl-sub { color: var(--muted); font-size: 0.85rem; margin-top: 0.3rem; }
.headline.drift .hl-you strong { color: var(--danger); }
.headline.ok .hl-you strong { color: var(--ok-text); }
.hint { color: var(--muted); font-size: 0.88rem; margin-top: 1rem; }

/* Shared */
h2 { font-size: 1rem; margin: 1.25rem 0 0.75rem; border-bottom: 1px solid var(--line); padding-bottom: 0.4rem; }
h2 small { color: var(--muted); font-weight: 400; margin-left: 0.5rem; }
.empty { color: var(--muted); }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

/* Contract findings */
.finding { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.finding-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.badge { text-transform: uppercase; font-size: 0.68rem; letter-spacing: 0.05em; padding: 0.15rem 0.45rem; border-radius: 5px; font-weight: 700; }
.badge.breaking { background: var(--danger); color: var(--on-danger); }
.badge.warning { background: var(--warn); color: var(--on-warn); }
.badge.info { background: var(--accent); color: var(--on-accent); }
/* DESCRIPTION: amber outline — informational, never a severity claim. */
.badge.description { background: transparent; color: var(--warn-text); border: 1px solid var(--warn-text); }
.endpoint { font-weight: 600; }
.rule { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85rem; }
.drift-row { display: flex; align-items: stretch; gap: 0.75rem; margin-top: 0.85rem; flex-wrap: wrap; }
.col { flex: 1; min-width: 140px; background: var(--panel2); border: 1px solid var(--line); border-radius: 8px; padding: 0.5rem 0.65rem; }
.col .k { font-size: 0.72rem; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
.col .v { margin-top: 0.2rem; font-family: ui-monospace, monospace; word-break: break-word; }
.v.expected { color: var(--ok-text); }
.v.actual { color: var(--danger); }
/* DESCRIPTION rows: a wording change is not a severity diff — plain ink. */
.drift-row.plain .v.expected, .drift-row.plain .v.actual { color: var(--ink); }
.arrow { align-self: center; color: var(--muted); font-size: 1.2rem; }
.detail { color: var(--muted); margin: 0.75rem 0 0; }
.corr { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-top: 0.85rem; padding-top: 0.75rem; border-top: 1px dashed var(--line); }
.corr-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); }
.corr-k { font-size: 0.82rem; color: var(--muted); }
.corr-k code { color: var(--accent); background: var(--panel2); padding: 0.1rem 0.35rem; border-radius: 4px; }
.actions { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; margin-top: 0.9rem; }
/* Buttons (shared by the Connect panel, Flag sheet and Threads tab) */
.btn { background: var(--panel2); color: var(--ink); border: 1px solid var(--line); border-radius: 8px; padding: 0.42rem 0.85rem; font: inherit; font-size: 0.88rem; font-weight: 600; cursor: pointer; }
.btn:hover { border-color: var(--muted); }
.btn.primary { background: var(--accent); color: var(--on-accent); border-color: var(--accent); }
.btn.primary:hover { filter: brightness(1.08); }
.btn.ghost { background: transparent; color: var(--muted); }
.btn.ghost:hover { color: var(--ink); }
.btn.small { padding: 0.28rem 0.65rem; font-size: 0.8rem; }
.btn.attention { color: var(--warn-text); border-color: var(--warn-text); }
.btn:disabled { opacity: 0.6; cursor: default; }
.chip { display: inline-flex; align-items: center; font-size: 0.8rem; font-weight: 600; color: var(--ok-text); border: 1px solid var(--ok-text); border-radius: 999px; padding: 0.15rem 0.6rem; }
.chip.attention { color: var(--warn-text); border-color: var(--warn-text); }
.hint-inline { color: var(--muted); font-size: 0.82rem; }
.small-err { font-size: 0.82rem; }
.pill-btn { cursor: pointer; font: inherit; font-size: 0.8rem; }
.pill.ok { color: var(--ok-text); border-color: var(--ok-text); }
.tab-right { margin-left: auto; }
.tab-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--warn); display: inline-block; }
.tab-dot.disconnected { background: var(--muted); }
.connect-banner { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; flex-wrap: wrap; margin: 0.75rem 0 0; padding: 0.6rem 0.9rem; border: 1px solid var(--warn-text); border-radius: 10px; background: var(--panel); font-size: 0.88rem; }
.connect-banner-actions { display: flex; gap: 0.5rem; }
.connect-banner.info { border-color: var(--line); color: var(--muted); }
.error { color: var(--danger); }

/* Traffic toolbar: search + facet filters + live/pause control */
.tr-toolbar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; padding: 0.6rem 0; background: var(--bg); }
.tr-search { flex: 1 1 240px; min-width: 180px; background: var(--panel); border: 1px solid var(--line); border-radius: 8px; color: var(--ink); font: inherit; font-size: 0.88rem; padding: 0.4rem 0.7rem; }
.tr-search::placeholder { color: var(--muted); }
.tr-search:focus, .tr-select:focus { outline: none; border-color: var(--accent); }
.tr-select { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; color: var(--ink); font: inherit; font-size: 0.82rem; padding: 0.38rem 0.5rem; }
.tr-chk { display: inline-flex; align-items: center; gap: 0.35rem; color: var(--muted); font-size: 0.82rem; cursor: pointer; white-space: nowrap; user-select: none; }
.tr-chk input { accent-color: var(--accent); }
.tr-clear { background: transparent; border: 1px solid var(--line); border-radius: 8px; color: var(--muted); font: inherit; font-size: 0.8rem; padding: 0.3rem 0.6rem; cursor: pointer; }
.tr-clear:hover { color: var(--ink); border-color: var(--muted); }
.tr-count { color: var(--muted); font-size: 0.8rem; font-variant-numeric: tabular-nums; white-space: nowrap; margin-left: auto; }
.live-btn { display: inline-flex; align-items: center; gap: 0.4rem; background: var(--panel); border: 1px solid var(--ok-text); border-radius: 999px; color: var(--ok-text); font: inherit; font-size: 0.8rem; font-weight: 700; padding: 0.3rem 0.75rem; cursor: pointer; white-space: nowrap; }
.live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); animation: live-pulse 1.6s ease-in-out infinite; }
.live-btn.paused { border-color: var(--warn-text); color: var(--warn-text); }
.live-btn.paused .live-dot { background: var(--warn); animation: none; }
@keyframes live-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }
.pending-bar { display: block; width: 100%; background: var(--panel2); border: 1px solid var(--accent); border-radius: 8px; color: var(--accent); font: inherit; font-size: 0.82rem; font-weight: 700; padding: 0.45rem 0.75rem; margin: 0 0 0.5rem; cursor: pointer; text-align: center; }
.pending-bar:hover { background: var(--panel); }
.tr-nomatch { display: flex; align-items: center; gap: 0.6rem; margin: 0; padding: 1rem; }

/* Traffic table */
.traffic { border: 1px solid var(--line); border-radius: 12px; overflow: hidden; background: var(--panel); }
.tr-head, .tr-row {
  display: grid;
  grid-template-columns: 1.15fr 1.9fr 1.35fr 0.55fr 1.4fr 0.9fr;
  gap: 0.65rem;
  align-items: center;
  padding: 0.55rem 0.9rem;
}
.tr-head { color: var(--muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; border-bottom: 1px solid var(--line); background: var(--panel2); }
.tr-row { border-top: 1px solid var(--line); cursor: pointer; font-size: 0.9rem; }
.tr-row:first-child { border-top: 0; }
.tr-row:hover { background: var(--panel2); }
.tr-row.open { background: var(--panel2); }
.tr-row.drift { box-shadow: inset 3px 0 0 var(--danger); }
.chev { color: var(--muted); display: inline-block; width: 1rem; }
.c-when { color: var(--muted); white-space: nowrap; }
.c-call { display: flex; align-items: center; gap: 0.5rem; min-width: 0; }
.method { font-weight: 700; font-size: 0.72rem; padding: 0.1rem 0.4rem; border-radius: 4px; background: var(--panel2); border: 1px solid var(--line); color: var(--muted); text-transform: uppercase; }
.method.post { color: var(--ok-text); border-color: var(--ok-text); }
.method.get { color: var(--accent); border-color: var(--accent); }
.method.delete { color: var(--danger); border-color: var(--danger); }
.route { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-code { font-family: ui-monospace, monospace; }
.status-code.err { color: var(--danger); }
.c-corr { display: flex; flex-direction: column; font-size: 0.78rem; color: var(--accent); min-width: 0; }
.c-corr span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-corr .dim { color: var(--muted); }
.tag { font-size: 0.72rem; font-weight: 700; padding: 0.12rem 0.5rem; border-radius: 999px; text-transform: uppercase; letter-spacing: 0.03em; }
.tag.ok { color: var(--ok-text); border: 1px solid var(--ok-text); }
.tag.drift { background: var(--danger); color: var(--on-danger); }
.tag.warn { background: var(--warn); color: var(--on-warn); }
.tag.none { color: var(--muted); border: 1px solid var(--line); }

/* Traffic counterparty cell */
.c-peer { display: flex; align-items: center; gap: 0.45rem; min-width: 0; }
.dir-chip { font-size: 0.64rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; border-radius: 4px; padding: 0.08rem 0.35rem; flex: none; }
.dir-chip.out { color: var(--accent); border: 1px solid var(--accent); }
.dir-chip.in { color: var(--ok-text); border: 1px solid var(--ok-text); }
.peer-host { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 0.82rem; }

/* Contract cards (self + provider) */
.provider { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.provider.self { border-color: var(--accent); }
.prov-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.prov-name { font-weight: 700; font-size: 1.02rem; }
.prov-host { color: var(--muted); font-size: 0.85rem; }
.fmt-badge { text-transform: uppercase; font-size: 0.66rem; letter-spacing: 0.05em; font-weight: 700; color: var(--accent); border: 1px solid var(--accent); border-radius: 5px; padding: 0.1rem 0.4rem; }
.prov-ver { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85rem; }
.prov-status { margin-left: auto; }
.prov-links { display: flex; align-items: center; gap: 0.9rem; flex-wrap: wrap; margin-top: 0.65rem; }
.doc-link { color: var(--accent); font-size: 0.85rem; text-decoration: none; border: 1px solid var(--line); border-radius: 8px; padding: 0.28rem 0.7rem; background: var(--panel2); }
.doc-link:hover { border-color: var(--accent); }
.prov-meta { color: var(--muted); font-size: 0.8rem; }
.prov-nospec { color: var(--muted); font-size: 0.88rem; margin: 0.6rem 0 0; }
.prov-integration { color: var(--muted); font-size: 0.78rem; }
.finding.nested { background: var(--panel2); margin: 0.75rem 0 0; }
/* Acked rows: dimmed in place (matches the disabled idiom); evidence stays visible. */
.finding.acked { opacity: 0.55; }

.tr-detail { border-top: 1px dashed var(--line); background: var(--bg); padding: 0.85rem 0.9rem 1.1rem; }
.meta-line { color: var(--muted); font-size: 0.82rem; display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; margin-bottom: 0.75rem; }
.meta-line .dim { opacity: 0.5; }
.redacted-tag { color: var(--warn-text); }
.reqres { display: grid; grid-template-columns: 1fr 1fr; gap: 0.85rem; }
.rr-col { min-width: 0; }
.rr-title { font-weight: 700; font-size: 0.85rem; margin-bottom: 0.4rem; }
.rr-sub { color: var(--muted); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.04em; margin: 0.55rem 0 0.25rem; }
.hdrs { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.hdrs td { padding: 0.12rem 0.4rem; border-bottom: 1px solid var(--line); vertical-align: top; }
.hk { color: var(--muted); white-space: nowrap; width: 1%; }
.hv { color: var(--ink); word-break: break-all; }
pre.body { background: var(--panel2); border: 1px solid var(--line); border-radius: 8px; padding: 0.6rem 0.75rem; margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.8rem; line-height: 1.45; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }

/* Edges overview */
.edge-groups { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.5rem; }
.edge-group { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 0.9rem 1rem 1.1rem; }
.edge-title { font-size: 0.95rem; margin: 0 0 0.75rem; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.edge-title small { color: var(--muted); font-weight: 400; }
.dir-badge { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 700; padding: 0.15rem 0.5rem; border-radius: 5px; }
.dir-badge.out { background: var(--accent); color: var(--on-accent); }
.dir-badge.in { background: var(--ok); color: var(--on-ok); }
.edge-table { border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
.edge-head, .edge-row { display: grid; grid-template-columns: 2.4fr 1fr; gap: 0.5rem; align-items: center; padding: 0.4rem 0.7rem; }
.edge-head { color: var(--muted); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em; background: var(--panel2); border-bottom: 1px solid var(--line); }
.edge-row { border-top: 1px solid var(--line); font-size: 0.85rem; }
.edge-row:first-child { border-top: 0; }
.edge-row.drift { box-shadow: inset 3px 0 0 var(--danger); }
.edge-row .peer { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.edge-row .role { color: var(--muted); }
.edge-row .num { text-align: right; font-variant-numeric: tabular-nums; }
.edge-row .unit { color: var(--muted); font-size: 0.72rem; margin-left: 0.12rem; }
.empty.small { font-size: 0.85rem; }
.occ { font-size: 0.72rem; color: var(--warn-text); font-weight: 700; border: 1px solid var(--warn-text); border-radius: 999px; padding: 0.05rem 0.45rem; }
.occ.single { color: var(--muted); border-color: var(--line); font-weight: 500; }

/* ─── MCP surfaces (v0.5) ─── */
.mcp-badge { font-size: 0.64rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; color: var(--accent); border: 1px solid var(--accent); border-radius: 4px; padding: 0.08rem 0.35rem; margin-left: 0.4rem; vertical-align: middle; white-space: nowrap; }
.mcp-headline { margin-top: -0.35rem; }
.mcp-headline .hl-you { font-size: 1rem; display: flex; align-items: center; gap: 0.55rem; }
.mcp-headline .mcp-badge { margin-left: 0; }
.local-notices { margin: 1rem 0; padding: 0.85rem 1.1rem; border-radius: 12px; border: 1px dashed var(--line); background: var(--panel); }
.ln-head { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; }
.ln-title { font-weight: 700; font-size: 0.9rem; }
.ln-sub { color: var(--muted); font-size: 0.82rem; }
.ln-list { margin: 0.55rem 0 0; padding-left: 1.1rem; }
.ln-item { color: var(--muted); font-size: 0.88rem; margin-top: 0.25rem; }
.ln-item.acked { opacity: 0.55; }
.ln-ackmark { font-weight: 600; }
.method.tool { color: var(--accent); border-color: var(--accent); }
.tool-rows { margin-top: 0.65rem; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
.tool-row { padding: 0.42rem 0.7rem; border-top: 1px solid var(--line); }
.tool-row:first-child { border-top: 0; }
.tool-line { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.tool-name { font-size: 0.85rem; }
.tool-tag { font-size: 0.72rem; color: var(--ok-text); border: 1px solid var(--ok-text); border-radius: 999px; padding: 0.05rem 0.45rem; }
.tool-tag.partial { color: var(--muted); border-color: var(--line); }
.tool-note { color: var(--muted); font-size: 0.8rem; margin: 0.3rem 0 0; }

/* Appearance (Settings) */
.theme-field { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; }
.theme-label { font-size: 0.88rem; font-weight: 600; }
.theme-help { color: var(--muted); font-size: 0.82rem; }
.seg { display: inline-flex; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
.seg button { background: var(--panel); border: 0; border-left: 1px solid var(--line); color: var(--muted); font: inherit; font-size: 0.85rem; font-weight: 600; padding: 0.35rem 0.85rem; cursor: pointer; }
.seg button:first-child { border-left: 0; }
.seg button:hover { color: var(--ink); }
.seg button.active { background: var(--accent); color: var(--on-accent); }

@media (max-width: 720px) {
  .tr-head { display: none; }
  .tr-row { grid-template-columns: 1fr 1fr; grid-auto-rows: min-content; }
  .reqres { grid-template-columns: 1fr; }
  .edge-groups { grid-template-columns: 1fr; }
}
</style>
