// Pure helpers for the thread-link flow (v0.1a): labels, paste text, the
// prefilled flag message. No DOM, no fetch — unit-tested with vitest.

export type ThreadTurn = 'waiting_on_provider' | 'provider_replied' | 'fix_reported' | 'replied_while_closed';
export type ThreadState = 'open' | 'closed';
export type LinkStatus = 'active' | 'replaced' | 'expired';

/** One row of the control plane's thread list (CONTRACTS-CP §5.5a) — the SAME
 *  object `GET /api/v1/threads/{id}/summary` returns. It is thread STATE only:
 *  no finding id, no thread_url, and never a token. */
export interface ThreadSummary {
  id: string;
  thread_public_id: string;
  state: ThreadState;
  closed_at?: string | null;
  reopened_at?: string | null;
  turn: ThreadTurn;
  consumer_display_name?: string;
  provider_display_name?: string;
  endpoint?: string;
  evidence_count?: number;
  opened_count?: number;
  knock_count?: number;
  message_count?: number;
  last_reply_at?: string | null;
  fixed_claim?: { display_name: string; at: string } | null;
  link?: { status: LinkStatus; expires_at?: string } | null;
  archived?: boolean;
  created_at?: string;
  /** Last activity — what the §5.5a order sorts on (a reply, a close/reopen, a
   *  link replace). The archive sweep deliberately does not move it. */
  updated_at?: string;
}

/** One `GET /api/threads` row: the CP summary above, with the local fields the
 *  control plane cannot carry joined on by the collector — `finding_id` (the
 *  finding chip), `integration`, and `thread_url`.
 *
 *  `thread_url` is the collector's own stored copy of the thread link, EMPTY
 *  when it holds none for a thread the CP listed (a wiped local store) — the
 *  token lives only in a URL fragment and never comes back from the CP. The
 *  read-only tab no longer renders it: copying and replacing the link happen
 *  on the thread page (View thread), and the relay keeps persisting the link
 *  the flag POST returned so nothing is lost. */
export interface ThreadRow {
  thread_id: string;
  thread_public_id: string;
  finding_id: string;
  endpoint: string;
  provider: string;
  integration?: string;
  thread_url: string;
  created_at: string;
  updated_at?: string;
  summary: ThreadSummary | null;
}

/** The collector's own `GET /api/threads` envelope (its internal shape, not a
 *  published contract). `total` / `has_more` are the control plane's: §5.5a has
 *  no cursor, so a collector with more threads than the hard cap gets a short
 *  list, and the tab has to say so instead of quietly dropping rows. */
export interface ThreadListResponse {
  threads: ThreadRow[];
  count: number;
  total: number;
  limit: number;
  has_more: boolean;
}

/** The Threads tab is READ-ONLY (slice 2, D1): thread operations live on the
 *  thread page, and View thread is the only row action. This muted line under
 *  the tab header says so — always visible, exact deck copy. */
export const THREADS_READ_ONLY_NOTE = 'Close, reopen and link changes happen on the thread page — View thread opens it.';

/** The workspace link-out (v1 phase 3). This tab lists the threads THIS collector
 *  created; the CP workspace lists every thread the person is part of, including
 *  ones they answered as a respondent at someone else's collector — which is a
 *  different set, and the reason the line is worth having at all.
 *
 *  It is one muted line under an existing list, shown only when the collector
 *  already offers a `dashboard_url` (Connected, with an address a browser can
 *  actually open). The brief's rule for this slice is NO NEW NAG SURFACES: no
 *  banner, no dismissable card, nothing that appears before there is anything on
 *  the other end. */
export const WORKSPACE_LINK_OUT = 'See all your threads — create your workspace.';

/** Shown when the control plane has more threads than one page can carry: say
 *  what is on screen and what is not. There is no cursor to page with. The
 *  participant inbox (`peek-web/assets/inbox.js`) says this same sentence — one
 *  idea, one phrasing, on both surfaces. */
export function truncationNote(count: number, total: number): string {
  return 'Showing the ' + count + ' most recently active threads of ' + total + ". The rest aren't on this page.";
}

/** A finding's thread state is only knowable from the control plane. When the
 *  list has never loaded, say so — never fall back to "no thread", which offers
 *  Create thread for a thread that already exists. */
