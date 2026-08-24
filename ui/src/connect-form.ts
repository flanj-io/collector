// Connect-form seeding (v0.5 follow-up bugfix): background polls refresh the
// Connect state every ~5s, and the form must NEVER lose what the user typed —
// a seed may only fill fields the user has not touched. Explicit entry points
// (open "Change contact" / "Add address", Cancel, successful submit) force a
// full re-seed and clear the touched set.
import type { ConnectState } from './threads';

export interface ConnectFormValues {
  org: string;
  name: string;
  email: string;
  localUrl: string;
}

export type ConnectFormTouched = { [K in keyof ConnectFormValues]: boolean };

export function untouched(): ConnectFormTouched {
  return { org: false, name: false, email: false, localUrl: false };
}

/** The values a fresh seed would produce from the current state. */
export function seededValues(state: ConnectState | null, defaultOrg: string | undefined, origin: string): ConnectFormValues {
  return {
    org: state?.consumer_display_name || defaultOrg || '',
    name: state?.contact_display_name || '',
    email: state?.contact_email || '',
    localUrl: state?.local_ui_url || origin
  };
}

/**
 * Merge a seed into the current values. A background poll (`force` false) may fill a field only when
 * there is nothing to clobber: the field is pristine (never typed in), or it is empty again — except
 * the field the user is focused on right now, which is never touched by a background seed.
 * `force` (an explicit user action — open edit, Cancel, after submit) re-seeds everything.
 */
export function applySeed(
  current: ConnectFormValues,
  seeded: ConnectFormValues,
  touched: ConnectFormTouched,
  force: boolean,
  focused: keyof ConnectFormValues | null = null
): ConnectFormValues {
  const pick = (k: keyof ConnectFormValues): string => {
    if (force) return seeded[k];
    if (k === focused) return current[k];
    if (!touched[k] || current[k] === '') return seeded[k];
    return current[k];
  };
  return { org: pick('org'), name: pick('name'), email: pick('email'), localUrl: pick('localUrl') };
}
