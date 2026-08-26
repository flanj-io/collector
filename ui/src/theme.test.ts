import { describe, expect, it } from 'vitest';
import {
  THEME_STORAGE_KEY,
  applyTheme,
  hasStoredThemeChoice,
  loadThemePref,
  normalizeTheme,
  saveThemePref,
  shouldShowThemeFlipNotice,
  themeAttribute
} from './theme';

function fakeStorage(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init));
  return {
    getItem: (k: string) => (m.has(k) ? m.get(k)! : null),
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    dump: () => Object.fromEntries(m)
  };
}

function fakeRoot() {
  const attrs = new Map<string, string>();
  return {
    setAttribute: (n: string, v: string) => void attrs.set(n, v),
    get: (n: string) => attrs.get(n) ?? null
  };
}

describe('theme preference (Light / Dark, default Light — ux-design-v2 §3)', () => {
  it('normalizes: unknown, missing and the legacy "system" value are Light', () => {
    expect(normalizeTheme('light')).toBe('light');
    expect(normalizeTheme('dark')).toBe('dark');
    expect(normalizeTheme(null)).toBe('light');
    expect(normalizeTheme(undefined)).toBe('light');
    expect(normalizeTheme('sepia')).toBe('light');
    // The System users are exactly who the flip moves to light.
    expect(normalizeTheme('system')).toBe('light');
  });

  it('always stamps data-theme — there is no attribute-less state any more', () => {
    expect(themeAttribute('light')).toBe('light');
    expect(themeAttribute('dark')).toBe('dark');
    const root = fakeRoot();
    applyTheme('dark', root);
    expect(root.get('data-theme')).toBe('dark');
    applyTheme('light', root);
    expect(root.get('data-theme')).toBe('light');
  });

  it('persists under vinifera.theme; Light is written explicitly, not by absence', () => {
    const s = fakeStorage();
    expect(loadThemePref(s)).toBe('light'); // key absent = light
    saveThemePref('dark', s);
    expect(s.dump()).toEqual({ [THEME_STORAGE_KEY]: 'dark' });
    expect(loadThemePref(s)).toBe('dark');
    saveThemePref('light', s);
    expect(s.dump()).toEqual({ [THEME_STORAGE_KEY]: 'light' });
    expect(loadThemePref(s)).toBe('light');
  });

  it('migration spares an explicit choice and moves System users to light', () => {
    expect(loadThemePref(fakeStorage({ [THEME_STORAGE_KEY]: 'dark' }))).toBe('dark');
    expect(loadThemePref(fakeStorage({ [THEME_STORAGE_KEY]: 'light' }))).toBe('light');
    expect(loadThemePref(fakeStorage())).toBe('light');
  });

  it('knows whether this browser ever chose', () => {
    expect(hasStoredThemeChoice(fakeStorage())).toBe(false);
    expect(hasStoredThemeChoice(fakeStorage({ [THEME_STORAGE_KEY]: 'system' }))).toBe(false);
    expect(hasStoredThemeChoice(fakeStorage({ [THEME_STORAGE_KEY]: 'light' }))).toBe(true);
    expect(hasStoredThemeChoice(fakeStorage({ [THEME_STORAGE_KEY]: 'dark' }))).toBe(true);
  });
});

describe('the one-time light-default notice is gated on BOTH conditions (§3.4)', () => {
  const base = { storedChoice: false, heldPriorData: true, dismissed: false };

  it('shows for a System user on a collector that held prior data', () => {
    expect(shouldShowThemeFlipNotice(base)).toBe(true);
  });

  it('never shows on a fresh install, even with no stored choice', () => {
    expect(shouldShowThemeFlipNotice({ ...base, heldPriorData: false })).toBe(false);
  });

  it('never shows to someone who already chose a theme', () => {
    expect(shouldShowThemeFlipNotice({ ...base, storedChoice: true })).toBe(false);
    expect(shouldShowThemeFlipNotice({ storedChoice: true, heldPriorData: false, dismissed: false })).toBe(false);
  });

  it('never returns once dismissed on this browser', () => {
    expect(shouldShowThemeFlipNotice({ ...base, dismissed: true })).toBe(false);
  });
});