export const THREAD_STATE_UNKNOWN = "Thread status unknown — couldn't reach the control plane.";

export type ConnectStatus = 'disconnected' | 'pending' | 'connected';

/** Create thread is possible: connected, or a confirmed contact still exists
 *  while a newer one is pending (the relay gates on the same rule). */
export function canCreateThread(state: ConnectState | null | undefined): boolean {
  return !!state && (state.status === 'connected' || !!state.confirmed_contact_email);
}

/** Post-Connect nudge (v0.1b): Connected but no collector address on file —
 *  notification emails cannot deep-link back to this UI until it is set. */
export function needsCollectorAddress(state: ConnectState | null | undefined): boolean {
  return !!state && state.status === 'connected' && !state.local_ui_url;
}

/** `GET /api/threads` refuses with `412 not_connected` for exactly one state:
 *  the collector holds no collector key, so the relay has no keyed control-plane
 *  client (`extension/flanjui/threads.go` → `keyedClient`). That empty key is
 *  also the one thing `/api/connect` reports as `disconnected`
 *  (`extension/flanjui/connect.go` → `status()`), so the UI can know the answer
 *  without asking — and must, because a request that can only 4xx is logged by
 *  the BROWSER as a failed resource. On a 15s poll that is a permanent red
 *  console for a designed state the tab already renders correctly.
 *
 *  `null` / `undefined` is NOT "cannot": it means `/api/connect` has not
 *  answered yet. Treating unknown as disconnected would flash the not-connected
 *  notice at a collector that is in fact connected. */
export function cannotListThreads(state: ConnectState | null | undefined): boolean {
  return !!state && state.status === 'disconnected';
}

/** The line the relay's own `412 not_connected` carries on `GET /api/threads`
 *  (`extension/flanjui/messages.go` → `msgThreadsNotConnected`). Mirrored here
 *  because the UI no longer makes that request while disconnected: the Threads
 *  tab must show the same sentence — with the same inline Connect — that the
 *  refused response used to supply. Keep the two byte-identical. */
export const THREADS_NOT_CONNECTED_NOTICE =
  "Not connected — this collector can't list threads. Connect in Settings to see them.";

export interface ConnectState {
  status: ConnectStatus;
  /** The deployment's NAME (2026-09-14): unique in the workspace, changeable.
   *  The CP's copy — a rename made on the dashboard lands here through `me`. */
  collector_name?: string | null;
  consumer_display_name?: string;
  contact_email?: string;
  contact_display_name?: string;
  collector_public_id?: string;
  registered_at?: string;
  confirmed_at?: string;
  /** The contact threads are created with right now — stays set (the previous
   *  confirmed one) while a newer contact is pending; null until the first
   *  confirmation. Create thread is available whenever this is set. */
  confirmed_contact_email?: string | null;
  local_ui_url?: string;
  /** The CP dashboard link — the ONE door out of the local UI. Present only
   *  while Connected AND the collector has an address a browser can open
   *  (`cp_public_url`, or a public-looking `cp_base_url`); absent otherwise,
   *  and absence keeps the Connected pill a Settings button. A collector on a
   *  split network (docker DNS, a k8s Service) used to send its in-network
   *  base here, which was a dead link off-host. */
  dashboard_url?: string;
  cp_configured?: boolean;
  /** Whether this collector registers its external edges to the control plane
   *  (`edge_sync`, CONTRACTS §8 — default true). Drives the Connect panel's
   *  disclosure line, which must state what actually leaves: with the switch
   *  off the on-copy would be a lie. Absent on a collector predating v1 phase 2
   *  — the panel then renders no disclosure rather than guessing. */
  edge_sync?: boolean;
  error?: string;
  /** What actually happened to the confirmation mail on THIS request (CONTRACTS-CP §5.1):
   *  `sent` | `failed` | `cooldown`. Present only when a send was attempted — absent on a poll,
   *  on an already-confirmed contact, and from a control plane predating the field. Absent is
   *  "no send was attempted here", NEVER "it went out": a 2xx is the registration's verdict, not
   *  the mail's, and reading it as delivery is the defect this field closes. */
  confirmation_mail?: 'sent' | 'failed' | 'cooldown';
  /** Seconds left on the 1-per-10-minutes floor. Rides only on `cooldown`. */
  confirmation_mail_retry_after_s?: number;
}

