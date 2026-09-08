<script setup lang="ts">
/**
 * The uploader — the ONE place a provider contract enters this collector.
 *
 * The Edges panel links here rather than offering a second uploader: two entry
 * points to one mutation means two confirm flows and two binding models, and an
 * edge row cannot render the after-state (version, endpoint count, Replace,
 * View spec) so the operator would be bounced here anyway.
 *
 * NO URL FETCH, deliberately and permanently. A "fetch this URL for me" button
 * in a localhost UI sits INSIDE the customer's network — an SSRF pivot onto
 * internal admin and metadata endpoints — and this product's Settings panel
 * promises in so many words that nothing leaves until you Connect. If it ever
 * returns it returns as a control-plane-side fetch on a Connected collector,
 * never a collector-side fetch of a user-typed URL.
 *
 * Parse-before-commit is the shape, not a nicety: an unparseable document fails
 * at the confirm step and is never persisted, because the drift processor
 * parses whatever is in the store and a bad row would cost that host detection
 * with no symptom left for the operator to see.
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue';
import { ApiError, apiPost } from './api';
import {
  BIND_ANYWAY,
  CONTRACT_TOO_LARGE,
  UPLOAD_FORMATS,
  UPLOAD_NO_URL_FETCH,
  UPLOAD_PROMPT,
  UPLOAD_STAYS_LOCAL,
  UPLOAD_TAKES_EFFECT,
  bindingChecks,
  bindingTiming,
  contractFileTooLarge,
  endpointCount,
  hasBindingWarning,
  noTrafficYet
} from './contracts';

const props = defineProps<{
  /** The provider this contract binds to. Empty means the operator must name
   *  it — the pre-traffic case, which is the whole state of a fresh install. */
  host: string;
}>();

const emit = defineEmits<{
  (e: 'uploaded', notice: string): void;
  (e: 'cancel'): void;
  /** True once this uploader holds work that closing it would throw away — a
   *  document read, or a confirm step on screen. App.vue asks before swapping
   *  one uploader for another, because there is only one at a time and the
   *  swap used to discard an in-progress confirm without a word. */
  (e: 'dirty', dirty: boolean): void;
}>();

interface ContractPreview {
  peer_host: string;
  integration: string;
  title?: string;
  version?: string;
  endpoints: number;
  docs_url?: string;
  servers: string[];
  servers_match: boolean;
  replaces?: string;
  has_traffic: boolean;
}

const typedHost = ref(props.host);
const doc = ref('');
const filename = ref('');
const preview = ref<ContractPreview | null>(null);
const error = ref('');
const busy = ref(false);
const dragging = ref(false);
/** Set when a file was chosen before the host was named. The file is KEPT and
 *  the host field asks for what it needs — the previous behaviour read the file
 *  and then did nothing at all, which is indistinguishable from a broken
 *  control. */
const awaitingHost = ref(false);
const hostField = ref<HTMLInputElement | null>(null);
/** The file input is `hidden`, which takes it OUT of the tab order — so the
 *  control the operator reaches is a real button that forwards its click here.
 *  A `<label>` wrapping it, which is what this used to be, is not focusable at
 *  all: the recorded tab order went host field -> Cancel -> page header and
 *  never once landed on "Choose a file". With paste-the-document and URL fetch
 *  both cut from v1, the picker is the ONLY way a contract enters the
 *  collector, so a keyboard-only operator could not complete the Aha at all. */
const fileInput = ref<HTMLInputElement | null>(null);
const chooseButton = ref<HTMLButtonElement | null>(null);
/** The host field has been edited since the preview that is on screen was
 *  parsed. The confirm button is held until the re-parse lands, so a binding is
 *  never committed against a preview describing a different host. */
const hostDirty = ref(false);

/** Arriving from a provider row the host is already known, so the confirm step
 *  shows one line and there is no field to fill. Zero-question binding for the
 *  common case is the whole ergonomic reason to route through this tab. */
