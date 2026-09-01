// Record types mirrored from the collector's read API (CONTRACTS §3/§4).

export interface Correlation {
  request_id?: string | null;
  idempotency_key?: string | null;
  trace_id?: string | null;
  span_id?: string | null;
  /** v0.5 MCP: the JSON-RPC id observed on the client's OWN outgoing message —
   *  CLIENT-generated, never presented as a provider-issued id. */
  client_request_id?: string | null;
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
  // v0.5 MCP call fields (additive; absent on HTTP calls).
  transport?: 'mcp' | string;
  mcp_tool_name?: string;
  mcp_is_error?: boolean;
  mcp_server_name?: string;
  mcp_server_version?: string;
  mcp_protocol_version?: string;
  mcp_session_id?: string;
  /** Store-owned: THIS call produced a live-vs-spec finding, set on every
   *  occurrence. Never infer drift from the endpoint — one drifting call would
   *  relabel every conforming call on it. */
  drifted?: boolean;
}

export type FindingKind =
  | 'live-vs-spec'
  | 'version-diff'
  // v0.5 MCP finding kinds (spec §1/§4.C).
  | 'output_mismatch'
  | 'definition_change'
  | 'stale_client';

export interface Finding {
  id: string;
  kind: FindingKind;
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
  detected_at?: string;
  detail?: string;
  signature?: string;
  occurrence_count?: number;
  first_seen?: string;
  last_seen?: string;
  /** v0.5 MCP (additive, optional): the ObservedAt of the tools/list snapshot
   *  backing the finding — the CURRENT one for output_mismatch, the AFTER one
   *  for definition_change. Absent on other kinds and older collectors. */
  snapshot_observed_at?: string;
  /** Additive, optional: the PREVIOUS snapshot's ObservedAt on a
   *  definition_change — the structured sibling of snapshot_observed_at.
   *  Absent on other kinds and older collectors. */
  snapshot_observed_from?: string;
  /** Local acknowledge state (read-API join, never part of the wire contract):
   *  true when this finding's signature is acknowledged on this collector. */
  acked?: boolean;
  /** When the acknowledge landed (RFC3339); set only with acked. */
  acked_at?: string;
  /** The evidence version the acknowledgement covers — the AFTER snapshot hash
   *  on a definition_change, absent on every other kind. The SPA re-checks it
   *  against spec_version_to so a NEW change can never inherit an old ack
   *  (ux-design-v2 §2.8). */
  acked_evidence_version?: string;
}

export interface Health {
  status: string;
  /** Absent until the collector has observed ≥1 external outbound edge (or a
   *  finding) — pre-traffic honesty (v1p1): never emitted from bare config at
   *  zero traffic, and the UI renders no fragment while it is absent. */
  integration?: string;
  window_rows: number;
  calls: number;
  findings: number;
  cp_configured: boolean;
  connect_status?: 'disconnected' | 'pending' | 'connected';
  collector_version: string;
  /** Did this collector hold data before the light-default upgrade? The second
   *  gate on the one-time theme-flip notice (ux-design-v2 §3.4). Absent on an
   *  older collector, which reads as "no notice" — the safe direction. */
  held_prior_data?: boolean;
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
