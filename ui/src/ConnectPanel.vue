<script setup lang="ts">
// Connect panel (v0.1a; open since 2026-09-14): registers this collector with
// the control plane — a collector NAME (mandatory, unique in the workspace,
// changeable), the org name and a contact email the control plane confirms
// with one click. No pre-issued token: the confirmation click is the consent.
// Shown on the Settings tab and inline in the Flag sheet. Local data viewing is
// never gated on it; only creating a thread link is. A RENAME is the same
// form, re-sent with the stored key — the relay carries it; the CP updates the
// record the key names and nothing else.
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { ApiError, apiPost } from './api';
import { needsCollectorAddress, type ConnectState } from './threads';
import { applySeed, seededValues, untouched, type ConnectFormTouched } from './connect-form';
import { mailNotice, type MailAttempt } from './connect-mail';
import { edgeDisclosure } from './connect-disclosure';

const props = defineProps<{
  state: ConnectState | null;
  defaultOrg?: string;
  inline?: boolean;
  /** Post-Connect address nudge: shared "remembered dismissal" flag (App owns storage). */
  addressNudgeDismissed?: boolean;
  /** Bumped by App when "Add address" is clicked elsewhere (Threads tab) — opens the form and focuses the address field. */
  focusAddressTick?: number;
}>();
const emit = defineEmits<{ (e: 'update:state', s: ConnectState): void; (e: 'cancel'): void; (e: 'dismiss-address-nudge'): void }>();

const org = ref('');
const collectorName = ref('');
const name = ref('');
const email = ref('');
const localUrl = ref('');
const editing = ref(false);
const busy = ref(false);
const errorMsg = ref('');
const validation = ref('');

/**
 * The last send this panel actually attempted. Held HERE, not read off `props.state`: the outcome
 * describes one request, so the next background poll (~5s) carries no `confirmation_mail` and
 * would otherwise wipe a "not sent" warning and restore "check your inbox".
 */
const attempt = ref<MailAttempt | null>(null);
/** The address a confirmation mail was actually DELIVERED to by this panel, so "Sent again" is
 *  said of a delivery, not of a button: a Retry after a `failed` send is the FIRST mail that ever
 *  left. Post-reload the panel cannot know, and the button stays the fallback. */
const deliveredTo = ref<string | null>(null);
/** Ticks only while a cooldown notice is on screen, so its countdown expires by itself. */
const now = ref(Date.now());
let ticker: ReturnType<typeof setInterval> | null = null;
const notice = computed(() => mailNotice(attempt.value, now.value));

/** v1 phase 2 — what Connecting causes, stated before the operator Connects. */
const disclosure = computed(() => edgeDisclosure(props.state?.edge_sync));

watch(
  () => notice.value.kind === 'cooldown',
  (counting) => {
    if (ticker) {
      clearInterval(ticker);
      ticker = null;
    }
    // Without this the floor's "try again in about 6 minutes" would still read the same — and
    // Resend would still be disabled — long after the floor had actually lifted.
    if (counting) ticker = setInterval(() => (now.value = Date.now()), 1000);
  },
  { immediate: true }
);
onBeforeUnmount(() => {
  if (ticker) clearInterval(ticker);
});

const touched = ref<ConnectFormTouched>(untouched());
const focusedField = ref<keyof ConnectFormTouched | null>(null);
const fieldRefs = { org, collectorName, name, email, localUrl } as const;
function markTouched(k: keyof ConnectFormTouched) {
  // Typing marks the field as the user's; clearing it back to empty releases it for
  // prefill again (applySeed still never touches the focused field).
  touched.value[k] = fieldRefs[k].value !== '';
}
function setFocus(k: keyof ConnectFormTouched | null) {
  focusedField.value = k;
}

/** force = an explicit user action (open edit / cancel / after submit) — background polls never force. */
function seedForm(force = false) {
  const seeded = seededValues(props.state, props.defaultOrg, typeof window !== 'undefined' ? window.location.origin : '');
  const next = applySeed(
    { org: org.value, collectorName: collectorName.value, name: name.value, email: email.value, localUrl: localUrl.value },
    seeded,
    touched.value,
    force,
    focusedField.value
  );
  org.value = next.org;
  collectorName.value = next.collectorName;
  name.value = next.name;
  email.value = next.email;
  localUrl.value = next.localUrl;
  if (force) touched.value = untouched();
}
seedForm(true);
watch(
  () => [props.state?.status, props.state?.contact_email, props.state?.collector_name, props.defaultOrg],
  () => {
    // Background refresh: fill only pristine fields — never clobber typed text.
    if (!editing.value) seedForm(false);
  }
);

