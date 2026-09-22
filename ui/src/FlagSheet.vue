<script setup lang="ts">
// The Flag sheet (v0.1a): one finding → one thread on the control plane → a
// Thread link the consumer copies into the channel the two teams already share.
// Three phases: the Connect prompt (when this collector is not Connected with a
// confirmed contact — the sheet polls and unlocks the moment the click lands),
// compose (evidence line, "what leaves this collector", the optional message,
// Create thread) and the success state (the link, Copy thread link, Copy link +
// message, View thread). Nothing is emailed by Flanj on flag.
import { computed, nextTick, onMounted, onUnmounted, ref, useId, watch } from 'vue';
import { ApiError, apiGet, apiPost, openThreadInNewTab } from './api';
import { copyText, selectInput } from './clipboard';
import ConnectPanel from './ConnectPanel.vue';
import {
  canCreateThread,
  correlationCount,
  defaultFlagMessage,
  evidenceLine,
  pasteText,
  CALL_LESS_DISCLOSURE_LEAD,
  CALL_LESS_DISCLOSURE_TAIL,
  OPEN_TO_DEFAULT_MODE,
  OPEN_TO_DOMAINS_HELP,
  OPEN_TO_DOMAINS_PLACEHOLDER,
  OPEN_TO_EMAILS_HELP,
  OPEN_TO_EMAILS_PLACEHOLDER,
  OPEN_TO_LEGEND,
  gatedShareWarning,
  openToAnyoneHelp,
  openToBody,
  openToModeLabel,
  openToPrefillNote,
  parseOpenTo,
  peopleShareWarning,
  type OpenToMode,
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
import { fetchedSourceForThread } from './contracts';
import type { ContractSpec } from './contracts';
import type { Correlation, DirectoryHint, Finding, FlagResult, RedactedCall } from './types';

const props = defineProps<{
  finding?: Finding | null;
  correlation?: Correlation | null;
  /** The finding's representative source call (MCP server identity rides on it). */
  call?: RedactedCall | null;
  /** The provider host the finding is about (its pinned call's peer
   *  host) — what the "Open to" prefill asks the directory about. */
  providerHost?: string | null;
  /** The contract bound to this provider, when one is. Passed so the default
   *  message can name the provider's OWN published spec URL and the moment it
   *  was read — the evidence upgrade a fetched contract buys. A
   *  contract from any other source contributes nothing and the sentence is
   *  simply absent. */
  spec?: ContractSpec | null;
  provider: string;
  consumer: string;
  connect: ConnectState | null;
}>();
const emit = defineEmits<{
  (e: 'close'): void;
  (e: 'created', r: FlagResult): void;
  (e: 'update:connect', s: ConnectState): void;
}>();

const DISCLOSURE_KEY = 'flanj.flag.disclosure.seen';

// ─── Who can open the thread (thread-domain-gate; three modes 2026-09-15) ──
// A fieldset of three radios — specific people, anyone at a domain (the
// default), anyone with the link — each followed by its own input and help;
// only the selected option's render. Create thread stays ENABLED: a create
// with an unusable list shows the guard, focuses the field and posts nothing.
// Guards wait until the field was typed in (or a create was attempted), so a
// sheet never opens already scolding. The directory prefills the domains field
// when the provider host is a CLAIMED entry (a D5 domain proof: someone there
// proved they control it, so it is honestly their email domain); anything less
// proves nothing about a mailbox, and the field stays required input.
const openToMode = ref<OpenToMode>(OPEN_TO_DEFAULT_MODE);
const emailsText = ref('');
const domainsText = ref('');
/** The operator typed into the field. For domains it also means a prefill that lands later must not overwrite it. */
const emailsTouched = ref(false);
const domainsTouched = ref(false);
/** A Create thread click happened: every guard of the selected mode shows from here on. */
const createAttempted = ref(false);
/** The claimed directory domain the prefill PUT in the field ('' when it put nothing). */
const openToPrefill = ref('');
const emailsParsed = computed(() => parseOpenTo(emailsText.value, 'emails'));
const domainsParsed = computed(() => parseOpenTo(domainsText.value, 'domains'));
const emailsGuard = computed(() => (emailsTouched.value || createAttempted.value ? emailsParsed.value.guard : ''));
const domainsGuard = computed(() => (domainsTouched.value || createAttempted.value ? domainsParsed.value.guard : ''));
/** Whether the selected mode can be posted — judged whether or not its guard is showing yet. */
const openToBlocked = computed(() => {
  if (openToMode.value === 'emails') return emailsParsed.value.guard !== '';
  if (openToMode.value === 'domains') return domainsParsed.value.guard !== '';
  return false;
});
/** The prefill note stays only while the field still holds what the directory put there. */
const domainsHelp = computed(() =>
  openToPrefill.value && domainsText.value.trim() === openToPrefill.value ? openToPrefillNote(openToPrefill.value) : OPEN_TO_DOMAINS_HELP
);
const anyoneHelp = computed(() => openToAnyoneHelp(props.finding ? 'evidence' : 'message'));
const emailsInput = ref<HTMLInputElement | null>(null);
const domainsInput = ref<HTMLInputElement | null>(null);
// Per-sheet ids for the help/guard descriptions and the option labels.
const uid = useId();
const openToIds = {
  emailsLabel: `open-to-emails-label-${uid}`,
  emailsHelp: `open-to-emails-help-${uid}`,
  emailsGuard: `open-to-emails-guard-${uid}`,
  domainsLabel: `open-to-domains-label-${uid}`,
  domainsHelp: `open-to-domains-help-${uid}`,
  domainsGuard: `open-to-domains-guard-${uid}`,
  anyoneHelp: `open-to-anyone-help-${uid}`
};
/** What the created thread was opened to — read by the success state's warning. */
const createdOpenTo = ref<{ mode: OpenToMode; list: string[] } | null>(null);
const hintHost = computed(() => props.providerHost || props.call?.peer_host || props.finding?.peer_host || '');

async function loadOpenToHint() {
  const host = hintHost.value;
  if (!host) return;
  try {
    const hint = await apiGet<DirectoryHint>(`/api/directory/hint?host=${encodeURIComponent(host)}`);
    if (!hint || !hint.claimed || !hint.domain) return;
    if (domainsTouched.value || domainsText.value.trim() !== '') return;
    domainsText.value = hint.domain;
    openToPrefill.value = hint.domain;
  } catch {
    // No hint is the required-input case, not an error: the operator types the domain.
  }
}
// v0.5: MCP findings carry the MCP evidence / IDs / disclosure / prefill
// copy; HTTP findings keep the v0.1a strings unchanged.
const isMcp = computed(() => !!props.finding && isMcpFinding(props.finding));
const mcpServer = computed(() => props.call?.mcp_server_name || props.provider);
const mcpTool = computed(() => props.finding?.endpoint ?? '');

/** The provenance sentence a stranger can check, or '' — one phrasing, shared
 *  with the contract card and the finding (contracts.ts). MCP is excluded by
 *  construction: its baseline is `observed`, so there is no URL to name. */
const specSourceLine = props.spec ? fetchedSourceForThread(props.spec, shortDate) : '';
const message = ref(
  !props.finding
    ? ''
    : isMcpFinding(props.finding)
      ? mcpDefaultMessage(props.finding, shortDate)
      : defaultFlagMessage(props.finding, props.correlation?.request_id, shortDate, specSourceLine)
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
  !props.finding ? '' : isMcp.value ? mcpEvidenceLine(props.finding, mcpServer.value, shortDate) : evidenceLine(props.finding)
);
// The mute-risk guard renders on the DESCRIPTION class only: this class is the
// one where the finding is a question, not a defect, and saying so is what
// keeps a subjective flag from reading as an accusation.
const descriptionGuard = computed(() => (isMcp.value && props.finding && isDescriptionChange(props.finding) ? FLAG_DESCRIPTION_GUARD : ''));
// The IDs line: an MCP flag uses the JSON-RPC line ONLY while the
// client-generated id is the sole correlation key — mixed keys fall back to
// the standard count line with an honest note for the client-generated one.
// HTTP keeps the existing line.
// A definition_change is CALL-LESS: there is no call, so "No request IDs were
// captured on this call." would be answering a question nobody asked about a
// thing that does not exist. The disclosure carries what leaves instead.
const idsLine = computed(() => {
  // A question captured nothing, so there are no ids of theirs to promise. Nor
  // does any OTHER call-less finding — since v1p4 a version diff reaches this
  // sheet too, and "No request IDs were captured on this call" would be
  // answering a question nobody asked about a call that does not exist.
  if (!props.finding || props.finding.kind === 'definition_change' || !props.finding.source_call_id) return '';
  return isMcp.value
    ? mcpIdsLineFor(props.correlation, props.provider)
    : requestIdsLine(correlationCount(props.correlation));
});
const disclosureLead = computed(() => {
  if (!props.finding) return '';
  if (isMcp.value) return mcpDisclosureLead(props.finding, mcpTool.value);
  if (!props.finding.source_call_id) return CALL_LESS_DISCLOSURE_LEAD;
  return 'This redacted request/response, the finding, the correlation keys, the endpoint, your message, and';
});
const disclosureTail = computed(() => {
  if (!props.finding) return '';
  if (isMcp.value) return mcpDisclosureTail(props.finding);
  if (!props.finding.source_call_id) return CALL_LESS_DISCLOSURE_TAIL;
  return 'Raw calls never leave.';
});
const since = computed(() => shortDate(props.finding?.first_seen || props.finding?.last_seen || ''));
const paste = computed(() => {
  if (!result.value || !props.finding) return '';
  return pasteText({
    endpoint: props.finding.endpoint,
    since: since.value,
    requestId: props.correlation?.request_id,
    link: result.value.thread_url
  });
});
/** The share warning. A thread opened to people or a domain says who can open
 *  it instead of "Anyone with this link", which is what the operator chose
 *  against. */
const shareWarning = computed(() => {
  const what = 'evidence';
  const created = createdOpenTo.value;
  if (created?.mode === 'emails') return peopleShareWarning(created.list, props.provider, what);
  if (created?.mode === 'domains') return gatedShareWarning(created.list, props.provider, what);
  return `Anyone with this link can read the redacted evidence and reply. Paste it where you already talk to ${props.provider}'s team. It lasts 30 days and extends with each reply.`;
});

function toggleDisclosure() {
  disclosureOpen.value = !disclosureOpen.value;
  // Remembered once the user has seen it (collapsed after first use).
  localStorage.setItem(DISCLOSURE_KEY, '1');
}

async function createThread() {
  if (busy.value) return;
  createAttempted.value = true;
  if (openToBlocked.value) {
    // The guard is showing now; put the operator in the field it is about. Nothing is posted.
    await nextTick();
    (openToMode.value === 'emails' ? emailsInput.value : domainsInput.value)?.focus();
    return;
  }
  busy.value = true;
  errorMsg.value = '';
  // Always BOTH keys on the wire: the chosen list and null, or both null for anyone.
  const mode = openToMode.value;
  const list = mode === 'emails' ? emailsParsed.value.list : mode === 'domains' ? domainsParsed.value.list : [];
  const access = openToBody(mode, emailsParsed.value.list, domainsParsed.value.list);
  try {
    const r = await apiPost<FlagResult>('/api/flag', {
      finding_id: props.finding?.id,
      message: message.value,
      provider_display_name: props.provider,
      ...access
    });
    createdOpenTo.value = { mode, list };
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
      errorMsg.value = "Couldn't reach Flanj — nothing was created or shared.";
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
    openError.value = e instanceof ApiError ? e.message : "Couldn't reach Flanj.";
  } finally {
    openBusy.value = false;
  }
}

// ─── Modal behaviour ──────────────────────────────────────────────────────
// `aria-modal="true"` is a promise: while the sheet is open, the rest of the
// page is unreachable. Three things make it true (each was once missing — a screen reader
// believed the page was gone while a keyboard user was tabbing through the
// Overview behind the backdrop):
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
  void loadOpenToHint();
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
          Connect first. Creating a thread link needs a confirmed contact email — viewing local data never does.
        </p>
        <ConnectPanel :state="connect" inline @update:state="(s) => emit('update:connect', s)" @cancel="emit('close')" />
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

        <!-- Who can open it: three radios in spec order, each followed by its
             own input and help; only the selected option's render. Each guard
             sits in a live wrapper that exists before the guard does, so its
             arrival is announced. -->
        <fieldset class="open-to-choice">
          <legend class="field-label">{{ OPEN_TO_LEGEND }}</legend>

          <label class="check open-to-option">
            <input v-model="openToMode" type="radio" name="open_to_mode" value="emails" :disabled="busy" />
            <span :id="openToIds.emailsLabel">{{ openToModeLabel('emails') }}</span>
          </label>
          <div v-if="openToMode === 'emails'" class="open-to-detail">
            <input
              ref="emailsInput"
              v-model="emailsText"
              class="open-to-emails"
              type="text"
              name="allowed_emails"
              :placeholder="OPEN_TO_EMAILS_PLACEHOLDER"
              :disabled="busy"
              :aria-labelledby="openToIds.emailsLabel"
              :aria-describedby="`${openToIds.emailsHelp} ${openToIds.emailsGuard}`"
              :aria-invalid="emailsGuard ? 'true' : undefined"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              @input="emailsTouched = true"
            />
            <p :id="openToIds.emailsHelp" class="ids open-to-note">{{ OPEN_TO_EMAILS_HELP }}</p>
            <!-- The guard is always in the DOM (hidden while empty): the input's aria-describedby must resolve before
                 a guard speaks, and the live region must already exist when its text changes, or nothing is announced. -->
            <div class="open-to-live" aria-live="polite">
              <p v-show="emailsGuard" :id="openToIds.emailsGuard" class="guard open-to-guard">{{ emailsGuard }}</p>
            </div>
          </div>

          <label class="check open-to-option">
            <input v-model="openToMode" type="radio" name="open_to_mode" value="domains" :disabled="busy" />
            <span :id="openToIds.domainsLabel">{{ openToModeLabel('domains') }}</span>
          </label>
          <div v-if="openToMode === 'domains'" class="open-to-detail">
            <input
              ref="domainsInput"
              v-model="domainsText"
              class="open-to"
              type="text"
              name="allowed_domains"
              :placeholder="OPEN_TO_DOMAINS_PLACEHOLDER"
              :disabled="busy"
              :aria-labelledby="openToIds.domainsLabel"
              :aria-describedby="`${openToIds.domainsHelp} ${openToIds.domainsGuard}`"
              :aria-invalid="domainsGuard ? 'true' : undefined"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              @input="domainsTouched = true"
            />
            <p :id="openToIds.domainsHelp" class="ids open-to-note">{{ domainsHelp }}</p>
            <div class="open-to-live" aria-live="polite">
              <p v-show="domainsGuard" :id="openToIds.domainsGuard" class="guard open-to-guard">{{ domainsGuard }}</p>
            </div>
          </div>

          <label class="check open-to-option">
            <input
              v-model="openToMode"
              type="radio"
              name="open_to_mode"
              value="anyone"
              :disabled="busy"
              :aria-describedby="openToMode === 'anyone' ? openToIds.anyoneHelp : undefined"
            />
            <span>{{ openToModeLabel('anyone') }}</span>
          </label>
          <div v-if="openToMode === 'anyone'" class="open-to-detail">
            <p :id="openToIds.anyoneHelp" class="ids open-to-note">{{ anyoneHelp }}</p>
          </div>
        </fieldset>

        <p v-if="descriptionGuard" class="guard">{{ descriptionGuard }}</p>

        <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
        <div class="sheet-actions">
          <!-- Stays enabled over an unusable Who can open it: the click shows the guard and focuses the field. -->
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
        <p class="warning">{{ shareWarning }}</p>
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
/* Blueprint: the sheet is a surface card in a 2px ink frame under the one
   overlay shadow the token layer allows; inside it, the same mono eyebrows,
   2px inputs and outline buttons as the page. */
.sheet-backdrop { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.55); display: flex; align-items: flex-start; justify-content: center; padding: 4vh 16px; z-index: 50; overflow-y: auto; }
.sheet { background: var(--surface); border: var(--border-w) solid var(--ink); border-radius: var(--radius); padding: 20px; width: min(640px, 100%); display: flex; flex-direction: column; gap: 12px; box-shadow: var(--shadow-overlay); }
/* The title is a sentence, not an eyebrow: it must not inherit the page's mono
   uppercase h2. */
