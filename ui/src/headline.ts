// The Overview headline — the first line an operator reads, and the one that
// was asserting an all-clear before anything had been checked.
//
// It lived as a computed inside App.vue, where vitest cannot see it, and it
// said `No drift detected` whenever the live-finding list was empty. Nothing
// gated that on whether there were any calls, whether a contract was bound, or
// whether a single call had actually been validated. The first-launch QA walk
// hit it on all three lanes:
//   - a store with zero calls and no contract: green
//   - after traffic, with `0 of 2 providers checked against a contract`
//     rendering directly below it: still green
//   - with a vendor hard down and nothing reaching the provider at all: still
//     green
//
// An empty finding list has two completely different causes and this line was
// reporting only the flattering one. "We looked and found nothing" and "nothing
// looked" are not the same sentence, and the second one is what a fresh install
// is actually in. So the zero state is now NEUTRAL — neither the green
// all-clear nor the red drift banner — and says nothing has been validated yet.
//
// This is known-open item A6 in docs/contract-upload-ux.md ("Open — owner
// calls", 4), whose ruling is the WORDING, not whether the branch exists. The
// branch ships; the string is one constant so the owner's call is a one-line
// change here and nowhere else.
//
// ─── Evidence is per EDGE (2026-09-07) ───────────────────────────────────
//
// The neutral gate above was fed a count of validated calls from the whole
// window, and the window holds two kinds of edge. The second QA walk drove one
// REST provider (no contract uploaded) and one MCP server (which delivers its
// own contract on tools/list) on a fresh install, reloaded, and read — on
// sqlite, postgres and tiered alike:
//
//     You: No drift detected                                    ← green
//     on integration acme-payments
//     0 of 1 provider checked against a contract · 1 MCP server self-reports theirs
//
// The MCP tool calls had been validated against the snapshot, so the count was
// non-zero, and this line spent that evidence on the REST provider it named
// directly beneath — which nothing had checked. The MCP server has its own
// headline two lines down (ui/src/mcp.ts mcpHeadline); its evidence belongs
// there and nowhere else. So the caller now hands over the calls themselves,
// each carrying its per-call validated fact, and THIS function decides which
// of them are evidence for THIS line: the REST ones. A caller that pre-counts
// can count the wrong edge, and did.
//
// The `on integration <slug>` fragment had the same hole one level up.
// /api/health emits the slug from static config (`flanjui.integration_id`)
// the moment ANY external outbound edge exists — an MCP edge included — so an
// install that had only ever talked to an MCP server read "Nothing validated
// yet on integration acme-payments" about a REST integration it had never
// observed. The v1 build spec's pre-traffic honesty rule settles it: no
// integration tag sourced from static config, names attach to DISCOVERED edges
// only. The fragment therefore renders only when a REST call or a live finding
// in the window actually carries that integration — absent otherwise, with no
// replacement copy, exactly as the pre-traffic state already reads.
//
// Pure and vitest-covered, deliberately: it is a lie in this line that the
// whole coverage story sits underneath, and App.vue logic is invisible to the
// suite.

/** The neutral zero state. A6's owner ruling changes THIS and nothing else. */
export const NOTHING_VALIDATED_YET = 'Nothing validated yet';

/**
 * The same zero state in clause position, for a headline that reads
 * `Server: <name>. You: <clause>` — the per-server MCP line. Derived, not
 * retyped, so A6's ruling moves both lines with one edit.
 */
export const NOTHING_VALIDATED_YET_CLAUSE =
  NOTHING_VALIDATED_YET.charAt(0).toLowerCase() + NOTHING_VALIDATED_YET.slice(1) + '.';

/** The all-clear, which now costs at least one validated call. */
export const NO_DRIFT_DETECTED = 'No drift detected';

/**
 * `neutral` is a third state, not a shade of the other two. Rendering it green
 * asserts the all-clear this whole change exists to stop; rendering it red
 * accuses a provider of something on evidence that does not exist either.
 */
export type HeadlineTone = 'ok' | 'drift' | 'neutral';

/** The minimum a finding needs to name itself on the headline. */
export interface HeadlineFinding {
  endpoint: string;
  /**
   * The integration the finding is filed under. A finding vouches for the
   * `on integration` fragment only when it names that integration.
   */
  integration?: string;
}

