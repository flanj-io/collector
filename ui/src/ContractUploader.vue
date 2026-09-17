<script setup lang="ts">
/**
 * The uploader — the ONE place a provider contract enters this collector.
 *
 * The Edges panel links here rather than offering a second uploader: two entry
 * points to one mutation means two confirm flows and two binding models, and an
 * edge row cannot render the after-state (version, endpoint count, Replace,
 * View spec) so the operator would be bounced here anyway.
 *
 * URL FETCH, added 2026-09-17 under ruling R5 (`architecture.md` §3.4), which
 * REVERSES the "no URL fetch, deliberately and permanently" position this
 * comment used to state. The old text is worth keeping in view, because the
 * reversal has to answer it rather than forget it:
 *
 *   (a) "An SSRF pivot onto internal admin and metadata endpoints." Real, and
 *       answered where it can be — the cloud metadata service is refused at
 *       DIAL time, on the RESOLVED address (`dialGuard`), so a hostname whose
 *       DNS points at it is refused too and every redirect hop is covered by
 *       the same check; the route is behind
 *       the same `X-Flanj-UI` + Origin guard as upload so no foreign page can
 *       drive it, and the PROBE can only ever reach a host this collector
 *       already calls. Private and internal addresses stay reachable on the
 *       typed-URL path, because an internal provider's spec lives on an
 *       internal host and refusing RFC1918 would refuse the self-hosted case
 *       this product exists for.
 *   (b) "Nothing leaves until you Connect." Still true, and this does not break
 *       it: a fetch sends no data anywhere. It is a GET for a document the
 *       provider publishes to the world, made by the collector, landing here.
 *       FETCH_STAYS_LOCAL says exactly that at the field.
 *   (c) "If it returns it returns as a control-plane-side fetch." Overruled on
 *       purpose. A CP-side fetch cannot reach an internal provider at all, and
 *       it would put the free single-player path behind Connect — which R5
 *       names as the roadmap hollowing out its own OSS wedge.
 *
 * What the fetch buys is not convenience, it is EVIDENCE: "your own published
 * spec at <url>, fetched <when>" is checkable by the provider reading a flagged
 * thread, and "somebody here had a file" never was.
 *
 * Suggest-and-approve, on both new paths: a fetch previews and waits, the probe
 * only ever OFFERS, and a human presses the button that binds. A WRONG CONTRACT
 * IS WORSE THAN NO CONTRACT — no contract renders `not checked`, honestly,
 * while a mismatched one renders DRIFTED, loudly, to a stranger, on their real
 * provider.
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
  FETCH_ACTION,
  FETCH_ONCE_ONLY,
  FETCH_PROMPT,
  FETCH_STAYS_LOCAL,
  FETCH_TAB_FILE,
  FETCH_TAB_URL,
  FETCH_BIND_FAILED_FALLBACK,
  FETCH_UNREACHABLE_FALLBACK,
  PROBE_ACTION,
  PROBE_FAILED_FALLBACK,
  PROBE_NOTHING_FOUND,
  PROBE_OFFER_ONLY,
  REFETCH_AFTER_HOST_EDIT,
  UPLOAD_FORMATS,
  UPLOAD_PROMPT,
  UPLOAD_STAYS_LOCAL,
  UPLOAD_TAKES_EFFECT,
  bindingChecks,
  bindingTiming,
  contractFileTooLarge,
  endpointCount,
  hasBindingWarning,
  noTrafficYet,
  probeCandidateLine
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

/* ── From a URL (ruling R5) ────────────────────────────────────────────── */

/** Which half of the panel is showing. The file path is the default: it is the
 *  one that works for a provider who publishes nothing. */
const source = ref<'file' | 'url'>('file');
const specUrl = ref('');
/** The staged fetch's handle. Held ONLY while its preview is on screen — the
 *  server binds this token and nothing else, so the document that gets written
 *  is byte-for-byte the one described above the button. */
const fetchToken = ref('');
/** Where the document actually came from, after redirects, and when. Shown on
 *  the confirm step because it is what the card, the finding and the thread
 *  will all claim afterwards — the operator approves the CLAIM, not just the
 *  document. */
const fetchedFrom = ref('');
const fetchedAt = ref('');
const requestedUrl = ref('');

interface ProbeCandidate {
  url: string;
  title?: string;
  version?: string;
  endpoints: number;
  servers: string[];
  servers_match: boolean;
}
const probeBusy = ref(false);
const probeRan = ref(false);
const probeTried = ref<string[]>([]);
const probeCandidates = ref<ProbeCandidate[]>([]);

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

