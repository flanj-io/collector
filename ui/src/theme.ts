// Theme preference (qfix2-2026-08-26, ux-design-v2 §3): LIGHT by default, dark
// opt-in. There is no System option — it was deleted, along with the
// `prefers-color-scheme` media query in the collector stylesheet that served it
// (leaving that query in place while defaulting to light would give a dark-OS
// user a dark first paint and quietly reintroduce System).
//
// Storage mirrors the thread page's `vinifera.peek.theme` exactly: the same key
// `vinifera.theme`, values 'light' | 'dark', KEY ABSENT = light. `data-theme` is
// always stamped on <html> — the dark palette lives under [data-theme="dark"]
// only.
//
// Migration (§3.4), which spares anyone who chose:
//   'dark'   → stays dark (an explicit choice, and it means the same thing in
//              both models)
//   'light'  → stays light
//   absent   → light, including on dark-OS machines (these are the System users)
//
// Pure helpers, unit-tested; DOM/storage access is injected.

export type ThemePref = 'light' | 'dark';

export const THEME_STORAGE_KEY = 'vinifera.theme';

/** Per-browser, permanent dismissal of the one-time light-default notice. */
export const THEME_FLIP_NOTICE_KEY = 'vinifera.theme.flip.dismissed';

/** Anything unknown (including null / the legacy 'system' value) is Light. */
export function normalizeTheme(v: string | null | undefined): ThemePref {
  return v === 'dark' ? 'dark' : 'light';
}

/** The `data-theme` value for a preference. Always an attribute now. */
export function themeAttribute(pref: ThemePref): 'light' | 'dark' {
  return pref;
}

interface ThemeRoot {
  setAttribute(name: string, value: string): void;
}

/** Stamp `data-theme` on the root element. */
export function applyTheme(pref: ThemePref, root: ThemeRoot = document.documentElement): void {
  root.setAttribute('data-theme', themeAttribute(pref));
}

interface ThemeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function loadThemePref(storage: ThemeStorage = localStorage): ThemePref {
  try {
    return normalizeTheme(storage.getItem(THEME_STORAGE_KEY));
  } catch {
    return 'light';
  }
}

/**
 * Persist the choice. Unlike the old model, LIGHT is written explicitly rather
 * than represented by key-absence: absence now means "never chose", which is
 * what gates the one-time notice.
 */
export function saveThemePref(pref: ThemePref, storage: ThemeStorage = localStorage): void {
  try {
    storage.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    /* private mode etc. — the choice still applies for this page */
  }
}

/** Has this browser ever chosen a theme? (A legacy/garbage value has not.) */
export function hasStoredThemeChoice(storage: ThemeStorage = localStorage): boolean {
  try {
    const v = storage.getItem(THEME_STORAGE_KEY);
    return v === 'light' || v === 'dark';
  } catch {
    return false;
  }
}

/**
 * The one-time light-default notice (§3.4), gated on BOTH conditions — getting
 * either one wrong either spams fresh installs or reaches nobody (§7 risk 8):
 *
 *   1. this browser has NO stored theme choice (i.e. it was on System), and
 *   2. the collector reports it held data before this upgrade
 *      (`held_prior_data` on /api/health) — so a fresh install never sees it.
 *
 * Plus the per-browser, permanent dismissal.
 */
export function shouldShowThemeFlipNotice(opts: {
  storedChoice: boolean;
  heldPriorData: boolean;
  dismissed: boolean;
}): boolean {
  return !opts.storedChoice && opts.heldPriorData && !opts.dismissed;
}
