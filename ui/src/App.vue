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
  status_code: number;
  request_body: string;
  response_body: string;
  correlation: Correlation;
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

const health = ref<Health | null>(null);
const findings = ref<Finding[]>([]);
const calls = ref<RedactedCall[]>([]);
const loadError = ref('');
const flagState = ref<Record<string, { busy?: boolean; peekUrl?: string; error?: string }>>({});

const callsById = computed(() => {
  const m: Record<string, RedactedCall> = {};
  for (const c of calls.value) m[c.id] = c;
  return m;
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

    <p v-if="loadError" class="error">Failed to load: {{ loadError }}</p>

    <!-- Health divergence headline -->
    <section class="headline" :class="{ ok: headline.ok, drift: !headline.ok }">
      <div class="hl-provider">Provider: <strong>operational</strong></div>
      <div class="hl-you">
        You: <strong>{{ headline.you }}</strong>
      </div>
    </section>

    <!-- Contract drift: live-traffic-vs-spec -->
    <section>
      <h2>Contract drift <small>live traffic vs published spec</small></h2>
      <p v-if="liveFindings.length === 0" class="empty">No live-vs-spec drift.</p>
      <article v-for="f in liveFindings" :key="f.id" class="finding">
        <div class="finding-head">
          <span class="badge breaking">{{ f.severity }}</span>
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

    <!-- Version diff: spec v1 -> v2 breaking changes -->
    <section>
      <h2>Spec version diff <small>v1 → v2 breaking changes</small></h2>
      <p v-if="versionFindings.length === 0" class="empty">No version-diff findings (no v2 spec configured).</p>
      <article v-for="f in versionFindings" :key="f.id" class="finding subtle">
        <div class="finding-head">
          <span class="badge breaking">{{ f.severity }}</span>
          <span class="endpoint">{{ f.endpoint }}</span>
          <span class="rule">{{ f.rule }}</span>
          <span class="ver">{{ f.spec_version_from }} → {{ f.spec_version_to }}</span>
        </div>
        <p class="detail">{{ f.detail }}</p>
      </article>
    </section>
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
.page { max-width: 980px; margin: 0 auto; padding: 1.5rem 1.25rem 4rem; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.brand { font-weight: 700; letter-spacing: -0.02em; font-size: 1.2rem; }
.brand span { color: var(--muted); font-weight: 500; margin-left: 0.35rem; }
.meta { display: flex; gap: 0.5rem; flex-wrap: wrap; }
.pill { background: var(--panel2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; padding: 0.15rem 0.6rem; font-size: 0.8rem; }
.pill.warn { color: var(--warn); border-color: var(--warn); }
.headline { margin: 1.25rem 0; padding: 1rem 1.15rem; border-radius: 12px; border: 1px solid var(--line); background: var(--panel); }
.headline.drift { border-color: var(--danger); background: var(--danger-bg); }
.hl-provider { color: var(--muted); }
.hl-you { font-size: 1.15rem; margin-top: 0.25rem; }
.headline.drift .hl-you strong { color: var(--danger); }
.headline.ok .hl-you strong { color: var(--ok); }
h2 { font-size: 1rem; margin: 1.75rem 0 0.75rem; border-bottom: 1px solid var(--line); padding-bottom: 0.4rem; }
h2 small { color: var(--muted); font-weight: 400; margin-left: 0.5rem; }
.empty { color: var(--muted); }
.finding { background: var(--panel); border: 1px solid var(--line); border-radius: 12px; padding: 1rem 1.1rem; margin-bottom: 0.9rem; }
.finding.subtle { background: var(--panel2); }
.finding-head { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.badge { text-transform: uppercase; font-size: 0.68rem; letter-spacing: 0.05em; padding: 0.15rem 0.45rem; border-radius: 5px; font-weight: 700; }
.badge.breaking { background: var(--danger); color: #200; }
.endpoint { font-weight: 600; }
.rule, .ver { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85rem; }
.drift-row { display: flex; align-items: stretch; gap: 0.75rem; margin-top: 0.85rem; flex-wrap: wrap; }
.col { flex: 1; min-width: 140px; background: var(--panel2); border: 1px solid var(--line); border-radius: 8px; padding: 0.5rem 0.65rem; }
.col .k { font-size: 0.72rem; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
.col .v { margin-top: 0.2rem; font-family: ui-monospace, monospace; }
.v.expected { color: var(--ok); }
.v.actual { color: var(--danger); }
.arrow { align-self: center; color: var(--muted); font-size: 1.2rem; }
.mono { font-family: ui-monospace, monospace; }
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
</style>
