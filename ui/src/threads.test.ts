import { describe, expect, it } from 'vitest';
import {
  THREADS_NOT_CONNECTED_NOTICE,
  canCreateThread,
  cannotListThreads,
  chipLabel,
  correlationCount,
  defaultFlagMessage,
  evidenceLine,
  fieldName,
  findingIdFromHash,
  knockNote,
  linkLabel,
  linkNeedsAttention,
  needsCollectorAddress,
  pasteText,
  requestIdsLine,
  threadIdFromHash,
  timeAgo,
  THREADS_READ_ONLY_NOTE,
  truncationNote,
  turnLabel,
  type ThreadRow,
  type ThreadSummary
} from './threads';

// A §5.5a row exactly as the control plane sends it — the SAME object the
// per-thread summary returns, and the source of the Threads tab list since
// slice-inbox. It carries no finding id, no thread_url and never a token.
const base: ThreadSummary = {
  id: 't1',
  thread_public_id: 'p1',
  state: 'open',
  closed_at: null,
  reopened_at: null,
  turn: 'waiting_on_provider',
  consumer_display_name: 'Acme Consumer Ltd',
  provider_display_name: 'Acme Payments',
  endpoint: 'POST /v1/charges',
  evidence_count: 1,
  opened_count: 3,
  knock_count: 0,
  message_count: 0,
  last_reply_at: null,
  fixed_claim: null,
  link: { status: 'active', expires_at: '2026-09-22T00:00:00Z' },
  archived: false,
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T11:00:00Z'
};

/** A merged `GET /api/threads` row: the CP summary above with the collector's
 *  local join fields on top. */
const merged: ThreadRow = {
  thread_id: 't1',
  thread_public_id: 'p1',
  finding_id: 'fnd_1',
  endpoint: 'POST /v1/charges',
  provider: 'Acme Payments',
  integration: 'acme-payments',
  thread_url: 'https://cp/t/p1#k=tok',
  created_at: '2026-08-23T10:00:00Z',
  updated_at: '2026-08-23T11:00:00Z',
  summary: base
};

/** The same thread as the CP lists it when this collector holds NO local record
 *  for it (a wiped local store): every CP field is there, the local join fields
 *  are empty. */
const orphan: ThreadRow = { ...merged, finding_id: '', integration: undefined, thread_url: '' };

describe('turnLabel (copy deck status column)', () => {
  it('walks the derived turn', () => {
    expect(turnLabel(base, 'Acme Payments')).toBe('Waiting on Acme Payments');
    expect(turnLabel({ ...base, turn: 'provider_replied' }, 'Acme Payments')).toBe('Acme Payments replied');
    expect(turnLabel({ ...base, turn: 'fix_reported', fixed_claim: { display_name: 'Dana', at: 'x' } }, 'Acme Payments')).toBe(
      'Fix reported by Dana'
    );
    expect(turnLabel({ ...base, state: 'closed' }, 'Acme Payments')).toBe('Closed');
    expect(turnLabel({ ...base, state: 'closed', turn: 'replied_while_closed' }, 'Acme Payments')).toBe('Closed · new reply');
  });
  it('falls back to waiting when the summary is missing', () => {
    expect(turnLabel(null, 'Globex')).toBe('Waiting on Globex');
  });
});

