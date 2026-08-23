<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';

interface Correlation {
  request_id?: string | null;
  idempotency_key?: string | null;
  trace_id?: string | null;
  span_id?: string | null;
}
interface RedactedCall {
  id: string;
  captured_at: string;
  integration: string;
  method: string;
  route: string;
  url?: string;
  status_code: number;
  request_headers?: Record<string, string>;
  request_body: string;
  request_content_type?: string;
  response_headers?: Record<string, string>;
  response_body: string;
  response_content_type?: string;
  correlation: Correlation;
  duration_ms?: number;
  redaction: { applied: boolean; patterns: string[]; spec_aware: boolean };
  direction?: 'client' | 'server';
  peer_host?: string;
  peer_addr?: string;
  edge_class?: string;
}
interface Finding {
  id: string;
  kind: 'live-vs-spec' | 'version-diff';
  severity: string;
  integration: string;
  endpoint: string;
  field_path?: string | null;
  location?: string | null;
  expected: string;
  actual: string;
  rule: string;
  spec_version_from?: string | null;
  spec_version_to?: string | null;
  source_call_id?: string | null;
  detail?: string;
  signature?: string;
  occurrence_count?: number;
  first_seen?: string;
  last_seen?: string;
}
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
interface Health {
  status: string;
  integration: string;
  window_rows: number;
  calls: number;
  findings: number;
  cp_configured: boolean;
  collector_version: string;
}

type Tab = 'overview' | 'traffic' | 'contract';

const health = ref<Health | null>(null);
const findings = ref<Finding[]>([]);
const calls = ref<RedactedCall[]>([]);
const edges = ref<Edge[]>([]);
const contracts = ref<SpecInfo[]>([]);
// True once /api/contracts has answered OK — older collectors lack the endpoint,
// and only a real answer lets us assert "no contract loaded" per provider.
const contractsKnown = ref(false);
const loadError = ref('');
interface FlagShareState {
  busy?: boolean;
  peekUrl?: string;
  threadId?: string;
  error?: string;
  // Copy-link / channel share (the same per-thread link, channel-tagged via the §5 request body).
  channel?: string;
  cardDetail?: boolean;
  linkBusy?: boolean;
  linkUrl?: string;
  copied?: boolean;
  revokedNote?: string;
}
const flagState = ref<Record<string, FlagShareState>>({});

// Copy-time channel picker: where the consumer is about to paste the link. Email stays the
// fallback (the Flag action itself); Slack/Telegram apps are a v4 integration seam — for now the
// human carries the link into the channel the two teams already share.
const SHARE_CHANNELS = [
  { value: 'link', label: 'Just copy' },
  { value: 'slack', label: 'Slack' },
  { value: 'whatsapp', label: 'WhatsApp' },
  { value: 'telegram', label: 'Telegram' },
  { value: 'teams', label: 'Teams' },
  { value: 'other', label: 'Other' }
];

function patchFlag(id: string, patch: Partial<FlagShareState>) {
  flagState.value = { ...flagState.value, [id]: { ...flagState.value[id], ...patch } };
}
const tab = ref<Tab>('overview');
const expanded = ref<Record<string, boolean>>({});

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

const methodOptions = computed(() =>
  Array.from(new Set(calls.value.map((c) => c.method.toUpperCase()))).sort()
);

const peerOptions = computed(() =>
  Array.from(new Set(calls.value.map((c) => c.peer_host).filter(Boolean) as string[])).sort()
);

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
    if (fMethod.value && c.method.toUpperCase() !== fMethod.value) return false;
    if (fStatus.value) {
      if (fStatus.value === 'err') {
        if (c.status_code < 400) return false;
      } else if (`${Math.floor(c.status_code / 100)}xx` !== fStatus.value) {
        return false;
      }
    }
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
        c.correlation?.trace_id
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
  for (const f of liveFindings.value) {
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

async function flag(f: Finding) {
  const email = window.prompt('Provider engineer email to invite to this thread:');
  if (!email) return;
  flagState.value = { ...flagState.value, [f.id]: { busy: true } };
  try {
    const resp = await fetch('/api/flag', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ finding_id: f.id, invitee_email: email })
    });
    const body = await resp.json();
    if (!resp.ok) {
      flagState.value = { ...flagState.value, [f.id]: { error: body.error || `HTTP ${resp.status}` } };
      return;
    }
    flagState.value = {
      ...flagState.value,
      [f.id]: { peekUrl: body.peek_url, threadId: body.thread_id, channel: 'link', cardDetail: false }
    };
    await refresh();
  } catch (e) {
    flagState.value = { ...flagState.value, [f.id]: { error: String(e) } };
  }
}

