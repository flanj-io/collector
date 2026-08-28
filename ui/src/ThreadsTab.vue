<script setup lang="ts">
// Threads tab (v0.1a; list source moved to the control plane in slice-inbox):
// the compact state-only list of the threads this collector created. The list
// itself now comes from the CP (`GET /api/threads` relays CONTRACTS-CP §5.5a,
// most-recently-active first) with the collector's own local fields joined on;
// every operation the tab has always had stays exactly where it was.
// Two deliberate lines per row — line 1 the thread facts
// (provider · endpoint · evidence · status · last activity · last reply · opens),
// line 2 the link strip (link state + knock note) beside the link actions:
// View thread (owner handoff in a new tab), Copy thread link, Replace link…,
// Close thread / Reopen thread. The conversation itself is read and answered
// on the control plane; this list polls `GET /api/threads` over the relay.
import { nextTick, ref, watch } from 'vue';
import { ApiError, apiPost, openThreadInNewTab } from './api';
import { copyText } from './clipboard';
import {
  NO_LINK_COPY_NOTE,
  NO_LINK_COPY_TITLE,
  canCopyLink,
  knockNote,
  linkLabel,
  linkNeedsAttention,
  shortDate,
  timeAgo,
  truncationNote,
  turnLabel,
  type ThreadRow
} from './threads';

const props = defineProps<{
  rows: ThreadRow[];
  loaded: boolean;
  highlightId: string | null;
  loadError: string;
  /** The relay's own answer when it CANNOT list (not connected / no control
   *  plane configured). Shown in place of the empty state, because "no threads"
   *  would be a claim this collector is in no position to make. */
  notice: string;
  /** §5.5a has no cursor: total is the control plane's count, hasMore says the
   *  list on screen is short of it. */
  total: number;
  hasMore: boolean;
}>();
const emit = defineEmits<{ (e: 'refresh'): void; (e: 'connect'): void }>();

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
  // Both operands are live: `replaced` is the link this tab just minted, which
  // covers the moment between Replace link and the refreshed list landing (the
  // collector persists it for every row now, but the fetch is still in flight);
  // `row.thread_url` is the normal path.
  const url = replaced.value[row.thread_id] || row.thread_url;
  if (!url) return; // no copy of the link here — the control is disabled

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

    <!-- "No threads yet" is a claim only a collector that could see the list may
         make. Not connected / no control plane configured carry the relay's own
         line instead, and a failed load says nothing at all. -->
    <!-- The relay's notice is the same 412 the Flag sheet answers with an inline
         Connect. Answer it the same way here: the fix is one control away, not a
         scavenger hunt through the tabs. -->
    <p v-if="!loadError && loaded && rows.length === 0" class="empty">
      <span>{{ notice || 'No threads yet. Flag a finding on Contracts to start one.' }}</span>
      <button v-if="notice" type="button" class="btn small" @click="emit('connect')">Connect</button>
    </p>

    <div v-else-if="rows.length" class="th-table">
      <!-- Line 1: thread facts. Opens = times the thread link was opened
           (a count, not a time). The list is ordered by `updated_at`, so the
           column that explains a row's position is LAST ACTIVITY, not Created —
           a reply used to float an old thread to the top with nothing on the row
           accounting for the move. Last reply stays: `—` still means nobody ever
           answered, and when the two differ you can see the move was a close, a
           reopen or a link replace. -->
      <div class="th-head">
        <span>Provider</span><span>Endpoint</span><span>Evidence</span><span>Status</span><span title="What this list is ordered by — a reply, a close, a reopen or a link replace">Last activity</span><span>Last reply</span><span title="Times the thread link was opened">Opens</span>
      </div>
      <div v-for="row in rows" :id="'thread-' + row.thread_id" :key="row.thread_id" class="th-row" :class="{ highlight: row.thread_id === highlightId, closed: row.summary?.state === 'closed' }">
        <div class="th-main">
          <span class="th-provider">{{ row.provider || row.summary?.provider_display_name || '—' }}</span>
          <span class="th-endpoint mono">{{ row.endpoint || row.summary?.endpoint || '—' }}</span>
          <span class="th-evidence">{{ row.summary?.evidence_count ?? 1 }}</span>
          <span class="th-status" :class="{ attention: row.summary?.turn === 'fix_reported' || row.summary?.turn === 'replied_while_closed' }">
            {{ turnLabel(row.summary, row.provider) }}
          </span>
          <span class="th-activity">{{ timeAgo(row.updated_at || row.created_at) }}</span>
          <span class="th-last">{{ lastReply(row) }}</span>
          <span class="th-opens">×{{ row.summary?.opened_count ?? 0 }}</span>
        </div>
        <!-- Line 2: the link strip — link facts beside the link actions.
             Amber only when review is needed NOW (expired / replaced /
             expiring soon); knocks alone stay muted (lifetime counter). -->
        <div class="th-linkline">
          <span class="th-linkfacts">
            <span class="th-link" :class="{ attention: linkNeedsAttention(row.summary) }">Thread link: {{ linkLabel(row.summary, shortDate) }}</span>
            <!-- Active links only (threads.ts knockNote): on Expired/Replaced rows the
                 count is already in the label and "re-share with Copy thread link"
                 would copy a dead link. -->
            <span v-if="row.summary?.link?.status === 'active' && (row.summary?.knock_count || 0) > 0" class="th-knock">{{ knockNote(row.summary?.knock_count || 0) }}</span>
            <!-- A disabled button is out of the tab order, so the `title` on Copy
                 thread link reaches nobody who cannot hover. The fact belongs on
                 the row, beside the link label. -->
            <span v-if="!canCopyLink(row)" class="th-nolink">{{ NO_LINK_COPY_NOTE }}</span>
          </span>
          <span class="th-actions">
            <button type="button" class="btn primary small" :disabled="!!busy[row.thread_id]" @click="open(row)">
              {{ busy[row.thread_id] === 'open' ? 'Opening…' : 'View thread' }}
            </button>
            <!-- Disabled, never hidden, when this collector holds no copy of the
                 link (the token lives only in a URL fragment and never comes
                 back from the control plane): Replace link… is the way back. -->
            <button
              type="button"
              class="btn small"
              :disabled="!canCopyLink(row)"
              :title="canCopyLink(row) ? 'Copies the same active link — share it again anywhere. Nothing changes.' : NO_LINK_COPY_TITLE"
              @click="copyLink(row)"
            >
              {{ copied[row.thread_id] ? 'Copied' : 'Copy thread link' }}
            </button>
            <button type="button" class="btn ghost small" :disabled="!!busy[row.thread_id]" title="Makes a new link. Every copy shared so far stops working." @click="replacing = row.thread_id">
              {{ busy[row.thread_id] === 'replace' ? 'Replacing…' : 'Replace link…' }}
            </button>
            <button v-if="row.summary?.state === 'closed'" type="button" class="btn small" :disabled="!!busy[row.thread_id]" @click="act(row, 'reopen')">
              {{ busy[row.thread_id] === 'reopen' ? 'Reopening…' : 'Reopen thread' }}
            </button>
            <button v-else type="button" class="btn small" :disabled="!!busy[row.thread_id]" @click="act(row, 'close')">
              {{ busy[row.thread_id] === 'close' ? 'Closing…' : 'Close thread' }}
            </button>
          </span>
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

    <!-- §5.5a has no cursor, so the list stops at the control plane's cap. Say
         what is on screen and what is not, rather than dropping rows silently. -->
    <p v-if="hasMore && rows.length" class="th-truncated">{{ truncationNote(rows.length, total) }}</p>
  </section>