const boundHost = computed(() => (props.host || typedHost.value).trim());
const needsHost = computed(() => !props.host);

/**
 * The host the SERVER will bind, echoed back by the preview — not the string in
 * the field.
 *
 * `normalizeHost` (contracts_upload.go) strips a pasted URL's scheme, path,
 * query and userinfo, lowercases, trims, and drops a scheme's default port. So
 * pasting `https://API.Acme.test:443/v1` binds `api.acme.test` while the confirm
 * step quoted the URL back — describing a binding that was never going to
 * happen. Re-deriving that in TypeScript would be a second normalizer to keep in
 * step; the preview already carries the server's own answer, and the
 * post-commit notice already reads it.
 */
const previewHost = computed(() => preview.value?.peer_host || '');

/** Work that closing this uploader would throw away. */
const dirty = computed(() => !!preview.value || !!doc.value);
watch(dirty, (d) => emit('dirty', d), { immediate: true });

/**
 * Focus lands INSIDE the uploader when it opens. It did not before: the panel
 * appeared mid-page and the caret stayed wherever it was, so reaching the new
 * controls meant tabbing forward through the whole card that opened it.
 *
 * Where it lands is what the operator has to answer first — the host when this
 * is the pre-traffic route and there is a field, the picker when the host came
 * from the row it opened on.
 */
onMounted(() => {
  if (needsHost.value) hostField.value?.focus();
  else chooseButton.value?.focus();
});

/** Both the button and Enter/Space on the drop zone route here. */
function openPicker() {
  fileInput.value?.click();
}

async function readFile(file: File | null | undefined) {
  if (!file) return;
  error.value = '';
  preview.value = null;
  // Size is settled here, before the file is read and before anything is sent.
  //
  // The relay does enforce the cap, but its 413 is not reliably deliverable:
  // `http.MaxBytesReader` half-closes and waits about half a second, so a
  // browser still streaming a large body sees a connection reset instead of the
  // response. `apiPost` then rejects with a network error rather than an
  // ApiError and `runPreview` falls back to "Couldn’t read that document." — a
  // parse verdict for a size problem, which is exactly the wrong diagnosis the
  // 413 was added to stop. Answering locally, in the server's own words, is the
  // only way the operator reads the truth every time.
  //
  // The file is NOT kept: there is nothing to resume, and a stale `doc` would
  // let a later host entry re-run the preview with the document just refused.
  if (contractFileTooLarge(file.size)) {
    doc.value = '';
    filename.value = '';
    awaitingHost.value = false;
    error.value = CONTRACT_TOO_LARGE;
    return;
  }
  filename.value = file.name;
  try {
    doc.value = await file.text();
  } catch {
    error.value = 'Couldn’t read that file.';
    return;
  }
  if (!boundHost.value) {
    // Keep the file. Ask for the missing half, and put the cursor where the
    // answer goes — never accept input and then show nothing.
    awaitingHost.value = true;
    await nextTick();
    hostField.value?.focus();
    return;
  }
  await runPreview();
}

/** The host was named after the file was chosen — resume where we stopped. */
async function onHostEntered() {
  if (!boundHost.value) return;
  awaitingHost.value = false;
  if (doc.value && !preview.value) await runPreview();
}

function onFilePicked(ev: Event) {
  const input = ev.target as HTMLInputElement;
  void readFile(input.files?.[0]);
  // Clear it so choosing the SAME file again still fires a change event —
  // otherwise retrying after a parse error looks like a dead control.
  input.value = '';
}

function onDrop(ev: DragEvent) {
  dragging.value = false;
  void readFile(ev.dataTransfer?.files?.[0]);
}

