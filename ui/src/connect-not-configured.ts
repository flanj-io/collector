/**
 * What the Settings tab says when this collector cannot Connect.
 *
 * The sentence is the backend's own (`msgCPNotConfigured` in
 * extension/flanjui/messages.go) — what the API answers when Connect is tried
 * anyway — followed by what still works, because a first-time operator reads
 * "not set up" as "broken". It names `cp_base_url` and nothing else: no token
 * is needed to Connect, the contact's confirmation click is the consent.
 * connect-not-configured.test.ts reads the backend file, so the two cannot
 * drift apart the way they once did.
 *
 * Returned as parts so the template can set the config key in <code> without
 * the sentence living in two places.
 */
export const CP_NOT_CONFIGURED_KEYS = ['cp_base_url'] as const;

export interface CopyPart {
  text: string;
  /** Render as <code>: a config key the operator types. */
  code?: boolean;
}

export function cpNotConfigured(): CopyPart[] {
  return [
    { text: 'This collector is not set up to connect to Flanj (set ' },
    { text: CP_NOT_CONFIGURED_KEYS[0], code: true },
    { text: '). Local capture, detection and this UI work without it.' },
  ];
}