/** Work that closing this uploader would throw away — including a staged fetch,
 *  which is a document already read from somebody's host. */
const dirty = computed(() => !!preview.value || !!doc.value || !!fetchToken.value);
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
  if (source.value === 'url') {
    // A URL-sourced preview cannot be re-derived from a document this component
    // holds — the bytes live server-side, staged against the host they were
    // previewed for. Editing the host therefore INVALIDATES the staged fetch
    // rather than re-parsing it: keeping the token would bind a document that
    // was described against a different host, which is precisely the wrong
    // binding this whole confirm step exists to prevent.
    fetchToken.value = '';
    preview.value = null;
    hostDirty.value = false;
    error.value = REFETCH_AFTER_HOST_EDIT;
    return;
  }
  hostDebounce = setTimeout(() => void runPreview(), 400);
}

/** Fetch and describe. Persists NOTHING — the server stages the bytes and hands
 *  back a handle; this is the confirm step, same as the file path's. */
async function runFetch(url?: string) {
  const target = (url ?? specUrl.value).trim();
  if (!target) return;
  busy.value = true;
  error.value = '';
  preview.value = null;
  fetchToken.value = '';
  try {
    const res = await apiPost<{
      token: string;
      source_url: string;
      requested_url?: string;
      fetched_at: string;
      preview: ContractPreview;
    }>('/api/contracts/fetch', { url: target, peer_host: boundHost.value || undefined });
    fetchToken.value = res.token;
    fetchedFrom.value = res.source_url;
    requestedUrl.value = res.requested_url || '';
    fetchedAt.value = res.fetched_at;
    preview.value = res.preview;
    // The server may have derived the host from the URL. Reflect its answer, so
    // the field and the binding never disagree.
    if (!props.host) typedHost.value = res.preview.peer_host;
  } catch (e) {
    // The server's sentence, always — every fetch refusal has one that names
    // what happened (a 404, a timeout, an HTML page, a document over the cap),
    // and replacing it with a generic line here would undo the whole point of
    // a fetch failure being a STATED state.
    error.value = e instanceof ApiError ? e.message : FETCH_UNREACHABLE_FALLBACK;
  } finally {
    busy.value = false;
    hostDirty.value = false;
  }
}

/** Ask the provider's host whether it publishes a spec at a conventional path.
 *  OFFERS what it finds. Binds nothing — every candidate below goes through the
 *  same fetch confirm step a typed URL does. */
async function runProbe() {
  if (!boundHost.value) return;
  probeBusy.value = true;
  error.value = '';
  try {
    const res = await apiPost<{ peer_host: string; tried: string[]; candidates: ProbeCandidate[] }>(
      '/api/contracts/probe',
      { peer_host: boundHost.value }
    );
    probeTried.value = res.tried;
    probeCandidates.value = res.candidates;
    probeRan.value = true;
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : PROBE_FAILED_FALLBACK;
  } finally {
    probeBusy.value = false;
  }
}

/** Taking an offer is exactly a typed fetch of that URL — one bind path, so a
 *  suggestion can never reach the store by a route a human did not walk. */
function takeCandidate(c: ProbeCandidate) {
  specUrl.value = c.url;
  void runFetch(c.url);
}

/** Bind what was fetched. Sends the TOKEN — never the document, and never the
 *  URL: the server writes the bytes it read, from the URL it recorded. */
async function confirmFetched() {
  if (!preview.value || !fetchToken.value) return;
  busy.value = true;
  error.value = '';
  try {
    const res = await apiPost<{ replaced: boolean; breaking_changes?: number }>(
      '/api/contracts/fetch',
      { token: fetchToken.value }
    );
    let notice = preview.value.has_traffic ? UPLOAD_TAKES_EFFECT : noTrafficYet(preview.value.peer_host);
    if (res.replaced && res.breaking_changes) {
      const n = res.breaking_changes;
      notice += ` ${n} breaking change${n === 1 ? '' : 's'} against the version it replaced.`;
    }
    emit('uploaded', notice);
  } catch (e) {
    // A token that expired or was already spent answers here. The sentence
    // says fetch again, which is the only thing to do — and nothing was bound.
    error.value = e instanceof ApiError ? e.message : FETCH_BIND_FAILED_FALLBACK;
    fetchToken.value = '';
    preview.value = null;
  } finally {
    busy.value = false;
  }
}

/** Which confirm button the step shows. A staged token means the bytes are the
 *  server's; no token means they are the file in `doc`. */
const confirmingFetch = computed(() => !!fetchToken.value);