/** Status column / chip label from the derived `turn` + state (copy deck). */
export function turnLabel(summary: ThreadSummary | null | undefined, provider: string): string {
  const p = provider || summary?.provider_display_name || 'the provider';
  if (!summary) return 'Waiting on ' + p;
  if (summary.state === 'closed') {
    return summary.turn === 'replied_while_closed' ? 'Closed · new reply' : 'Closed';
  }
  switch (summary.turn) {
    case 'provider_replied':
      return p + ' replied';
    case 'fix_reported':
      return 'Fix reported by ' + (summary.fixed_claim?.display_name || p);
    case 'replied_while_closed':
      return 'Closed · new reply';
    default:
      return 'Waiting on ' + p;
  }
}

/** Link-strip label (copy deck): `Active · expires <D>` · `Replaced` ·
 *  `Expired · N tried to open`. Knocks on a LIVE link are NOT part of the
 *  label — they get their own muted knock note (see knockNote). */
export function linkLabel(summary: ThreadSummary | null | undefined, fmtDate: (iso: string) => string = shortDate): string {
  const link = summary?.link;
  if (!link) return '—';
  const knocks = summary?.knock_count || 0;
  switch (link.status) {
    case 'active':
      return link.expires_at ? 'Active · expires ' + fmtDate(link.expires_at) : 'Active';
    case 'replaced':
      return 'Replaced';
    case 'expired':
      return knocks > 0 ? 'Expired · ' + knocks + ' tried to open' : 'Expired';
    default:
      return String(link.status);
  }
}

/** The muted knock note under an active link (shown only when knock_count > 0).
 *  Since the tab went read-only (D1) the copy/replace controls live on the
 *  thread page, so the note points there instead of naming removed buttons
 *  (UX-gate amendment, 2026-08-29). Muted, never amber — the counter is
 *  lifetime and never resets on Replace, so an amber knock would be a
 *  permanent alarm. */
export function knockNote(n: number): string {
  return n + ' tried an old link — open the thread page (View thread) to copy or replace the link.';
}

/** Amber window before expiry: a link expiring within 72h needs review now. */
export const LINK_EXPIRY_ATTENTION_MS = 72 * 60 * 60 * 1000;

/** Amber policy for the link strip: states that need review NOW — Expired,
 *  Replaced, or an active link expiring within 72h. Knocks alone never amber
 *  (lifetime counter — see knockNote). */
export function linkNeedsAttention(summary: ThreadSummary | null | undefined, now: number = Date.now()): boolean {
  const link = summary?.link;
  if (!link) return false;
  if (link.status !== 'active') return true;
  if (link.expires_at) {
    const t = new Date(link.expires_at).getTime();
    if (!isNaN(t) && t - now < LINK_EXPIRY_ATTENTION_MS) return true;
  }
  return false;
}

/** Finding-row chip: `In thread · <turn label> · opened ×N` (opened = respondent opens). */
export function chipLabel(row: ThreadRow): string {
  const opened = row.summary?.opened_count ?? 0;
  return 'In thread · ' + turnLabel(row.summary, row.provider) + ' · opened ×' + opened;
}

/**
 * The fixed paste text for "Copy link + message": request ID first, never the
 * user's free text (that lives in the thread).
 */
export function pasteText(opts: { endpoint: string; since: string; requestId?: string | null; link: string }): string {
  const lead = opts.requestId
    ? 'Request ID ' + opts.requestId + ' — seeing drift on ' + opts.endpoint + ' since ' + opts.since + ', check your logs.'
    : 'Seeing drift on ' + opts.endpoint + ' since ' + opts.since + ' — check your logs.';
  return lead + ' Details and reply here: ' + opts.link;
}

/**
 * "What leaves this collector" for a finding that has NO source call — since
 * v1p4 that is a flaggable state on every kind, not just `definition_change`
 * (the collector relay stopped answering 400 finding_has_no_call). The standing
 * HTTP lead names "this redacted request/response", which is not there.
 */
