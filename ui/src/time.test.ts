import { describe, it, expect } from 'vitest';
import { isoStamp, isoDate } from './time';

describe('isoStamp — the surface has one timestamp format', () => {
  it('renders ISO date + HH:MM:SS, whole seconds, local time', () => {
    const iso = '2026-09-13T20:44:52.964Z';
    const d = new Date(iso);
    const expected =
      `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}` +
      ` ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`;
    expect(isoStamp(iso)).toBe(expected);
    expect(isoStamp(iso)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
    // Never the locale register, never milliseconds, never a T.
    expect(isoStamp(iso)).not.toMatch(/[A-Za-z]|\.\d{3}/);
  });

  it('is honest about what it cannot parse', () => {
    expect(isoStamp('')).toBe('');
    expect(isoStamp(null)).toBe('');
    expect(isoStamp(undefined)).toBe('');
    expect(isoStamp('not a date')).toBe('not a date');
  });
});

describe('isoDate — the bare date the Edges tables read', () => {
  it('renders YYYY-MM-DD, local time, no hour', () => {
    const iso = '2026-08-02T23:44:52.964Z';
    const d = new Date(iso);
    const expected = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    expect(isoDate(iso)).toBe(expected);
    expect(isoDate(iso)).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('is honest about what it cannot parse', () => {
    expect(isoDate('')).toBe('');
    expect(isoDate(null)).toBe('');
    expect(isoDate(undefined)).toBe('');
    expect(isoDate('not a date')).toBe('not a date');
  });
});