/** Parse and describe. Persists NOTHING — this is the confirm step. */
async function runPreview() {
  if (!doc.value || !boundHost.value) return;
  busy.value = true;
  error.value = '';
  try {
    preview.value = await apiPost<ContractPreview>('/api/contracts/preview', {
      peer_host: boundHost.value,
      document: doc.value,
      filename: filename.value
    });
  } catch (e) {
    preview.value = null;
    error.value = e instanceof ApiError ? e.message : 'Couldn’t read that document.';
  } finally {
    busy.value = false;
    // In `finally`, not after the await: a re-parse that FAILS must release the
    // confirm button too. Left on the success path only, a rejected preview
    // stranded it disabled with "Re-reading the document…" forever.
    hostDirty.value = false;
  }
}

async function confirm() {
  if (!preview.value) return;
  busy.value = true;
  error.value = '';
  try {
    const res = await apiPost<{ replaced: boolean; breaking_changes?: number }>('/api/contracts/upload', {
      peer_host: boundHost.value,
      document: doc.value,
      filename: filename.value
    });
    // Say what happens NEXT, not "success". The operator's real question is
    // whether the calls already on the Traffic tab are about to light up.
    let notice = preview.value.has_traffic ? UPLOAD_TAKES_EFFECT : noTrafficYet(preview.value.peer_host);
    if (res.replaced && res.breaking_changes) {
      const n = res.breaking_changes;
      notice += ` ${n} breaking change${n === 1 ? '' : 's'} against the version it replaced.`;
    }
    emit('uploaded', notice);
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'Couldn’t save the contract.';
  } finally {
    busy.value = false;
  }
}

/**
 * Everything knowable about whether this binding is the one that was meant,
 * shown together. A typo'd host trips all three at once and that pattern is
 * unmistakable; one whispered line was not.
 *
 * Read off the preview's echo — the host the SERVER resolved — so the verdict
 * describes the binding that will actually happen (see previewHost). Editing
 * the host re-parses on a debounce, and `hostDirty` holds the confirm button
 * until it lands, so the checks on screen are never about a different host than
 * the one the button would bind.
 */
const checks = computed(() =>
  preview.value ? bindingChecks(previewHost.value, preview.value.servers) : []
);
/** What happens next — informational, never a check: neither answer is a
 *  problem, and scoring them made the list cry wolf. */
const timing = computed(() =>
  preview.value ? bindingTiming(previewHost.value, preview.value.has_traffic) : ''
);
const warned = computed(() => hasBindingWarning(checks.value));

/** Re-parse when the host changes at the confirm step: the server-side preview
 *  carries the binding, so a stale one would confirm the wrong thing. */
let hostDebounce: ReturnType<typeof setTimeout> | undefined;
function onHostEdited() {
  if (!preview.value) return;
  // Held from the first keystroke, not from the re-parse: between the two the
  // checks below describe a host the field no longer names, and confirming
  // there would bind against a preview of something else.
  hostDirty.value = true;
  clearTimeout(hostDebounce);
  hostDebounce = setTimeout(() => void runPreview(), 400);
}
</script>