// Copy link (and regenerate): mint a fresh channel-tagged token on the SAME per-thread link via
// the collector relay, then put the URL on the clipboard. `revoke` kills every outstanding link
// first — revocation is immediate, including live peek sessions.
async function shareLink(f: Finding, revoke: boolean) {
  const st = flagState.value[f.id];
  if (!st?.threadId) return;
  patchFlag(f.id, { linkBusy: true, error: undefined, revokedNote: undefined });
  try {
    const resp = await fetch('/api/peek-link', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        thread_id: st.threadId,
        channel: st.channel || 'link',
        revoke_existing: revoke,
        card_endpoint_detail: st.cardDetail ?? false
      })
    });
    const body = await resp.json();
    if (!resp.ok) {
      patchFlag(f.id, { error: body.error || `HTTP ${resp.status}` });
      return;
    }
    patchFlag(f.id, {
      linkUrl: body.peek_url,
      revokedNote: revoke && body.revoked > 0 ? `${body.revoked} previous link${body.revoked === 1 ? '' : 's'} revoked` : undefined
    });
    try {
      await navigator.clipboard.writeText(body.peek_url);
      patchFlag(f.id, { copied: true });
      setTimeout(() => patchFlag(f.id, { copied: false }), 2000);
    } catch {
      // Clipboard may be unavailable; the minted link is rendered for manual copy.
    }
  } catch (e) {
    patchFlag(f.id, { error: String(e) });
  } finally {
    patchFlag(f.id, { linkBusy: false });
  }
}

let timer: number | undefined;
onMounted(() => {
  refresh();
  timer = window.setInterval(refresh, 5000);
});
onUnmounted(() => timer && window.clearInterval(timer));
</script>

