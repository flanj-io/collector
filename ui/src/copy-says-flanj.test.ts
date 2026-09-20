// The product's own vocabulary for the hosted side is "Flanj" (see
// ConnectPanel.vue: "Connect to Flanj" / "your Flanj workspace") — "control
// plane" is this repo's internal component name for that side, and it must
// never leak into copy a person using the collector's UI can read. This is a
// structural gate, like org-pill-case.test.ts: it scans the SOURCE rather than
// a mounted component, so it catches a string nobody's test happens to render.
//
// The extraction (template text incl. static attrs + bound-expression/interp
// string literals; script string literals; comments and bare JS expressions
// stripped) mirrors what extension/flanjui/naming_test.go already does in Go
// for its own denylist scan of these same sources, so a person reading either
// guard recognizes the other.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { extname, join } from 'node:path';
import { describe, expect, it } from 'vitest';

function listSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      out.push(...listSourceFiles(p));
      continue;
    }
    if (name.endsWith('.test.ts') || name.endsWith('.test.tsx')) continue;
    if (extname(name) === '.vue' || extname(name) === '.ts') out.push(p);
  }
  return out;
}

const RE_SCRIPT_BLOCK = /<script[^>]*>([\s\S]*?)<\/script>/g;
const RE_TEMPLATE_BLOCK = /<template>([\s\S]*)<\/template>/;
const RE_STR_LIT = /`(?:[^`\\]|\\.)*`|'(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"/g;
const RE_BOUND_ATTR = /\s(?::[\w.-]+|@[\w.-]+|v-[\w.:-]+)="[^"]*"/g;
const RE_INTERP = /\{\{[\s\S]*?\}\}/g;
const RE_TAG = /<[^>]*>/g;
const RE_HTML_COMMENT = /<!--[\s\S]*?-->/g;
const RE_LINE_COMMENT = /^\s*\/\/.*$/gm;
const RE_BLOCK_COMMENT = /\/\*[\s\S]*?\*\//g;
const RE_STATIC_ATTR = /\s(?:placeholder|title|aria-label|alt|value|label)="([^"]*)"/g;

interface Chunk {
  file: string;
  text: string;
}

// userFacingChunks extracts what a person could read: template text (tags,
// bound expressions and interpolations stripped down to their own string
// literals; static placeholder/title/aria attributes kept) + every string
// literal in script/ts, with comments removed.
function userFacingChunks(file: string, src: string, isVue: boolean): Chunk[] {
  const out: Chunk[] = [];

  let scriptSrc = src;
  if (isVue) {
    scriptSrc = '';
    for (const m of src.matchAll(RE_SCRIPT_BLOCK)) scriptSrc += m[1] + '\n';

    const tm = RE_TEMPLATE_BLOCK.exec(src);
    if (tm) {
      const tpl = tm[1];
      for (const am of tpl.matchAll(RE_STATIC_ATTR)) out.push({ file, text: am[1] });

      let clean = tpl.replace(RE_HTML_COMMENT, '');
      for (const re of [RE_INTERP, RE_BOUND_ATTR]) {
        for (const em of clean.matchAll(re)) {
          for (const lit of em[0].matchAll(RE_STR_LIT)) out.push({ file, text: lit[0] });
        }
      }
      clean = clean.replace(RE_BOUND_ATTR, ' ').replace(RE_INTERP, ' ').replace(RE_TAG, '\n');
      for (const ln of clean.split('\n')) {
        const s = ln.trim();
        if (s) out.push({ file, text: s });
      }
    }
  }

  const clean = scriptSrc.replace(RE_BLOCK_COMMENT, '').replace(RE_LINE_COMMENT, '');
  for (const ln of clean.split('\n')) {
    for (const lit of ln.matchAll(RE_STR_LIT)) {
      if (lit[0].startsWith("'/api/") || lit[0].startsWith('"/api/')) continue; // wire paths
      out.push({ file, text: lit[0] });
    }
  }
  return out;
}

describe('user-visible collector UI copy never says "control plane"', () => {
  it('says Flanj — the product vocabulary for the hosted side — everywhere a person using the UI can read', () => {
    const files = listSourceFiles(__dirname);
    expect(files.length).toBeGreaterThan(0);

    const deny = /control[ -]plane/i;
    const hits: string[] = [];
    for (const file of files) {
      const raw = readFileSync(file, 'utf8');
      for (const chunk of userFacingChunks(file, raw, file.endsWith('.vue'))) {
        if (deny.test(chunk.text)) {
          hits.push(`${file}: ${chunk.text.trim()}`);
        }
      }
    }
    expect(hits, `found "control plane" in user-visible copy:\n${hits.join('\n')}`).toEqual([]);
  });
});
