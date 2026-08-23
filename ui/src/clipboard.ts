// Clipboard with a fallback for plain-http origins (navigator.clipboard is
// only exposed in secure contexts; a collector UI reached through a port-forward
// or a LAN address is often plain http). The async Clipboard API is tried first;
// when it is missing or refuses, the link input is selected and the legacy
// document.execCommand('copy') is attempted — and the caller shows the "select
// the link and press Ctrl/Cmd+C" hint, because the legacy path cannot be trusted
// to have succeeded.

export type CopyOutcome = 'copied' | 'fallback';

export async function copyText(text: string, input?: HTMLInputElement | HTMLTextAreaElement | null): Promise<CopyOutcome> {
  const clip = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
  if (clip && typeof clip.writeText === 'function') {
    try {
      await clip.writeText(text);
      return 'copied';
    } catch {
      /* fall through to the legacy path */
    }
  }
  if (input) {
    try {
      input.focus();
      input.select();
      input.setSelectionRange(0, input.value.length);
      document.execCommand?.('copy');
    } catch {
      /* nothing else to try */
    }
  }
  return 'fallback';
}

/** Select the contents of an input (the auto-select on the success state). */
export function selectInput(input?: HTMLInputElement | HTMLTextAreaElement | null): void {
  if (!input) return;
  try {
    input.focus();
    input.select();
    input.setSelectionRange(0, input.value.length);
  } catch {
    /* ignore */
  }
}