<template>
  <div class="page">
    <header class="topbar">
      <div class="brand">Vinifera<span>Collector</span></div>
      <div class="meta" v-if="health">
        <span class="pill">{{ health.integration }}</span>
        <span class="pill" :class="{ warn: !health.cp_configured }">
          {{ health.cp_configured ? 'control plane linked' : 'control plane not configured' }}
        </span>
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
        <span v-if="liveFindings.length" class="tab-count bad">{{ liveFindings.length }}</span>
      </button>
    </nav>

    <!-- ───────────────────────── OVERVIEW ───────────────────────── -->
    <div v-show="tab === 'overview'">
      <section class="headline" :class="{ ok: headline.ok, drift: !headline.ok }">
        <div class="hl-provider">Provider: <strong>operational</strong></div>
        <div class="hl-you">
          You: <strong>{{ headline.you }}</strong>
        </div>
        <div class="hl-sub">on integration <code>{{ headline.integration }}</code></div>
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
                <span class="peer mono">{{ e.peer_host }}</span>
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
                <span class="peer mono">{{ e.peer_host }}</span>
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
            <span class="prov-status">
              <span v-if="p.findings.length" class="tag drift">
                {{ p.findings.length }} drift finding{{ p.findings.length === 1 ? '' : 's' }}
              </span>
              <span v-else-if="p.spec" class="tag ok">conforming</span>
              <span v-else class="tag none">no contract loaded</span>
            </span>
          </div>

          <div v-if="p.spec" class="prov-links">
            <a class="doc-link" :href="specHref(p.spec)" target="_blank" rel="noopener">View OpenAPI spec</a>
            <a v-if="p.spec.docs_url" class="doc-link" :href="p.spec.docs_url" target="_blank" rel="noopener">
              API docs ↗
            </a>
            <span v-if="p.spec.endpoints" class="prov-meta">{{ p.spec.endpoints }} endpoints</span>
            <span class="prov-meta">loaded {{ humanTime(p.spec.loaded_at) }}</span>
          </div>
          <p v-else class="prov-nospec">
            No spec loaded for this provider — point <code>viniferadrift.spec_path</code> at its
            OpenAPI document to validate live traffic against it.
          </p>

          <article v-for="f in p.findings" :key="f.id" class="finding nested">
            <div class="finding-head">
              <span class="badge" :class="f.severity">{{ f.severity }}</span>
              <span class="endpoint">{{ f.endpoint }}</span>
              <span class="rule">{{ f.rule }}</span>
              <span v-if="f.occurrence_count && f.occurrence_count > 1" class="occ" title="calls carrying this same drift">
                ×{{ f.occurrence_count }} calls
              </span>
              <span v-else class="occ single">1 call</span>
            </div>
            <div class="drift-row">
              <div class="col">
                <div class="k">expected (per spec)</div>
                <div class="v expected">{{ f.expected }}</div>
              </div>
              <div class="arrow">≠</div>
              <div class="col">
                <div class="k">actual (live)</div>
                <div class="v actual">{{ f.actual }}</div>
              </div>
              <div class="col loc">
                <div class="k">location</div>
                <div class="v mono">{{ f.location }}</div>
              </div>
            </div>
            <p class="detail" v-if="f.detail">{{ f.detail }}</p>

            <div class="corr" v-if="correlationFor(f)">
              <span class="corr-title">correlation keys</span>
              <span v-if="correlationFor(f)!.request_id" class="corr-k">
                request-id <code>{{ correlationFor(f)!.request_id }}</code>
              </span>
              <span v-if="correlationFor(f)!.idempotency_key" class="corr-k">
                idempotency-key <code>{{ correlationFor(f)!.idempotency_key }}</code>
              </span>
              <span v-if="correlationFor(f)!.trace_id" class="corr-k">
                trace-id <code>{{ correlationFor(f)!.trace_id }}</code>
              </span>
            </div>

            <div class="actions">
              <button class="flag" :disabled="flagState[f.id]?.busy" @click="flag(f)">
                {{ flagState[f.id]?.busy ? 'Flagging…' : 'Flag this' }}
              </button>
              <span v-if="flagState[f.id]?.peekUrl" class="flagged">
                Flagged — invite emailed. Peek link:
                <a :href="flagState[f.id]!.peekUrl" target="_blank" rel="noopener">{{ flagState[f.id]!.peekUrl }}</a>
              </span>
              <span v-if="flagState[f.id]?.error" class="error">{{ flagState[f.id]!.error }}</span>
            </div>

            <!-- Copy-link into the teams' existing channel (email is the fallback above). The
                 channel tag rides the request body, never the URL. -->
            <div v-if="flagState[f.id]?.threadId" class="share">
              <div class="share-row">
                <span class="share-label">Share into your existing channel:</span>
                <select
                  class="share-channel"
                  :value="flagState[f.id]!.channel || 'link'"
                  @change="patchFlag(f.id, { channel: ($event.target as HTMLSelectElement).value })"
                >
                  <option v-for="c in SHARE_CHANNELS" :key="c.value" :value="c.value">{{ c.label }}</option>
                </select>
                <button class="share-copy" :disabled="flagState[f.id]?.linkBusy" @click="shareLink(f, false)">
                  {{ flagState[f.id]?.copied ? 'Copied' : flagState[f.id]?.linkBusy ? 'Minting…' : 'Copy link' }}
                </button>
                <button class="share-revoke" :disabled="flagState[f.id]?.linkBusy" @click="shareLink(f, true)">
                  Revoke &amp; regenerate
                </button>
              </div>
              <label class="share-detail">
                <input
                  type="checkbox"
                  :checked="flagState[f.id]!.cardDetail ?? false"
                  @change="patchFlag(f.id, { cardDetail: ($event.target as HTMLInputElement).checked })"
                />
                Include endpoint + finding type in the link's preview card (off = low-information card)
              </label>
              <p class="share-warning">
                Anyone with this link can view this thread's redacted evidence and reply. Share it only
                where you'd paste the logs.
              </p>
              <p v-if="flagState[f.id]?.linkUrl" class="share-minted mono">{{ flagState[f.id]!.linkUrl }}</p>
              <p v-if="flagState[f.id]?.revokedNote" class="share-revoked">{{ flagState[f.id]!.revokedNote }}</p>
            </div>
          </article>
        </article>
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
              <option v-for="p in peerOptions" :key="p" :value="p">{{ p }}</option>
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
                <span class="method" :class="c.method.toLowerCase()">{{ c.method }}</span>
                <span class="route mono">{{ c.route || c.url }}</span>
              </span>
              <span class="c-peer" :title="c.peer_addr ? 'peer address ' + c.peer_addr : undefined">
                <span class="dir-chip" :class="c.direction === 'server' ? 'in' : 'out'">{{ dirLabel(c.direction) }}</span>
                <span class="peer-host mono">{{ c.peer_host || c.peer_addr || '—' }}</span>
              </span>
              <span class="c-status">
                <span class="status-code" :class="{ err: c.status_code >= 400 }">{{ c.status_code }}</span>
              </span>
              <span class="c-corr mono">
                <span v-if="c.correlation?.request_id" title="request-id">{{ c.correlation.request_id }}</span>
                <span v-if="c.correlation?.idempotency_key" class="dim" title="idempotency-key">
                  {{ c.correlation.idempotency_key }}
                </span>
                <span v-if="!c.correlation?.request_id && !c.correlation?.idempotency_key" class="dim">—</span>
              </span>
              <span class="c-mark">
                <span v-if="isDrifted(c)" class="tag drift">drifted</span>
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
  </div>
