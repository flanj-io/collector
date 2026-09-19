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
  /** The caller's OTel service.name (CONTRACTS §3, 2026-09-19): which of this
   *  deployment's services made the call. Local only — never flagged out. */
  service_name?: string;
  mcp_server_name?: string;
  mcp_server_version?: string;
  mcp_protocol_version?: string;
  mcp_session_id?: string;
  /** Store-owned: THIS call produced a per-call finding (live-vs-spec on REST,
   *  output_mismatch on MCP), set on every occurrence. Never infer drift from
   *  the endpoint — one drifting call would relabel every conforming call on it. */
  drifted?: boolean;
  /** The drift processor's OWN verdict on THIS call, stamped where validation
   *  runs (CONTRACTS §2 `flanj.validated`, §3): `clean` | `drifted` — it
   *  validated the call; `not-validated` — it explicitly could not, and
   *  `validated_reason` names the gate; `unknown` — the record reached the
   *  store with no verdict (an older front, or a pipeline running no drift
   *  processor). ABSENT on a row stored before verdicts were recorded. The
   *  contract chip reads THIS, never the contract list — see coverage.ts. */
  validated?: 'clean' | 'drifted' | 'not-validated' | 'unknown' | string;
  validated_reason?: string;
}

export type FindingKind =
  | 'live-vs-spec'
  | 'version-diff'
  // v0.5 MCP finding kinds (spec §1/§4.C).
  | 'output_mismatch'
  | 'definition_change'
  | 'stale_client'
  // 2026-09-17 (R-B's collector-only rows): a value in observed responses
  // changed meaning (change_kind `value`), and a call with arguments that
  // previously succeeded was rejected with -32602 (change_kind
  // `observed_failure`).
  | 'value_change'
  | 'input_rejection';

/** R-A: WHAT moved. A finer axis than `kind`, independent of `severity`. */
export type ChangeKind = 'wording' | 'input' | 'output' | 'catalog' | 'value' | 'observed_failure';

export interface Finding {
  id: string;
  kind: FindingKind;
  /** R-A (2026-09-17), additive + optional: absent on HTTP kinds and on
   *  findings from older collectors. */
  change_kind?: ChangeKind;
  severity: string;
  /** R-E: the dispatcher a call went through when it was re-attributed to
   *  the inner tool — the original request, kept as evidence. */
  via_dispatch?: string;
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
  /** The provider host this finding is about — the peer host of its pinned
   *  source call, joined in by GET /api/findings (read-API only, never part of
   *  the wire contract). It is how a finding finds its contract card: the
   *  calls page holds only the 200 newest rows, and a finding's source call is
   *  frozen at the first occurrence, so resolving the host in the browser lost
   *  the join as soon as that call aged out. Absent on a call-less finding. */
  peer_host?: string;
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
  /** The deployment's NAME (2026-09-14), the CP's copy — '' until Connected. Replaced the config `integration` slug. */
  collector_name?: string;
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
  /** Does this pod hand its stored contracts to FRONT collectors — i.e. is it
   *  the store pod of a tiered deployment (`flanjstore.spec_endpoint`)?
   *
   *  The Contracts card needs it before it can say anything about the 8 MB
   *  document cap, which belongs to that hop alone: a single pod's drift
   *  processor reads the same rows in-process with no cap, so an oversized
   *  document there is bound and validating. Absent on an older collector,
   *  which reads as "no fronts" — the pre-tiered default, and the one that
   *  claims nothing. */
  serves_fronts?: boolean;
}

/**
 * GET /api/directory/hint?host= — the sheet's "Open to" prefill question
 * (thread-domain-gate): the host's registrable domain, and whether the local
 * directory table holds a CLAIMED entry for it (a D5 domain proof — what makes
 * prefilling it as the share domain honest). Read from the local table only.
 */
export interface DirectoryHint {
  host: string;
  domain: string;
  name: string | null;
  tier: string | null;
  claimed: boolean;
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
