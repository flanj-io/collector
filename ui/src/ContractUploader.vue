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
import { computed, ref } from 'vue';
import { ApiError, apiPost } from './api';
import {
  BIND_ANYWAY,
  UPLOAD_FORMATS,
  UPLOAD_NO_URL_FETCH,
  UPLOAD_PROMPT,
  UPLOAD_STAYS_LOCAL,
  UPLOAD_TAKES_EFFECT,
  endpointCount,
  noTrafficYet,
  serversLine
} from './contracts';

const props = defineProps<{
  /** The provider this contract binds to. Empty means the operator must name
   *  it — the pre-traffic case, which is the whole state of a fresh install. */
  host: string;
}>();

const emit = defineEmits<{
  (e: 'uploaded', notice: string): void;
  (e: 'cancel'): void;
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

/** Arriving from a provider row the host is already known, so the confirm step
 *  shows one line and there is no field to fill. Zero-question binding for the
 *  common case is the whole ergonomic reason to route through this tab. */
const boundHost = computed(() => (props.host || typedHost.value).trim());
const needsHost = computed(() => !props.host);

async function readFile(file: File | null | undefined) {
  if (!file) return;
  error.value = '';
  preview.value = null;
  filename.value = file.name;
  try {
    doc.value = await file.text();
  } catch {
    error.value = 'Couldn’t read that file.';
    return;
  }
  await runPreview();
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

const serversNote = computed(() =>
  preview.value ? serversLine(preview.value.servers, preview.value.peer_host) : ''
);
</script>

<template>
  <div class="uploader">
    <!-- Step 1 — choose a document. -->
    <div v-if="!preview">
      <label v-if="needsHost" class="uploader-host">
        <span>Provider host</span>
        <input v-model="typedHost" type="text" placeholder="api.acme.test" spellcheck="false" @change="runPreview" />
      </label>

      <div
        class="dropzone"
        :class="{ dragging }"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
      >
        <p class="dz-prompt">{{ UPLOAD_PROMPT }}</p>
        <p class="dz-formats">{{ UPLOAD_FORMATS }}</p>
        <label class="btn small">
          Choose a file
          <input type="file" accept=".json,.yaml,.yml,application/json,text/yaml" hidden @change="onFilePicked" />
        </label>
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
        <div><dt>Binds to</dt><dd class="mono">{{ preview.peer_host }}</dd></div>
        <div v-if="preview.replaces"><dt>Replaces</dt><dd>v{{ preview.replaces }}</dd></div>
      </dl>

      <!-- `servers:` CORROBORATES, never decides: proxy, gateway and staging
           hosts are legitimate and common, so a mismatch warns and the button
           still says go. -->
      <p v-if="serversNote" class="confirm-servers" :class="{ mismatch: !preview.servers_match }">{{ serversNote }}</p>
      <p v-if="!preview.has_traffic" class="confirm-note">{{ noTrafficYet(preview.peer_host) }}</p>
      <p class="uploader-privacy">{{ UPLOAD_STAYS_LOCAL }}</p>

      <div class="uploader-actions">
        <button type="button" class="btn" :disabled="busy" @click="confirm">
          {{ preview.servers_match || !preview.servers.length ? 'Add contract' : BIND_ANYWAY }}
        </button>
        <button type="button" class="btn ghost" :disabled="busy" @click="preview = null">Choose a different file</button>
        <button type="button" class="btn ghost" :disabled="busy" @click="emit('cancel')">Cancel</button>
      </div>
    </div>

    <p v-if="error" class="uploader-error">{{ error }}</p>
    <button v-if="!preview" type="button" class="btn ghost small" @click="emit('cancel')">Cancel</button>
  </div>
</template>
