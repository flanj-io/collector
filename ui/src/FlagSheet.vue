<script setup lang="ts">
// The Flag sheet (v0.1a): one finding → one thread on the control plane → a
// Thread link the consumer copies into the channel the two teams already share.
// Three phases: the Connect prompt (when this collector is not Connected with a
// confirmed contact — the sheet polls and unlocks the moment the click lands),
// compose (evidence line, "what leaves this collector", the optional message,
// Create thread) and the success state (the link, Copy thread link, Copy link +
// message, View thread). Nothing is emailed by Flanj on flag.
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { ApiError, apiPost, openThreadInNewTab } from './api';
import { copyText, selectInput } from './clipboard';
import ConnectPanel from './ConnectPanel.vue';
import {
  canCreateThread,
  correlationCount,
  defaultFlagMessage,
  evidenceLine,
  pasteText,
  requestIdsLine,
  shortDate,
  type ConnectState
} from './threads';
import {
  FLAG_DESCRIPTION_GUARD,
  isDescriptionChange,
  isMcpFinding,
  mcpDefaultMessage,
  mcpDisclosureLead,
  mcpDisclosureTail,
  mcpEvidenceLine,
  mcpIdsLineFor
} from './mcp';
import type { Correlation, Finding, FlagResult, RedactedCall } from './types';

const props = defineProps<{
  finding: Finding;
  correlation: Correlation | null;
  /** The finding's representative source call (MCP server identity rides on it). */
  call?: RedactedCall | null;
  provider: string;
  consumer: string;
  connect: ConnectState | null;
  defaultOrg?: string;
}>();
const emit = defineEmits<{
  (e: 'close'): void;
  (e: 'created', r: FlagResult): void;
  (e: 'update:connect', s: ConnectState): void;
}>();

const DISCLOSURE_KEY = 'flanj.flag.disclosure.seen';

// v0.5: MCP findings carry the deck's MCP evidence / IDs / disclosure / prefill
// copy; HTTP findings keep the v0.1a strings unchanged.
const isMcp = computed(() => isMcpFinding(props.finding));
const mcpServer = computed(() => props.call?.mcp_server_name || props.provider);
const mcpTool = computed(() => props.finding.endpoint);

const message = ref(
  isMcpFinding(props.finding)
    ? mcpDefaultMessage(props.finding, shortDate)
    : defaultFlagMessage(props.finding, props.correlation?.request_id, shortDate)
);
const disclosureOpen = ref(localStorage.getItem(DISCLOSURE_KEY) !== '1');
const busy = ref(false);
const errorMsg = ref('');
const result = ref<FlagResult | null>(null);
const headline = ref('Thread created.');
const copyHint = ref('');
const copiedWhat = ref<'' | 'link' | 'message'>('');
const openBusy = ref(false);
const openError = ref('');
const blockedOwnerUrl = ref(''); // shown as a plain link only when the popup was blocked
const linkInput = ref<HTMLInputElement | null>(null);
const sheet = ref<HTMLElement | null>(null);
let copiedTimer: number | undefined;

// Create thread stays available while a NEW contact is pending as long as a
// confirmed one exists (the relay applies the same rule); the disclosure names
// the contact threads are actually created with.
const connected = computed(() => canCreateThread(props.connect));
const contactEmail = computed(() => props.connect?.confirmed_contact_email || props.connect?.contact_email || '');
const evidence = computed(() =>
  isMcp.value ? mcpEvidenceLine(props.finding, mcpServer.value, shortDate) : evidenceLine(props.finding)
);
// The mute-risk guard renders on the DESCRIPTION class only (ux-design-v2
// §2.7.4): this class is the one where the finding is a question, not a defect,
// and saying so is what keeps a subjective flag from reading as an accusation.
const descriptionGuard = computed(() => (isMcp.value && isDescriptionChange(props.finding) ? FLAG_DESCRIPTION_GUARD : ''));
// The IDs line: an MCP flag uses the deck's JSON-RPC line ONLY while the
// client-generated id is the sole correlation key — mixed keys fall back to
// the standard count line with an honest note for the client-generated one
// (deck §5). HTTP keeps the existing line.
// A definition_change is CALL-LESS: there is no call, so "No request IDs were
// captured on this call." would be answering a question nobody asked about a
// thing that does not exist. The disclosure carries what leaves instead.
const idsLine = computed(() => {
  if (props.finding.kind === 'definition_change') return '';
  return isMcp.value
    ? mcpIdsLineFor(props.correlation, props.provider)
    : requestIdsLine(correlationCount(props.correlation));
});
const disclosureLead = computed(() =>
  isMcp.value
    ? mcpDisclosureLead(props.finding, mcpTool.value)
    : 'This redacted request/response, the finding, the correlation keys, the endpoint, your message, and'
);
const disclosureTail = computed(() => (isMcp.value ? mcpDisclosureTail(props.finding) : 'Raw calls never leave.'));
const since = computed(() => shortDate(props.finding.first_seen || props.finding.last_seen || ''));
const paste = computed(() =>
  result.value
    ? pasteText({ endpoint: props.finding.endpoint, since: since.value, requestId: props.correlation?.request_id, link: result.value.thread_url })
    : ''
);