describe('linkLabel + knock note + amber policy', () => {
  const d = (iso: string) => iso.slice(0, 10);
  it('active / replaced / expired — knocks on a live link leave the label (they get the knock note)', () => {
    expect(linkLabel(base, d)).toBe('Active · expires 2026-09-22');
    expect(linkLabel({ ...base, knock_count: 2 }, d)).toBe('Active · expires 2026-09-22');
    expect(linkLabel({ ...base, link: { status: 'active' }, knock_count: 1 }, d)).toBe('Active');
    expect(linkLabel({ ...base, link: { status: 'replaced' }, knock_count: 3 }, d)).toBe('Replaced');
    expect(linkLabel({ ...base, link: { status: 'expired' }, knock_count: 2 }, d)).toBe('Expired · 2 tried to open');
    expect(linkLabel({ ...base, link: { status: 'expired' } }, d)).toBe('Expired');
    expect(linkLabel(null, d)).toBe('—');
  });
  it('knock note points at the thread page — the tab is read-only, so it must not name removed controls (UX-gate 2026-08-29)', () => {
    expect(knockNote(2)).toBe('2 tried an old link — open the thread page (View thread) to copy or replace the link.');
    expect(knockNote(1)).toBe('1 tried an old link — open the thread page (View thread) to copy or replace the link.');
  });
  it('amber = review NOW: expired, replaced, expiring within 72h — knocks alone never', () => {
    const now = Date.parse('2026-08-25T00:00:00Z');
    expect(linkNeedsAttention(base, now)).toBe(false); // expires ~4 weeks out
    expect(linkNeedsAttention({ ...base, knock_count: 5 }, now)).toBe(false); // lifetime counter ≠ alarm
    expect(linkNeedsAttention({ ...base, link: { status: 'expired' } }, now)).toBe(true);
    expect(linkNeedsAttention({ ...base, link: { status: 'replaced' } }, now)).toBe(true);
    expect(linkNeedsAttention({ ...base, link: { status: 'active', expires_at: '2026-08-26T00:00:00Z' } }, now)).toBe(true); // tomorrow
    expect(linkNeedsAttention({ ...base, link: { status: 'active', expires_at: '2026-08-29T00:00:00Z' } }, now)).toBe(false); // 4 days out
    expect(linkNeedsAttention(null, now)).toBe(false);
  });
});

describe('chipLabel', () => {
  it('renders In thread · <turn> · opened ×N', () => {
    expect(chipLabel(merged)).toBe('In thread · Waiting on Acme Payments · opened ×3');
    expect(chipLabel({ ...merged, summary: null })).toBe('In thread · Waiting on Acme Payments · opened ×0');
  });
});

// The row model is a CP §5.5a summary merged with the collector's local
// pointer. Everything the READ-ONLY tab renders must survive a row whose local
// record is missing — the thread exists on the control plane, and every column
// comes from the CP row alone. (The canCopyLink family retired with slice 2:
// copying/replacing the link happens on the thread page, so the tab no longer
// touches thread_url at all.)
describe('merged row (CP summary + local pointer)', () => {
  it('renders every column from the CP row alone', () => {
    // chipLabel is deliberately NOT asserted here: the finding chip is keyed by
    // finding_id (App.vue threadsByFinding), and an orphan's is '', so no chip
    // can ever render for one.
    expect(turnLabel(orphan.summary, orphan.provider)).toBe('Waiting on Acme Payments');
    expect(linkLabel(orphan.summary, (iso) => iso.slice(0, 10))).toBe('Active · expires 2026-09-22');
  });
  it('falls back to the summary when the local provider name is missing', () => {
    expect(turnLabel(base, '')).toBe('Waiting on Acme Payments');
    expect(turnLabel({ ...base, turn: 'provider_replied' }, '')).toBe('Acme Payments replied');
  });
  // Archived rows are INCLUDED by §5.5a and flagged, never dropped — an
  // archived thread still renders its real state, not a special one.
  it('renders an archived row like any other', () => {
    expect(turnLabel({ ...base, archived: true, state: 'closed' }, 'Acme Payments')).toBe('Closed');
    expect(turnLabel({ ...base, archived: true }, 'Acme Payments')).toBe('Waiting on Acme Payments');
  });
});

// The tab is read-only since slice 2: thread operations live on the thread
// page, and the always-visible line under the header is the exact deck copy.
describe('the read-only header line', () => {
  it('is the deck sentence, verbatim', () => {
    expect(THREADS_READ_ONLY_NOTE).toBe('Close, reopen and link changes happen on the thread page — View thread opens it.');
  });
});

