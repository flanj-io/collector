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
  WORKSPACE_LINK_OUT,
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
  /** The CP workspace address, or '' when the collector offers none (not
   *  Connected, or no address a browser off-host could open). Empty means the
   *  link-out is not rendered — never a dead link, the same rule the Connected
   *  pill follows. */
  dashboardUrl: string;
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
    <!-- v1 phase 3: the one line out to the person's own workspace. This list is
         the threads THIS collector created; the workspace is every thread they
         are part of, which for anyone who has also answered someone else's
         thread is a strictly larger set. Rendered only when there is somewhere
         to go — absence keeps this tab exactly as it was. -->
    <p v-if="dashboardUrl" class="th-workspace">
      <a :href="dashboardUrl" target="_blank" rel="noopener noreferrer">{{ WORKSPACE_LINK_OUT }}</a>
    </p>
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
/* Blueprint: a framed table with mono uppercase heads on the sunk surface,
   hairline row separators, two-line rows (facts, then the link strip beside the
   one action). Layout and component rules only — every colour is a token. */
.th-readonly { color: var(--ink-soft); font-size: 13px; margin: -4px 0 4px; }
/* The workspace link-out: an offer, not a prompt — same muted register as the
   read-only note above it, and it names no colour of its own. */
.th-workspace { font-size: 13px; margin: 0 0 12px; }
.th-workspace a { color: var(--ink-soft); }
.th-workspace a:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-offset); }
.th-table { border: var(--border-w) solid var(--rule); border-radius: var(--radius); background: var(--surface); }
.th-head, .th-main { display: grid; grid-template-columns: 1.05fr 1.55fr 0.5fr 1.45fr 0.95fr 0.85fr 0.45fr; gap: 10px; align-items: center; padding: 10px 14px; }
.th-head { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-soft); border-bottom: var(--border-w) solid var(--rule); background: var(--surface-sunk); }
.th-row { border-top: var(--border-w-hair) solid var(--rule-soft); padding-bottom: 10px; transition: opacity var(--dur) var(--ease), background-color var(--dur-fast) var(--ease); }
.th-head + .th-row { border-top: 0; }
.th-row.highlight { box-shadow: inset var(--border-w-stripe) 0 0 var(--accent); background: var(--surface-sunk); }
/* A closed thread dims in place, like an acknowledged finding. */
.th-row.closed { opacity: 0.6; }
.th-main { font-size: 13.5px; padding-bottom: 4px; }
.th-provider { font-weight: 600; }
.th-endpoint { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.th-evidence { font-family: var(--f-mono); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.th-status { font-family: var(--f-mono); font-size: 11.5px; letter-spacing: 0.02em; }
/* `attention` is a turn state (fix reported, replied while closed), not a finding
   severity: the accent carries it, and the status text is the label. */
.th-status.attention { color: var(--accent-ink); font-weight: 600; }
.th-activity, .th-last { color: var(--ink-soft); font-family: var(--f-mono); font-size: 12.5px; white-space: nowrap; }
.th-opens { font-family: var(--f-mono); font-size: 12.5px; font-variant-numeric: tabular-nums; }
/* Line 2: link facts beside the one action, full row width. */
.th-linkline { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; padding: 0 14px; }
.th-linkfacts { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; min-width: 0; }
.th-link { font-size: 12.5px; color: var(--ink-soft); white-space: nowrap; }
.th-link.attention { color: var(--accent-ink); }
.th-knock { font-size: 12.5px; color: var(--ink-soft); }
.th-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-left: auto; }
.th-truncated { color: var(--ink-soft); font-size: 13px; margin: 8px 0 0; }
/* The unreachable-relay line: the load error, red ink behind a red rule. */
.error { color: var(--sev-breaking-ink); margin: 6px 14px 0; font-size: 13px; padding-left: 10px; border-left: var(--border-w-stripe) solid var(--sev-breaking); }
.th-table + .error, .th-workspace + .error, .th-readonly + .error { margin-left: 0; }
/* The blocked-tab note is prose with a link in it: body ink behind an accent
   rule, so the underlined link is the only accent-coloured text in the line. */
.th-note { color: var(--ink); margin: 6px 14px 0; padding-left: 10px; border-left: var(--border-w-stripe) solid var(--accent); font-size: 13px; }
.th-note a { color: var(--accent-ink); text-decoration: underline; }
.empty { color: var(--ink-soft); display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.mono { font-family: var(--f-mono); }
@media (max-width: 800px) {
  .th-head { display: none; }
  .th-main { grid-template-columns: 1fr 1fr; }
  .th-actions { margin-left: 0; }
}
</style>
