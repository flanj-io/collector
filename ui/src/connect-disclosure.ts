/**
 * The Connect panel's edge-registration disclosure (v1 phase 2 — CONTRACTS §5,
 * `POST /api/v1/edges/sync`).
 *
 * Connecting starts a second thing leaving this collector besides findings: the
 * EXTERNAL domains it has discovered, registered so the owner's control-plane
 * dashboard can show the integration graph before anything has broken. That is
 * a data flow the operator must read BEFORE they Connect, not discover
 * afterwards — so the line renders in the panel in every state, disconnected
 * included, and it is deliberately specific about what does NOT go: no calls,
 * no bodies, no payloads, and no internal edges at all.
 *
 * It is honest about the switch too. `edge_sync: false` makes the on-copy a
 * lie, so a collector with registration turned off says exactly that instead —
 * a disclosure that overstates what leaves is as wrong as one that understates
 * it. `edge_sync` absent (a collector predating this slice, or a response that
 * has not arrived yet) renders NOTHING rather than guessing: claiming a flow
 * that may not exist is the same error in the other direction.
 */

export interface EdgeDisclosure {
  /** The sentence to render. */
  text: string;
  /** `on` = registration is live; `off` = the switch is disabling it. */
  state: 'on' | 'off';
}

/** The on-copy, verbatim — the spec's sentence (`v1-build-spec.md` §3 Step 2). */
export const EDGE_REGISTRATION_DISCLOSURE =
  'This collector registers the external domains it observes — never calls, bodies, or payloads. Internal edges never leave.';

/** The off-copy: registration is configured off, so nothing is registered at all. */
export const EDGE_REGISTRATION_DISCLOSURE_OFF =
  'Edge registration is off in this collector’s config (`edge_sync: false`) — no domains are sent. Findings still sync.';

/**
 * The disclosure for a Connect state, or `null` when the collector has not told
 * us which way the switch is set.
 */
export function edgeDisclosure(edgeSync: boolean | undefined | null): EdgeDisclosure | null {
  if (edgeSync === true) return { text: EDGE_REGISTRATION_DISCLOSURE, state: 'on' };
  if (edgeSync === false) return { text: EDGE_REGISTRATION_DISCLOSURE_OFF, state: 'off' };
  return null;
}