export const CALL_LESS_DISCLOSURE_LEAD = 'This finding, the endpoint, your message, and';
export const CALL_LESS_DISCLOSURE_TAIL =
  'No call is attached — this was found by comparing two versions of the contract, not by a call.';

// ─── Question-only threads (v1 phase 4) ───────────────────────────────────
// A thread started from an EDGE row carries no call and no finding. Every
// string below exists because the evidence-bearing equivalent would be a false
// claim on a thread that holds no evidence.

/** Under the sheet title, where an evidence line sits on a flag. */
export const QUESTION_LEAD =
  'No evidence is attached — this thread carries your message and the domain, and nothing else.';

/** The message field's label. On a flag it reads "Message (optional)"; here the
 *  message IS the thread, so it is required and says so. */
export const QUESTION_MESSAGE_LABEL = 'Your question';

/** Shown while the textarea is empty: Create thread is inert until it is not. */
export const QUESTION_MESSAGE_REQUIRED = 'Write your question first — there is no evidence to send in its place.';

/** "What leaves this collector", question variant: the lead before the
 *  `<C> · <E>` names, and the tail after them. */
export function questionDisclosureLead(domain: string): string {
  return `Your message, the domain ${domain}, and`;
}
export const QUESTION_DISCLOSURE_TAIL = 'No calls, no findings and no local data leave this collector.';

/** The success-state warning. The flag variant says "read the redacted
 *  evidence", which is not true of a thread that carries none. */
export function questionShareWarning(provider: string): string {
  return `Anyone with this link can read your message and reply. Paste it where you already talk to ${provider}'s team. It lasts 30 days and extends with each reply.`;
}

/** "Copy link + message" for a question: no request id, no endpoint, no drift
 *  claim — the domain and the link, which is all there is. */
export function questionPasteText(opts: { domain: string; link: string }): string {
  return `A question about ${opts.domain} — reply here: ${opts.link}`;
}

/** Prefilled, editable, optional message for the Flag sheet. */
export function defaultFlagMessage(f: {
  endpoint: string;
  field_path?: string | null;
  location?: string | null;
  expected: string;
  actual: string;
  first_seen?: string;
  detected_at?: string;
}, requestId: string | null | undefined, fmtDate: (iso: string) => string = shortDate,
   specSource?: string): string {
  const field = fieldName(f.field_path || f.location || '');
  const since = fmtDate(f.first_seen || f.detected_at || '');
  const what = field ? 'Seeing ' + field + ' come back as ' + f.actual : 'Seeing ' + f.actual;
  let msg = what + ' on ' + f.endpoint + (since ? ' since ' + since : '') + ' — spec says ' + f.expected + '.';
  // WHICH spec, when the answer is checkable. A fetched contract can name the
  // provider's own published URL and the moment it was read, so the person
  // reading this thread can go and look — which is the difference between a
  // claim and evidence, and the whole reason ruling R5's fetch exists.
  //
  // An uploaded contract says nothing here. "Spec says X" already implies a
  // spec; adding "from a file we have" would be words without a fact in them,
  // and this message is read by a stranger who did not ask for our filing
  // arrangements. Built by contracts.ts → fetchedSourceForThread, which is the
  // ONE place this sentence is phrased.
  if (specSource) msg += ' ' + specSource;
  if (requestId) msg += ' Request ID ' + requestId + ' is in the thread.';
  msg += ' Can you confirm on your side?';
  return msg;
}

