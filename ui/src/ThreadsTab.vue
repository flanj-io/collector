<script setup lang="ts">
// Threads tab (v0.1a): the compact state-only list of the threads this
// collector created — provider · endpoint · evidence · status (derived turn) ·
// opened ×N · last reply · link — with Open (owner handoff in a new tab),
// Close / Reopen and Replace link. The conversation itself is read and answered
// on the control plane; this list polls `GET /api/threads` over the relay.
import { nextTick, ref, watch } from 'vue';
import { ApiError, apiPost, openThreadInNewTab } from './api';
import { copyText } from './clipboard';
import { linkLabel, linkNeedsAttention, shortDate, timeAgo, turnLabel, type ThreadRow } from './threads';

const props = defineProps<{
  rows: ThreadRow[];
  loaded: boolean;
  connected: boolean;
  highlightId: string | null;
  loadError: string;
}>();
const emit = defineEmits<{ (e: 'refresh'): void }>();

const busy = ref<Record<string, string>>({}); // thread id -> action in flight
const errors = ref<Record<string, string>>({});
const replacing = ref<string | null>(null); // thread id awaiting confirm
const replaced = ref<Record<string, string>>({}); // thread id -> new link (shown once)
const copied = ref<Record<string, boolean>>({});
const blockedOwnerUrl = ref<Record<string, string>>({}); // popup blocked → offer a plain link once
const linkInputs = ref<Record<string, HTMLInputElement | null>>({});

function setBusy(id: string, action: string) {
  busy.value = { ...busy.value, [id]: action };
}
function clearBusy(id: string) {
  const b = { ...busy.value };
  delete b[id];
  busy.value = b;
}
function setError(id: string, msg: string) {
  errors.value = { ...errors.value, [id]: msg };
}

async function act(row: ThreadRow, action: 'close' | 'reopen') {
  setBusy(row.thread_id, action);
  setError(row.thread_id, '');
  try {
    await apiPost(`/api/threads/${encodeURIComponent(row.thread_id)}/${action}`);
    emit('refresh');
  } catch (e) {
    setError(row.thread_id, e instanceof ApiError ? e.message : "Couldn't reach the control plane.");
  } finally {
    clearBusy(row.thread_id);
  }
}

async function open(row: ThreadRow) {
  setBusy(row.thread_id, 'open');
  setError(row.thread_id, '');
  blockedOwnerUrl.value = { ...blockedOwnerUrl.value, [row.thread_id]: '' };
  try {
    const out = await openThreadInNewTab(row.thread_id);
    if (!out.opened) blockedOwnerUrl.value = { ...blockedOwnerUrl.value, [row.thread_id]: out.url };
  } catch (e) {
    setError(row.thread_id, e instanceof ApiError ? e.message : "Couldn't reach the control plane.");
  } finally {
    clearBusy(row.thread_id);
  }
}

async function replaceLink(row: ThreadRow) {
  replacing.value = null;
  setBusy(row.thread_id, 'replace');
  setError(row.thread_id, '');
  try {
    const out = await apiPost<{ thread_url: string; revoked: number }>(`/api/threads/${encodeURIComponent(row.thread_id)}/replace-link`);
    replaced.value = { ...replaced.value, [row.thread_id]: out.thread_url };
    emit('refresh');
    await nextTick();
    linkInputs.value[row.thread_id]?.select();
  } catch (e) {
    setError(row.thread_id, e instanceof ApiError ? e.message : "Couldn't reach the control plane.");
  } finally {
    clearBusy(row.thread_id);
  }
}

async function copyLink(row: ThreadRow) {
  const url = replaced.value[row.thread_id] || row.thread_url;
  const outcome = await copyText(url, linkInputs.value[row.thread_id]);
  if (outcome === 'copied') {
    copied.value = { ...copied.value, [row.thread_id]: true };
    window.setTimeout(() => (copied.value = { ...copied.value, [row.thread_id]: false }), 2000);
  } else {
    setError(row.thread_id, 'Auto-copy is blocked on this address — select the link and press Ctrl/Cmd+C.');
  }
}

function setLinkInput(id: string, el: unknown) {
  linkInputs.value[id] = (el as HTMLInputElement | null) ?? null;
}

function lastReply(row: ThreadRow): string {
  return timeAgo(row.summary?.last_reply_at ?? null);
}

// Scroll the deep-linked row into view once it is rendered.
watch(
  () => [props.highlightId, props.rows.length],
  () => {
    if (!props.highlightId) return;
    nextTick(() => document.getElementById('thread-' + props.highlightId)?.scrollIntoView({ block: 'center' }));
  },
  { immediate: true }
);
</script>

