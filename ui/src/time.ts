// ONE timestamp format for the whole surface (UX review 2026-09-14): the
// Traffic table's captured column read `Sep 13, 11:52:57 PM` while a finding's
// snapshot labels showed the raw `2026-09-13T20:44:52.964Z` — the same instant
// in two registers, and the mono eyebrow then uppercased the second one. The
// content rules ask for ISO dates and exact numbers, so every stamp is
// `YYYY-MM-DD HH:MM:SS` in the browser's local time, whole seconds. The full
// RFC 3339 value stays available for a title attribute. Pure; unit-tested.

function pad(n: number): string {
  return n < 10 ? '0' + n : String(n);
}

/** `2026-09-13T20:44:52.964Z` → `2026-09-13 23:44:52` (local time). An empty or
 *  unparseable input comes back as given, so a missing time never prints as
 *  a fake epoch. */
export function isoStamp(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    ` ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  );
}
