// The Traffic tab's LIVE light was the last flat CSS hexagon in this UI — a `clip-path`
// polygon on a span, the pre-Blueprint mark (spotted 2026-09-14). The design system
// has one status light: the hex bolt, green when live. Structural gate, like tokens.test.ts:
// the light is a <use> of the one #hxbolt symbol with the ok tone, and no clip-path hexagon
// is drawn anywhere in a component's style block.
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const src = __dirname;
const app = readFileSync(join(src, 'App.vue'), 'utf8');

describe('the LIVE light is the hex bolt', () => {
  it('the live button carries a <use href="#hxbolt"> with the ok tone when live', () => {
    const btn = app.match(/<button\s+class="live-btn"[\s\S]*?<\/button>/)?.[0];
    expect(btn, 'the live button').toBeTruthy();
    expect(btn!).toMatch(/<svg class="hx sm live-dot"[^>]*><use href="#hxbolt" \/><\/svg>/);
    expect(btn!).toContain("'tone-ok'");
    expect(btn!).not.toContain('<span class="live-dot">');
  });

  it('no component draws a flat clip-path hexagon any more', () => {
    for (const f of readdirSync(src).filter((n) => n.endsWith('.vue'))) {
      const style = readFileSync(join(src, f), 'utf8').match(/<style[^>]*>([\s\S]*?)<\/style>/g) ?? [];
      expect(style.join('\n'), `clip-path polygon in ${f}`).not.toMatch(/clip-path:\s*polygon/);
    }
  });
});
