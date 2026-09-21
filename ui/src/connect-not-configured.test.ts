import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { CP_NOT_CONFIGURED_KEYS, cpNotConfigured } from './connect-not-configured';

/**
 * What the Settings tab tells an operator whose collector cannot Connect.
 *
 * It once read "set cp_base_url and cp_deploy_token". A deploy token has been
 * optional since Connect stopped needing one — the confirmation click is the
 * consent — so that sentence sent operators looking for a credential they do
 * not need, while the backend's own message, the README and the deployment
 * docs all named cp_base_url alone. The backend's sentence is the source of
 * truth here: it is what the API answers, so this reads it rather than
 * restating it.
 */
const backend = readFileSync(join(__dirname, '../../extension/flanjui/messages.go'), 'utf8');
const backendSentence = backend.match(/msgCPNotConfigured\s*=\s*"([^"]+)"/)?.[1] ?? '';
const appVue = readFileSync(join(__dirname, 'App.vue'), 'utf8');

describe('the not-set-up-to-connect message', () => {
  it('names exactly the keys the backend says are needed', () => {
    expect(backendSentence).not.toBe('');
    const named = [...backendSentence.matchAll(/\b(cp_[a-z_]+)\b/g)].map((m) => m[1]);
    expect(CP_NOT_CONFIGURED_KEYS).toEqual(named);
    expect(CP_NOT_CONFIGURED_KEYS).toEqual(['cp_base_url']);
  });

  it('opens with the backend sentence, word for word, and then says what still works', () => {
    const text = cpNotConfigured().map((p) => p.text).join('');
    expect(text.startsWith(backendSentence)).toBe(true);
    expect(text).toBe(`${backendSentence} Local capture, detection and this UI work without it.`);
  });

  it('never asks for a deploy token', () => {
    expect(cpNotConfigured().map((p) => p.text).join('')).not.toMatch(/deploy|token/i);
  });

  it('is what the Settings tab renders — no second copy of the sentence in the template', () => {
    expect(appVue).toContain('cpNotConfigured()');
    expect(appVue).not.toMatch(/not set up to connect to Flanj \(set/);
    expect(appVue).not.toContain('cp_deploy_token');
  });
});