<template>
  <section class="threads">
    <h2>
      Threads <small>state only — read and reply on the thread itself; close, reopen and replace the link from here</small>
    </h2>
    <p v-if="loadError" class="error">{{ loadError }}</p>

    <p v-if="loaded && rows.length === 0" class="empty">
      {{ connected ? 'No threads yet. Flag a finding on Contracts to start one.' : 'No threads yet. Connect in Settings, then flag a finding on Contracts.' }}
    </p>

    <div v-else-if="rows.length" class="th-table">
      <div class="th-head">
        <span>Provider</span><span>Endpoint</span><span>Evidence</span><span>Status</span><span>Opened</span><span>Last reply</span><span>Link</span>
      </div>
      <div v-for="row in rows" :id="'thread-' + row.thread_id" :key="row.thread_id" class="th-row" :class="{ highlight: row.thread_id === highlightId, closed: row.summary?.state === 'closed' }">
        <div class="th-main">
          <span class="th-provider">{{ row.provider || row.summary?.provider_display_name || '—' }}</span>
          <span class="th-endpoint mono">{{ row.endpoint || row.summary?.endpoint || '—' }}</span>
          <span class="th-evidence">{{ row.summary?.evidence_count ?? 1 }}</span>
          <span class="th-status" :class="{ attention: row.summary?.turn === 'fix_reported' || row.summary?.turn === 'replied_while_closed' }">
            {{ turnLabel(row.summary, row.provider) }}
            <span v-if="row.error && !row.summary" class="dim" title="the control plane did not answer for this thread">· state unavailable</span>
          </span>
          <span class="th-opened">×{{ row.summary?.opened_count ?? 0 }}</span>
          <span class="th-last">{{ lastReply(row) }}</span>
          <span class="th-link" :class="{ attention: linkNeedsAttention(row.summary) }">{{ linkLabel(row.summary, shortDate) }}</span>
        </div>
        <div class="th-actions">
          <button type="button" class="btn primary small" :disabled="!!busy[row.thread_id]" @click="open(row)">
            {{ busy[row.thread_id] === 'open' ? 'Opening…' : 'Open' }}
          </button>
          <button v-if="row.summary?.state === 'closed'" type="button" class="btn small" :disabled="!!busy[row.thread_id]" @click="act(row, 'reopen')">
            {{ busy[row.thread_id] === 'reopen' ? 'Reopening…' : 'Reopen' }}
          </button>
          <button v-else type="button" class="btn small" :disabled="!!busy[row.thread_id]" @click="act(row, 'close')">
            {{ busy[row.thread_id] === 'close' ? 'Closing…' : 'Close thread' }}
          </button>
          <button type="button" class="btn small" @click="copyLink(row)">{{ copied[row.thread_id] ? 'Copied' : 'Copy thread link' }}</button>
          <button type="button" class="btn ghost small" :class="{ attention: linkNeedsAttention(row.summary) }" :disabled="!!busy[row.thread_id]" @click="replacing = row.thread_id">
            {{ busy[row.thread_id] === 'replace' ? 'Replacing…' : 'Replace link' }}
          </button>
          <span class="dim small-note">created {{ timeAgo(row.created_at) }}</span>
        </div>

        <div v-if="replacing === row.thread_id" class="th-confirm">
          <p>Replace the thread link? Every copy shared so far stops working. People who already replied keep their access, and so do you.</p>
          <div class="th-actions">
            <button type="button" class="btn primary small" @click="replaceLink(row)">Replace link</button>
            <button type="button" class="btn ghost small" @click="replacing = null">Cancel</button>
          </div>
        </div>
        <div v-if="replaced[row.thread_id]" class="th-replaced">
          <p>New link ready — copy and re-share it. Previous links stopped working.</p>
          <input :ref="(el) => setLinkInput(row.thread_id, el)" class="link-input mono" type="text" readonly :value="replaced[row.thread_id]" aria-label="New thread link" />
        </div>
        <input v-else :ref="(el) => setLinkInput(row.thread_id, el)" class="link-input mono sr" type="text" readonly :value="row.thread_url" tabindex="-1" aria-hidden="true" />
        <p v-if="errors[row.thread_id]" class="error">{{ errors[row.thread_id] }}</p>
        <p v-if="blockedOwnerUrl[row.thread_id]" class="th-note">
          Your browser blocked the new tab — <a :href="blockedOwnerUrl[row.thread_id]" target="_blank" rel="noopener">open the thread here</a> (this link works once, for 10 minutes).
        </p>
      </div>
    </div>
  </section>
</template>

<style scoped>
.th-table { border: 1px solid var(--line); border-radius: 12px; overflow: hidden; background: var(--panel); }
.th-head, .th-main { display: grid; grid-template-columns: 1.1fr 1.6fr 0.6fr 1.5fr 0.6fr 0.8fr 1.3fr; gap: 0.6rem; align-items: center; padding: 0.55rem 0.9rem; }
.th-head { color: var(--muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; border-bottom: 1px solid var(--line); background: var(--panel2); }
.th-row { border-top: 1px solid var(--line); padding-bottom: 0.6rem; }
.th-row:first-of-type { border-top: 0; }
.th-row.highlight { box-shadow: inset 3px 0 0 var(--accent); background: var(--panel2); }
.th-row.closed .th-main { color: var(--muted); }
.th-main { font-size: 0.9rem; }
.th-provider { font-weight: 600; }
.th-endpoint { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.th-status.attention { color: var(--warn); font-weight: 600; }
.th-link { font-size: 0.82rem; color: var(--muted); }
.th-link.attention { color: var(--warn); }
.th-actions { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; padding: 0 0.9rem; }
.th-confirm, .th-replaced { margin: 0.6rem 0.9rem 0; background: var(--panel2); border: 1px solid var(--warn); border-radius: 8px; padding: 0.6rem 0.75rem; font-size: 0.88rem; display: flex; flex-direction: column; gap: 0.5rem; }
.th-replaced { border-color: var(--ok); }
.th-confirm p, .th-replaced p { margin: 0; }
.link-input { width: 100%; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; color: var(--ink); font-size: 0.85rem; padding: 0.4rem 0.6rem; }
.link-input.sr { position: absolute; left: -9999px; width: 1px; height: 1px; opacity: 0; }
.small-note { font-size: 0.78rem; margin-left: auto; }
.dim { color: var(--muted); }
.error { color: var(--danger); margin: 0.4rem 0.9rem 0; font-size: 0.85rem; }
.th-note { color: var(--warn); margin: 0.4rem 0.9rem 0; font-size: 0.85rem; }
.th-note a { color: var(--accent); }
.empty { color: var(--muted); }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
@media (max-width: 800px) {
  .th-head { display: none; }
  .th-main { grid-template-columns: 1fr 1fr; }
}
</style>