</template>

<style>
:root {
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
  --warn: #f4b740;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 15px/1.5 system-ui, sans-serif; }
.page { max-width: 1040px; margin: 0 auto; padding: 1.5rem 1.25rem 4rem; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.brand { font-weight: 700; letter-spacing: -0.02em; font-size: 1.2rem; }
.brand span { color: var(--muted); font-weight: 500; margin-left: 0.35rem; }
.meta { display: flex; gap: 0.5rem; flex-wrap: wrap; }
.pill { background: var(--panel2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; padding: 0.15rem 0.6rem; font-size: 0.8rem; }
.pill.warn { color: var(--warn); border-color: var(--warn); }
.banner { margin: 1rem 0 0; }

/* Tabs */
.tabs { display: flex; gap: 0.25rem; margin: 1.35rem 0 0.5rem; border-bottom: 1px solid var(--line); }
.tabs button { background: transparent; border: 0; border-bottom: 2px solid transparent; color: var(--muted); font: inherit; font-weight: 600; padding: 0.55rem 0.9rem; cursor: pointer; display: inline-flex; align-items: center; gap: 0.45rem; margin-bottom: -1px; }
.tabs button:hover { color: var(--ink); }
.tabs button.active { color: var(--ink); border-bottom-color: var(--accent); }
.tab-count { background: var(--panel2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; font-size: 0.72rem; font-weight: 700; padding: 0.02rem 0.4rem; min-width: 1.2rem; text-align: center; }
.tab-count.bad { background: var(--danger); border-color: var(--danger); color: #200; }

/* Health */
.headline { margin: 1rem 0; padding: 1rem 1.15rem; border-radius: 12px; border: 1px solid var(--line); background: var(--panel); }
.headline.drift { border-color: var(--danger); background: var(--danger-bg); }
.hl-provider { color: var(--muted); }
.hl-you { font-size: 1.15rem; margin-top: 0.25rem; }
.hl-sub { color: var(--muted); font-size: 0.85rem; margin-top: 0.3rem; }
.headline.drift .hl-you strong { color: var(--danger); }
.headline.ok .hl-you strong { color: var(--ok); }
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
.badge.breaking { background: var(--danger); color: #200; }
.badge.warning { background: var(--warn); color: #201800; }
.badge.info { background: var(--accent); color: #04122e; }
.endpoint { font-weight: 600; }
.rule { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85rem; }
.drift-row { display: flex; align-items: stretch; gap: 0.75rem; margin-top: 0.85rem; flex-wrap: wrap; }
.col { flex: 1; min-width: 140px; background: var(--panel2); border: 1px solid var(--line); border-radius: 8px; padding: 0.5rem 0.65rem; }
.col .k { font-size: 0.72rem; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
.col .v { margin-top: 0.2rem; font-family: ui-monospace, monospace; word-break: break-word; }
.v.expected { color: var(--ok); }
.v.actual { color: var(--danger); }
.arrow { align-self: center; color: var(--muted); font-size: 1.2rem; }
.detail { color: var(--muted); margin: 0.75rem 0 0; }
.corr { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-top: 0.85rem; padding-top: 0.75rem; border-top: 1px dashed var(--line); }
.corr-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted); }
.corr-k { font-size: 0.82rem; color: var(--muted); }
.corr-k code { color: var(--accent); background: var(--panel2); padding: 0.1rem 0.35rem; border-radius: 4px; }
.actions { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; margin-top: 0.9rem; }
button.flag { background: var(--accent); color: #04122e; border: 0; border-radius: 8px; padding: 0.45rem 0.9rem; font-weight: 700; cursor: pointer; }
button.flag:disabled { opacity: 0.6; cursor: default; }
.flagged { color: var(--ok); font-size: 0.85rem; }
.flagged a { color: var(--accent); }
.share { margin-top: 0.6rem; padding: 0.6rem 0.75rem; border: 1px dashed var(--line, #2a3550); border-radius: 8px; display: flex; flex-direction: column; gap: 0.4rem; }
.share-row { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
.share-label { font-size: 0.82rem; opacity: 0.85; }
.share-channel { background: transparent; color: inherit; border: 1px solid var(--line, #2a3550); border-radius: 6px; padding: 0.25rem 0.4rem; font: inherit; font-size: 0.82rem; }
.share-copy { background: var(--accent); color: #04122e; border: 0; border-radius: 6px; padding: 0.3rem 0.7rem; font-weight: 700; cursor: pointer; font-size: 0.82rem; }
.share-copy:disabled { opacity: 0.6; cursor: default; }
.share-revoke { background: transparent; color: var(--warn, #e0a34a); border: 1px solid currentColor; border-radius: 6px; padding: 0.3rem 0.7rem; cursor: pointer; font-size: 0.82rem; }
.share-revoke:disabled { opacity: 0.6; cursor: default; }
.share-detail { font-size: 0.78rem; opacity: 0.8; display: flex; align-items: center; gap: 0.4rem; }
.share-warning { margin: 0; font-size: 0.78rem; color: var(--warn, #e0a34a); }
.share-minted { margin: 0; font-size: 0.78rem; word-break: break-all; opacity: 0.9; }
.share-revoked { margin: 0; font-size: 0.78rem; color: var(--ok); }
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
.live-btn { display: inline-flex; align-items: center; gap: 0.4rem; background: var(--panel); border: 1px solid var(--ok); border-radius: 999px; color: var(--ok); font: inherit; font-size: 0.8rem; font-weight: 700; padding: 0.3rem 0.75rem; cursor: pointer; white-space: nowrap; }
.live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); animation: live-pulse 1.6s ease-in-out infinite; }
.live-btn.paused { border-color: var(--warn); color: var(--warn); }
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
.method.post { color: var(--ok); border-color: var(--ok); }
.method.get { color: var(--accent); border-color: var(--accent); }
.method.delete { color: var(--danger); border-color: var(--danger); }
.route { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-code { font-family: ui-monospace, monospace; }
.status-code.err { color: var(--danger); }
.c-corr { display: flex; flex-direction: column; font-size: 0.78rem; color: var(--accent); min-width: 0; }
.c-corr span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-corr .dim { color: var(--muted); }
.tag { font-size: 0.72rem; font-weight: 700; padding: 0.12rem 0.5rem; border-radius: 999px; text-transform: uppercase; letter-spacing: 0.03em; }
.tag.ok { color: var(--ok); border: 1px solid var(--ok); }
.tag.drift { background: var(--danger); color: #200; }
.tag.none { color: var(--muted); border: 1px solid var(--line); }

/* Traffic counterparty cell */
.c-peer { display: flex; align-items: center; gap: 0.45rem; min-width: 0; }
.dir-chip { font-size: 0.64rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; border-radius: 4px; padding: 0.08rem 0.35rem; flex: none; }
.dir-chip.out { color: var(--accent); border: 1px solid var(--accent); }
.dir-chip.in { color: var(--ok); border: 1px solid var(--ok); }
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
.finding.nested { background: var(--panel2); margin: 0.75rem 0 0; }

.tr-detail { border-top: 1px dashed var(--line); background: var(--bg); padding: 0.85rem 0.9rem 1.1rem; }
.meta-line { color: var(--muted); font-size: 0.82rem; display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; margin-bottom: 0.75rem; }
.meta-line .dim { opacity: 0.5; }
.redacted-tag { color: var(--warn); }
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
.dir-badge.out { background: var(--accent); color: #04122e; }
.dir-badge.in { background: var(--ok); color: #04231a; }
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
.occ { font-size: 0.72rem; color: var(--warn); font-weight: 700; border: 1px solid var(--warn); border-radius: 999px; padding: 0.05rem 0.45rem; }
.occ.single { color: var(--muted); border-color: var(--line); font-weight: 500; }

@media (max-width: 720px) {
  .tr-head { display: none; }
  .tr-row { grid-template-columns: 1fr 1fr; grid-auto-rows: min-content; }
  .reqres { grid-template-columns: 1fr; }
  .edge-groups { grid-template-columns: 1fr; }
}
</style>