function toggleDisclosure() {
  disclosureOpen.value = !disclosureOpen.value;
  // Remembered once the user has seen it (collapsed after first use).
  localStorage.setItem(DISCLOSURE_KEY, '1');
}

async function createThread() {
  busy.value = true;
  errorMsg.value = '';
  try {
    const r = await apiPost<FlagResult>('/api/flag', {
      finding_id: props.finding.id,
      message: message.value,
      provider_display_name: props.provider
    });
    result.value = r;
    localStorage.setItem(DISCLOSURE_KEY, '1');
    emit('created', r);
    await nextTick();
    selectInput(linkInput.value);
    // Auto-copy: the headline flips only once the clipboard promise resolves.
    const outcome = await copyText(r.thread_url, linkInput.value);
    if (outcome === 'copied') headline.value = 'Thread created — link copied.';
    else copyHint.value = 'Auto-copy is blocked on this address — select the link and press Ctrl/Cmd+C.';
  } catch (e) {
    if (e instanceof ApiError && e.status === 412) {
      // The relay says Connect/confirmation is missing — fold it into the prompt.
      emit('update:connect', {
        ...(props.connect || { status: 'disconnected' }),
        status: e.code === 'contact_unconfirmed' ? 'pending' : 'disconnected',
        confirmed_contact_email: null
      } as ConnectState);
      errorMsg.value = '';
    } else if (e instanceof ApiError) {
      errorMsg.value = e.message;
    } else {
      errorMsg.value = "Couldn't reach the control plane — nothing was created or shared.";
    }
  } finally {
    busy.value = false;
  }
}

async function copyLink() {
  if (!result.value) return;
  const outcome = await copyText(result.value.thread_url, linkInput.value);
  flashCopied('link', outcome === 'copied');
}

async function copyLinkAndMessage() {
  if (!result.value) return;
  const outcome = await copyText(paste.value, linkInput.value);
  flashCopied('message', outcome === 'copied');
}

function flashCopied(what: 'link' | 'message', ok: boolean) {
  if (ok) {
    copyHint.value = '';
    copiedWhat.value = what;
    window.clearTimeout(copiedTimer);
    copiedTimer = window.setTimeout(() => (copiedWhat.value = ''), 2000);
  } else {
    copyHint.value = 'Auto-copy is blocked on this address — select the link and press Ctrl/Cmd+C.';
  }
}

async function openThread() {
  if (!result.value) return;
  openBusy.value = true;
  openError.value = '';
  blockedOwnerUrl.value = '';
  try {
    const out = await openThreadInNewTab(result.value.thread_id);
    if (!out.opened) blockedOwnerUrl.value = out.url;
  } catch (e) {
    openError.value = e instanceof ApiError ? e.message : "Couldn't reach the control plane.";
  } finally {
    openBusy.value = false;
  }
}

// ─── Modal behaviour ──────────────────────────────────────────────────────
// `aria-modal="true"` is a promise: while the sheet is open, the rest of the
// page is unreachable. Three things make it true (launch-week item 9 found all
// three missing — a screen reader believed the page was gone while a keyboard
// user was tabbing through the Overview behind the backdrop):
//   1. `inert` on everything outside the sheet — pointer, focus and the
//      accessibility tree at once. Every targeted browser honours it (the build
//      targets Vite 8's baseline-widely-available set: Chrome 111 / Edge 111 /
//      Firefox 114 / Safari 16.4 and up — all past `inert`'s arrival, Firefox
//      112 being the last), so there is no aria-hidden fallback to maintain.
//   2. Tab / Shift+Tab wrap inside the sheet's own controls (first ↔ last).
//   3. Focus returns to the control that opened the sheet when it closes.
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

const backdrop = ref<HTMLElement | null>(null);
/** The control that opened the sheet, and where it stood — see returnFocus. */
let opener: HTMLElement | null = null;
let openerHome: HTMLElement | null = null;
let restoreOutside: (() => void) | null = null;

function focusables(): HTMLElement[] {
  return sheet.value ? Array.from(sheet.value.querySelectorAll<HTMLElement>(FOCUSABLE)) : [];
}

