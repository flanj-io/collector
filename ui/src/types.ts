// Record types mirrored from the collector's read API (CONTRACTS §3/§4).

export interface Correlation {
  request_id?: string | null;
  idempotency_key?: string | null;
  trace_id?: string | null;
  span_id?: string | null;
}

export interface RedactedCall {
  id: string;
  captured_at: string;
  integration: string;
  method: string;
  route: string;
  url?: string;
  status_code: number;
  request_headers?: Record<string, string>;
  request_body: string;
  request_content_type?: string;
  response_headers?: Record<string, string>;
  response_body: string;
  response_content_type?: string;
  correlation: Correlation;
  duration_ms?: number;
  redaction: { applied: boolean; patterns: string[]; spec_aware: boolean };
  direction?: 'client' | 'server';
  peer_host?: string;
  peer_addr?: string;
  edge_class?: string;
}

export interface Finding {
  id: string;
  kind: 'live-vs-spec' | 'version-diff';
  severity: string;
  integration: string;
  endpoint: string;
  field_path?: string | null;
  location?: string | null;
  expected: string;
  actual: string;
  rule: string;
  spec_version_from?: string | null;
  spec_version_to?: string | null;
  source_call_id?: string | null;
  detail?: string;
  signature?: string;
  occurrence_count?: number;
  first_seen?: string;
  last_seen?: string;
}

export interface Health {
  status: string;
  integration: string;
  window_rows: number;
  calls: number;
  findings: number;
  cp_configured: boolean;
  connect_status?: 'disconnected' | 'pending' | 'connected';
  collector_version: string;
  consumer_display_name?: string;
  provider_display_name?: string;
}

/** POST /api/flag success body. */
export interface FlagResult {
  thread_id: string;
  thread_public_id: string;
  thread_url: string;
  state: string;
  status: string;
  finding_id?: string;
}
