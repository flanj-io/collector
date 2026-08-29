// Thin fetch helpers for the collector's localhost relay. Every mutating route
// requires `X-Flanj-UI: 1` + a JSON content type (the relay refuses anything
// else, and the custom header forces a CORS preflight the relay never answers,
// so a foreign page cannot drive it). Errors come back as `{error, message}`;
// ApiError carries both so the UI can switch on the code and show the message.

export const UI_HEADERS: Record<string, string> = { 'X-Flanj-UI': '1', 'Content-Type': 'application/json' };

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/** True when the failure was a transport problem (the relay itself was unreachable). */
export function isNetworkError(e: unknown): boolean {
  return !(e instanceof ApiError);
}

async function parse<T>(resp: Response): Promise<T> {
  const text = await resp.text();
  let body: any = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  if (!resp.ok) {
    const code = (body && typeof body.error === 'string' && body.error) || `http_${resp.status}`;
    const message = (body && typeof body.message === 'string' && body.message) || `HTTP ${resp.status}`;
    throw new ApiError(resp.status, code, message);
  }
  return body as T;
}

export async function apiGet<T>(path: string): Promise<T> {
  const resp = await fetch(path, { headers: { Accept: 'application/json' }, cache: 'no-store' });
  return parse<T>(resp);
}

export async function apiPost<T>(path: string, body: unknown = {}): Promise<T> {
  const resp = await fetch(path, { method: 'POST', headers: UI_HEADERS, body: JSON.stringify(body ?? {}) });
  return parse<T>(resp);
}

/**
 * "View thread": fetch a 10-minute single-use owner handoff from the relay and
 * open it in a new tab. The tab is opened synchronously on the click (so popup
 * blockers allow it) and pointed at the handoff once it arrives; the URL is
 * never stored or logged. When the popup was blocked, `opened` is false and the
 * caller offers the URL as a plain link instead.
 */
export async function openThreadInNewTab(threadId: string): Promise<{ url: string; opened: boolean }> {
  let win: Window | null = null;
  try {
    win = window.open('about:blank', '_blank');
    if (win) win.opener = null;
  } catch {
    win = null;
  }
  try {
    const out = await apiPost<{ owner_url: string; expires_at?: string }>(`/api/threads/${encodeURIComponent(threadId)}/open`);
    if (win) win.location.replace(out.owner_url);
    return { url: out.owner_url, opened: !!win };
  } catch (e) {
    if (win) win.close();
    throw e;
  }
}
