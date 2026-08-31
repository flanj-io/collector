import { describe, expect, it } from 'vitest';
import {
  BADGE_USER,
  EDIT_NAME_LABEL,
  RENAME_LABEL,
  SAVE_ERROR,
  SUGGEST_FAILED_NOTE,
  SUGGEST_NAME_CAP,
  SUGGEST_TOO_LONG,
  badgeLabel,
  beginEdit,
  cancelEdit,
  editorClosed,
  placeholderFor,
  pollArrived,
  renameLabel,
  saveFailed,
  saveStart,
  suggestLabelFor,
  suggestTooLong,
  toggleSuggest,
  typeDraft
} from './edge-names';

const row = {
  peer_host: 'api.stripe.com',
  registrable_domain: 'stripe.com',
  display_name: 'Stripe',
  name_source: 'directory'
};

describe('copy', () => {
  it('renders the one provenance badge exactly; every other tier carries none', () => {
    expect(badgeLabel('user')).toBe(BADGE_USER);
    expect(BADGE_USER).toBe('named by you');
    // 'contract' — the title of the contract uploaded for this domain — carries
    // no badge: the row already shows `contract v1.0.0 · uploaded 12d ago` in
    // the meta line below the host, which says where the name came from more
    // precisely than a chip AND is a control. It replaced the 'config' tier,
    // whose badge went with it.
    expect(badgeLabel('contract')).toBe('');
    expect(badgeLabel('config')).toBe('');
    // A directory name carries NO badge (owner ruling 2026-08-31): the directory is
    // infrastructure, the host stays visible beside the name, and the remaining badges
    // mark only what the operator themselves did.
    expect(badgeLabel('directory')).toBe('');
    // 'auto' carries no badge either (owner ruling 2026-08-31): an unnamed row is
    // simply its host, so a chip saying "auto" announces nothing actionable.
    expect(badgeLabel('auto')).toBe('');
    // Unknown / absent sources read as auto — never an empty badge.
    expect(badgeLabel(undefined)).toBe('');
    expect(badgeLabel('???')).toBe('');
  });

  it('offers Rename on non-user rows and Edit name on user-named rows', () => {
    expect(renameLabel('directory')).toBe(RENAME_LABEL);
    expect(renameLabel(undefined)).toBe(RENAME_LABEL);
    expect(renameLabel('user')).toBe(EDIT_NAME_LABEL);
  });

  it('names the domain in the placeholder and the opt-in label', () => {
    expect(placeholderFor('stripe.com')).toBe('Display name for stripe.com');
    expect(suggestLabelFor('stripe.com')).toBe(
      'Suggest this name for stripe.com to the Flanj directory — leaves this collector for review'
    );
  });

  it('keeps the exact error and partial-success strings', () => {
    expect(SAVE_ERROR).toBe("Couldn't save the name.");
    expect(SUGGEST_FAILED_NOTE).toBe("Name saved. The suggestion didn't reach the directory — it stays local.");
  });
});

describe('editor state machine', () => {
  it('begin-edit seeds from the row, with the opt-in UNCHECKED by default', () => {
    const s = beginEdit(row);
    expect(s.host).toBe('api.stripe.com');
    expect(s.domain).toBe('stripe.com');
    expect(s.draft).toBe('Stripe');
    expect(s.suggest).toBe(false); // opt-in per mapping — never pre-checked
    expect(s.busy).toBe(false);
    expect(s.error).toBe('');
  });

  it('begin-edit on an unnamed (auto) row starts with an empty draft', () => {
    const s = beginEdit({ peer_host: 'api.zz.dev', registrable_domain: 'zz.dev' });
    expect(s.draft).toBe('');
    expect(s.domain).toBe('zz.dev');
  });

  it('typing replaces the draft and clears a previous error', () => {
    let s = saveFailed(beginEdit(row));
    s = typeDraft(s, 'Stripe Payments');
    expect(s.draft).toBe('Stripe Payments');
    expect(s.error).toBe('');
  });

  it('a background poll never clobbers the open editor (typed text survives)', () => {
    let s = typeDraft(beginEdit(row), 'Half-typed na');
    s = toggleSuggest(s, true);
    const after = pollArrived(s);
    expect(after).toBe(s); // identity: the refresh may repaint rows, never the editor
    expect(after!.draft).toBe('Half-typed na');
    expect(after!.suggest).toBe(true);
    // A poll with no editor open stays no-editor.
    expect(pollArrived(null)).toBeNull();
  });

  it('save walks busy → closed on success', () => {
    const busy = saveStart(typeDraft(beginEdit(row), 'Stripe Payments'));
    expect(busy.busy).toBe(true);
    expect(editorClosed()).toBeNull();
  });

  it('a failed save keeps the draft and shows the copy-deck error (Retry path)', () => {
    let s = saveStart(typeDraft(beginEdit(row), 'Stripe Payments'));
    s = saveFailed(s);
    expect(s.busy).toBe(false);
    expect(s.error).toBe(SAVE_ERROR);
    expect(s.draft).toBe('Stripe Payments'); // Retry re-submits this exact draft
    // Retry: saveStart again from the same state.
    const retry = saveStart(s);
    expect(retry.draft).toBe('Stripe Payments');
    expect(retry.error).toBe('');
  });

  it('cancel closes the editor and discards nothing server-side', () => {
    expect(editorClosed()).toBeNull();
    expect(cancelEdit(beginEdit(row))).toBeNull();
    expect(cancelEdit(null)).toBeNull();
  });

  it('cancel is inert while a save is in flight (Escape mirrors the disabled button)', () => {
    const busy = saveStart(typeDraft(beginEdit(row), 'Stripe Payments'));
    expect(busy.busy).toBe(true);
    const after = cancelEdit(busy);
    expect(after).toBe(busy); // identity while busy — the editor stays open
    // Once the save settles (here: failed), cancel closes as usual.
    expect(cancelEdit(saveFailed(busy))).toBeNull();
  });

  it('pre-checks the 64-char directory cap only while the suggest box is ticked', () => {
    const long = 'x'.repeat(SUGGEST_NAME_CAP + 1);
    const atCap = 'x'.repeat(SUGGEST_NAME_CAP);
    // Unticked: no block, whatever the length (local saves keep the 80 cap).
    let s = typeDraft(beginEdit(row), long);
    expect(suggestTooLong(s)).toBe(false);
    expect(saveStart(s).busy).toBe(true);
    // Ticked + over the cap: blocked — Save is identity, the message shows.
    s = toggleSuggest(s, true);
    expect(suggestTooLong(s)).toBe(true);
    expect(saveStart(s)).toBe(s);
    expect(saveStart(s).busy).toBe(false);
    expect(SUGGEST_TOO_LONG).toBe(
      'Suggestions are capped at 64 characters — shorten the name to suggest it, or save it locally.'
    );
    // Exactly 64 chars suggests fine.
    expect(suggestTooLong(typeDraft(s, atCap))).toBe(false);
    expect(saveStart(typeDraft(s, atCap)).busy).toBe(true);
    // Unticking the box lifts the block — the long name saves locally.
    const unticked = toggleSuggest(s, false);
    expect(suggestTooLong(unticked)).toBe(false);
    expect(saveStart(unticked).busy).toBe(true);
  });
});

// The host → name resolution the OTHER surfaces (Traffic, Contracts) read.

