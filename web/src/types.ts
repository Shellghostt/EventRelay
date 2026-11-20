export interface Session {
  csrf_nonce: string;
  expires_at: string;
}
export interface Endpoint {
  id: string;
  url: string;
  description: string;
  enabled: boolean;
  max_attempts: number;
  revision: number;
  created_at: string;
}
export interface EventSummary {
  id: string;
  endpoint_id: string;
  event_type: string;
  status: string;
  attempt_count: number;
  replay_count: number;
  cycle_attempt_count: number;
  cycle_max_attempts: number;
  cycle_started_at: string;
  cycle_finished_at: string | null;
  next_attempt_at: string | null;
  received_at: string;
  delivered_at: string | null;
  last_error?: string;
  last_error_truncated: boolean;
}
export interface EventDetail extends EventSummary {
  payload: unknown;
  payload_available: boolean;
  payload_bytes: number;
  payload_unavailable_reason: string | null;
  terminal_reason: string | null;
}
export interface Attempt {
  attempt_no: number;
  cycle_no: number;
  cycle_attempt_no: number;
  endpoint_revision: number | null;
  started_at: string;
  finished_at: string | null;
  outcome: string;
  response_status: number | null;
  duration_ms: number | null;
  error: string | null;
  error_truncated: boolean;
  response_body_available: boolean;
  response_headers_available: boolean;
}
export interface Cycle {
  cycle_no: number;
  started_at: string;
  finished_at: string | null;
  replayed_at: string;
  final_status: string;
  terminal_reason: string | null;
  attempt_count: number;
  max_attempts: number;
  first_attempt_no: number | null;
  last_attempt_no: number | null;
  last_error: string | null;
  last_error_truncated: boolean;
}
export interface Stats {
  available: boolean;
  unavailable_reason: string | null;
  window_from: string;
  window_to: string;
  http_response_attempts: number | null;
  success_http_attempts: number | null;
  no_response_attempts: number | null;
  network_attempts: number | null;
  timeout_attempts: number | null;
  policy_attempts: number | null;
  lease_expired_attempts: number | null;
  success_rate: number | null;
  attempt_limit: number;
}
export interface EventPage {
  events: EventSummary[];
  next_cursor: string | null;
}
export interface EndpointPage {
  endpoints: Endpoint[];
  next_cursor: string | null;
}
export interface AttemptPage {
  attempts: Attempt[];
  next_cursor: string | null;
}
export interface CyclePage {
  cycles: Cycle[];
  next_cursor: string | null;
}
