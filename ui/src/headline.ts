// The Overview headline — the first line an operator reads, and the one that
// was asserting an all-clear before anything had been checked.
//
// It lived as a computed inside App.vue, where vitest cannot see it, and it
// said `No drift detected` whenever the live-finding list was empty. Nothing
// gated that on whether there were any calls, whether a contract was bound, or
// whether a single call had actually been validated. The first-launch exploratory pass
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
// The WORDING of this zero state is still open, not whether the branch exists.
// The branch ships; the string is one constant so a wording change is a
// one-line change here and nowhere else.
//
// ─── Evidence is per EDGE (2026-09-07) ───────────────────────────────────
//
// The neutral gate above was fed a count of validated calls from the whole
// window, and the window holds two kinds of edge. The second exploratory pass drove one
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
// The `on integration <slug>` fragment that once rode under this line had the
// same hole one level up (a slug from static config naming a REST integration
// nothing had observed). It is gone with the config key (CONTRACTS §8,
// 2026-09-14): the scope line now names the DEPLOYMENT by its collector name,
// an identity the operator chose, rendered by App.vue from the Connect state.
//
// Pure and vitest-covered, deliberately: it is a lie in this line that the
// whole coverage story sits underneath, and App.vue logic is invisible to the
// suite.

/** The neutral zero state. A wording change edits THIS and nothing else. */
export const NOTHING_VALIDATED_YET = 'Nothing validated yet';

/**
 * The same zero state in clause position, for a headline that reads
 * `Server: <name> — <clause>` — the per-server MCP line. Derived, not
 * retyped, so a wording change moves both lines with one edit.
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
  /** The integration the finding is filed under (the SDK's stamp); informational here. */
  integration?: string;
  /**
   * `breaking` | `warning` | `info`. The headline speaks for BREAKING findings
   * only (see isHeadlineDrift): a deprecation is a live-vs-spec finding at
   * `warning`, and it must not turn this line red.
   *
   * Optional, and absent counts as breaking — every live-vs-spec finding was
   * breaking before deprecation detection, and a reader that omits the field is
   * describing one of those.
   */
  severity?: string;
}

/**
 * Does this finding belong on the divergence headline? Only a BREAKING one.
 *
 * The line reads "N contract drift findings" in a red tone, and drift means the
 * provider departed from what they published. A deprecation is the opposite
 * shape of news: the contract still holds, and the provider is telling you in
 * advance that it will stop. It lists on the Contracts tab, in the warning
 * tier, where it can be read and acted on — turning the Overview red for it
 * would spend the one alarm this product has on a surface that still works.
 */
export function isHeadlineDrift(f: HeadlineFinding): boolean {
  return (f.severity ?? 'breaking') === 'breaking';
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
   * LIVE findings only — spec-version diffs are informational and stay out of
   * the divergence status (unchanged from the original computed). Of these,
   * only the BREAKING ones reach the line (isHeadlineDrift); the caller passes
   * them all and the filter lives here, beside the tests that pin it.
   */
  liveFindings: readonly HeadlineFinding[];
  /**
   * Every call in the loaded window, on ANY edge. The split into "evidence
   * for this line" and "evidence for an MCP server's line" happens here, on
   * purpose: it is the split the caller got wrong.
   */
  calls: readonly HeadlineCall[];
}

export interface Headline {
  you: string;
  tone: HeadlineTone;
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
 * The headline, in priority order.
 *
 * Findings first: a finding is positive evidence that something was validated,
 * whatever the window's call rows happen to say about it — a drifting call can
 * be evicted, or its finding can outlive it. Then the neutral gate, on REST
 * evidence only. The all-clear is last and has to be earned on this edge.
 */
export function headlineFor(input: HeadlineInput): Headline {
  const rest = input.calls.filter(isRestCall);
  const drifts = input.liveFindings.filter(isHeadlineDrift);
  const n = drifts.length;

  if (n > 0) {
    const endpoints = Array.from(new Set(drifts.map((f) => f.endpoint)));
    return {
      you: `${n} contract drift finding${n === 1 ? '' : 's'} on ${endpoints.join(', ')}`,
      tone: 'drift'
    };
  }

  // The all-clear is earned on REST evidence: any REST edge's validated call
  // counts, and an MCP tool call never does (its server has its own line). The
  // "on integration <slug>" scope that once narrowed this went with the config
  // key (2026-09-14) — the scope line names the collector now, not an edge.
  if (!rest.some((c) => c.validated)) {
    return { you: NOTHING_VALIDATED_YET, tone: 'neutral' };
  }

  return { you: NO_DRIFT_DETECTED, tone: 'ok' };
}
