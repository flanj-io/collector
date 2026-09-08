import { describe, expect, it } from 'vitest';
import { formatRemaining, mailNotice, remainingSeconds, type MailAttempt } from './connect-mail';

const T0 = 1_756_000_000_000;
const attempt = (over: Partial<MailAttempt>): MailAttempt => ({
  outcome: 'sent',
  retryAfterS: undefined,
  email: 'ops@acme.example',
  at: T0,
  resend: false,
  ...over
});

/**
 * The Connect panel's three states. The regression these guard: the panel used to have ONE — it
 * printed "Check your inbox" and "Sent again to …" off a bare 2xx, so a dead SMTP looked exactly
 * like a delivered mail. Every case below asserts what the operator is TOLD, because the wrong
 * sentence is the whole bug.
 */
describe('confirmation-mail notice: sent / not sent / refused by cooldown', () => {
  it('sent on a RESEND — "Sent again", offers no retry, and lets another send through', () => {
    const n = mailNotice(attempt({ outcome: 'sent', resend: true }), T0);
    expect(n.kind).toBe('sent');
    expect(n.text).toBe('Sent again to ops@acme.example.');
    expect(n.retryable).toBe(false);
    expect(n.canSend).toBe(true);
  });

  it('sent on the FIRST send — no extra line: "again" is a claim about a second mail (2026-09-07)', () => {
    // The panel's standing line already reads "Check your inbox — we sent …". After the very first
    // Connect (and after Change contact to a NEW address) the notice used to add "Sent again to …"
    // on top of it — a resend that never happened; Mailpit held exactly one mail.
    const n = mailNotice(attempt({ outcome: 'sent', resend: false }), T0);
    expect(n.kind).toBe('sent');
    expect(n.text).toBe('');
    expect(n.text).not.toContain('again');
    expect(n.retryable).toBe(false);
    expect(n.canSend).toBe(true);
  });

  it('the button pressed changes only the wording of a success — never the failed / cooldown verdicts', () => {
    // `resend` is about phrasing; what happened to the mail does not depend on which button asked.
    expect(mailNotice(attempt({ outcome: 'failed', resend: true }), T0)).toEqual(mailNotice(attempt({ outcome: 'failed', resend: false }), T0));
    expect(mailNotice(attempt({ outcome: 'cooldown', retryAfterS: 360, resend: true }), T0)).toEqual(
      mailNotice(attempt({ outcome: 'cooldown', retryAfterS: 360, resend: false }), T0)
    );
  });

  it('failed — states plainly that nothing arrived, names the address, and offers a WORKING retry', () => {
    const n = mailNotice(attempt({ outcome: 'failed' }), T0);
    expect(n.kind).toBe('failed');
    expect(n.text).toContain('Not sent');
    expect(n.text).toContain('ops@acme.example');
    // The reason, in one sentence — the operator must know it is the mail server, not their address.
    expect(n.text).toContain("couldn't reach its mail server");
    // Never the old claim.
    expect(n.text).not.toContain('Sent again');
    expect(n.text).not.toContain('Check your inbox');
    expect(n.retryable).toBe(true);
    // Load-bearing: a failed send spends no cooldown on the CP, so Retry must not be blocked.
    expect(n.canSend).toBe(true);
  });

  it('cooldown — reports the remaining time and refuses to send again, without calling it a failure', () => {
    const n = mailNotice(attempt({ outcome: 'cooldown', retryAfterS: 360 }), T0);
    expect(n.kind).toBe('cooldown');
    expect(n.text).toContain('about 6 minutes');
    expect(n.text).toContain('one confirmation per 10 minutes');
    // The earlier mail is real and its link works — this is not a delivery failure.
    expect(n.text).toContain('The last one still works');
    expect(n.canSend).toBe(false);
    expect(n.retryable).toBe(false);
  });

  it('a cooldown expires by itself: the notice retires and Resend comes back', () => {
    const a = attempt({ outcome: 'cooldown', retryAfterS: 360 });
    expect(remainingSeconds(a, T0 + 60_000)).toBe(300);
    expect(mailNotice(a, T0 + 60_000).text).toContain('about 5 minutes');
    // …and once the floor lifts there is nothing left to say, and nothing left to disable.
    const after = mailNotice(a, T0 + 361_000);
    expect(after.kind).toBe('unknown');
    expect(after.text).toBe('');
    expect(after.canSend).toBe(true);
  });

  it('no attempt yet — no notice at all, and never a claim that mail went out', () => {
    const n = mailNotice(null, T0);
    expect(n.kind).toBe('unknown');
    expect(n.text).toBe('');
    expect(n.canSend).toBe(true);
  });

  it('an outcome this build does not know (older or newer control plane) says nothing', () => {
    // A CP predating the field sends no outcome; a newer one could invent a fourth value. Both
    // must fall through to silence — inventing "sent" from a 2xx is exactly the defect.
    expect(mailNotice(attempt({ outcome: undefined }), T0).kind).toBe('unknown');
    expect(mailNotice(attempt({ outcome: 'queued' as never }), T0).kind).toBe('unknown');
  });

  it('formatRemaining rounds up and never says "0 minutes"', () => {
    expect(formatRemaining(0)).toBe('under a minute');
    expect(formatRemaining(1)).toBe('under a minute');
    expect(formatRemaining(60)).toBe('under a minute');
    expect(formatRemaining(61)).toBe('about 2 minutes');
    expect(formatRemaining(600)).toBe('about 10 minutes');
    expect(formatRemaining(Number.NaN)).toBe('under a minute');
  });

  it('a cooldown with no retry-after still refuses nothing forever', () => {
    // The field rides only on cooldown, but a CP that omitted it must not disable Resend for good.
    const n = mailNotice(attempt({ outcome: 'cooldown', retryAfterS: undefined }), T0);
    expect(n.kind).toBe('unknown');
    expect(n.canSend).toBe(true);
  });
});