<template>
  <div class="uploader">
    <!-- Step 1 — choose a document. -->
    <div v-if="!preview">
      <label v-if="needsHost" class="uploader-host" :class="{ awaiting: awaitingHost }">
        <span>Provider host</span>
        <input
          ref="hostField"
          v-model="typedHost"
          type="text"
          placeholder="api.acme.test"
          spellcheck="false"
          autocapitalize="off"
          autocorrect="off"
          @change="onHostEntered"
          @keydown.enter.prevent="onHostEntered"
        />
        <small v-if="awaitingHost" class="host-awaiting">
          {{ filename ? `“${filename}” is ready — which provider is it for?` : 'Which provider is this for?' }}
        </small>
      </label>

      <!-- The zone itself is reachable and operable: tabindex puts it in the
           order, Enter/Space opens the picker. Deliberately NOT role="button" —
           it contains a real button, and nesting one widget inside another
           announces badly; a focusable region whose prompt is read out, with the
           explicit control one Tab further on, is the honest shape. -->
      <div
        class="dropzone"
        :class="{ dragging }"
        tabindex="0"
        :aria-label="UPLOAD_PROMPT"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
        @keydown.enter.prevent="openPicker"
        @keydown.space.prevent="openPicker"
      >
        <p class="dz-prompt">{{ UPLOAD_PROMPT }}</p>
        <p class="dz-formats">{{ UPLOAD_FORMATS }}</p>
        <!-- A real button, because `hidden` takes the input out of the tab
             order and the <label> that used to wrap it was never focusable —
             so this control could not be reached by keyboard at all. -->
        <button ref="chooseButton" type="button" class="btn small" @click="openPicker">Choose a file</button>
        <input
          ref="fileInput"
          type="file"
          accept=".json,.yaml,.yml,application/json,text/yaml"
          hidden
          @change="onFilePicked"
        />
      </div>

      <p class="uploader-privacy">{{ UPLOAD_STAYS_LOCAL }}</p>
      <p class="uploader-note">{{ UPLOAD_NO_URL_FETCH }}</p>
    </div>

    <!-- Step 2 — confirm what it will bind to. -->
    <div v-else class="uploader-confirm">
      <dl class="confirm-facts">
        <div><dt>Contract</dt><dd>{{ preview.title || filename || 'OpenAPI document' }}</dd></div>
        <div v-if="preview.version"><dt>Version</dt><dd>v{{ preview.version }}</dd></div>
        <div><dt>Endpoints</dt><dd>{{ endpointCount(preview.endpoints) }}</dd></div>
        <div v-if="preview.replaces"><dt>Replaces</dt><dd>v{{ preview.replaces }}</dd></div>
      </dl>

      <!-- The host stays a FIELD here, not a fact. It was static text, so the
           only way to correct a typo was "Choose a different file" — which is
           the wrong thing to want when the file is fine and the host is wrong. -->
      <label class="uploader-host confirm-host">
        <span>Binds to</span>
        <input
          v-model="typedHost"
          type="text"
          placeholder="api.acme.test"
          spellcheck="false"
          autocapitalize="off"
          autocorrect="off"
          :disabled="!!props.host"
          @input="onHostEdited"
        />
        <small v-if="props.host" class="host-locked">From the provider row you came from.</small>
        <small v-else class="host-hint">One document, one host. Re-upload the same file for a sibling like <code>api-eu.acme.test</code>.</small>
      </label>

      <!-- Everything knowable about the binding, together. A typo'd host trips
           every line at once, which is the pattern worth seeing; each one is a
           reason to look again, never a refusal — gateway hosts legitimately
           mismatch `servers:`, and a fresh install legitimately has no traffic. -->
      <ul v-if="checks.length" class="binding-checks" :class="{ warned }">
        <li v-for="c in checks" :key="c.text" :class="c.level">
          <span class="chk" aria-hidden="true">{{ c.level === 'ok' ? '✓' : '!' }}</span>
          <span>{{ c.text }}</span>
        </li>
      </ul>
      <!-- Informational, deliberately OUTSIDE the checks: neither answer is a
           problem to weigh, and scoring them made the list cry wolf. -->
      <p v-if="timing" class="confirm-timing">{{ timing }}</p>

      <p class="uploader-privacy">{{ UPLOAD_STAYS_LOCAL }}</p>

      <div class="uploader-actions">
        <button
          type="button"
          class="btn"
          :class="{ warn: warned }"
          :disabled="busy || hostDirty || !boundHost"
          :title="hostDirty ? 'Re-reading the document against the new host…' : ''"
          @click="confirm"
        >
          {{ warned ? BIND_ANYWAY : 'Add contract' }}
        </button>
        <button type="button" class="btn ghost" :disabled="busy" @click="preview = null">Choose a different file</button>
        <button type="button" class="btn ghost" :disabled="busy" @click="emit('cancel')">Cancel</button>
      </div>
    </div>

    <p v-if="error" class="uploader-error">{{ error }}</p>
    <button v-if="!preview" type="button" class="btn ghost small" @click="emit('cancel')">Cancel</button>
  </div>
</template>
