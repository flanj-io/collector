// The header org pill shows a NAME someone typed (consumer_display_name) — the same
// string the provider sees on every thread and the one e2e reads back verbatim
// (mcp.spec.ts "baseline": `expect(overviewText).toContain(CONSUMER)`). The Blueprint
// port made every `.pill` uppercase mono, which turned "CustomerX" into "CUSTOMERX" in
// the rendered text (innerText applies text-transform) and broke that read. Uppercase
// is for labels; a name keeps its own case. Structural gate, like tokens.test.ts:
// the rule and the class must both be there, in App.vue.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const app = readFileSync(join(__dirname, 'App.vue'), 'utf8');

describe('the org pill keeps the name\'s own case', () => {
  it('the pill bound to orgPillName carries .pill-name', () => {
    const span = app.match(/<span[^>]*v-if="orgPillName"[^>]*>/)?.[0];
    expect(span, 'the org pill span').toBeTruthy();
    expect(span!).toMatch(/class="[^"]*\bpill-name\b[^"]*"/);
  });

  it('.pill-name turns the uppercase transform off', () => {
    const rule = app.match(/^\.pill-name\s*\{([^}]*)\}/m)?.[1] ?? null;
    expect(rule, '.pill-name rule in App.vue').not.toBeNull();
    expect(rule!.replace(/\s+/g, ' ')).toMatch(/text-transform:\s*none/);
  });
});
