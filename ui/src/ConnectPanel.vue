<script setup lang="ts">
// Connect panel (v0.1a): registers this collector with the Vinifera network —
// org name + a contact email the control plane confirms with one click. Shown
// on the Settings tab and inline in the Flag sheet. Local data viewing is never
// gated on it; only creating a thread link is.
import { computed, ref, watch } from 'vue';
import { ApiError, apiPost } from './api';
import type { ConnectState } from './threads';

const props = defineProps<{
  state: ConnectState | null;
  defaultOrg?: string;
  inline?: boolean;
}>();
const emit = defineEmits<{ (e: 'update:state', s: ConnectState): void; (e: 'cancel'): void }>();

const org = ref('');
const name = ref('');
const email = ref('');
const localUrl = ref('');
const editing = ref(false);
const busy = ref(false);
const resent = ref(false);
const errorMsg = ref('');
const validation = ref('');

function seedForm() {
  const s = props.state;
  org.value = s?.consumer_display_name || props.defaultOrg || '';
  name.value = s?.contact_display_name || '';
  email.value = s?.contact_email || '';
  localUrl.value = s?.local_ui_url || (typeof window !== 'undefined' ? window.location.origin : '');
}
seedForm();
watch(
  () => [props.state?.status, props.state?.contact_email, props.defaultOrg],
  () => {
    if (!editing.value) seedForm();
  }
);

const status = computed(() => props.state?.status ?? 'disconnected');

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
  if (!validEmail(email.value)) {
    validation.value = 'Enter a valid email.';
    return;
  }
  busy.value = true;
  resent.value = false;
  try {
    const s = await apiPost<ConnectState>('/api/connect', {
      consumer_display_name: org.value.trim(),
      contact_email: email.value.trim(),
      contact_display_name: name.value.trim(),
      local_ui_url: localUrl.value.trim()
    });
    editing.value = false;
    resent.value = resend;
    emit('update:state', s);
  } catch (e) {
    errorMsg.value = e instanceof ApiError ? e.message : "Couldn't reach the control plane — nothing was sent.";
  } finally {
    busy.value = false;
  }
}

function changeEmail() {
  seedForm();
  editing.value = true;
  resent.value = false;
  errorMsg.value = '';
}

function cancelEdit() {
  editing.value = false;
  validation.value = '';
  errorMsg.value = '';
  seedForm();
  emit('cancel');
}
</script>

<template>
  <div class="connect" :class="{ inline }">
    <template v-if="!inline">
      <h3 class="connect-title">Connect to Vinifera network</h3>
      <p class="connect-sub">Required to create thread links. Viewing your own traffic and findings never needs it.</p>
    </template>

    <!-- connected -->
    <div v-if="status === 'connected' && !editing" class="connect-state ok">
      <p class="connect-line">
        Connected as <strong>{{ state?.consumer_display_name }}</strong> · <strong>{{ state?.contact_email }}</strong> confirmed
        <span v-if="state?.contact_display_name && state?.contact_display_name !== state?.consumer_display_name" class="dim">
          · replies as {{ state?.contact_display_name }}
        </span>
      </p>
      <div class="connect-actions">
        <button type="button" class="btn ghost" @click="changeEmail">Change contact</button>
      </div>
    </div>

    <!-- pending -->
    <div v-else-if="status === 'pending' && !editing" class="connect-state pending">
      <p class="connect-line">
        Check your inbox — we sent "Confirm your Vinifera contact" to <strong>{{ state?.contact_email }}</strong>. The link works once, for 72 hours.
      </p>
      <p v-if="resent" class="connect-note">Sent again to {{ state?.contact_email }}.</p>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
      <div class="connect-actions">
        <button type="button" class="btn" :disabled="busy" @click="submit(true)">{{ busy ? 'Sending…' : 'Resend' }}</button>
        <button type="button" class="btn ghost" :disabled="busy" @click="changeEmail">Change email</button>
      </div>
    </div>

    <!-- form -->
    <form v-else class="connect-form" @submit.prevent="submit(false)">
      <label class="field">
        <span class="field-label">Your organization</span>
        <input v-model="org" type="text" autocomplete="organization" :disabled="busy" />
        <span class="field-help">Shown to the provider on every thread.</span>
      </label>
      <label class="field">
        <span class="field-label">Your name <span class="dim">(optional)</span></span>
        <input v-model="name" type="text" autocomplete="name" :placeholder="org ? 'e.g. Dana (' + org + ')' : 'e.g. Dana'" :disabled="busy" />
        <span class="field-help">Shown next to your messages on the thread. Defaults to your organization.</span>
      </label>
      <label class="field">
        <span class="field-label">Contact email</span>
        <input v-model="email" type="email" autocomplete="email" :disabled="busy" />
        <span class="field-help">Gets a one-time confirmation now and reply notifications later. Shown on your messages.</span>
      </label>
      <label class="field">
        <span class="field-label">This collector's address <span class="dim">(optional)</span></span>
        <input v-model="localUrl" type="url" :disabled="busy" />
        <span class="field-help">Used for the "Open in collector" link in your notification emails. Vinifera never calls it.</span>
      </label>
      <p v-if="validation" class="error">{{ validation }}</p>
      <p v-if="errorMsg" class="error">{{ errorMsg }}</p>
      <div class="connect-actions">
        <button type="submit" class="btn primary" :disabled="busy">
          {{ busy ? 'Connecting…' : errorMsg ? 'Retry' : 'Connect' }}
        </button>
        <button v-if="editing || inline" type="button" class="btn ghost" :disabled="busy" @click="cancelEdit">Cancel</button>
      </div>
      <p class="connect-foot">Nothing leaves this collector until you click Connect. Connect sends only the fields above.</p>
    </form>
  </div>
</template>

<style scoped>
.connect { display: flex; flex-direction: column; gap: 0.5rem; }
.connect-title { margin: 0; font-size: 1rem; }
.connect-sub { margin: 0 0 0.5rem; color: var(--muted); font-size: 0.9rem; }
.connect-state { background: var(--panel2); border: 1px solid var(--line); border-radius: 10px; padding: 0.8rem 1rem; }
.connect-state.ok { border-color: var(--ok); }
.connect-state.pending { border-color: var(--warn); }
.connect-line { margin: 0; }
.connect-note { margin: 0.35rem 0 0; color: var(--ok); font-size: 0.85rem; }
.connect-form { display: flex; flex-direction: column; gap: 0.7rem; background: var(--panel2); border: 1px solid var(--line); border-radius: 10px; padding: 0.9rem 1rem; }
.connect.inline .connect-form { background: transparent; border: 0; padding: 0; }
.field { display: flex; flex-direction: column; gap: 0.2rem; }
.field-label { font-size: 0.82rem; font-weight: 600; }
.field-help { font-size: 0.78rem; color: var(--muted); }
.field input { background: var(--bg); border: 1px solid var(--line); border-radius: 8px; color: var(--ink); font: inherit; font-size: 0.92rem; padding: 0.45rem 0.65rem; }
.field input:focus { outline: none; border-color: var(--accent); }
.connect-actions { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-top: 0.4rem; }
.connect-foot { margin: 0.2rem 0 0; font-size: 0.78rem; color: var(--muted); }
.dim { color: var(--muted); font-weight: 400; }
.error { color: var(--danger); margin: 0; font-size: 0.88rem; }
</style>