.sheet-title { margin: 0; font: 600 17px/1.3 var(--f-sans); letter-spacing: -0.01em; text-transform: none; color: var(--ink); border: 0; padding: 0; }
.prompt { margin: 0; }
.prompt-foot { margin: 0; color: var(--ink-soft); font-size: 12.5px; }
.evidence { margin: 0; font-size: 13.5px; }
.evidence .k { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); margin-right: 6px; }
.ids { margin: 0; color: var(--ink-soft); font-size: 13.5px; }
.disclosure { align-self: flex-start; background: transparent; border: 0; color: var(--accent-ink); font: inherit; font-size: 13.5px; cursor: pointer; padding: 0; transition: color var(--dur-fast) var(--ease); }
.disclosure:hover { color: var(--ink); }
.disclosure-body { margin: 0; color: var(--ink-soft); font-size: 13.5px; background: var(--surface-sunk); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 8px 12px; }
.field { display: flex; flex-direction: column; gap: 4px; }
.field-label { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
textarea, .open-to, .open-to-emails { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 14px; padding: 8px 10px; transition: border-color var(--dur-fast) var(--ease); }
textarea { resize: vertical; }
textarea:focus, .open-to:focus, .open-to-emails:focus { border-color: var(--ink); }
.open-to:disabled, .open-to-emails:disabled { color: var(--ink-soft); background: var(--surface-sunk); }
/* Who can open it: a bare fieldset under the same mono eyebrow as the other
   fields. Each option's field and help hang under its radio, indented to the
   label text, so the three read as one choice rather than three forms. */
.open-to-choice { border: 0; margin: 0; padding: 0; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.open-to-choice legend { padding: 0; margin-bottom: 4px; }
.check { display: flex; align-items: center; gap: 8px; font-size: 13.5px; color: var(--ink-soft); cursor: pointer; }
.check input { margin: 0; accent-color: var(--ink); }
.open-to-option { color: var(--ink); align-self: flex-start; }
.open-to-detail { display: flex; flex-direction: column; padding-left: 21px; margin-bottom: 4px; }
.open-to-detail .open-to, .open-to-detail .open-to-emails { width: 100%; box-sizing: border-box; }
.open-to-detail .open-to-note { margin-top: 4px; }
/* The live wrapper stays in the DOM with no box of its own; only a guard inside it takes space. */
.open-to-detail .open-to-guard { margin-top: 4px; }
textarea:focus-visible, .open-to:focus-visible, .open-to-emails:focus-visible, .check input:focus-visible, .link-input:focus-visible, .disclosure:focus-visible, .paste-preview summary:focus-visible, .hint-copy a:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-offset); }
.sheet-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
/* The thread link: mono, in an accent frame — it is the one thing on the
   success state to take away. */
.link-input { width: 100%; background: var(--surface-sunk); border: var(--border-w) solid var(--accent); border-radius: var(--radius); color: var(--ink); font-size: 13px; padding: 8px 10px; }
/* Cautions in the sheet are prose, not findings: body ink behind an accent rule
   rather than the warning tier's colour, which is now the accent's own hue. */
.hint-copy, .warning { margin: 0; color: var(--ink); font-size: 13px; padding-left: 10px; border-left: var(--border-w-stripe) solid var(--accent); }
.hint-copy a { color: var(--accent-ink); }
.paste-preview { font-size: 12.5px; color: var(--ink-soft); }
.paste-preview summary { cursor: pointer; }
.paste { margin: 6px 0 0; word-break: break-all; font-size: 12.5px; background: var(--surface-sunk); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 8px 10px; }
.guard { margin: 0; color: var(--ink-soft); font-size: 13.5px; overflow-wrap: anywhere; }
.error { color: var(--sev-breaking-ink); margin: 0; font-size: 13.5px; }
.mono { font-family: var(--f-mono); }
</style>
