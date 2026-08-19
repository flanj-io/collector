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

type Tab = 'health' | 'contract' | 'traffic';

const health = ref<Health | null>(null);
const findings = ref<Finding[]>([]);
const calls = ref<RedactedCall[]>([]);
const loadError = ref('');
const flagState = ref<Record<string, { busy?: boolean; peekUrl?: string; error?: string }>>({});
const tab = ref<Tab>('health');
const expanded = ref<Record<string, boolean>>({});

const callsById = computed(() => {
  const m: Record<string, RedactedCall> = {};
  for (const c of calls.value) m[c.id] = c;
  return m;
});

// Set of call ids that some finding points at — a call is "drifted" when a
// finding's source_call_id references it.
const driftedCallIds = computed(() => {
  const s = new Set<string>();
  for (const f of findings.value) if (f.source_call_id) s.add(f.source_call_id);
  return s;
});

const liveFindings = computed(() => findings.value.filter((f) => f.kind === 'live-vs-spec'));
const versionFindings = computed(() => findings.value.filter((f) => f.kind === 'version-diff'));

const headline = computed(() => {
  const n = findings.value.length;
  const integration = health.value?.integration || 'provider';
  if (n === 0) return { you: 'No drift detected', ok: true, integration };
  const endpoints = Array.from(new Set(findings.value.map((f) => f.endpoint)));
  return {
    you: `${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')}`,
    ok: false,
    integration
  };
});

