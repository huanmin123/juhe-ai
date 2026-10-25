package auditlog

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_log_owner_leases (
  lease_key text PRIMARY KEY, owner_id text NOT NULL, fence_token bigint NOT NULL,
  lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_logs (
  id text PRIMARY KEY, trace_id text NOT NULL, traffic_source text NOT NULL,
  system_account_id text, api_key_id text, conversation_key text, session_id text,
  session_client_type text, group_id text, route_group_id text, account_id text, provider_code text,
  method text NOT NULL, path text NOT NULL, query_string text, model text, upstream_model text,
  pricing_model text, model_mapping_applied boolean NOT NULL DEFAULT false,
  model_mapping_source text, source_endpoint_family text, upstream_endpoint_family text,
  stream boolean NOT NULL DEFAULT false, client_ip text, user_agent text, audit_outcome text NOT NULL,
  success boolean NOT NULL DEFAULT false, final_status_code integer, error_phase text, error_code text,
  error_message text, sample_bucket integer NOT NULL, sample_reason text NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0, payload_count integer NOT NULL DEFAULT 0,
  raw_payload_bytes bigint NOT NULL DEFAULT 0, compressed_payload_bytes bigint NOT NULL DEFAULT 0,
  compression_saved_bytes bigint NOT NULL DEFAULT 0, error_group_id text, capture_status text NOT NULL,
  lifecycle_status text NOT NULL, started_at timestamptz NOT NULL, ended_at timestamptz NOT NULL,
  duration_ms bigint, http_completed_at timestamptz, http_duration_ms bigint, first_token_ms bigint,
  created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_log_attempts (
  id text PRIMARY KEY, audit_log_id text NOT NULL REFERENCES juhe_dataset.audit_logs(id) ON DELETE CASCADE,
  attempt_index integer NOT NULL, account_id text, account_owner_system_account_id text, group_id text,
  member_group_id text, proxy_url text, provider_code text, attempt_model text, attempt_upstream_model text,
  attempt_pricing_model text, attempt_model_mapping_applied boolean NOT NULL DEFAULT false,
  attempt_model_mapping_source text, attempt_source_endpoint_family text,
  attempt_upstream_endpoint_family text, upstream_method text NOT NULL, upstream_url text NOT NULL,
  upstream_status_code integer, success boolean NOT NULL DEFAULT false, error_phase text,
  error_code text, error_message text, started_at timestamptz NOT NULL, ended_at timestamptz,
  duration_ms bigint
);
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_blobs (
  id text PRIMARY KEY, sha256 text NOT NULL, raw_size_bytes bigint NOT NULL,
  compressed_size_bytes bigint NOT NULL, content_type text NOT NULL, content_encoding text,
  compression text NOT NULL DEFAULT 'none', storage_key text NOT NULL, ref_count bigint NOT NULL DEFAULT 0,
  first_seen_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL, created_at timestamptz NOT NULL,
  UNIQUE(sha256, raw_size_bytes, content_type)
);
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_refs (
  id text PRIMARY KEY, audit_log_id text NOT NULL REFERENCES juhe_dataset.audit_logs(id) ON DELETE CASCADE,
  attempt_id text REFERENCES juhe_dataset.audit_log_attempts(id) ON DELETE SET NULL, part_type text NOT NULL,
  sequence_index integer NOT NULL, content_type text, content_encoding text,
  headers_blob_id text REFERENCES juhe_dataset.audit_payload_blobs(id) ON DELETE SET NULL,
  body_blob_id text REFERENCES juhe_dataset.audit_payload_blobs(id) ON DELETE SET NULL,
  headers_sha256 text, body_sha256 text, raw_size_bytes bigint NOT NULL DEFAULT 0,
  compressed_size_bytes bigint NOT NULL DEFAULT 0, capture_status text NOT NULL, drop_reason text,
  created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.audit_error_groups (
  id text PRIMARY KEY, fingerprint text NOT NULL, window_started_at timestamptz NOT NULL,
  window_ended_at timestamptz NOT NULL, system_account_id text, api_key_id text, group_id text,
  account_id text, provider_code text, path text, model text, status_code integer, error_phase text,
  error_code text, error_type text, request_fingerprint text, error_fingerprint text,
  count bigint NOT NULL DEFAULT 0, first_event_id text, last_event_id text, sample_event_id text,
  last_message text, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  UNIQUE(fingerprint, window_started_at)
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON juhe_dataset.audit_logs(created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_persisted_created ON juhe_dataset.audit_logs(created_at, id)
  WHERE traffic_source NOT IN ('account_health_check', 'runtime_recovery_probe', 'cooldown_retest');
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_created ON juhe_dataset.audit_logs(system_account_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_persisted_created ON juhe_dataset.audit_logs(system_account_id, created_at, id)
  WHERE traffic_source NOT IN ('account_health_check', 'runtime_recovery_probe', 'cooldown_retest');
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_trace_created ON juhe_dataset.audit_logs(system_account_id, trace_id COLLATE "C", created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_client_ip_created ON juhe_dataset.audit_logs(system_account_id, client_ip COLLATE "C", created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_api_key_created ON juhe_dataset.audit_logs(system_account_id, api_key_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_group_created ON juhe_dataset.audit_logs(system_account_id, group_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_id_created ON juhe_dataset.audit_logs(system_account_id, account_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_error_group_created ON juhe_dataset.audit_logs(error_group_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_session_created ON juhe_dataset.audit_logs(session_id, created_at, id, session_client_type) WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_log_attempts_log_index ON juhe_dataset.audit_log_attempts(audit_log_id, attempt_index);
CREATE INDEX IF NOT EXISTS idx_audit_payload_blobs_created ON juhe_dataset.audit_payload_blobs(created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_log_part ON juhe_dataset.audit_payload_refs(audit_log_id, part_type, sequence_index);
CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_log_sequence ON juhe_dataset.audit_payload_refs(audit_log_id, sequence_index);
CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_attempt ON juhe_dataset.audit_payload_refs(attempt_id) WHERE attempt_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_headers_blob ON juhe_dataset.audit_payload_refs(headers_blob_id);
CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_body_blob ON juhe_dataset.audit_payload_refs(body_blob_id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_window ON juhe_dataset.audit_error_groups(window_started_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_fingerprint_window ON juhe_dataset.audit_error_groups(fingerprint, window_started_at);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_updated ON juhe_dataset.audit_error_groups(updated_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_path_updated ON juhe_dataset.audit_error_groups(path, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_model_updated ON juhe_dataset.audit_error_groups(model, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_status_updated ON juhe_dataset.audit_error_groups(status_code, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_error_groups_api_key_account ON juhe_dataset.audit_error_groups(api_key_id, system_account_id);
`

// auditSuperGroupColumn 描述超级分组双层身份列（设计 §4.3）。
type auditSuperGroupColumn struct {
	table      string
	column     string
	definition string
}

var superGroupAuditColumns = []auditSuperGroupColumn{
	{table: "audit_logs", column: "route_group_id", definition: "TEXT"},
	{table: "audit_log_attempts", column: "member_group_id", definition: "TEXT"},
}