/** Back to step one. A staged fetch is DROPPED here rather than kept around:
 *  the server expires it anyway, and a token surviving a "different document"
 *  press is how the previous document gets bound by the next confirm. */
function startOver() {
  preview.value = null;
  fetchToken.value = '';
  error.value = '';
}

function switchSource(to: 'file' | 'url') {
  if (source.value === to) return;
  source.value = to;
  // Half-finished work on the other path is dropped rather than carried: a
  // staged fetch and a read file are two different documents, and keeping both
  // alive is how the wrong one gets bound.
  error.value = '';
  preview.value = null;
  fetchToken.value = '';
  doc.value = '';
  filename.value = '';
  awaitingHost.value = false;
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

      <!-- Two sources, one binding model. The tabs are real buttons in the tab
           order with aria-pressed, not a styled radio group: each one switches
           a panel, which is what a button does. The FILE half is first and is
           the default — it is the one that works for a provider who publishes
           nothing, which is most of them. -->
      <div class="source-tabs" role="group" aria-label="Where the contract comes from">
        <button
          type="button"
          class="src-tab"
          :class="{ on: source === 'file' }"
          :aria-pressed="source === 'file'"
          @click="switchSource('file')"
        >{{ FETCH_TAB_FILE }}</button>
        <button
          type="button"
          class="src-tab"
          :class="{ on: source === 'url' }"
          :aria-pressed="source === 'url'"
          @click="switchSource('url')"
        >{{ FETCH_TAB_URL }}</button>
      </div>

      <!-- From a URL. The collector makes the request; the document lands here.
           Nothing re-reads the URL afterwards, which FETCH_ONCE_ONLY says
           plainly — an operator's reasonable assumption about a URL is that it
           is a subscription, and it is not one. -->
      <div v-if="source === 'url'" class="fetch-panel">
        <label class="uploader-host">
          <span>Spec URL</span>
          <input
            ref="urlField"
            v-model="specUrl"
            type="url"
            :placeholder="'https://api.acme.test/openapi.json'"
            spellcheck="false"
            autocapitalize="off"
            autocorrect="off"
            :disabled="busy"
            @keydown.enter.prevent="runFetch()"
          />
        </label>
        <p class="dz-prompt">{{ FETCH_PROMPT }}</p>
        <div class="uploader-actions">
          <button type="button" class="btn" :disabled="busy || !specUrl.trim()" @click="runFetch()">
            {{ busy ? 'Fetching…' : FETCH_ACTION }}
          </button>
          <!-- The probe. Only offered once a host is known, because it can only
               ask a host this collector already calls — and it OFFERS, which is
               why the control says "look for" and not "find". -->
          <button
            v-if="boundHost"
            type="button"
            class="btn ghost"
            :disabled="probeBusy || busy"
            @click="runProbe"
          >{{ probeBusy ? 'Looking…' : PROBE_ACTION }}</button>
        </div>

        <!-- What the probe found, OFFERED. Every row needs a press to go any
             further, and that press runs the same fetch confirm a typed URL
             does — there is no path from a suggestion to a bound contract that
             a human did not walk. -->
        <div v-if="probeRan" class="probe-results">
          <template v-if="probeCandidates.length">
            <p class="probe-lead">{{ PROBE_OFFER_ONLY }}</p>
            <ul class="probe-list">
              <li v-for="c in probeCandidates" :key="c.url">
                <div class="probe-url mono">{{ c.url }}</div>
                <div class="probe-meta">{{ probeCandidateLine(c) }}</div>
                <button type="button" class="btn small" :disabled="busy" @click="takeCandidate(c)">
                  {{ FETCH_ACTION }}
                </button>
              </li>
            </ul>
          </template>
          <template v-else>
            <p class="probe-lead">{{ PROBE_NOTHING_FOUND }}</p>
            <!-- What was actually asked. "We looked" with no list is a claim
                 the operator cannot check, and this panel is about checkable
                 claims. -->
            <ul class="probe-tried mono">
              <li v-for="t in probeTried" :key="t">{{ t }}</li>
            </ul>
          </template>
        </div>

        <p class="uploader-privacy">{{ FETCH_STAYS_LOCAL }}</p>
        <p class="uploader-note">{{ FETCH_ONCE_ONLY }}</p>
      </div>

      <!-- The zone itself is reachable and operable: tabindex puts it in the
           order, Enter/Space opens the picker. Deliberately NOT role="button" —
           it contains a real button, and nesting one widget inside another
           announces badly; a focusable region whose prompt is read out, with the
           explicit control one Tab further on, is the honest shape. -->
      <div
        v-if="source === 'file'"
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

      <p v-if="source === 'file'" class="uploader-privacy">{{ UPLOAD_STAYS_LOCAL }}</p>
    </div>

    <!-- Step 2 — confirm what it will bind to. -->
    <div v-else class="uploader-confirm">
      <dl class="confirm-facts">
        <div><dt>Contract</dt><dd>{{ preview.title || filename || 'OpenAPI document' }}</dd></div>
        <div v-if="preview.version"><dt>Version</dt><dd>v{{ preview.version }}</dd></div>
        <div><dt>Endpoints</dt><dd>{{ endpointCount(preview.endpoints) }}</dd></div>
        <div v-if="preview.replaces"><dt>Replaces</dt><dd>v{{ preview.replaces }}</dd></div>
        <!-- WHERE it came from, as a fact on the confirm step, because it is
             what the card, the finding and the flagged thread will all claim
             afterwards. The operator is approving the CLAIM here, not only the
             document — so the URL they approve has to be the one that gets
             recorded, which is why this is the server's answer (after
             redirects) and not the string in the field. -->
        <div v-if="confirmingFetch"><dt>Fetched from</dt><dd class="mono break">{{ fetchedFrom }}</dd></div>
        <!-- A redirect that moved the document is shown, never swallowed: "I
             asked for A and got B" is exactly the fact an operator needs before
             binding B under a claim they will later stand behind. -->
        <div v-if="confirmingFetch && requestedUrl">
          <dt>Requested</dt><dd class="mono break">{{ requestedUrl }} <span class="redirect-note">— redirected</span></dd>
        </div>
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

      <p class="uploader-privacy">{{ confirmingFetch ? FETCH_STAYS_LOCAL : UPLOAD_STAYS_LOCAL }}</p>

      <div class="uploader-actions">
        <button
          type="button"
          class="btn"
          :class="{ warn: warned }"
          :disabled="busy || hostDirty || !boundHost"
          :title="hostDirty ? 'Re-reading the document against the new host…' : ''"
          @click="confirmingFetch ? confirmFetched() : confirm()"
        >
          {{ warned ? BIND_ANYWAY : 'Add contract' }}
        </button>
        <button type="button" class="btn ghost" :disabled="busy" @click="startOver">
          {{ confirmingFetch ? 'Fetch a different document' : 'Choose a different file' }}
        </button>
        <button type="button" class="btn ghost" :disabled="busy" @click="emit('cancel')">Cancel</button>
      </div>
    </div>

    <p v-if="error" class="uploader-error">{{ error }}</p>
    <button v-if="!preview" type="button" class="btn ghost small" @click="emit('cancel')">Cancel</button>
  </div>
