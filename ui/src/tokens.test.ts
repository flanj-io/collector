import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { createHash } from 'node:crypto';

/**
 * The token layer is only a design system for as long as nobody re-introduces a
 * literal. These are cheap structural guards, not style opinions:
 *
 *  - `tokens.css` is a VENDORED copy of docs/design/tokens.css and carries the
 *    same do-not-edit header as CONTRACTS.md. Editing it here is the failure
 *    mode: the vault copy is canonical, and a local tweak silently forks the
 *    palette away from the peek page and the drift dataset page.
 *  - no colour literal may live in an SFC any more.
 *  - the severity triad is product-fixed: `breaking`, never `error`, and the
 *    accent is never spent on one of the three tiers.
 */
const src = __dirname;
const sfcs = readdirSync(src).filter((f) => f.endsWith('.vue'));
const read = (f: string) => readFileSync(join(src, f), 'utf8');
const styleOf = (f: string) => read(f).slice(read(f).indexOf('<style'));

const CANONICAL_BANNER = '/* Flanj design tokens — CANONICAL SOURCE.';

/** Every `--name:` declared (not merely read through var()) in a stylesheet. */
function customPropertyNames(css: string): Set<string> {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
  return new Set([...stripped.matchAll(/(?:^|[{;])\s*(--[\w-]+)\s*:/g)].map((m) => m[1]));
}

/** Names a surface declares that the canonical file also defines. */
export function collidingDeclarations(css: string, canonical: Set<string>): string[] {
  return [...customPropertyNames(css)].filter((n) => canonical.has(n)).sort();
}

/** docs/design/tokens.css when the docs vault sits somewhere above this repo. */
function findVaultTokens(): string | null {
  let dir = src;
  for (let i = 0; i < 10; i++) {
    const candidate = join(dir, 'docs', 'design', 'tokens.css');
    if (existsSync(candidate)) return candidate;
    const parent = dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return null;
}

describe('design tokens', () => {
  const tokens = read('tokens.css');

  it('tokens.css is vendored, not authored here', () => {
    expect(tokens.startsWith('/* vendored from docs/design/tokens.css — do not edit here.')).toBe(true);
    expect(tokens).toContain('Canonical source lives in the docs vault');
    // The canonical file's own banner must survive the copy — its absence means
    // somebody hand-wrote a palette into this file instead of re-vendoring.
    expect(tokens).toContain(CANONICAL_BANNER);
  });

  it('tokens.css carries the full triad in both schemes', () => {
    for (const scheme of [':root {', '[data-flanj-theme="dark"] {']) {
      expect(tokens).toContain(scheme);
    }
    for (const t of ['--sev-breaking', '--sev-warning', '--sev-info']) {
      // once per scheme block plus the prefers-color-scheme mirror
      expect(tokens.split(`${t}:`).length - 1).toBeGreaterThanOrEqual(3);
    }
    expect(tokens).not.toContain('--sev-error');
  });

  it('every SFC style block is literal-free — colour comes from tokens only', () => {
    for (const f of sfcs) {
      const style = styleOf(f);
      expect(style.match(/#[0-9a-fA-F]{3,8}\b/g), `hex literal in ${f}`).toBeNull();
      // The one allowed non-token colour is the modal scrim, which is an alpha
      // wash over whatever is behind it rather than a palette value.
      const rgbas = style.match(/rgba?\([^)]*\)/g) ?? [];
      expect(rgbas.filter((c) => !c.startsWith('rgba(0, 0, 0,')), `rgb literal in ${f}`).toEqual([]);
    }
  });

  it('corners are square everywhere — radius 0 is a brand decision', () => {
    for (const f of sfcs) {
      const all = styleOf(f).match(/border-radius:[^;]+;/g) ?? [];
      const bad = all.filter((d) => d !== 'border-radius: var(--radius);');
      expect(bad, `hardcoded radius in ${f}`).toEqual([]);
    }
  });

  it('the severity triad is never spelled `error`, and accent never carries a tier', () => {
    for (const f of sfcs) {
      const style = styleOf(f);
      expect(style).not.toMatch(/--sev-error|--severity-error/);
      // The three severity badges are the tier renderer; each must use its own
      // family. `.badge.info { background: var(--accent) }` is the regression
      // this guards — a blue chip beside a red and a yellow one reads as a
      // fourth tier, and accent is never semantic.
      for (const [cls, fam] of [
        ['breaking', '--sev-breaking'],
        ['warning', '--sev-warning'],
        ['info', '--sev-info']
      ] as const) {
        const rule = style.match(new RegExp(`\\.badge\\.${cls}\\s*\\{[^}]*\\}`));
        // The badges live in App.vue. There the rule MUST be found — an
        // `if (rule)` here used to turn a reformatted selector into a silent
        // pass, which is the opposite of a guard.
        if (f === 'App.vue') expect(rule, `.badge.${cls} rule missing from App.vue`).not.toBeNull();
        if (!rule) continue;
        expect(rule[0], `.badge.${cls}`).toContain(`var(${fam})`);
        expect(rule[0], `.badge.${cls}`).not.toContain('var(--accent)');
      }
    }
  });

  it('first paint is light for everyone — the OS never decides here', () => {
    // tokens.css ships a prefers-color-scheme block for surfaces whose toggle is
    // optional. The collector opts out the way that file documents: a stamped
    // data-flanj-theme="light" on <html>, present in the served HTML rather than
    // applied by script, so a dark-OS visitor never sees a dark flash.
    const html = readFileSync(join(src, '..', 'index.html'), 'utf8');
    expect(html).toMatch(/<html lang="en" data-flanj-theme="light">/);
    expect(tokens).toContain(':root:not([data-flanj-theme="light"])');
  });

  it('every control draws the token focus ring on :focus-visible', () => {
    // Only .edge-contract-link had a :focus-visible rule before Blueprint; a
    // keyboard user tabbing through the rest saw the browser default or, on the
    // inputs, `outline: none`. Each control class must reference the ring
    // tokens from a :focus-visible selector, and no rule may switch the outline
    // off on plain :focus any more.
    const styles = Object.fromEntries(sfcs.map((f) => [f, styleOf(f)]));
    const all = Object.values(styles).join('\n');
    const ringRules = all.match(/[^{}]*:focus-visible[^{]*\{[^}]*\}/g) ?? [];
    const hasRing = (cls: string) =>
      ringRules.some((r) => r.includes(`${cls}:focus-visible`) && r.includes('var(--focus-ring)') && r.includes('var(--focus-offset)'));
    const controls = [
      '.btn', '.tabs button', '.seg button', '.pill-btn', '.live-btn', '.pending-bar',
      '.tr-search', '.tr-select', '.tr-clear', '.tr-chk input', '.doc-link',
      // A traffic row toggles the call's detail: it is a control (UX review 2026-09-14).
      '.tr-row',
      '.edge-contract-link', '.uploader-host input', '.dropzone',
      '.field input', 'textarea', '.link-input', '.disclosure'
    ];
    expect(controls.filter((c) => !hasRing(c)), 'controls without the token focus ring').toEqual([]);
    for (const [f, style] of Object.entries(styles)) {
      const off = style.match(/[^{}]*:focus\s*\{[^}]*outline:\s*none[^}]*\}/g) ?? [];
      expect(off, `outline switched off on :focus in ${f}`).toEqual([]);
    }
    // A ring drawn at a 2px offset lies outside the control's box, so a parent
    // that clips its overflow erases it while every assertion above stays
    // green — the Appearance segmented control shipped exactly that way once
    // (`.seg { overflow: hidden }`, the buttons' :focus-visible ring invisible
    // in both themes). The wrapper rules of the grouped controls must not clip.
    // happy-dom cannot paint, so the rendered ring on the segment is verified
    // by screenshot (scratchpad shots/wave-a-collector/seg-focus-*.png).
    const wrappers = ['.seg', '.tabs'];
    const clipped = wrappers.filter((w) => {
      const rule = all.match(new RegExp(`(^|[\\s}])${w.replace('.', '\\.')}\\s*\\{[^}]*\\}`, 'm'));
      return rule !== null && /overflow(-x|-y)?\s*:\s*(hidden|clip)/.test(rule[0]);
    });
    expect(clipped, 'grouped-control wrappers that would clip the offset focus ring').toEqual([]);
  });

  it('no surface rule declares a custom property the canonical file already defines', () => {
    // peek.css once declared `--ok-ink: #1f6d3a` on the same :root as the
    // vendored file and, loaded second, silently shadowed the canonical value.
    // Any name the vault defines belongs to the vault: a surface may READ it
    // and may declare its own names, never redeclare one of these.
    const canonical = customPropertyNames(tokens);
    expect(canonical.has('--ok-ink')).toBe(true);
    // The scanner must bite before it is trusted: peek's exact declaration.
    expect(collidingDeclarations(':root { --ok-ink: #1f6d3a; --peek-only: 1px; }', canonical)).toEqual(['--ok-ink']);
    expect(collidingDeclarations('.x { color: var(--ok-ink); --peek-only: 1px; }', canonical)).toEqual([]);
    const surfaces = [
      ...sfcs.map((f) => [f, styleOf(f)] as const),
      ...readdirSync(src)
        .filter((f) => f.endsWith('.css') && f !== 'tokens.css')
        .map((f) => [f, read(f)] as const)
    ];
    for (const [f, css] of surfaces) {
      expect(collidingDeclarations(css, canonical), `${f} redeclares canonical tokens`).toEqual([]);
    }
  });

  it('the vendored body below the header is the vault file, byte for byte', () => {
    const body = tokens.slice(tokens.indexOf(CANONICAL_BANNER));
    // The digest of the vault file as vendored. It changes only on a deliberate
    // re-vendor, which is the one place this line is edited; anywhere else, a
    // changed digest means somebody hand-edited the palette here.
    expect(createHash('sha256').update(body).digest('hex')).toBe(
      '45515ff6d45f7e7e31269b4ca001ae62811c309d9a6d16c12a106cb968657ffe'
    );
    // Where the docs vault is checked out beside this repo (the workspace
    // layout), compare the bytes directly as well — CI has no vault, so the
    // digest above is what it enforces.
    const vault = findVaultTokens();
    if (vault) expect(body).toBe(readFileSync(vault, 'utf8'));
  });

  it('the hex bolt is one inline symbol per document, and no bolt tone is an inline style', () => {
    // Blueprint port rule: the bolt ships ONCE as <symbol id="hxbolt"> and every
    // use is a class-toned <use>. A second symbol duplicates an id; an inline
    // style="color:…;--l:…" on a bolt (the kit's own idiom) is a palette
    // literal the tokens cannot reach. Comments are stripped first — both the
    // template and this file's own prose mention the symbol by name.
    const templateOf = (f: string) => {
      const src = read(f);
      const start = src.indexOf('<template>');
      const end = src.lastIndexOf('</template>');
      return start < 0 ? '' : src.slice(start, end).replace(/<!--[\s\S]*?-->/g, '');
    };
    const templates = sfcs.map((f) => [f, templateOf(f)] as const);
    const symbols = templates.reduce((n, [, t]) => n + (t.match(/<symbol id="hxbolt"/g) ?? []).length, 0);
    expect(symbols, 'exactly one <symbol id="hxbolt"> across the SFC templates').toBe(1);
    const uses = templates.reduce((n, [, t]) => n + (t.match(/<use href="#hxbolt"/g) ?? []).length, 0);
    expect(uses).toBeGreaterThan(0);
    for (const [f, t] of templates) {
      expect(t.match(/\sstyle="/g), `inline style attribute in ${f}'s template`).toBeNull();
    }
    // The scanner must bite before it is trusted: the kit's own bolt markup.
    expect('<svg class="hx" style="color:var(--red)"><use href="#hxbolt"/></svg>'.match(/\sstyle="/g)).not.toBeNull();
  });

  it('dimmed states dim with the palette, never with opacity', () => {
    // The kit dims an acknowledged finding (`.cx-find.acked { opacity: .55 }`)
    // and a closed thread row (`.cx-th-row.closed { opacity: .6 }`); the port
    // inherited both, plus its own `.meta-line .dim { opacity: .5 }`. At those
    // opacities 12–13px --ink-soft text measured 2.2–2.9:1 (UX review
    // 2026-09-14) — "evidence is never hidden" while hiding it from anyone
    // with low vision. Dimming is a palette move (--ink-soft, --rule-soft,
    // the steel chip); no rule whose selector names a dimmed state may set
    // opacity. A disabled INPUT is not a dimmed state and is not matched.
    const dimmed = /\.(acked|closed|dim)\b/;
    for (const f of sfcs) {
      const rules = styleOf(f).replace(/\/\*[\s\S]*?\*\//g, '').match(/[^{}]+\{[^}]*\}/g) ?? [];
      const bad = rules.filter((r) => dimmed.test(r.slice(0, r.indexOf('{'))) && /(^|[\s;{])opacity\s*:/.test(r));
      expect(bad, `opacity on a dimmed state in ${f}`).toEqual([]);
    }
    // The scanner must bite before it is trusted: the kit's own rule.
    expect(/(^|[\s;{])opacity\s*:/.test('.cx-find.acked{opacity:.55}')).toBe(true);
  });

  it("the traffic head's cells keep the head's register: per-cell rules are scoped to rows", () => {
    // `.c-when { font-size: 12.5px }` and `.c-corr { color: var(--ink) }` also
    // matched the HEAD's spans (same class names), so CAPTURED rendered larger
    // and CORRELATION in full ink while every other head was 10.5px --ink-soft
    // (UX review 2026-09-14). Every rule that names a traffic cell class must
    // be scoped under `.tr-row`.
    const style = styleOf('App.vue').replace(/\/\*[\s\S]*?\*\//g, '');
    const rules = style.match(/[^{}]+\{[^}]*\}/g) ?? [];
    const cell = /\.c-(when|call|peer|status|corr|mark)\b/;
    const unscoped = rules
      .map((r) => r.slice(0, r.indexOf('{')).trim())
      .filter((sel) => sel.split(',').some((part) => cell.test(part) && !/\.tr-row\b/.test(part)));
    expect(unscoped, 'traffic cell rules that would also style the head').toEqual([]);
    // The scanner must bite before it is trusted.
    expect(cell.test('.c-when') && !/\.tr-row\b/.test('.c-when')).toBe(true);
  });

  it('the green quarantine is gone — --ok is canonical and nothing references --verified*', () => {
    // tokens-pending.css held a `--verified*` family while the canonical set had
    // no positive colour. Blueprint ships `--ok*`; the file was deleted on
    // re-vendor and must not come back, nor may any surface rule still point at
    // the retired names (an unresolved var() renders as no colour at all).
    expect(existsSync(join(src, 'tokens-pending.css'))).toBe(false);
    expect(read('main.ts')).not.toContain('tokens-pending');
    for (const f of sfcs) {
      expect(read(f), `retired --verified* reference in ${f}`).not.toMatch(/--verified(?:-ink|-contrast)?\b/);
    }
    expect(tokens).toContain('--ok-ink:');
  });
});
