<script setup lang="ts">
// Threads tab (READ-ONLY since slice 2): the compact state-only list of the
// threads this collector created. The list comes from the CP (`GET
// /api/threads` relays CONTRACTS-CP §5.5a, most-recently-active first) with the
// collector's own local fields joined on. Close, reopen and link changes moved
// to the thread page — the one muted line under the header says so — and View
// thread (the owner handoff in a new tab) is the ONLY row action. The local
// relay routes for close/reopen/replace still exist (they are the
// key-authorized arm of the contract); only this UI stopped driving them.
// Two deliberate lines per row — line 1 the thread facts
// (provider · endpoint · evidence · status · last activity · last reply · opens),
// line 2 the link strip (link state + knock note), informational only.
import { nextTick, ref, watch } from 'vue';
import { ApiError, openThreadInNewTab } from './api';
import {
  THREADS_READ_ONLY_NOTE,
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
// 'connect' is the only event left: the read-only tab mutates nothing, so the
// old 'refresh' (emitted after close/reopen/replace) retired with the buttons.
const emit = defineEmits<{ (e: 'connect'): void }>();

const opening = ref<string | null>(null); // thread id whose handoff is in flight
const errors = ref<Record<string, string>>({});
const blockedOwnerUrl = ref<Record<string, string>>({}); // popup blocked → offer a plain link once

async function open(row: ThreadRow) {
  opening.value = row.thread_id;
  errors.value = { ...errors.value, [row.thread_id]: '' };
  blockedOwnerUrl.value = { ...blockedOwnerUrl.value, [row.thread_id]: '' };
  try {
    const out = await openThreadInNewTab(row.thread_id);
    if (!out.opened) blockedOwnerUrl.value = { ...blockedOwnerUrl.value, [row.thread_id]: out.url };
  } catch (e) {
    errors.value = { ...errors.value, [row.thread_id]: e instanceof ApiError ? e.message : "Couldn't reach the control plane." };
  } finally {
    opening.value = null;
  }
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
      Threads <small>state only — read and reply on the thread itself</small>
    </h2>
    <!-- The tab went read-only (slice 2): every thread operation lives on the
         thread page now. Always visible, so nobody hunts for the buttons that
         used to be here. -->
    <p class="th-readonly">{{ THREADS_READ_ONLY_NOTE }}</p>
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
        <!-- Line 2: the link strip — link facts, informational only, beside the
             one remaining action. Amber only when review is needed NOW (expired
             / replaced / expiring soon); knocks alone stay muted (lifetime
             counter). -->
        <div class="th-linkline">
          <span class="th-linkfacts">
            <span class="th-link" :class="{ attention: linkNeedsAttention(row.summary) }">Thread link: {{ linkLabel(row.summary, shortDate) }}</span>
            <!-- Active links only (threads.ts knockNote): on Expired/Replaced rows the
                 count is already in the label. -->
            <span v-if="row.summary?.link?.status === 'active' && (row.summary?.knock_count || 0) > 0" class="th-knock">{{ knockNote(row.summary?.knock_count || 0) }}</span>
          </span>
          <span class="th-actions">
            <button type="button" class="btn primary small" :disabled="opening === row.thread_id" @click="open(row)">
              {{ opening === row.thread_id ? 'Opening…' : 'View thread' }}
            </button>
          </span>
        </div>

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
.th-readonly { color: var(--muted); font-size: 0.85rem; margin: -0.25rem 0 0.75rem; }
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
/* Line 2: link facts beside the one action, full row width. */
.th-linkline { display: flex; align-items: center; gap: 0.75rem; flex-wrap: wrap; padding: 0 0.9rem; }
.th-linkfacts { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; min-width: 0; }
.th-link { font-size: 0.82rem; color: var(--muted); white-space: nowrap; }
.th-link.attention { color: var(--warn-text); }
.th-knock { font-size: 0.82rem; color: var(--muted); }
.th-actions { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; margin-left: auto; }
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
