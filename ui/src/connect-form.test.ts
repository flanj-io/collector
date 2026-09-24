import { describe, expect, it } from 'vitest';
import { applySeed, seededValues, untouched } from './connect-form';
import type { ConnectState } from './threads';

const state = {
  status: 'disconnected',
  collector_name: 'prod-eu',
  contact_display_name: 'Dana',
  contact_email: 'ops@acme.example',
  local_ui_url: 'http://collector:5335'
} as unknown as ConnectState;

describe('connect form seeding (background polls must never clobber typed text)', () => {
  it('seeds every field when nothing is touched', () => {
    const next = applySeed(
      { collectorName: '', name: '', email: '', localUrl: '' },
      seededValues(state, 'http://origin'),
      untouched(),
      false
    );
    expect(next).toEqual({ collectorName: 'prod-eu', name: 'Dana', email: 'ops@acme.example', localUrl: 'http://collector:5335' });
  });

  it('a touched field keeps the user text across a background seed; pristine fields still fill', () => {
    const touched = { ...untouched(), email: true };
    const next = applySeed(
      { collectorName: '', name: '', email: 'typing@partial', localUrl: '' },
      seededValues(state, 'http://origin'),
      touched,
      false
    );
    expect(next.email).toBe('typing@partial');
    expect(next.name).toBe('Dana'); // late-arriving prefill still lands on pristine fields
  });


  it("a field typed-then-cleared re-prefills on the next background seed — unless it is focused", () => {
    const touched = { ...untouched(), email: false }; // markTouched saw '' → released
    const cleared = applySeed(
      { collectorName: '', name: '', email: '', localUrl: '' },
      seededValues(state, 'http://origin'),
      touched,
      false
    );
    expect(cleared.email).toBe('ops@acme.example'); // empty again → nothing to clobber → prefill
    const focused = applySeed(
      { collectorName: '', name: '', email: '', localUrl: '' },
      seededValues(state, 'http://origin'),
      touched,
      false,
      'email'
    );
    expect(focused.email).toBe(''); // never seed under the user's cursor
  });

  it('a touched-but-now-empty field also re-prefills (nothing to clobber)', () => {
    const next = applySeed(
      { collectorName: '', name: '', email: '', localUrl: '' },
      seededValues(state, 'http://origin'),
      { ...untouched(), email: true },
      false
    );
    expect(next.email).toBe('ops@acme.example');
  });

  it('force (an explicit user action) overrides touched fields', () => {
    const touched = { collectorName: true, name: true, email: true, localUrl: true };
    const next = applySeed(
      { collectorName: 'v', name: 'y', email: 'z@z', localUrl: 'w' },
      seededValues(state, 'http://origin'),
      touched,
      true
    );
    expect(next).toEqual({ collectorName: 'prod-eu', name: 'Dana', email: 'ops@acme.example', localUrl: 'http://collector:5335' });
  });

  it('there is no organization field to seed: the contact names the workspace on the confirmation page', () => {
    expect(Object.keys(seededValues(state, 'http://origin'))).not.toContain('org');
    expect(Object.keys(untouched())).not.toContain('org');
  });

  it('null state falls back to the origin, still honoring touched', () => {
    const next = applySeed(
      { collectorName: '', name: '', email: 'kept@typed', localUrl: '' },
      seededValues(null, 'http://origin'),
      { ...untouched(), email: true },
      false
    );
    expect(next).toEqual({ collectorName: '', name: '', email: 'kept@typed', localUrl: 'http://origin' });
  });
});