/** Mark every element outside `el` inert — its siblings, its parent's siblings,
 *  and so on up to <body> — and return the undo. Elements that were already
 *  inert are left alone both ways. */
function inertOutside(el: HTMLElement): () => void {
  const marked: Element[] = [];
  for (let node: HTMLElement | null = el; node && node !== document.body && node.parentElement; node = node.parentElement) {
    for (const sibling of Array.from(node.parentElement.children)) {
      if (sibling === node || sibling.hasAttribute('inert')) continue;
      sibling.setAttribute('inert', '');
      marked.push(sibling);
    }
  }
  return () => marked.forEach((n) => n.removeAttribute('inert'));
}

function onKey(ev: KeyboardEvent) {
  if (ev.key === 'Escape') {
    emit('close');
    return;
  }
  if (ev.key !== 'Tab' || !sheet.value) return;
  const items = focusables();
  const active = document.activeElement;
  const inside = active instanceof HTMLElement && sheet.value.contains(active);
  if (items.length === 0) {
    // Everything is disabled (a create in flight): hold focus on the sheet.
    ev.preventDefault();
    sheet.value.focus();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  // Focus can rest INSIDE the sheet but on no control — the sheet div itself
  // (tabindex="-1") after a click on its text. From there the browser's own
  // previous/next focusable is outside and inert, so the trap must wrap.
  const onControl = active instanceof HTMLElement && items.includes(active);
  if (ev.shiftKey) {
    if (!inside || !onControl || active === first) {
      ev.preventDefault();
      last.focus();
    }
  } else if (!inside || !onControl || active === last) {
    ev.preventDefault();
    first.focus();
  }
}

function returnFocus() {
  if (opener?.isConnected) {
    opener.focus();
    return;
  }
  // The opener can be gone by the time the sheet closes: a flagged row swaps
  // its Flag button for the In-thread chip. Land on whatever now stands where
  // it stood (the chip's View thread) rather than dropping focus on <body>.
  openerHome?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
}

onMounted(() => {
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  openerHome = opener?.parentElement ?? null;
  if (backdrop.value) restoreOutside = inertOutside(backdrop.value);
  document.addEventListener('keydown', onKey);
  nextTick(() => (sheet.value?.querySelector<HTMLElement>('textarea, input, button') ?? sheet.value)?.focus());
});
onUnmounted(() => {
  document.removeEventListener('keydown', onKey);
  window.clearTimeout(copiedTimer);
  // Un-inert first: an inert element cannot take focus.
  restoreOutside?.();
  restoreOutside = null;
  returnFocus();
  opener = openerHome = null;
});

watch(result, (r) => {
  if (r) nextTick(() => selectInput(linkInput.value));
});
</script>

<template>
  <div ref="backdrop" class="sheet-backdrop" @click.self="emit('close')">
    <div ref="sheet" class="sheet" role="dialog" aria-modal="true" aria-labelledby="sheet-title" tabindex="-1">
      <!-- ─── Connect-first prompt ─── -->
      <template v-if="!connected && !result">
        <h2 id="sheet-title" class="sheet-title">New thread with {{ provider }}</h2>
        <p v-if="connect?.status === 'pending'" class="prompt">
          Confirm <strong>{{ contactEmail }}</strong> first — we sent "Confirm your Flanj contact".
        </p>
        <p v-else class="prompt">
          Connect first. Creating a thread link needs your org name and a confirmed contact email — viewing local data never does.
        </p>
        <ConnectPanel :state="connect" :default-org="defaultOrg" inline @update:state="(s) => emit('update:connect', s)" @cancel="emit('close')" />
        <p class="prompt-foot">
          Create thread unlocks the moment your contact is confirmed.
        </p>
        <div v-if="connect?.status === 'pending'" class="sheet-actions">
          <button type="button" class="btn ghost" @click="emit('close')">Cancel</button>
        </div>
      </template>

      <!-- ─── Compose ─── -->
      <template v-else-if="!result">
        <h2 id="sheet-title" class="sheet-title">New thread with {{ provider }}</h2>
        <p class="evidence"><span class="k">Evidence (1):</span> {{ evidence }}</p>
        <p v-if="idsLine" class="ids">{{ idsLine }}</p>

        <button type="button" class="disclosure" :aria-expanded="disclosureOpen" @click="toggleDisclosure">
          {{ disclosureOpen ? '▾' : '▸' }} What leaves this collector
        </button>
        <p v-if="disclosureOpen" class="disclosure-body">
          {{ disclosureLead }}
          <strong>{{ consumer }}</strong> · <strong>{{ contactEmail }}</strong>. {{ disclosureTail }}
        </p>

        <label class="field">
          <span class="field-label">Message (optional)</span>
          <textarea v-model="message" rows="4" :disabled="busy"></textarea>
        </label>

        <p v-if="descriptionGuard" class="guard">{{ descriptionGuard }}</p>

        <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
        <div class="sheet-actions">
          <button type="button" class="btn primary" :disabled="busy" @click="createThread">
            {{ busy ? 'Creating…' : errorMsg ? 'Retry' : 'Create thread' }}
          </button>
          <button type="button" class="btn ghost" :disabled="busy" @click="emit('close')">Cancel</button>
        </div>
      </template>

      <!-- ─── Success ─── -->
      <template v-else>
        <h2 id="sheet-title" class="sheet-title">{{ headline }}</h2>
        <input ref="linkInput" class="link-input mono" type="text" readonly :value="result.thread_url" aria-label="Thread link" @focus="selectInput(linkInput)" />
        <p v-if="copyHint" class="hint-copy">{{ copyHint }}</p>
        <div class="sheet-actions">
          <button type="button" class="btn primary" @click="copyLink">{{ copiedWhat === 'link' ? 'Copied' : 'Copy thread link' }}</button>
          <button type="button" class="btn" @click="copyLinkAndMessage">{{ copiedWhat === 'message' ? 'Copied' : 'Copy link + message' }}</button>
          <button type="button" class="btn" :disabled="openBusy" @click="openThread">{{ openBusy ? 'Opening…' : 'View thread' }}</button>
        </div>
        <p v-if="openError" class="error">{{ openError }}</p>
        <p v-if="blockedOwnerUrl" class="hint-copy">
          Your browser blocked the new tab — <a :href="blockedOwnerUrl" target="_blank" rel="noopener">open the thread here</a> (this link works once, for 10 minutes).
        </p>
        <p class="warning">
          Anyone with this link can read the redacted evidence and reply. Paste it where you already talk to {{ provider }}'s team. It lasts 30 days and extends with each reply.
        </p>
        <details class="paste-preview">
          <summary>What "Copy link + message" pastes</summary>
          <p class="paste mono">{{ paste }}</p>
        </details>
        <div class="sheet-actions">
          <button type="button" class="btn ghost" @click="emit('close')">Done</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.sheet-backdrop { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.55); display: flex; align-items: flex-start; justify-content: center; padding: 4vh 1rem; z-index: 50; overflow-y: auto; }