</template>

<style scoped>
/* Blueprint: the uploader is a framed block on the sunk surface, rendered
   inline on whichever row opened it (one mutation, so only one can be open).
   Layout and component rules only — every colour, rule width and radius is a
   token (src/tokens.css). Its focus-ring declarations live here, not in
   App.vue (src/tokens.test.ts lists them). */
.uploader { border: var(--border-w) solid var(--rule); border-radius: var(--radius); padding: 12px 14px; margin-top: 8px; background: var(--surface-sunk); }
.uploader-host { display: flex; flex-direction: column; gap: 4px; margin-bottom: 10px; }
.uploader-host > span { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); }
.uploader-host input {
  padding: 7px 10px; border: var(--border-w) solid var(--rule); border-radius: var(--radius);
  background: var(--surface); color: var(--ink); font: inherit; font-size: 13px; max-width: 352px;
  transition: border-color var(--dur-fast) var(--ease);
}
.uploader-host input:focus { border-color: var(--ink); }
/* The drop zone keeps a dashed 2px frame: the dash is the affordance. */
.dropzone {
  border: var(--border-w) dashed var(--rule); border-radius: var(--radius); padding: 18px 12px;
  text-align: center; background: var(--surface); transition: border-color var(--dur-fast) var(--ease), background-color var(--dur-fast) var(--ease);
}
.dropzone.dragging { border-color: var(--ink); background: var(--surface-sunk); }
.dz-prompt { margin: 0 0 2px; font-size: 13.5px; }
.dz-formats { margin: 0 0 10px; font-size: 12px; color: var(--ink-soft); }
/* The privacy line sits AT the picker, where the document is chosen — the one
   moment the operator is deciding whether to hand over a vendor's document. */