const status = computed(() => props.state?.status ?? 'disconnected');
const localUrlEl = ref<HTMLInputElement | null>(null);
const collectorNameEl = ref<HTMLInputElement | null>(null);
const showAddressNudge = computed(
  () => !props.inline && !editing.value && needsCollectorAddress(props.state) && !props.addressNudgeDismissed
);

/** "Add address": open the form with the address field focused (re-register with
 *  the same contact goes out with the collector key — an idempotent replay that
 *  only updates local_ui_url). */
async function addAddress() {
  seedForm(true);
  editing.value = true;
  attempt.value = null;
  validation.value = '';
  errorMsg.value = '';
  await nextTick();
  localUrlEl.value?.focus();
}

watch(
  () => props.focusAddressTick,
  (tick) => {
    if (tick) addAddress();
  }
);

/** "Rename": the same form, focused on the name. Submit re-registers with the stored key — the
 *  CP updates the record the key names and nothing else; no confirmation mail goes out. */
async function rename() {
  seedForm(true);
  editing.value = true;
  attempt.value = null;
  validation.value = '';
  errorMsg.value = '';
  await nextTick();
  collectorNameEl.value?.focus();
  collectorNameEl.value?.select();
}

function validEmail(v: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v.trim());
}

async function submit(resend = false) {
  validation.value = '';
  errorMsg.value = '';
  if (!org.value.trim()) {
    validation.value = 'Add your organization first.';
    return;
  }
  if (!collectorName.value.trim()) {
    validation.value = 'Give this collector a name.';
    return;
  }
  if (!validEmail(email.value)) {
    validation.value = 'Enter a valid email.';
    return;
  }
  busy.value = true;
  attempt.value = null;
  try {
    const s = await apiPost<ConnectState>('/api/connect', {
      consumer_display_name: org.value.trim(),
      collector_name: collectorName.value.trim(),
      contact_email: email.value.trim(),
      contact_display_name: name.value.trim(),
      local_ui_url: localUrl.value.trim()
    });
    editing.value = false;
    // A send WAS attempted (this is the register call), so the reply's `confirmation_mail` is the
    // thing to render — whichever of the three it says. What happened to the mail does not depend
    // on which button was pressed; `resend` decides only the wording of a success: "Sent again" is
    // true after Resend and false after the first Connect (or Change contact to a new address).
    now.value = Date.now();
    const to = s.contact_email ?? email.value.trim();
    attempt.value = {
      outcome: s.confirmation_mail,
      retryAfterS: s.confirmation_mail_retry_after_s,
      email: to,
      at: now.value,
      // "again" only when a mail has already reached THIS address from this panel.
      resend: resend && deliveredTo.value === to
    };
    if (s.confirmation_mail === 'sent') deliveredTo.value = to;
    touched.value = untouched(); // the server state is now the truth; future seeds may fill every field
    emit('update:state', s);
  } catch (e) {
    errorMsg.value = e instanceof ApiError ? e.message : "Couldn't reach the control plane — nothing was sent.";
  } finally {
    busy.value = false;
  }
}

function changeEmail() {
  seedForm(true);
  editing.value = true;
  attempt.value = null;
  errorMsg.value = '';
}

function cancelEdit() {
  editing.value = false;
  validation.value = '';
  errorMsg.value = '';
  seedForm(true);
  emit('cancel');
}
</script>

