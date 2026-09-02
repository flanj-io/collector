// What the Connect panel says about the confirmation mail.
//
// The bug this module exists to prevent: the panel used to print "Check your inbox — we sent …"
// and "Sent again to …" purely because the click returned 2xx. A 2xx is the REGISTRATION's
// verdict, not the mail's — the control plane answers 200/201 whether or not SMTP accepted
// anything — so the panel was claiming a delivery on evidence it never had.
//
// `confirmation_mail` (CONTRACTS-CP §5.1) carries the mail's own outcome, and describes ONE
// request: it is absent from a background poll, from an already-confirmed contact, and from a
// control plane predating the field. So the panel holds the outcome of the last attempt it made
// rather than reading it off the polled state — otherwise the next poll (~5s) would silently
// erase a "not sent" warning and put "check your inbox" back on screen.
export type ConfirmationMail = 'sent' | 'failed' | 'cooldown';

/** The last /api/connect call that actually attempted a send. `null` = none this session. */
export interface MailAttempt {
  outcome: ConfirmationMail | undefined;
  /** Seconds the control plane said were left on the floor; only meaningful for `cooldown`. */
  retryAfterS: number | undefined;
  /** The address the attempt was for, and when it happened (`Date.now()`). */
  email: string;
  at: number;
}

/** The three distinct things the pending panel can be showing, plus `unknown` (say nothing). */
export type MailNoticeKind = ConfirmationMail | 'unknown';

export interface MailNotice {
  kind: MailNoticeKind;
  /** One sentence, already resolved against the address. Empty for `unknown`. */
  text: string;
  /** Offer the action as a Retry (a send that failed is worth immediately re-trying). */
  retryable: boolean;
  /** May the panel attempt another send right now? False only while the floor is still closed. */
  canSend: boolean;
}

const NOTHING: MailNotice = { kind: 'unknown', text: '', retryable: false, canSend: true };

/** Human phrasing of a wait: "under a minute", "about 6 minutes". */
export function formatRemaining(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 60) return 'under a minute';
  return `about ${Math.ceil(seconds / 60)} minutes`;
}

/** Seconds still left on a cooldown attempt, counted down from when the answer arrived. */
export function remainingSeconds(attempt: MailAttempt, nowMs: number): number {
  const total = attempt.retryAfterS ?? 0;
  return Math.max(0, total - Math.floor((nowMs - attempt.at) / 1000));
}

/**
 * The notice for what happened to the confirmation mail on the last attempt.
 *
 * `null` (no send attempted) is `unknown`: the panel then shows only its standing line about the
 * pending contact and makes no claim about this session. An outcome the panel does not recognise
 * — a newer control plane inventing a fourth value — is `unknown` for the same reason: silence is
 * the only answer that cannot be wrong.
 */
export function mailNotice(attempt: MailAttempt | null, nowMs: number): MailNotice {
  if (!attempt) return NOTHING;
  switch (attempt.outcome) {
    case 'sent':
      return { kind: 'sent', text: `Sent again to ${attempt.email}.`, retryable: false, canSend: true };
    case 'failed':
      // Deliberately blames the mail server and nothing else: the collector cannot tell a bad
      // address from a dead relay, and guessing would send the operator down the wrong path.
      // `canSend` stays true — a failed send spends no cooldown, so Retry really does retry.
      return {
        kind: 'failed',
        text: `Not sent — the control plane couldn't reach its mail server, so nothing arrived at ${attempt.email}. Nothing else is wrong: retry, or change the address.`,
        retryable: true,
        canSend: true
      };
    case 'cooldown': {
      const left = remainingSeconds(attempt, nowMs);
      // Once the floor lifts, the notice retires itself rather than leaving Resend dead.
      if (left <= 0) return NOTHING;
      return {
        kind: 'cooldown',
        // This is a "you already have one" message, not a failure: the floor is measured from the
        // last mail that ACTUALLY went out, so that mail is in the inbox and its link still works.
        text: `Not sent again — one confirmation per 10 minutes. The last one still works; you can try again in ${formatRemaining(left)}.`,
        retryable: false,
        canSend: false
      };
    }
    default:
      return NOTHING;
  }
}