/** The minimum a call needs to be weighed as headline evidence. */
export interface HeadlineCall {
  /**
   * `mcp` on an MCP tool call; absent on HTTP rows. Decides WHICH headline the
   * call is evidence for — this one, or its server's own.
   */
  transport?: string;
  integration?: string;
  /**
   * Did a contract actually check this call? The caller derives it — today
   * from the coverage mirror (ui/src/coverage.ts), tomorrow from the
   * server-side stamp — and this module only ever reads it.
   */
  validated: boolean;
}

export interface HeadlineInput {
  /**
   * LIVE drift findings only — spec-version diffs are informational and stay
   * out of the divergence status (unchanged from the original computed).
   */
  liveFindings: readonly HeadlineFinding[];
  /**
   * Every call in the loaded window, on ANY edge. The split into "evidence
   * for this line" and "evidence for an MCP server's line" happens here, on
   * purpose: it is the split the caller got wrong.
   */
  calls: readonly HeadlineCall[];
  /**
   * Pre-traffic honesty (v1p1): absent from /api/health until the collector has
   * observed an external outbound edge (or a finding). While absent the
   * "on integration <slug>" fragment simply does not render — no replacement
   * copy, no fallback slug. Present, it is still only a CANDIDATE: the
   * fragment renders once a REST call or a live finding has been observed
   * under it (see integrationFragment).
   */
  integration?: string;
}

export interface Headline {
  you: string;
  tone: HeadlineTone;
  integration: string;
}

/**
 * Is this call on a REST provider edge — one of the edges THIS headline speaks
 * for? An MCP tool call is evidence for its server's own headline and must
 * never be spent here, however validated it is. HTTP rows carry no transport
 * at all, so the question is "not MCP", not "is HTTP".
 */
export function isRestCall(c: Pick<HeadlineCall, 'transport'>): boolean {
  return c.transport !== 'mcp';
}

/**
 * The `on integration <slug>` fragment, or '' when nothing on a REST edge has
 * been observed under that slug.
 *
 * A captured call is enough — validated or not — because the fragment scopes
 * the sentence, and "Nothing validated yet on integration acme-payments" is
 * true of an acme edge nothing has checked. A live finding is enough on its
 * own, because its call can be evicted while the finding outlives it. An MCP
 * call is not: it carries its own integration, and the slug from health is a
 * REST integration's name.
 */
function integrationFragment(
  slug: string | undefined,
  restCalls: readonly HeadlineCall[],
  findings: readonly HeadlineFinding[]
): string {
  if (!slug) return '';
  const observed =
    restCalls.some((c) => c.integration === slug) || findings.some((f) => f.integration === slug);
  return observed ? slug : '';
}

/**
 * The headline, in priority order.
 *
 * Findings first: a finding is positive evidence that something was validated,
 * whatever the window's call rows happen to say about it — a drifting call can
 * be evicted, or its finding can outlive it. Then the neutral gate, on REST
 * evidence only. The all-clear is last and has to be earned on this edge.
 */
export function headlineFor(input: HeadlineInput): Headline {
  const rest = input.calls.filter(isRestCall);
  const integration = integrationFragment(input.integration, rest, input.liveFindings);
  const n = input.liveFindings.length;

  if (n > 0) {
    const endpoints = Array.from(new Set(input.liveFindings.map((f) => f.endpoint)));
    return {
      you: `${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')}`,
      tone: 'drift',
      integration
    };
  }

  // The all-clear is earned on the edge the fragment NAMES. When the fragment
  // renders, only that integration's validated REST calls are evidence — a
  // second SDK instance under another FLANJ_INTEGRATION_ID exporting to this
  // collector must not turn "on integration acme-payments" green while acme
  // itself was never checked. Without a fragment, any REST edge's evidence counts.
  const evidence = integration ? rest.filter((c) => c.integration === integration) : rest;
  if (!evidence.some((c) => c.validated)) {
    return { you: NOTHING_VALIDATED_YET, tone: 'neutral', integration };
  }

  return { you: NO_DRIFT_DETECTED, tone: 'ok', integration };
}
