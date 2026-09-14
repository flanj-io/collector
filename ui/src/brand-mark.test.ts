import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The topbar mark is docs/design/flanj-mark.svg — the COLOUR mark — inlined.
 * It shipped as the mono variant once (two paths on currentColor) because the
 * vault rule said mono for product surfaces; the kits show the colour mark, and
 * the rule was wrong. These are structural guards on the port, not style
 * opinions:
 *
 *  - the geometry is the asset's, byte for byte — four paths: the pipes, the
 *    outline under the threads, the F thread, the J thread;
 *  - the pipes and the outline take their colour from the two logo tokens
 *    (`--logo-steel`, `--logo-outline`) through a CLASS in the style block, so
 *    they switch with [data-flanj-theme="dark"] and no path carries an inline
 *    style (the control-plane CSP forbids them; the markup stays portable);
 *  - the threads reference ONE copper gradient defined in the document's shared
 *    defs holder (the same hidden <svg> that carries #hxbolt), whose three stops
 *    are the asset's own hex as stop-color ATTRIBUTES — there is no token for
 *    the copper yet, and the values are identical in both schemes.
 */
const src = __dirname;
const read = (f: string) => readFileSync(join(src, f), 'utf8');

const PIPES =
  'M15 376.5H106M106 376.5V346.5H166.5M106 376.5V407H166.5M166.5 346.5V407M166.5 346.5V286H106V226H166.5V165.5M166.5 407V435.5H227V256.25V77H166.5V105.5M15 136H106M106 136V165.5H166.5M106 136V105.5H166.5M166.5 165.5V105.5M498 136H407M407 136V165.5H346.5M407 136V105.5H346.5M346.5 165.5V105.5M346.5 165.5V226H407V286H346.5V346.5M346.5 105.5V77H286V136M498 376.5H407M407 376.5V346.5H346.5M407 376.5V407H346.5M346.5 346.5V407M346.5 407V435.5H286V376.5M286 136H227M286 136V376.5M286 376.5H227';
const OUTLINE = 'M198.5 407.5V256M317 41H198.5V256M198.5 256H133M316.5 104.5V256M198 471H316.5V256M316.5 256H382';
const F_THREAD = 'M198.5 407.5V256M317 41H198.5V256M198.5 256H133';
const J_THREAD = 'M316.5 104.5V256M198 471H316.5V256M316.5 256H382';
/** The copper stops of docs/design/flanj-mark.svg, in offset order. */
const COPPER: ReadonlyArray<readonly [string, string]> = [
  ['0', '#e0b077'],
  ['0.55', '#a86b2d'],
  ['1', '#7d4e1f']
];
const GRADIENT_ID = 'brandcu';

type Attrs = Record<string, string>;

/** Every `<path …>` inside the first `<svg class="brand-mark">`, as attribute maps. */
export function brandMarkPaths(template: string): Attrs[] {
  const open = template.indexOf('<svg class="brand-mark"');
  if (open < 0) return [];
  const close = template.indexOf('</svg>', open);
  const svg = template.slice(open, close < 0 ? undefined : close);
  return [...svg.matchAll(/<path\b([^>]*)\/?>/g)].map((m) => attrsOf(m[1]));
}

function attrsOf(s: string): Attrs {
  return Object.fromEntries([...s.matchAll(/([\w:-]+)="([^"]*)"/g)].map((m) => [m[1], m[2]]));
}

/** The template of an SFC, comments stripped (the prose names the mono file). */
function templateOf(f: string): string {
  const sfc = read(f);
  const start = sfc.indexOf('<template>');
  const end = sfc.lastIndexOf('</template>');
  return start < 0 ? '' : sfc.slice(start, end).replace(/<!--[\s\S]*?-->/g, '');
}

function styleOf(f: string): string {
  const sfc = read(f);
  return sfc.slice(sfc.indexOf('<style')).replace(/\/\*[\s\S]*?\*\//g, '');
}

/** The `{…}` body of the first rule whose selector list is exactly `sel`. */
function ruleBody(css: string, sel: string): string | null {
  const m = css.match(new RegExp(`(?:^|[\\s}])${sel.replace(/[.]/g, '\\.')}\\s*\\{([^}]*)\\}`));
  return m ? m[1] : null;
}

describe('the brand mark is the colour mark', () => {
  const template = templateOf('App.vue');
  const style = styleOf('App.vue');
  const paths = brandMarkPaths(template);

  it('carries the four paths of docs/design/flanj-mark.svg, in order', () => {
    expect(paths.map((p) => p.d)).toEqual([PIPES, OUTLINE, F_THREAD, J_THREAD]);
    expect(paths.map((p) => p['stroke-width'])).toEqual(['15', '26', '18', '18']);
    for (const p of paths) {
      expect(p['stroke-linecap']).toBe('round');
      expect(p['stroke-linejoin']).toBe('round');
    }
  });

  it('pipes and outline take their colour by class, not by attribute', () => {
    const [pipes, outline] = paths;
    expect(pipes?.class).toBe('brand-pipes');
    expect(outline?.class).toBe('brand-outline');
    // The mono port set stroke="currentColor" on both; the colour mark binds
    // the two logo tokens in the style block instead. An attribute here would
    // win over nothing (a presentation attribute loses to any CSS rule), but
    // its presence means somebody pasted the file's own hex back in.
    for (const p of [pipes, outline]) expect(p?.stroke, 'stroke attribute on a token-bound path').toBeUndefined();
    for (const p of paths) expect(p?.style, 'inline style on a mark path').toBeUndefined();
  });

  it('pipes bind --logo-steel and the outline --logo-outline in the style block', () => {
    expect(ruleBody(style, '.brand-pipes')).toMatch(/(^|;)\s*stroke:\s*var\(--logo-steel\)\s*(;|$)/);
    expect(ruleBody(style, '.brand-outline')).toMatch(/(^|;)\s*stroke:\s*var\(--logo-outline\)\s*(;|$)/);
    // Both tokens must exist in BOTH schemes of the vendored file, or the mark
    // renders unstroked (an unresolved var() is no colour at all) in one of them.
    const tokens = read('tokens.css');
    for (const scheme of [':root {', '[data-flanj-theme="dark"] {']) {
      const block = tokens.slice(tokens.indexOf(scheme));
      const body = block.slice(0, block.indexOf('}'));
      expect(body, `--logo-steel under ${scheme}`).toMatch(/--logo-steel:\s*#[0-9a-f]{6};/);
      expect(body, `--logo-outline under ${scheme}`).toMatch(/--logo-outline:\s*#[0-9a-f]{6};/);
    }
  });

  it('both threads reference the one copper gradient', () => {
    const [, , f, j] = paths;
    expect(f?.stroke).toBe(`url(#${GRADIENT_ID})`);
    expect(j?.stroke).toBe(`url(#${GRADIENT_ID})`);
  });

  it('the copper gradient is defined once, in the shared defs holder, with the canonical stops', () => {
    const sfcs = readdirSync(src).filter((f) => f.endsWith('.vue'));
    const templates = sfcs.map((f) => templateOf(f));
    const defs = templates.flatMap((t) => t.match(new RegExp(`<linearGradient id="${GRADIENT_ID}"[^>]*>[\\s\\S]*?</linearGradient>`, 'g')) ?? []);
    expect(defs, `exactly one <linearGradient id="${GRADIENT_ID}"> across the SFC templates`).toHaveLength(1);
    const gradient = defs[0];
    // The diagonal of the asset: top-left to bottom-right of each thread's box.
    expect(attrsOf(gradient.slice(0, gradient.indexOf('>')))).toMatchObject({ x1: '0', y1: '0', x2: '1', y2: '1' });
    const stops = [...gradient.matchAll(/<stop\b([^>]*)\/?>/g)].map((m) => attrsOf(m[1]));
    expect(stops.map((s) => [s.offset, s['stop-color']])).toEqual(COPPER.map((c) => [...c]));
    for (const s of stops) expect(s.style, 'inline style on a gradient stop').toBeUndefined();
    // One holder per document: the hidden <svg> that already carries #hxbolt.
    const holder = template.match(/<svg class="hx-defs"[\s\S]*?<\/svg>/);
    expect(holder, 'the hx-defs holder').not.toBeNull();
    expect(holder![0]).toContain('<symbol id="hxbolt"');
    expect(holder![0]).toContain(`<linearGradient id="${GRADIENT_ID}"`);
  });

  it('the copper is attribute-only: no thread colour lives in the style block', () => {
    // tokens.test.ts already forbids hex in any <style>; this pins the reason
    // the stops are template attributes and not a `.brand-thread { stroke: … }`.
    for (const [, hex] of COPPER) expect(style).not.toContain(hex);
    expect(style).not.toMatch(/url\(#brandcu\)/);
  });

  it('nothing in the surface still describes the mark as the mono variant', () => {
    for (const f of ['App.vue', join('..', 'README.md')]) {
      expect(read(f), `${f} names the mono file`).not.toContain('flanj-mark-mono.svg');
    }
  });

  it('the scanner bites on the mono port', () => {
    // The markup this file replaced: two paths on currentColor, no threads.
    const mono =
      '<template><svg class="brand-mark" viewBox="0 0 512 512" fill="none">' +
      `<path d="${PIPES}" stroke="currentColor" stroke-width="15" stroke-linecap="round" stroke-linejoin="round" />` +
      `<path d="${OUTLINE}" stroke="currentColor" stroke-width="26" stroke-linecap="round" stroke-linejoin="round" />` +
      '</svg></template>';
    const old = brandMarkPaths(mono);
    expect(old).toHaveLength(2);
    expect(old[0].stroke).toBe('currentColor');
    expect(old[0].class).toBeUndefined();
    expect(ruleBody('.brand-mark { width: 30px; }', '.brand-pipes')).toBeNull();
  });
});