.sheet { background: var(--surface); border: 1px solid var(--rule); border-radius: var(--radius); padding: 1.2rem 1.3rem 1.3rem; width: min(640px, 100%); display: flex; flex-direction: column; gap: 0.7rem; box-shadow: var(--shadow-overlay); }
.sheet-title { margin: 0; font-size: 1.1rem; border: 0; padding: 0; }
.prompt { margin: 0; }
.prompt-foot { margin: 0; color: var(--ink-soft); font-size: 0.82rem; }
.evidence { margin: 0; }
.evidence .k { color: var(--ink-soft); }
.ids { margin: 0; color: var(--ink-soft); font-size: 0.88rem; }
.disclosure { align-self: flex-start; background: transparent; border: 0; color: var(--accent-ink); font: inherit; font-size: 0.88rem; cursor: pointer; padding: 0; }
.disclosure-body { margin: 0; color: var(--ink-soft); font-size: 0.88rem; background: var(--surface-sunk); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.55rem 0.7rem; }
.field { display: flex; flex-direction: column; gap: 0.25rem; }
.field-label { font-size: 0.82rem; font-weight: 600; }
textarea { background: var(--ground); border: 1px solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 0.92rem; padding: 0.5rem 0.65rem; resize: vertical; }
textarea:focus { outline: none; border-color: var(--accent); }
.sheet-actions { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
.link-input { width: 100%; background: var(--ground); border: 1px solid var(--accent); border-radius: var(--radius); color: var(--ink); font-size: 0.88rem; padding: 0.5rem 0.65rem; }
.hint-copy { margin: 0; color: var(--sev-warning-ink); font-size: 0.85rem; }
.warning { margin: 0; color: var(--sev-warning-ink); font-size: 0.85rem; }
.paste-preview { font-size: 0.82rem; color: var(--ink-soft); }
.paste-preview summary { cursor: pointer; }
.paste { margin: 0.35rem 0 0; word-break: break-all; font-size: 0.8rem; background: var(--surface-sunk); border: 1px solid var(--rule); border-radius: var(--radius); padding: 0.5rem 0.65rem; }
.guard { margin: 0; color: var(--ink-soft); font-size: 0.88rem; }
.error { color: var(--sev-breaking); margin: 0; font-size: 0.88rem; }
.mono { font-family: var(--font-mono); }
</style>