// §5.5a has no cursor: the list stops at the control plane's hard cap, and the
// tab has to say so rather than quietly losing rows (and, with them, the "In
// thread" chip on the findings those rows carry).
describe('truncationNote', () => {
  it('says what is shown and what is not', () => {
    expect(truncationNote(200, 250)).toBe("Showing the 200 most recently active threads of 250. The rest aren't on this page.");
  });
  // The participant inbox (control-plane peek-assets/inbox.js) renders the SAME sentence for the
  // same fact. Two phrasings of one idea is how the two surfaces drift apart.
  it('is the sentence the participant inbox says too', () => {
    expect(truncationNote(3, 9)).toBe("Showing the 3 most recently active threads of 9. The rest aren't on this page.");
  });
});

describe('pasteText (fixed template, request ID first, never free text)', () => {
  it('leads with the request id', () => {
    const t = pasteText({ endpoint: 'POST /v1/charges', since: 'Aug 23', requestId: 'req_abc', link: 'https://cp/t/p#k=tok' });
    expect(t).toBe(
      'Request ID req_abc — seeing drift on POST /v1/charges since Aug 23, check your logs. Details and reply here: https://cp/t/p#k=tok'
    );
    expect(t.indexOf('req_abc')).toBeLessThan(t.indexOf('POST /v1/charges'));
  });
  it('works without a request id', () => {
    expect(pasteText({ endpoint: 'POST /v1/charges', since: 'Aug 23', link: 'L' })).toBe(
      'Seeing drift on POST /v1/charges since Aug 23 — check your logs. Details and reply here: L'
    );
  });
});

describe('defaultFlagMessage + evidence', () => {
  const f = {
    endpoint: 'POST /v1/charges',
    field_path: '$.quantity',
    location: 'response.body.quantity',
    expected: 'integer',
    actual: 'string',
    first_seen: '2026-08-20T00:00:00Z',
    kind: 'live-vs-spec'
  };
  it('prefills the deck message', () => {
    const d = (iso: string) => (iso ? 'Aug 20' : '');
    expect(defaultFlagMessage(f, 'req_1', d)).toBe(
      'Seeing quantity come back as string on POST /v1/charges since Aug 20 — spec says integer. Request ID req_1 is in the thread. Can you confirm on your side?'
    );
    expect(defaultFlagMessage({ ...f, field_path: null, location: null, first_seen: '' }, null, d)).toBe(
      'Seeing string on POST /v1/charges — spec says integer. Can you confirm on your side?'
    );
  });
  it('names the field from several path shapes', () => {
    expect(fieldName('$.quantity')).toBe('quantity');
    expect(fieldName('/data/amount')).toBe('amount');
    expect(fieldName('response.body.items.0.sku')).toBe('sku');
    expect(fieldName('')).toBe('');
  });
  it('evidence line', () => {
    expect(evidenceLine(f)).toBe('POST /v1/charges — contract drift at $.quantity: expected integer, got string');
  });
  it('request id counts', () => {
    expect(correlationCount({ request_id: 'a', idempotency_key: 'b' })).toBe(2);
    expect(correlationCount(null)).toBe(0);
    expect(requestIdsLine(2)).toBe('2 request IDs will be shared so their team can check their own logs.');
    expect(requestIdsLine(1)).toBe('1 request ID will be shared so their team can check their own logs.');
  });
});