<template>
  <div class="connect" :class="{ inline }">
    <!-- Copy (content fundamentals): no "we", "the other side" rather than
         "your vendor", and no network vocabulary. -->
    <template v-if="!inline">
      <h3 class="connect-title">Connect to Flanj</h3>
      <p class="connect-sub">A detection becomes a thread the other side can act on. Required to create thread links; viewing your own traffic and findings never needs it.</p>
    </template>

    <!-- Edge-registration disclosure (v1 phase 2): what Connecting causes, said
         BEFORE Connecting and in every state — and said honestly when the
         `edge_sync` switch has turned it off. -->
    <p v-if="disclosure" class="connect-disclosure" :class="disclosure.state">
      {{ disclosure.text }}
    </p>

    <!-- connected: the kit's k/v grid (mono eyebrow, mono value in a hairline
         frame) — facts an operator can scan, not a sentence. `confirmed` is a
         green-bolt suffix on the contact; the collector address shows the
         Add address affordance inline while it is missing. -->
    <div v-if="status === 'connected' && !editing" class="connect-state ok">
      <p class="connect-line">Connected</p>
      <dl class="connect-facts">
        <div class="connect-field">
          <dt class="k">Collector name</dt>
          <dd class="v">
            <span class="v-main">{{ state?.collector_name }}</span>
            <button type="button" class="btn ghost small" @click="rename">Rename</button>
          </dd>
        </div>
        <div class="connect-field">
          <dt class="k">Organization</dt>
          <dd class="v">{{ state?.consumer_display_name }}</dd>
        </div>
        <div class="connect-field">
          <dt class="k">Contact</dt>
          <dd class="v">
            <span class="v-main">{{ state?.contact_email }}</span>
            <span class="v-mark ok"><svg class="hx sm tone-ok" aria-hidden="true" focusable="false"><use href="#hxbolt" /></svg>confirmed</span>
            <span v-if="state?.contact_display_name && state?.contact_display_name !== state?.consumer_display_name" class="dim">
              · replies as {{ state?.contact_display_name }}
            </span>
          </dd>
        </div>
        <div class="connect-field">
          <dt class="k">Collector address</dt>
          <dd class="v">
            <span v-if="state?.local_ui_url" class="v-main">{{ state?.local_ui_url }}</span>
            <template v-else>
              <span class="dim">not set</span>
              <button v-if="showAddressNudge" type="button" class="btn small" @click="addAddress">Add address</button>
            </template>
          </dd>
        </div>
      </dl>
      <div class="connect-actions">
        <button type="button" class="btn ghost" @click="changeEmail">Change contact</button>
      </div>
      <p v-if="showAddressNudge" class="connect-nudge">
        <span>Reply notification emails can link straight back to the thread here. Add this collector's address to turn that on.</span>
        <span class="connect-nudge-actions">
          <button type="button" class="btn ghost small" aria-label="Dismiss" @click="emit('dismiss-address-nudge')">Dismiss</button>
        </span>
      </p>
    </div>

    <!-- pending: the standing line describes the CONTACT's state; the notice below describes what
         happened to the mail on the last click, and only when a send was actually attempted. -->
    <div v-else-if="status === 'pending' && !editing" class="connect-state pending" :class="{ 'mail-failed': notice.kind === 'failed' }">
      <p v-if="notice.kind === 'failed'" class="connect-line">
        Waiting on <strong>{{ state?.contact_email }}</strong> to confirm — but the last confirmation mail did not go out.
      </p>
      <p v-else class="connect-line">
        Check your inbox — "Confirm your Flanj contact" went to <strong>{{ state?.contact_email }}</strong>. The link works once, for 72 hours.
      </p>
      <p v-if="notice.kind === 'sent' && notice.text" class="connect-note">{{ notice.text }}</p>
      <p v-else-if="notice.kind === 'failed'" class="error" role="alert">{{ notice.text }}</p>
      <p v-else-if="notice.kind === 'cooldown'" class="connect-note muted" role="status">{{ notice.text }}</p>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
      <div class="connect-actions">
        <button type="button" class="btn" :disabled="busy || !notice.canSend" @click="submit(true)">
          {{ busy ? 'Sending…' : notice.retryable ? 'Retry' : 'Resend' }}
        </button>
        <button type="button" class="btn ghost" :disabled="busy" @click="changeEmail">Change email</button>
      </div>
    </div>

    <!-- form -->
    <form v-else class="connect-form" @submit.prevent="submit(false)">
      <label class="field">
        <span class="field-label">Your organization</span>
        <input v-model="org" type="text" autocomplete="organization" :disabled="busy" @input="markTouched('org')" @focus="setFocus('org')" @blur="setFocus(null)" />
        <span class="field-help">Shown to the provider on every thread.</span>
      </label>
      <label class="field">
        <span class="field-label">Collector name</span>
        <input ref="collectorNameEl" v-model="collectorName" type="text" autocomplete="off" maxlength="80" placeholder="e.g. prod-eu" :disabled="busy" @input="markTouched('collectorName')" @focus="setFocus('collectorName')" @blur="setFocus(null)" />
        <span class="field-help">How this deployment appears in your Flanj workspace. Unique there, and you can change it later.</span>
      </label>
      <label class="field">
        <span class="field-label">Your name <span class="dim">(optional)</span></span>
        <input v-model="name" type="text" autocomplete="name" :placeholder="org ? 'e.g. Dana (' + org + ')' : 'e.g. Dana'" :disabled="busy" @input="markTouched('name')" @focus="setFocus('name')" @blur="setFocus(null)" />
        <span class="field-help">Shown next to your messages on the thread. Defaults to your organization.</span>
      </label>
      <label class="field">
        <span class="field-label">Contact email</span>
        <input v-model="email" type="email" autocomplete="email" :disabled="busy" @input="markTouched('email')" @focus="setFocus('email')" @blur="setFocus(null)" />
        <span class="field-help">Gets a one-time confirmation now and reply notifications later. Shown on your messages.</span>
      </label>
      <label class="field">
        <span class="field-label">Collector address <span class="dim">(optional)</span></span>
        <input ref="localUrlEl" v-model="localUrl" type="url" :disabled="busy" @input="markTouched('localUrl')" @focus="setFocus('localUrl')" @blur="setFocus(null)" />
        <span class="field-help">The URL where you open this UI. Sent with your registration and used only in links back here.</span>
      </label>
      <p v-if="validation" class="error">{{ validation }}</p>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
      <div class="connect-actions">
        <button type="submit" class="btn primary" :disabled="busy">
          {{ busy ? 'Connecting…' : errorMsg ? 'Retry' : 'Connect' }}
        </button>
        <button v-if="editing || inline" type="button" class="btn ghost" :disabled="busy" @click="cancelEdit">Cancel</button>
      </div>
      <p class="connect-foot">Nothing leaves this collector until you click Connect. Connect sends only the fields above; no token is needed — the contact's confirmation click is what adds this collector to their workspace.</p>
    </form>
  </div>
