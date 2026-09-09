// @vitest-environment happy-dom
//
// The edge-registration disclosure (v1 phase 2): the pure copy rule, and — on
// the REAL component — that the panel actually renders it, in every state,
// including BEFORE the operator Connects. The disclosure is the consent half of
// this slice; a helper that returns the right sentence to a call site that
// never renders it discloses nothing, which is exactly what a pure test cannot
// see.
import { describe, it, expect } from 'vitest';
import { mount } from '@vue/test-utils';
import ConnectPanel from './ConnectPanel.vue';
import {
  EDGE_REGISTRATION_DISCLOSURE,
  EDGE_REGISTRATION_DISCLOSURE_OFF,
  edgeDisclosure
} from './connect-disclosure';
import type { ConnectState } from './threads';

describe('edgeDisclosure', () => {
  it('states what leaves when registration is on', () => {
    const d = edgeDisclosure(true);
    expect(d).toEqual({ text: EDGE_REGISTRATION_DISCLOSURE, state: 'on' });
    // The three things that never leave are NAMED — a disclosure that only says
    // what is sent leaves the reader to imagine the rest.
    expect(d!.text).toContain('never calls, bodies, or payloads');
    expect(d!.text).toContain('Internal edges never leave');
    expect(d!.text).toContain('external domains');
  });

  it('says registration is off rather than repeating the on-copy', () => {
    const d = edgeDisclosure(false);
    expect(d).toEqual({ text: EDGE_REGISTRATION_DISCLOSURE_OFF, state: 'off' });
    expect(d!.text).toContain('edge_sync: false');
    // And it does not leave the operator thinking findings stopped too.
    expect(d!.text).toContain('Findings still sync');
  });

  it('renders nothing when the collector has not said which way the switch is set', () => {
    // A collector predating v1 phase 2, or a /api/connect response not yet in.
    // Guessing "on" would claim a flow that may not exist; guessing "off" would
    // hide one that does.
    expect(edgeDisclosure(undefined)).toBeNull();
    expect(edgeDisclosure(null)).toBeNull();
  });
});

const base = (over: Partial<ConnectState> = {}): ConnectState => ({
  status: 'disconnected',
  cp_configured: true,
  ...over
});

describe('ConnectPanel renders the disclosure', () => {
  it('BEFORE Connecting — the operator reads what Connecting causes first', () => {
    const w = mount(ConnectPanel, { props: { state: base({ edge_sync: true }) } });
    expect(w.text()).toContain(EDGE_REGISTRATION_DISCLOSURE);
    expect(w.find('.connect-disclosure').classes()).toContain('on');
  });

  it('while pending, and once connected — the line does not vanish after Connect', () => {
    for (const status of ['pending', 'connected'] as const) {
      const w = mount(ConnectPanel, {
        props: {
          state: base({
            status,
            edge_sync: true,
            consumer_display_name: 'Acme Consumer Ltd',
            contact_email: 'ops@acme.example',
            confirmed_contact_email: status === 'connected' ? 'ops@acme.example' : null,
            local_ui_url: 'http://localhost:5335'
          })
        }
      });
      expect(w.text(), status).toContain(EDGE_REGISTRATION_DISCLOSURE);
    }
  });

  it('says OFF when this collector is configured `edge_sync: false`', () => {
    const w = mount(ConnectPanel, { props: { state: base({ status: 'connected', edge_sync: false }) } });
    expect(w.text()).toContain(EDGE_REGISTRATION_DISCLOSURE_OFF);
    expect(w.text()).not.toContain(EDGE_REGISTRATION_DISCLOSURE);
    expect(w.find('.connect-disclosure').classes()).toContain('off');
  });

  it('renders no disclosure at all when the field is absent', () => {
    const w = mount(ConnectPanel, { props: { state: base() } });
    expect(w.find('.connect-disclosure').exists()).toBe(false);
  });
});
