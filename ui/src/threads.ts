// Pure helpers for the thread-link flow (v0.1a): labels, paste text, the
// prefilled flag message. No DOM, no fetch — unit-tested with vitest.

export type ThreadTurn = 'waiting_on_provider' | 'provider_replied' | 'fix_reported' | 'replied_while_closed';
export type ThreadState = 'open' | 'closed';
export type LinkStatus = 'active' | 'replaced' | 'expired';

export interface ThreadSummary {
  id: string;
  thread_public_id: string;
  state: ThreadState;
  closed_at?: string | null;
  reopened_at?: string | null;
  turn: ThreadTurn;
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
}

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
  error?: string;
}

export type ConnectStatus = 'disconnected' | 'pending' | 'connected';

/** Create thread is possible: connected, or a confirmed contact still exists
 *  while a newer one is pending (the relay gates on the same rule). */
export function canCreateThread(state: ConnectState | null | undefined): boolean {
  return !!state && (state.status === 'connected' || !!state.confirmed_contact_email);
}

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

/** Link column label (copy deck). */
export function linkLabel(summary: ThreadSummary | null | undefined, fmtDate: (iso: string) => string = shortDate): string {
  const link = summary?.link;
  if (!link) return '—';
  const knocks = summary?.knock_count || 0;
  switch (link.status) {
    case 'active': {
      // Knocks explain the amber state: the link is live, yet someone hit an old (replaced/expired) one.
      const active = link.expires_at ? 'Active · expires ' + fmtDate(link.expires_at) : 'Active';
      return knocks > 0 ? active + ' · ' + knocks + ' tried an old link' : active;
    }
    case 'replaced':
      return knocks > 0 ? 'Replaced · ' + knocks + ' tried to open' : 'Replaced';
    case 'expired':
      return knocks > 0 ? 'Expired · ' + knocks + ' tried to open' : 'Expired';
    default:
      return String(link.status);
  }
}

/** True when the row should offer Replace prominently (link not live, or someone knocked). */
export function linkNeedsAttention(summary: ThreadSummary | null | undefined): boolean {
  if (!summary?.link) return false;
  return summary.link.status !== 'active' || (summary.knock_count || 0) > 0;
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

/** `#threads/<thread_id>` deep link → the id, else null. */
export function threadIdFromHash(hash: string): string | null {
  const m = /^#threads\/([^/?#]+)/.exec(hash || '');
  return m ? decodeURIComponent(m[1]) : null;
}
