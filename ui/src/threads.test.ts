import { describe, expect, it } from 'vitest';
import {
  canCreateThread,
  chipLabel,
  correlationCount,
  defaultFlagMessage,
  evidenceLine,
  fieldName,
  linkLabel,
  linkNeedsAttention,
  needsCollectorAddress,
  pasteText,
  requestIdsLine,
  threadIdFromHash,
  timeAgo,
  turnLabel,
  type ThreadRow,
  type ThreadSummary
} from './threads';

const base: ThreadSummary = {
  id: 't1',
  thread_public_id: 'p1',
  state: 'open',
  turn: 'waiting_on_provider',
  provider_display_name: 'Acme Payments',
  endpoint: 'POST /v1/charges',
  evidence_count: 1,
  opened_count: 3,
  knock_count: 0,
  message_count: 0,
  link: { status: 'active', expires_at: '2026-09-22T00:00:00Z' }
};

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

describe('linkLabel', () => {
  const d = (iso: string) => iso.slice(0, 10);
  it('active / replaced / expired with knocks', () => {
    expect(linkLabel(base, d)).toBe('Active · expires 2026-09-22');
    // An ACTIVE link with knocks explains the amber state: someone hit an old link.
    expect(linkLabel({ ...base, knock_count: 2 }, d)).toBe('Active · expires 2026-09-22 · 2 tried an old link');
    expect(linkLabel({ ...base, link: { status: 'active' }, knock_count: 1 }, d)).toBe('Active · 1 tried an old link');
    expect(linkLabel({ ...base, link: { status: 'replaced' } }, d)).toBe('Replaced');
    expect(linkLabel({ ...base, link: { status: 'expired' }, knock_count: 2 }, d)).toBe('Expired · 2 tried to open');
    expect(linkLabel(null, d)).toBe('—');
  });
  it('needs attention when not active or knocked', () => {
    expect(linkNeedsAttention(base)).toBe(false);
    expect(linkNeedsAttention({ ...base, knock_count: 1 })).toBe(true);
    expect(linkNeedsAttention({ ...base, link: { status: 'expired' } })).toBe(true);
  });
});

describe('chipLabel', () => {
  it('renders In thread · <turn> · opened ×N', () => {
    const row: ThreadRow = {
      thread_id: 't1',
      thread_public_id: 'p1',
      finding_id: 'f1',
      endpoint: 'POST /v1/charges',
      provider: 'Acme Payments',
      thread_url: 'https://cp/t/p1#k=x',
      created_at: '2026-08-23T10:00:00Z',
      summary: base
    };
    expect(chipLabel(row)).toBe('In thread · Waiting on Acme Payments · opened ×3');
    expect(chipLabel({ ...row, summary: null })).toBe('In thread · Waiting on Acme Payments · opened ×0');
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