</template>

<style scoped>
.th-table { border: 1px solid var(--line); border-radius: 12px; overflow: hidden; background: var(--panel); }
.th-head, .th-main { display: grid; grid-template-columns: 1.05fr 1.55fr 0.5fr 1.45fr 0.95fr 0.85fr 0.45fr; gap: 0.6rem; align-items: center; padding: 0.55rem 0.9rem; }
.th-head { color: var(--muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; border-bottom: 1px solid var(--line); background: var(--panel2); }
.th-row { border-top: 1px solid var(--line); padding-bottom: 0.6rem; }
.th-row:first-of-type { border-top: 0; }
.th-row.highlight { box-shadow: inset 3px 0 0 var(--accent); background: var(--panel2); }
.th-row.closed .th-main { color: var(--muted); }
.th-main { font-size: 0.9rem; padding-bottom: 0.25rem; }
.th-provider { font-weight: 600; }
.th-endpoint { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.th-status.attention { color: var(--warn-text); font-weight: 600; }
.th-activity, .th-last { color: var(--muted); font-size: 0.85rem; white-space: nowrap; }
.th-opens { font-variant-numeric: tabular-nums; }
/* Line 2: link facts beside link actions, full row width. */
.th-linkline { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; padding: 0 0.9rem; }
.th-linkfacts { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; min-width: 0; }
.th-link { font-size: 0.82rem; color: var(--muted); white-space: nowrap; }
.th-link.attention { color: var(--warn-text); }
.th-knock { font-size: 0.82rem; color: var(--muted); }
.th-nolink { font-size: 0.82rem; color: var(--muted); }
.th-actions { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; margin-left: auto; }
.th-confirm, .th-replaced { margin: 0.6rem 0.9rem 0; background: var(--panel2); border: 1px solid var(--warn-text); border-radius: 8px; padding: 0.6rem 0.75rem; font-size: 0.88rem; display: flex; flex-direction: column; gap: 0.5rem; }
.th-replaced { border-color: var(--ok-text); }
.th-confirm p, .th-replaced p { margin: 0; }
.th-confirm .th-actions { margin-left: 0; padding: 0; }
.link-input { width: 100%; background: var(--bg); border: 1px solid var(--line); border-radius: 8px; color: var(--ink); font-size: 0.85rem; padding: 0.4rem 0.6rem; }
.link-input.sr { position: absolute; left: -9999px; width: 1px; height: 1px; opacity: 0; }
.th-truncated { color: var(--muted); font-size: 0.85rem; margin: 0.5rem 0 0; }
.error { color: var(--danger); margin: 0.4rem 0.9rem 0; font-size: 0.85rem; }
.th-note { color: var(--warn-text); margin: 0.4rem 0.9rem 0; font-size: 0.85rem; }
.th-note a { color: var(--accent); }
.empty { color: var(--muted); display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
@media (max-width: 800px) {
  .th-head { display: none; }
  .th-main { grid-template-columns: 1fr 1fr; }
  .th-actions { margin-left: 0; }
}
</style>
