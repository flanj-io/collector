import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The Outbound/Inbound tables' shared column grid (`table-layout: fixed`,
 * App.vue ~3495-3520). vitest's `happy-dom` environment never lays a page
 * out — every `scrollWidth`/`clientWidth`/`getComputedStyle(...).width` reads
 * back empty or zero regardless of what the CSS says (verified against this
 * very component before writing this file) — so a real overflow (content
 * wider than its column) is invisible to a mounted-component test here.
 * Pinned on the raw source instead, the same way `tokens.test.ts` pins badge
 * rules: assert the declared width, not a browser measurement this test
 * environment cannot make.
 *
 * `col-calls` at 92px left ~68px of content after the cell's 24px of padding, but the
 * cell holds `<count> · <rpm>/min` and never wraps — "764 · 9/min",
 * "2562 · 30/min" and "849 · 10/min" all spilled past the column's right
 * edge toward FIRST SEEN. `col-last` at 168px had the same problem on a
 * smaller scale ("2026-09-21 · just now" ran ~5px over). The widths below
 * were sized against the worst case each cell ever renders — a 5-digit call
 * count with a 3-digit rpm, and the longest string `timeAgo()` prints
 * (`just now`) beside a date — and hand-verified in a real Chromium render
 * (a throwaway page built from these exact rules, not checked in — adding a
 * browser-automation dependency to this package for two pixel values is not
 * worth the weight it puts on `npm test`/CI) with margin left over for a
 * monospace font wider than the fallback stack most visitors actually see,
 * since `--f-mono` names "IBM Plex Mono" first but the app ships no such
 * font file, so most renders fall back to the system's own monospace.
 */
const appVueSrc = readFileSync(join(__dirname, 'App.vue'), 'utf8');

describe('edges table column widths stay wide enough for their own worst-case content', () => {
  it('col-calls is wide enough for a 5-digit count and a 3-digit rpm ("12345 · 120/min")', () => {
    const m = appVueSrc.match(/\.edges-table col\.col-calls\s*\{\s*width:\s*(\d+)px;\s*\}/);
    expect(m, 'col-calls width rule not found').not.toBeNull();
    const width = Number(m![1]);
    // 92px (the pre-fix width) is the regression this guards against: it
    // rendered "2562 · 30/min" past the column's own right edge.
    expect(width).toBeGreaterThanOrEqual(120);
  });

  it('col-last is wide enough for a full date plus "just now" ("2026-09-21 · just now")', () => {
    const m = appVueSrc.match(/\.edges-table col\.col-last\s*\{\s*width:\s*(\d+)px;\s*\}/);
    expect(m, 'col-last width rule not found').not.toBeNull();
    const width = Number(m![1]);
    // 168px (the pre-fix width) is the regression this guards against.
    expect(width).toBeGreaterThanOrEqual(180);
  });
});

describe('the drift stripe lands on the counterparty cell only', () => {
  it("never re-adds `td:first-of-type` to `.edge-row.drift`'s box-shadow rule", () => {
    // `td:first-of-type` in a row whose first cell is a <th> (the counterparty
    // name) selects the STATUS cell instead — the first `<td>` — drawing a
    // second breaking stripe in the middle of the row. Both the desktop rule
    // and its mobile reset used to carry this selector; neither should again.
    expect(appVueSrc).not.toContain('.edge-row.drift td:first-of-type');
    expect(appVueSrc).toContain(".edge-row.drift th[scope='row'] { box-shadow: inset");
  });
});