async function refresh() {
  try {
    const [h, f, c] = await Promise.all([
      fetch('/api/health').then((r) => r.json()),
      fetch('/api/findings').then((r) => r.json()),
      fetch('/api/calls').then((r) => r.json())
    ]);
    health.value = h;
    findings.value = f.findings || [];
    calls.value = c.calls || [];
    loadError.value = '';
  } catch (e) {
    loadError.value = String(e);
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
    flagState.value = { ...flagState.value, [f.id]: { peekUrl: body.peek_url } };
    await refresh();
  } catch (e) {
    flagState.value = { ...flagState.value, [f.id]: { error: String(e) } };
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
        <span class="pill">window {{ health.window_rows }} calls</span>
        <span class="pill" :class="{ warn: !health.cp_configured }">
          {{ health.cp_configured ? 'control plane linked' : 'control plane not configured' }}
        </span>
      </div>
    </header>

    <p v-if="loadError" class="error banner">Failed to load: {{ loadError }}</p>

    <nav class="tabs" role="tablist">
      <button role="tab" :class="{ active: tab === 'health' }" @click="tab = 'health'">
        Health
        <span v-if="findings.length" class="tab-count" :class="{ bad: true }">{{ findings.length }}</span>
      </button>
      <button role="tab" :class="{ active: tab === 'contract' }" @click="tab = 'contract'">
        Contract
        <span v-if="liveFindings.length" class="tab-count bad">{{ liveFindings.length }}</span>
      </button>
      <button role="tab" :class="{ active: tab === 'traffic' }" @click="tab = 'traffic'">
        Traffic
        <span v-if="calls.length" class="tab-count">{{ calls.length }}</span>
      </button>
    </nav>

    <!-- ───────────────────────── HEALTH ───────────────────────── -->
    <div v-show="tab === 'health'">
      <section class="headline" :class="{ ok: headline.ok, drift: !headline.ok }">
        <div class="hl-provider">Provider: <strong>operational</strong></div>
        <div class="hl-you">
          You: <strong>{{ headline.you }}</strong>
        </div>
        <div class="hl-sub">on integration <code>{{ headline.integration }}</code></div>
      </section>

      <div class="stat-grid" v-if="health">
        <div class="stat">
          <div class="stat-n">{{ health.calls }}</div>
          <div class="stat-l">calls captured</div>
        </div>
        <div class="stat" :class="{ bad: health.findings > 0 }">
          <div class="stat-n">{{ health.findings }}</div>
          <div class="stat-l">drift findings</div>
        </div>
        <div class="stat">
          <div class="stat-n">{{ health.window_rows }}</div>
          <div class="stat-l">window rows</div>
        </div>
        <div class="stat" :class="{ warn: !health.cp_configured }">
          <div class="stat-n">{{ health.cp_configured ? 'linked' : 'off' }}</div>
          <div class="stat-l">control plane</div>
        </div>
      </div>
      <p class="hint">
        Collector <code>{{ health?.collector_version }}</code> · redaction-at-source ·
        outbound-only. Open <strong>Contract</strong> to review drift, <strong>Traffic</strong>
        to browse your captured calls.
      </p>
    </div>

    <!-- ───────────────────────── CONTRACT ───────────────────────── -->
    <div v-show="tab === 'contract'">
      <section>
        <h2>Contract drift <small>live traffic vs published spec</small></h2>
        <p v-if="liveFindings.length === 0" class="empty">No live-vs-spec drift.</p>
        <article v-for="f in liveFindings" :key="f.id" class="finding">
          <div class="finding-head">
            <span class="badge" :class="f.severity">{{ f.severity }}</span>
            <span class="endpoint">{{ f.endpoint }}</span>
            <span class="rule">{{ f.rule }}</span>
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
              Flagged. Peek link:
              <a :href="flagState[f.id]!.peekUrl" target="_blank" rel="noopener">{{ flagState[f.id]!.peekUrl }}</a>
            </span>
            <span v-if="flagState[f.id]?.error" class="error">{{ flagState[f.id]!.error }}</span>
          </div>
        </article>
      </section>

      <section>
        <h2>Spec version diff <small>v1 → v2 breaking changes</small></h2>
        <p v-if="versionFindings.length === 0" class="empty">No version-diff findings (no v2 spec configured).</p>
        <article v-for="f in versionFindings" :key="f.id" class="finding subtle">
          <div class="finding-head">
            <span class="badge" :class="f.severity">{{ f.severity }}</span>
            <span class="endpoint">{{ f.endpoint }}</span>
            <span class="rule">{{ f.rule }}</span>
            <span class="ver">{{ f.spec_version_from }} → {{ f.spec_version_to }}</span>
          </div>
          <p class="detail">{{ f.detail }}</p>
        </article>
      </section>
    </div>

    <!-- ───────────────────────── TRAFFIC ───────────────────────── -->
    <div v-show="tab === 'traffic'">
      <section>
        <h2>
          Traffic <small>recent captured calls — redacted at source, read-only</small>
        </h2>
        <p v-if="calls.length === 0" class="empty">No calls captured yet.</p>

        <div v-else class="traffic">
          <div class="tr-head">
            <span class="c-when">captured</span>
            <span class="c-call">call</span>
            <span class="c-status">status</span>
            <span class="c-corr">correlation</span>
            <span class="c-mark">contract</span>
          </div>

          <template v-for="c in calls" :key="c.id">
            <div
              class="tr-row"
              :class="{ drift: driftedCallIds.has(c.id), open: expanded[c.id] }"
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
                <span v-if="driftedCallIds.has(c.id)" class="tag drift">drifted</span>
                <span v-else class="tag ok">conforming</span>
              </span>
            </div>

            <div v-if="expanded[c.id]" class="tr-detail" :key="c.id + '-d'">
              <div class="meta-line">
                <span v-if="c.duration_ms != null">{{ c.duration_ms }} ms</span>
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
.stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 0.75rem; margin: 1rem 0; }
.stat { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 0.85rem 1rem; }
.stat.bad { border-color: var(--danger); }
.stat.warn { border-color: var(--warn); }
.stat-n { font-size: 1.5rem; font-weight: 700; letter-spacing: -0.02em; }
.stat.bad .stat-n { color: var(--danger); }
.stat.warn .stat-n { color: var(--warn); }
.stat-l { color: var(--muted); font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.15rem; }
.hint { color: var(--muted); font-size: 0.88rem; margin-top: 1rem; }

/* Shared */
h2 { font-size: 1rem; margin: 1.25rem 0 0.75rem; border-bottom: 1px solid var(--line); padding-bottom: 0.4rem; }
h2 small { color: var(--muted); font-weight: 400; margin-left: 0.5rem; }
.empty { color: var(--muted); }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

/* Contract findings */
.finding { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.finding.subtle { background: var(--panel2); }
.finding-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.badge { text-transform: uppercase; font-size: 0.68rem; letter-spacing: 0.05em; padding: 0.15rem 0.45rem; border-radius: 5px; font-weight: 700; }
.badge.breaking { background: var(--danger); color: #200; }
.badge.warning { background: var(--warn); color: #201800; }
.badge.info { background: var(--accent); color: #04122e; }
.endpoint { font-weight: 600; }
.rule, .ver { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85rem; }
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
.error { color: var(--danger); }

/* Traffic table */
.traffic { border: 1px solid var(--line); border-radius: 12px; overflow: hidden; background: var(--panel); }
.tr-head, .tr-row {
  display: grid;
  grid-template-columns: 1.3fr 2.4fr 0.7fr 2fr 1fr;
  gap: 0.75rem;
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

@media (max-width: 720px) {
  .tr-head { display: none; }
  .tr-row { grid-template-columns: 1fr 1fr; grid-auto-rows: min-content; }
  .reqres { grid-template-columns: 1fr; }
}
</style>
