import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

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

describe('design tokens', () => {
  const tokens = read('tokens.css');

  it('tokens.css is vendored, not authored here', () => {
    expect(tokens.startsWith('/* vendored from docs/design/tokens.css — do not edit here.')).toBe(true);
    expect(tokens).toContain('Canonical source lives in the docs vault');
    // The canonical file's own banner must survive the copy — its absence means
    // somebody hand-wrote a palette into this file instead of re-vendoring.
    expect(tokens).toContain('/* Flanj design tokens — CANONICAL SOURCE.');
  });

  it('tokens.css carries the full triad in both schemes', () => {
    for (const scheme of [':root {', '[data-theme="dark"] {']) {
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
        const rule = style.match(new RegExp(`\\.badge\\.${cls} \\{[^}]*\\}`));
        if (rule) expect(rule[0], `.badge.${cls}`).toContain(`var(${fam})`);
        if (rule) expect(rule[0], `.badge.${cls}`).not.toContain('var(--accent)');
      }
    }
  });

  it('first paint is light for everyone — the OS never decides here', () => {
    // tokens.css ships a prefers-color-scheme block for surfaces whose toggle is
    // optional. The collector opts out the way that file documents: a stamped
    // data-theme="light" on <html>, present in the served HTML rather than
    // applied by script, so a dark-OS visitor never sees a dark flash.
    const html = readFileSync(join(src, '..', 'index.html'), 'utf8');
    expect(html).toMatch(/<html lang="en" data-theme="light">/);
    expect(tokens).toContain(':root:not([data-theme="light"])');
  });

  it('the pending family names itself as non-canonical and points at its fork', () => {
    const pending = read('tokens-pending.css');
    expect(pending.startsWith('/* NOT CANONICAL')).toBe(true);
    expect(pending).toContain('This file exists to be deleted.');
    // Only the verified family may live here — anything else belongs in the vault.
    const declared = [...pending.matchAll(/^\s*(--[\w-]+):/gm)].map((m) => m[1]);
    expect([...new Set(declared)].sort()).toEqual(['--verified', '--verified-contrast', '--verified-ink']);
  });
});