/** `$.quantity` / `/amount` / `response.body.amount` → `quantity` / `amount`. */
export function fieldName(path: string): string {
  const cleaned = path.replace(/^\$\.?/, '').replace(/^\/+/, '');
  const parts = cleaned.split(/[./]/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : '';
}

/** Evidence line: `<EP> — contract drift at <field>: expected X, got Y`. */
export function evidenceLine(f: { endpoint: string; field_path?: string | null; location?: string | null; expected: string; actual: string; kind?: string }): string {
  const at = f.field_path || f.location;
  const drift = f.kind === 'version-diff' ? 'breaking spec change' : 'contract drift';
  return f.endpoint + ' — ' + drift + (at ? ' at ' + at : '') + ': expected ' + f.expected + ', got ' + f.actual;
}

/** How many request IDs (correlation keys) the flag will share. */
export function correlationCount(c?: { request_id?: string | null; idempotency_key?: string | null; trace_id?: string | null } | null): number {
  if (!c) return 0;
  return [c.request_id, c.idempotency_key, c.trace_id].filter(Boolean).length;
}

export function requestIdsLine(n: number): string {
  if (n === 0) return 'No request IDs were captured on this call.';
  if (n === 1) return '1 request ID will be shared so their team can check their own logs.';
  return n + ' request IDs will be shared so their team can check their own logs.';
}

export function shortDate(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

/** "3m ago" / "2h ago" / "5d ago" / "—". */
export function timeAgo(iso?: string | null, now: number = Date.now()): string {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (isNaN(t)) return '—';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return m + 'm ago';
  const h = Math.round(m / 60);
  if (h < 48) return h + 'h ago';
  return Math.round(h / 24) + 'd ago';
}

/** `#threads/<thread_id>` deep link → the id, else null. A malformed
 *  %-sequence decodes to null instead of throwing — applyHash runs in
 *  onMounted, and an uncaught URIError there kills the whole dashboard. */
export function threadIdFromHash(hash: string): string | null {
  const m = /^#threads\/([^/?#]+)/.exec(hash || '');
  if (!m) return null;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return null;
  }
}

/** `#contracts/<finding_id>` deep link → the finding id, else null. The control
 *  plane's findings index links here (`<local_ui_url>/#contracts/<finding_id>`)
 *  to open the Contracts tab on that finding's row — the mirror of
 *  threadIdFromHash for the Threads tab. A malformed %-sequence decodes to
 *  null instead of throwing (same rule as threadIdFromHash). */
export function findingIdFromHash(hash: string): string | null {
  const m = /^#contracts\/([^/?#]+)/.exec(hash || '');
  if (!m) return null;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return null;
  }
}

// ─── Who can open the thread (thread-domain-gate; three modes 2026-09-15) ──
// The sheet's "Who can open it" choice. Three modes, ALWAYS in this order:
// specific people (exact addresses), anyone at a domain (a reader confirms an
// address there), or — as an explicit choice — anyone who holds the link. On
// the wire it is always BOTH keys: the chosen list and null, or both null for
// anyone. The relay refuses a create that carries neither key, and this sheet
// refuses an unusable list before anything is posted. Every string here is
// final copy (open-to-v2 §6).

export type OpenToMode = 'emails' | 'domains' | 'anyone';
/** What a sheet is sharing: a flag's redacted evidence, or a question's message. */
export type OpenToWhat = 'evidence' | 'message';

export const OPEN_TO_LEGEND = 'Who can open it';
/** The three modes, in the order every surface shows them. */
export const OPEN_TO_MODES: ReadonlyArray<{ value: OpenToMode; label: string }> = [
  { value: 'emails', label: 'Only specific people' },
  { value: 'domains', label: 'Anyone at a domain' },
  { value: 'anyone', label: 'Anyone with the link — not recommended' }
];
/** The collector sheets (flag and edge) open on a domain. */
export const OPEN_TO_DEFAULT_MODE: OpenToMode = 'domains';
export function openToModeLabel(mode: OpenToMode): string {
  return OPEN_TO_MODES.find((m) => m.value === mode)?.label ?? '';
}

export const OPEN_TO_EMAILS_PLACEHOLDER = 'dana@their-company.com, sam@their-company.com';
export const OPEN_TO_EMAILS_HELP = 'Only these addresses can open the thread, after confirming once.';
export const OPEN_TO_DOMAINS_PLACEHOLDER = 'their-company.com';
/** The domains help when nothing was prefilled. */
export const OPEN_TO_DOMAINS_HELP = "Their email domain, like acme.com — not always the API's domain. Separate several with commas.";
/** The domains help when the directory prefilled a CLAIMED domain: says why it is trusted. */
export function openToPrefillNote(domain: string): string {
  return `Prefilled from the Flanj directory: ${domain} is verified. Change it if their email addresses end in something else.`;
}
export function openToAnyoneHelp(what: OpenToWhat): string {
  const reads = what === 'evidence' ? 'the redacted evidence' : 'your message';
  return `Anyone holding the link can read ${reads}. Paste it only where just their team can see it.`;
}