</template>

<style scoped>
/* Blueprint: the Connect card's states are framed blocks on the sunk surface;
   the form's labels are mono eyebrows over 2px inputs. Layout and component
   rules only — every colour is a token. */
.connect { display: flex; flex-direction: column; gap: 8px; }
.connect-title { margin: 0; font-size: 14px; font-weight: 600; }
.connect-sub { margin: 0 0 8px; color: var(--ink-soft); font-size: 13.5px; }
.connect-state { background: var(--surface-sunk); border: var(--border-w) solid var(--rule); border-left-width: var(--border-w-stripe-lg); border-radius: var(--radius); padding: 12px 16px; }
/* Connected is a reached state: the green rule. */
.connect-state.ok { border-left-color: var(--ok); }
/* Pending is an attention state, not a finding: the accent outlines it. */
.connect-state.pending { border-left-color: var(--accent); }
/* A mail that never left is a failure, not a "waiting" state — the rule must not say otherwise. */
.connect-state.pending.mail-failed { border-left-color: var(--sev-breaking); }
.connect-line { margin: 0; }
/* The kit's k/v grid: mono eyebrow, mono value in a hairline frame. */
.connect-facts { margin: 10px 0 0; }
.connect-field { display: grid; grid-template-columns: 130px 1fr; gap: 10px; align-items: center; margin: 0 0 8px; }
.connect-field .k { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); margin: 0; }
.connect-field .v { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 0; min-width: 0; font-family: var(--f-mono); font-size: 12.5px; border: var(--border-w-hair) solid var(--rule); background: var(--surface); padding: 6px 10px; color: var(--ink); }
.connect-field .v-main { overflow-wrap: anywhere; }
/* `confirmed` is a reached state: green ink beside a green bolt. */
.connect-field .v-mark { display: inline-flex; align-items: center; gap: 4px; font-size: 10.5px; letter-spacing: 0.08em; text-transform: uppercase; }
.connect-field .v-mark.ok { color: var(--ok-ink); }
.connect-field .hx { width: 10px; height: 10px; }
.connect-note { margin: 6px 0 0; color: var(--ok-ink); font-size: 13px; }
.connect-note.muted { color: var(--ink-soft); }
.connect-nudge { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin: 10px 0 0; padding-top: 10px; border-top: var(--border-w-hair) solid var(--rule); color: var(--ink-soft); font-size: 13.5px; }
.connect-nudge-actions { display: flex; gap: 8px; }
/* The edge-registration disclosure sits between the intro and the state block —
   read before Connecting, not hidden behind it. Muted, never alarming: it
   describes a designed, disclosed flow. `off` reads the same weight; the switch
   being off is a configuration fact, not a warning. */
.connect-disclosure { margin: 0 0 12px; padding-left: 10px; border-left: var(--border-w-stripe) solid var(--rule); color: var(--ink-soft); font-size: 13px; line-height: 1.45; }
.connect-form { display: flex; flex-direction: column; gap: 12px; background: var(--surface-sunk); border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 14px 16px; }
.connect.inline .connect-form { background: transparent; border: 0; padding: 0; }
.field { display: flex; flex-direction: column; gap: 4px; }
.field-label { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
.field-help { font-size: 12px; color: var(--ink-soft); }
.field input { background: var(--surface); border: var(--border-w) solid var(--rule); border-radius: var(--radius); color: var(--ink); font: inherit; font-size: 14px; padding: 8px 10px; transition: border-color var(--dur-fast) var(--ease); }
.field input:focus { border-color: var(--ink); }
.field input:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-offset); }
.field input:disabled { opacity: 0.6; }
.connect-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-top: 6px; }
.connect-foot { margin: 3px 0 0; font-size: 12px; color: var(--ink-soft); }
.dim { color: var(--ink-soft); font-weight: 400; text-transform: none; letter-spacing: 0; }
.error { color: var(--sev-breaking-ink); margin: 0; font-size: 13.5px; }
@media (max-width: 720px) {
  .connect-field { grid-template-columns: 1fr; gap: 4px; }
}
</style>
