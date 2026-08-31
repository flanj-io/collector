// Edge naming (v1 phase 1) — the copy deck and the inline rename editor's
// state machine, pure and vitest-covered (App.vue only renders it).
//
// Poll-clobber discipline (the connect-form precedent, connect-form.ts): once
// an edit is open, the 5s background refresh of the edges list must NEVER
// touch what the user typed — the draft lives in this state, not in the row,
// and `pollArrived` is deliberately the identity on an open editor. Only
// explicit user events (type, toggle, save, cancel) and the save round-trip
// move the state.

export type EdgeNameSource = 'user' | 'config' | 'directory' | 'auto';

/** The naming fields a GET /api/edges outbound row carries. */
export interface NamedEdgeRow {
  peer_host: string;
  registrable_domain?: string;
  display_name?: string;
  name_source?: string;
}

// ─── Copy (exact strings from the v1p1 copy deck) ─────────────────────────
export const BADGE_USER = 'named by you';
export const BADGE_CONFIG = 'config';
export const RENAME_LABEL = 'Rename';
export const EDIT_NAME_LABEL = 'Edit name';
export const SAVE_LABEL = 'Save';
export const CANCEL_LABEL = 'Cancel';
export const REMOVE_NAME_LABEL = 'Remove name';
export const SAVE_ERROR = "Couldn't save the name.";
export const RETRY_LABEL = 'Retry';
/** Partial success: the save landed, only the opt-in suggestion did not. */
export const SUGGEST_FAILED_NOTE = "Name saved. The suggestion didn't reach the directory — it stays local.";
/** The directory's name cap (chars, post-NFKC on the CP side) — pre-checked
 * locally ONLY while the suggest box is ticked; local saves keep the 80 cap. */
export const SUGGEST_NAME_CAP = 64;
export const SUGGEST_TOO_LONG =
  'Suggestions are capped at 64 characters — shorten the name to suggest it, or save it locally.';

/** The provenance badge for a row's name source (absent/unknown reads auto). */
export function badgeLabel(source?: string): string {
  switch (source) {
    case 'user':
      return BADGE_USER;
    case 'config':
      return BADGE_CONFIG;
    default:
      // Everything else — 'directory' and 'auto' — carries NO badge (owner
      // rulings 2026-08-31). A badge is for what the OPERATOR did: renamed a
      // row, or carried a legacy YAML value they should migrate. A directory
      // name is infrastructure they do not manage (and the host stays visible
      // beside it, so nobody is misled); an 'auto' row is simply its own host,
      // so labelling it announces nothing they can act on.
      return '';
  }
}

/** The per-row affordance: `Edit name` on rows named by you, else `Rename`. */
export function renameLabel(source?: string): string {
  return source === 'user' ? EDIT_NAME_LABEL : RENAME_LABEL;
}

export function placeholderFor(domain: string): string {
  return `Display name for ${domain}`;
}

/** The OPT-IN checkbox label — names the egress plainly. Default UNCHECKED. */
export function suggestLabelFor(domain: string): string {
  return `Suggest this name for ${domain} to the Flanj directory — leaves this collector for review`;
}

// ─── Editor state machine ─────────────────────────────────────────────────

export interface EdgeNameEdit {
  /** The row's observed host (what the save posts). */
  host: string;
  /** The registrable domain — the naming key the copy renders. */
  domain: string;
  draft: string;
  /** The directory opt-in. ALWAYS starts false — opt-in per mapping. */
  suggest: boolean;
  busy: boolean;
  /** SAVE_ERROR after a failed save; Retry re-submits, the draft survives. */
  error: string;
}

/** Open the editor on a row, seeded with its current name (empty when auto). */
export function beginEdit(row: NamedEdgeRow): EdgeNameEdit {
  return {
    host: row.peer_host,
    domain: row.registrable_domain || row.peer_host,
    draft: row.display_name || '',
    suggest: false,
    busy: false,
    error: ''
  };
}

export function typeDraft(s: EdgeNameEdit, draft: string): EdgeNameEdit {
  return { ...s, draft, error: '' };
}

export function toggleSuggest(s: EdgeNameEdit, on: boolean): EdgeNameEdit {
  return { ...s, suggest: on };
}

/**
 * A background poll refreshed the edges list while the editor is open. The
 * typed draft (and the opt-in choice) survive UNTOUCHED — the row's new
 * server-side name never overwrites an open editor. Identity by design.
 */
export function pollArrived(s: EdgeNameEdit | null): EdgeNameEdit | null {
  return s;
}

/**
 * The client-side directory-cap pre-check: true ONLY while the suggest box is
 * ticked AND the draft exceeds 64 chars (code points). While true, Save is
 * blocked and SUGGEST_TOO_LONG shows; unticking the box (or shortening the
 * name) lifts the block — a local-only save keeps the 80 cap, server-enforced.
 */
export function suggestTooLong(s: EdgeNameEdit): boolean {
  return s.suggest && [...s.draft.trim()].length > SUGGEST_NAME_CAP;
}

/** Identity while the suggest pre-check blocks the save — Save is inert. */
export function saveStart(s: EdgeNameEdit): EdgeNameEdit {
  if (suggestTooLong(s)) return s;
  return { ...s, busy: true, error: '' };
}

/** A failed save keeps the editor open with the draft intact + the error. */
export function saveFailed(s: EdgeNameEdit): EdgeNameEdit {
  return { ...s, busy: false, error: SAVE_ERROR };
}

/** Save success and Cancel both close the editor (draft discarded). */
export function editorClosed(): null {
  return null;
}

/**
 * Cancel — the button AND the Escape key route through here. While a save is
 * in flight the editor must not vanish under the round-trip: identity while
 * busy (the Cancel button is disabled then; Escape gets the same guard here).
 */
export function cancelEdit(s: EdgeNameEdit | null): EdgeNameEdit | null {
  if (s && s.busy) return s;
  return editorClosed();
}

// ─── Host → name resolution for the OTHER surfaces ────────────────────────
// Owner ruling 2026-08-31: a display name substitutes for the raw host
// everywhere a host is shown, not only on the Edges panel — Traffic (the calls
// table's counterparty cell + the counterparty facet) and the Contracts
// provider cards resolve the same names.
//
// ONE resolution path, and this is it: the index is built from the edges list
// the Edges panel has ALREADY loaded (`GET /api/edges`, which resolved
// user > config > directory > auto server-side). No second lookup, no control
// plane request, no per-row re-resolution from the settings KV — a cache miss
// never reaches the network, permanently.