export const OPEN_TO_MAX = 20;
export const OPEN_TO_EMAILS_EMPTY = 'Add at least one email address.';
export const OPEN_TO_DOMAINS_EMPTY = 'Add at least one domain.';
export const OPEN_TO_TOO_MANY_PEOPLE = `A thread can be open to at most ${OPEN_TO_MAX} people.`;
export const OPEN_TO_TOO_MANY_DOMAINS = `A thread can be open to at most ${OPEN_TO_MAX} domains.`;

/** Entries longer than this are shortened in a guard: a pasted log must not become one unbroken line. */
export const OPEN_TO_NOTE_ENTRY_MAX = 48;
export function shortOpenToEntry(entry: string): string {
  return entry.length > OPEN_TO_NOTE_ENTRY_MAX ? `${entry.slice(0, OPEN_TO_NOTE_ENTRY_MAX)}…` : entry;
}
/** Domains field: an entry that is not a bare domain. */
export function openToInvalidNote(entry: string): string {
  return `"${shortOpenToEntry(entry)}" is not a domain — write each like acme.com, with no @, path or port.`;
}
/** Domains field: an address typed where a domain belongs — names the domain to write instead. */
export function openToAddressInDomainsNote(entry: string, domain: string): string {
  return `"${shortOpenToEntry(entry)}" is an address — write just the domain, ${shortOpenToEntry(domain)}.`;
}
/** Emails field: an entry that is not an email address. */
export function openToInvalidEmailNote(entry: string): string {
  return `"${shortOpenToEntry(entry)}" is not an email address — write each like dana@acme.com.`;
}
/** Emails field: a domain typed where an address belongs — points at the other mode. */
export function openToDomainInEmailsNote(entry: string, domain: string): string {
  return `"${shortOpenToEntry(entry)}" is a domain — choose Anyone at a domain, or write an address like dana@${shortOpenToEntry(domain)}.`;
}

/** A bare domain: labels of letters, digits and hyphens, at least one dot. */
const DOMAIN_RE = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;

/** Domain normalizing (§1): trim, lower-case, drop a leading `@` and a trailing `.`. */
function normalizeDomain(entry: string): string {
  return entry.trim().toLowerCase().replace(/^@+/, '').replace(/\.+$/, '');
}
function isBareDomain(d: string): boolean {
  return d.length <= 253 && DOMAIN_RE.test(d);
}
/** Address normalizing (§1): trim, lower-case. */
function normalizeEmail(entry: string): string {
  return entry.trim().toLowerCase();
}
/** Exactly one `@`, a local part with no spaces, a bare domain after it — the relay's own rule. */
function isPlainEmail(address: string): boolean {
  const at = address.indexOf('@');
  if (at <= 0 || at !== address.lastIndexOf('@') || address.length > 254) return false;
  const local = address.slice(0, at);
  if (local.length > 64 || /\s/.test(local)) return false;
  return isBareDomain(address.slice(at + 1));
}

/**
 * Split what the operator typed into entries: on `,` `;` and newlines; a piece
 * holding `<…>` yields what is inside (a pasted `Dana Lee <dana@acme.com>`);
 * any other piece splits on whitespace. Normalizing is the caller's.
 */
export function splitOpenTo(text: string): string[] {
  const entries: string[] = [];
  for (const raw of text.split(/[,;\r\n]+/)) {
    const piece = raw.trim();
    if (!piece) continue;
    const bracketed = Array.from(piece.matchAll(/<([^<>]*)>/g), (m) => m[1].trim());
    if (bracketed.length > 0) {
      const inside = bracketed.filter(Boolean);
      // `Dana <>` holds nothing usable: keep the piece whole so the guard can name it.
      entries.push(...(inside.length > 0 ? inside : [piece]));
      continue;
    }
    entries.push(...piece.split(/\s+/).filter(Boolean));
  }
  return entries;
}

export interface ParsedOpenTo {
  /** The normalized, de-duplicated list — postable only while `guard` is empty. */
  list: string[];
  /** The one sentence that says what is wrong with the field, or '' when it is usable. */
  guard: string;
}

