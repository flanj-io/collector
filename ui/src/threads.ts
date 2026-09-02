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
  cp_configured?: boolean;
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

/** Prefilled, editable, optional message for the Flag sheet. */
export function defaultFlagMessage(f: {
  endpoint: string;
  field_path?: string | null;
  location?: string | null;
  expected: string;
  actual: string;
  first_seen?: string;
  detected_at?: string;
}, requestId: string | null | undefined, fmtDate: (iso: string) => string = shortDate): string {
  const field = fieldName(f.field_path || f.location || '');
  const since = fmtDate(f.first_seen || f.detected_at || '');
  const what = field ? 'Seeing ' + field + ' come back as ' + f.actual : 'Seeing ' + f.actual;
  let msg = what + ' on ' + f.endpoint + (since ? ' since ' + since : '') + ' — spec says ' + f.expected + '.';
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
