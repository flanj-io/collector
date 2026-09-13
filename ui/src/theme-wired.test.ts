// @vitest-environment happy-dom
//
// The theme toggle as WIRED, not as a pure module: mount the app, open
// Settings, click the Appearance segments, and read <html>'s attribute. The
// pure theme.test.ts proves applyTheme writes whatever name it is given; this
// proves the name the click path writes is the one the vendored tokens.css
// selects on. A rename that lands in only one of the two — the 2026-09
// data-theme -> data-flanj-theme move — leaves a toggle that looks active and
// does nothing, and only a mounted test can see that.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import App from './App.vue';
import { THEME_STORAGE_KEY } from './theme';

const THEME_ATTR = 'data-flanj-theme';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

let wrapper: VueWrapper | null = null;

beforeEach(() => {
  localStorage.clear();
  window.location.hash = '';
  document.documentElement.removeAttribute(THEME_ATTR);
  vi.useFakeTimers();
  // A collector with nothing in it: every read answers an empty body. The
  // control under test needs no data.
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => json({}))
  );
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mountApp(): Promise<VueWrapper> {
  const w = mount(App, { attachTo: document.body });
  wrapper = w;
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 6; i++) await Promise.resolve();
  await w.vm.$nextTick();
  return w;
}

describe('Appearance, as wired', () => {
  it('clicking Dark then Light stamps <html> with the attribute tokens.css selects on', async () => {
    const w = await mountApp();
    const settingsTab = w.findAll('nav.tabs button').find((b) => b.text().startsWith('Settings'));
    expect(settingsTab, 'Settings tab').toBeDefined();
    await settingsTab!.trigger('click');

    const seg = w.find('.seg[aria-label="Theme"]');
    expect(seg.exists()).toBe(true);
    const dark = seg.findAll('button').find((b) => b.text() === 'Dark')!;
    const light = seg.findAll('button').find((b) => b.text() === 'Light')!;

    await dark.trigger('click');
    expect(document.documentElement.getAttribute(THEME_ATTR)).toBe('dark');
    expect(dark.attributes('aria-pressed')).toBe('true');
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark');

    await light.trigger('click');
    expect(document.documentElement.getAttribute(THEME_ATTR)).toBe('light');
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('light');

    // Nothing else may be stamped in the attribute's place: the old name
    // stays absent on both clicks.
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  });

  it('the attribute the click writes is the one the vendored dark block selects on', () => {
    const tokens = readFileSync(join(__dirname, 'tokens.css'), 'utf8');
    expect(tokens).toContain(`[${THEME_ATTR}="dark"] {`);
    const html = readFileSync(join(__dirname, '..', 'index.html'), 'utf8');
    expect(html).toContain(`<html lang="en" ${THEME_ATTR}="light">`);
  });
});