.uploader-privacy { margin: 8px 0 0; font-size: 12px; color: var(--ink); }
.uploader-note { margin: 3px 0 0; font-size: 12px; color: var(--ink-soft); }
/* Errors (too large, unreadable, the relay's refusal): red ink behind a red rule. */
.uploader-error { margin: 8px 0 0; font-size: 12.5px; color: var(--sev-breaking-ink); padding-left: 10px; border-left: var(--border-w-stripe) solid var(--sev-breaking); }
.uploader-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.confirm-facts { display: grid; gap: 4px; margin: 0 0 8px; }
.confirm-facts > div { display: flex; gap: 8px; align-items: baseline; font-size: 13px; }
.confirm-facts dt { font: 500 10.5px/1.5 var(--f-mono); letter-spacing: 0.08em; text-transform: uppercase; color: var(--ink-soft); min-width: 96px; }
.confirm-facts dd { margin: 0; }
/* The binding checklist. A `servers:` mismatch used to render in body ink —
   "warn, never block" means warn VISIBLY, and that was a whisper. Warnings get
   the warning tier's wash and edge rule plus a `!` marker; the button still
   says go. The tier shares its hue with the accent, so the marker and the
   text carry it, never the colour alone. */
.binding-checks { list-style: none; margin: 0 0 8px; padding: 8px 10px; display: grid; gap: 4px; border-radius: var(--radius); background: var(--surface); border: var(--border-w-hair) solid var(--rule); }
.binding-checks.warned { border-color: var(--sev-warning-edge); border-left: var(--border-w-stripe) solid var(--sev-warning-edge); background: var(--sev-warning-wash); }
.binding-checks li { display: flex; gap: 8px; align-items: flex-start; font-size: 12.5px; line-height: 1.45; color: var(--ink-soft); }
.binding-checks li.warn { color: var(--ink); }
.binding-checks .chk { flex: none; width: 1em; text-align: center; font-family: var(--f-mono); font-weight: 700; color: var(--ink-soft); }
.binding-checks li.warn .chk { color: var(--sev-warning-ink); }
.confirm-host { margin: 2px 0 10px; }
.confirm-host input:disabled { opacity: 0.7; cursor: not-allowed; }
.host-hint, .host-locked { font-size: 11px; color: var(--ink-soft); }
.host-awaiting { font-size: 12px; color: var(--ink); }
.uploader-host.awaiting input { border-color: var(--accent); }
.confirm-timing { margin: 0 0 6px; font-size: 12px; color: var(--ink-soft); }
.host-hint code { font-size: 0.95em; }
/* Two sources, one panel. The tabs sit on the sunk surface; the selected one
   carries the ink rule, never colour alone. */
.source-tabs { display: flex; gap: 6px; margin-bottom: 10px; }
.src-tab {
  padding: 5px 10px; border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius);
  background: var(--surface); color: var(--ink-soft); font: inherit; font-size: 12.5px; cursor: pointer;
  transition: border-color var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}
.src-tab.on { border-color: var(--ink); color: var(--ink); }
.src-tab:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-offset); }
.fetch-panel { border: var(--border-w-hair) solid var(--rule); border-radius: var(--radius); padding: 12px; background: var(--surface); }
.fetch-panel .uploader-host { margin-bottom: 6px; }
.fetch-panel .uploader-host input { max-width: 100%; }
.fetch-panel .dz-prompt { font-size: 12px; color: var(--ink-soft); }
/* Probe results are OFFERS: framed as a list of things to look at, with the
   control on each row, never a single highlighted "best" result. */
.probe-results { margin-top: 10px; }
.probe-lead { margin: 0 0 6px; font-size: 12.5px; color: var(--ink); }
.probe-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.probe-list li {
  display: grid; gap: 2px; padding: 8px 10px; border: var(--border-w-hair) solid var(--rule);
  border-radius: var(--radius); background: var(--surface-sunk);
}
.probe-list li .btn { justify-self: start; margin-top: 4px; }
.probe-url { font-size: 12px; word-break: break-all; }
.probe-meta { font-size: 11.5px; color: var(--ink-soft); }
.probe-tried { list-style: none; margin: 6px 0 0; padding: 0; font-size: 11px; color: var(--ink-soft); display: grid; gap: 2px; }
.probe-tried li { word-break: break-all; }
.confirm-facts dd.break { word-break: break-all; }
.redirect-note { color: var(--ink-soft); }
/* `Bind anyway` keeps the warning tier's outline: it is the one control that
   proceeds past a warned checklist, and its label says so. */
.btn.warn { border-color: var(--sev-warning-ink); color: var(--sev-warning-ink); }
.uploader-host input:focus-visible, .dropzone:focus-visible { outline: var(--focus-ring); outline-offset: var(--focus-offset); }
</style>
