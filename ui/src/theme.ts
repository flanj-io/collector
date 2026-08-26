// Theme preference (qfix-2026-08-25): System / Light / Dark, default System.
// Persisted in localStorage (`vinifera.theme`, mirroring the banner-dismiss
// pattern) and applied as `data-theme` on <html>: the attribute is set only for
// an EXPLICIT choice — the System state carries no attribute, and the CSS
// `prefers-color-scheme` media query (guarded with :root:not([data-theme]))
// tracks the OS live, so no JS listener is needed while System is selected.
// Pure helpers, unit-tested; DOM/storage access is injected.

export type ThemePref = 'system' | 'light' | 'dark';

export const THEME_STORAGE_KEY = 'vinifera.theme';

/** Anything unknown (including null / legacy values) is System. */
export function normalizeTheme(v: string | null | undefined): ThemePref {
  return v === 'light' || v === 'dark' ? v : 'system';
}

/** The `data-theme` value for a preference — null means "no attribute" (System). */
export function themeAttribute(pref: ThemePref): 'light' | 'dark' | null {
  return pref === 'system' ? null : pref;
}

interface ThemeRoot {
  setAttribute(name: string, value: string): void;
  removeAttribute(name: string): void;
}

/** Stamp (or clear) `data-theme` on the root element. */
export function applyTheme(pref: ThemePref, root: ThemeRoot = document.documentElement): void {
  const attr = themeAttribute(pref);
  if (attr) root.setAttribute('data-theme', attr);
  else root.removeAttribute('data-theme');
}

interface ThemeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export function loadThemePref(storage: ThemeStorage = localStorage): ThemePref {
  try {
    return normalizeTheme(storage.getItem(THEME_STORAGE_KEY));
  } catch {
    return 'system';
  }
}

export function saveThemePref(pref: ThemePref, storage: ThemeStorage = localStorage): void {
  try {
    if (pref === 'system') storage.removeItem(THEME_STORAGE_KEY);
    else storage.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    /* private mode etc. — the choice still applies for this page */
  }
}
