import { describe, expect, it } from 'vitest';
import { THEME_STORAGE_KEY, applyTheme, loadThemePref, normalizeTheme, saveThemePref, themeAttribute } from './theme';

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
    removeAttribute: (n: string) => void attrs.delete(n),
    get: (n: string) => attrs.get(n) ?? null
  };
}

describe('theme preference (System / Light / Dark, default System)', () => {
  it('normalizes: unknown and missing values are System', () => {
    expect(normalizeTheme('light')).toBe('light');
    expect(normalizeTheme('dark')).toBe('dark');
    expect(normalizeTheme('system')).toBe('system');
    expect(normalizeTheme(null)).toBe('system');
    expect(normalizeTheme(undefined)).toBe('system');
    expect(normalizeTheme('sepia')).toBe('system');
  });

  it('System carries NO data-theme attribute; explicit choices do', () => {
    expect(themeAttribute('system')).toBeNull();
    expect(themeAttribute('light')).toBe('light');
    expect(themeAttribute('dark')).toBe('dark');
    const root = fakeRoot();
    applyTheme('dark', root);
    expect(root.get('data-theme')).toBe('dark');
    applyTheme('light', root);
    expect(root.get('data-theme')).toBe('light');
    applyTheme('system', root);
    expect(root.get('data-theme')).toBeNull();
  });

  it('persists under vinifera.theme; System clears the key', () => {
    const s = fakeStorage();
    saveThemePref('dark', s);
    expect(s.dump()).toEqual({ [THEME_STORAGE_KEY]: 'dark' });
    expect(loadThemePref(s)).toBe('dark');
    saveThemePref('system', s);
    expect(s.dump()).toEqual({});
    expect(loadThemePref(s)).toBe('system');
    expect(loadThemePref(fakeStorage({ [THEME_STORAGE_KEY]: 'light' }))).toBe('light');
  });
});
