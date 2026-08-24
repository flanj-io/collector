import { describe, expect, it } from 'vitest';
import { applySeed, seededValues, untouched } from './connect-form';
import type { ConnectState } from './threads';

const state = {
  status: 'disconnected',
  consumer_display_name: 'Acme Consumer Ltd',
  contact_display_name: 'Dana',
  contact_email: 'ops@acme.example',
  local_ui_url: 'http://collector:5335'
} as unknown as ConnectState;

describe('connect form seeding (background polls must never clobber typed text)', () => {
  it('seeds every field when nothing is touched', () => {
    const next = applySeed(
      { org: '', name: '', email: '', localUrl: '' },
      seededValues(state, undefined, 'http://origin'),
      untouched(),
      false
    );
    expect(next).toEqual({ org: 'Acme Consumer Ltd', name: 'Dana', email: 'ops@acme.example', localUrl: 'http://collector:5335' });
  });

  it('a touched field keeps the user text across a background seed; pristine fields still fill', () => {
    const touched = { ...untouched(), email: true };
    const next = applySeed(
      { org: '', name: '', email: 'typing@partial', localUrl: '' },
      seededValues(state, undefined, 'http://origin'),
      touched,
      false
    );
    expect(next.email).toBe('typing@partial');
    expect(next.org).toBe('Acme Consumer Ltd'); // late-arriving prefill still lands on pristine fields
  });


  it("a field typed-then-cleared re-prefills on the next background seed — unless it is focused", () => {
    const touched = { ...untouched(), email: false }; // markTouched saw '' → released
    const cleared = applySeed(
      { org: 'Acme Consumer Ltd', name: '', email: '', localUrl: '' },
      seededValues(state, undefined, 'http://origin'),
      touched,
      false
    );
    expect(cleared.email).toBe('ops@acme.example'); // empty again → nothing to clobber → prefill
    const focused = applySeed(
      { org: 'Acme Consumer Ltd', name: '', email: '', localUrl: '' },
      seededValues(state, undefined, 'http://origin'),
      touched,
      false,
      'email'
    );
    expect(focused.email).toBe(''); // never seed under the user's cursor
  });

  it('a touched-but-now-empty field also re-prefills (nothing to clobber)', () => {
    const next = applySeed(
      { org: '', name: '', email: '', localUrl: '' },
      seededValues(state, undefined, 'http://origin'),
      { ...untouched(), email: true },
      false
    );
    expect(next.email).toBe('ops@acme.example');
  });

  it('force (an explicit user action) overrides touched fields', () => {
    const touched = { org: true, name: true, email: true, localUrl: true };
    const next = applySeed(
      { org: 'x', name: 'y', email: 'z@z', localUrl: 'w' },
      seededValues(state, undefined, 'http://origin'),
      touched,
      true
    );
    expect(next).toEqual({ org: 'Acme Consumer Ltd', name: 'Dana', email: 'ops@acme.example', localUrl: 'http://collector:5335' });
  });

  it('null state falls back to defaultOrg + origin, still honoring touched', () => {
    const next = applySeed(
      { org: '', name: '', email: 'kept@typed', localUrl: '' },
      seededValues(null, 'Health Org', 'http://origin'),
      { ...untouched(), email: true },
      false
    );
    expect(next).toEqual({ org: 'Health Org', name: '', email: 'kept@typed', localUrl: 'http://origin' });
  });
});