/**
 * Parse one field of the choice. The first entry that does not belong names the
 * guard — and when it belongs in the OTHER field (a domain typed as a person, an
 * address typed as a domain) the guard says so. The relay and the control plane
 * normalize and refuse the same shapes; this is what lets the sheet refuse
 * before anything is posted.
 */
export function parseOpenTo(text: string, kind: 'emails' | 'domains'): ParsedOpenTo {
  const list: string[] = [];
  for (const entry of splitOpenTo(text)) {
    if (kind === 'emails') {
      const address = normalizeEmail(entry);
      if (!isPlainEmail(address)) {
        const asDomain = normalizeDomain(entry);
        return { list, guard: isBareDomain(asDomain) ? openToDomainInEmailsNote(entry, asDomain) : openToInvalidEmailNote(entry) };
      }
      if (!list.includes(address)) list.push(address);
    } else {
      const domain = normalizeDomain(entry);
      if (!isBareDomain(domain)) {
        const asAddress = normalizeEmail(entry);
        return {
          list,
          guard: isPlainEmail(asAddress) ? openToAddressInDomainsNote(entry, asAddress.slice(asAddress.indexOf('@') + 1)) : openToInvalidNote(entry)
        };
      }
      if (!list.includes(domain)) list.push(domain);
    }
  }
  if (list.length === 0) return { list, guard: kind === 'emails' ? OPEN_TO_EMAILS_EMPTY : OPEN_TO_DOMAINS_EMPTY };
  if (list.length > OPEN_TO_MAX) return { list, guard: kind === 'emails' ? OPEN_TO_TOO_MANY_PEOPLE : OPEN_TO_TOO_MANY_DOMAINS };
  return { list, guard: '' };
}

/** The request fields (§1): always BOTH keys — the chosen list and null, or both null for anyone. */
export function openToBody(
  mode: OpenToMode,
  emails: string[],
  domains: string[]
): { allowed_emails: string[] | null; allowed_domains: string[] | null } {
  return {
    allowed_emails: mode === 'emails' ? emails : null,
    allowed_domains: mode === 'domains' ? domains : null
  };
}

/** `acme.test` · `acme.test or globex.test` · `acme.test, globex.test or corp.test` */
export function domainsSentence(domains: string[]): string {
  if (domains.length <= 1) return domains[0] ?? '';
  return `${domains.slice(0, -1).join(', ')} or ${domains[domains.length - 1]}`;
}

/** `a` · `a and b` · `a, b and c` · more than three: `a, b, c and N others`. */
export function peopleSentence(emails: string[]): string {
  if (emails.length <= 1) return emails[0] ?? '';
  if (emails.length <= 3) return `${emails.slice(0, -1).join(', ')} and ${emails[emails.length - 1]}`;
  const rest = emails.length - 3;
  return `${emails.slice(0, 3).join(', ')} and ${rest} ${rest === 1 ? 'other' : 'others'}`;
}

function readsAndReplies(what: OpenToWhat): string {
  return what === 'evidence' ? 'read the redacted evidence and reply' : 'read your message and reply';
}

/**
 * The success-state warning for a DOMAINS thread. The open-link variants promise
 * "Anyone with this link can read…", which is exactly what the operator chose
 * against; this one says who can, and that nobody else can.
 */
export function gatedShareWarning(domains: string[], provider: string, what: OpenToWhat): string {
  // Every domain carries its own @ — "@a or b" read as one address at a plus a bare word (QA 2026-09-14).
  return `People with an ${domainsSentence(domains.map((d) => `@${d}`))} address can open this link — they confirm it once, then ${readsAndReplies(what)}. Nobody else can read it. Paste it where you already talk to ${provider}'s team. It lasts 30 days and extends with each reply.`;
}

/** The success-state warning for a SPECIFIC PEOPLE thread: names up to three of them. */
export function peopleShareWarning(emails: string[], provider: string, what: OpenToWhat): string {
  return `Only ${peopleSentence(emails)} can open this link — they confirm their address once, then ${readsAndReplies(what)}. Nobody else can read it. Paste it where you already talk to ${provider}'s team. It lasts 30 days and extends with each reply.`;
}