describe('misc', () => {
  it('timeAgo', () => {
    const now = Date.parse('2026-08-23T12:00:00Z');
    expect(timeAgo('2026-08-23T11:59:50Z', now)).toBe('just now');
    expect(timeAgo('2026-08-23T11:30:00Z', now)).toBe('30m ago');
    expect(timeAgo('2026-08-23T09:00:00Z', now)).toBe('3h ago');
    expect(timeAgo('2026-08-18T12:00:00Z', now)).toBe('5d ago');
    expect(timeAgo(null, now)).toBe('—');
  });
  it('deep link hash', () => {
    expect(threadIdFromHash('#threads/0191-abc')).toBe('0191-abc');
    expect(threadIdFromHash('#threads')).toBeNull();
    expect(threadIdFromHash('')).toBeNull();
    // a malformed %-sequence must decode to null, never throw out of applyHash
    expect(threadIdFromHash('#threads/%E0%A4%A')).toBeNull();
    expect(threadIdFromHash('#threads/%')).toBeNull();
  });
  // The CP findings index deep-links to `<local_ui_url>/#contracts/<finding_id>`
  // — the Contracts tab's mirror of the threads deep link.
  it('contracts deep link hash', () => {
    expect(findingIdFromHash('#contracts/0191-def')).toBe('0191-def');
    expect(findingIdFromHash('#contracts/0191%2Fx')).toBe('0191/x');
    expect(findingIdFromHash('#contracts')).toBeNull();
    expect(findingIdFromHash('#threads/0191-abc')).toBeNull();
    expect(findingIdFromHash('')).toBeNull();
    // a malformed %-sequence must decode to null, never throw out of applyHash
    expect(findingIdFromHash('#contracts/%E0%A4%A')).toBeNull();
    expect(findingIdFromHash('#contracts/%')).toBeNull();
    // and the two never claim each other's hash
    expect(threadIdFromHash('#contracts/0191-def')).toBeNull();
  });
  it('needsCollectorAddress: only when Connected with no local_ui_url', () => {
    expect(needsCollectorAddress(null)).toBe(false);
    expect(needsCollectorAddress({ status: 'disconnected' })).toBe(false);
    expect(needsCollectorAddress({ status: 'pending', contact_email: 'a@b.c' })).toBe(false);
    expect(needsCollectorAddress({ status: 'connected', confirmed_contact_email: 'a@b.c' })).toBe(true);
    expect(needsCollectorAddress({ status: 'connected', local_ui_url: 'http://collector.internal:5335' })).toBe(false);
  });
  it('canCreateThread: connected, or a confirmed contact exists while a new one is pending', () => {
    expect(canCreateThread(null)).toBe(false);
    expect(canCreateThread({ status: 'disconnected' })).toBe(false);
    expect(canCreateThread({ status: 'pending', contact_email: 'a@b.c', confirmed_contact_email: null })).toBe(false);
    expect(canCreateThread({ status: 'connected', contact_email: 'a@b.c', confirmed_contact_email: 'a@b.c' })).toBe(true);
    // change of contact: new@ pending, ops@ still confirmed → Create thread stays available
    expect(canCreateThread({ status: 'pending', contact_email: 'new@b.c', confirmed_contact_email: 'ops@b.c' })).toBe(true);
  });
});

// Console hygiene (qa-gate 2026-08-30): the Threads poll used to fire while
// disconnected and collect a 412 on every tick. The relay's refusal is correct
// and the tab's notice is correct — but the BROWSER logs each refused request
// as a failed resource, which no JS can suppress. The only cure is not asking,
// so the poll gates on the connect state the UI already holds.
describe('cannotListThreads (gate on the 412 the relay would answer)', () => {
  it('true only for a state that is KNOWN disconnected', () => {
    expect(cannotListThreads({ status: 'disconnected' })).toBe(true);
    // pending still has a collector key, so the relay CAN list — asking is right
    expect(cannotListThreads({ status: 'pending', contact_email: 'a@b.c' })).toBe(false);
    expect(cannotListThreads({ status: 'connected', confirmed_contact_email: 'a@b.c' })).toBe(false);
  });
  it('unknown is not disconnected — an unanswered /api/connect must not flash the notice', () => {
    // App.vue waits for the connect answer on this; treating null as
    // disconnected would show "Not connected" to a connected collector for one
    // frame on every page load.
    expect(cannotListThreads(null)).toBe(false);
    expect(cannotListThreads(undefined)).toBe(false);
  });
  it('carries the relay 412 line verbatim, so the tab renders what the response used to supply', () => {
    // Byte-identical to msgThreadsNotConnected in extension/flanjui/messages.go.
    expect(THREADS_NOT_CONNECTED_NOTICE).toBe("Not connected — this collector can't list threads. Connect in Settings to see them.");
    // Non-empty is what ThreadsTab keys the inline Connect button off (v-if="notice"),
    // so the not-connected notice keeps its affordance without the request.
    expect(THREADS_NOT_CONNECTED_NOTICE.length).toBeGreaterThan(0);
  });
});
