-- +goose Up
CREATE SCHEMA IF NOT EXISTS juhe_dataset;

CREATE TABLE IF NOT EXISTS juhe_dataset.audit_logs (
  id text PRIMARY KEY, trace_id text NOT NULL, traffic_source text NOT NULL,
  system_account_id text, api_key_id text, group_id text, account_id text, provider_code text,
  method text NOT NULL, path text NOT NULL, query_string text, model text, upstream_model text, pricing_model text,
  model_mapping_applied integer NOT NULL DEFAULT 0, model_mapping_source text,
  source_endpoint_family text, upstream_endpoint_family text, stream integer NOT NULL DEFAULT 0,
  client_ip text, user_agent text, audit_outcome text NOT NULL, success integer NOT NULL DEFAULT 0,
  final_status_code integer, error_phase text, error_code text, error_message text,
  sample_bucket integer NOT NULL, sample_reason text NOT NULL, attempt_count integer NOT NULL DEFAULT 0,
  payload_count integer NOT NULL DEFAULT 0, raw_payload_bytes bigint NOT NULL DEFAULT 0,
  compressed_payload_bytes bigint NOT NULL DEFAULT 0, compression_saved_bytes bigint NOT NULL DEFAULT 0,
  error_group_id text, capture_status text NOT NULL DEFAULT 'complete', started_at text NOT NULL,
  ended_at text NOT NULL, duration_ms integer, http_completed_at text, http_duration_ms integer,
  first_token_ms integer, created_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.audit_log_attempts (
  id text PRIMARY KEY, audit_log_id text NOT NULL, attempt_index integer NOT NULL,
  account_id text, account_owner_system_account_id text, group_id text, proxy_url text, provider_code text,
  attempt_model text, attempt_upstream_model text, attempt_pricing_model text,
  attempt_model_mapping_applied integer NOT NULL DEFAULT 0, attempt_model_mapping_source text,
  attempt_source_endpoint_family text, attempt_upstream_endpoint_family text,
  upstream_method text NOT NULL, upstream_url text NOT NULL, upstream_status_code integer,
  success integer NOT NULL DEFAULT 0, error_phase text, error_code text, error_message text,
  started_at text NOT NULL, ended_at text, duration_ms integer,
  FOREIGN KEY (audit_log_id) REFERENCES juhe_dataset.audit_logs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_blobs (
  id text PRIMARY KEY, sha256 text NOT NULL, raw_size_bytes bigint NOT NULL DEFAULT 0,
  compressed_size_bytes bigint NOT NULL DEFAULT 0, content_type text NOT NULL DEFAULT 'application/octet-stream',
  content_encoding text, compression text NOT NULL DEFAULT 'none', storage_key text NOT NULL,
  ref_count integer NOT NULL DEFAULT 0, first_seen_at text NOT NULL, last_seen_at text NOT NULL, created_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.audit_payload_refs (
  id text PRIMARY KEY, audit_log_id text NOT NULL, attempt_id text, part_type text NOT NULL,
  sequence_index integer NOT NULL DEFAULT 0, content_type text, content_encoding text,
  headers_blob_id text, body_blob_id text, headers_sha256 text, body_sha256 text,
  raw_size_bytes bigint NOT NULL DEFAULT 0, compressed_size_bytes bigint NOT NULL DEFAULT 0,
  capture_status text NOT NULL DEFAULT 'complete', created_at text NOT NULL,
  FOREIGN KEY (audit_log_id) REFERENCES juhe_dataset.audit_logs(id) ON DELETE CASCADE,
  FOREIGN KEY (attempt_id) REFERENCES juhe_dataset.audit_log_attempts(id) ON DELETE SET NULL,
  FOREIGN KEY (headers_blob_id) REFERENCES juhe_dataset.audit_payload_blobs(id) ON DELETE SET NULL,
  FOREIGN KEY (body_blob_id) REFERENCES juhe_dataset.audit_payload_blobs(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.audit_error_groups (
  id text PRIMARY KEY, fingerprint text NOT NULL, window_started_at text NOT NULL, window_ended_at text NOT NULL,
  system_account_id text, api_key_id text, group_id text, account_id text, provider_code text,
  path text, model text, status_code integer, error_phase text, error_code text, error_type text,
  request_fingerprint text, error_fingerprint text, count integer NOT NULL DEFAULT 0,
  first_event_id text, last_event_id text, sample_event_id text, last_message text,
  created_at text NOT NULL, updated_at text NOT NULL, UNIQUE (fingerprint, window_started_at)
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON juhe_dataset.audit_logs(created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_created ON juhe_dataset.audit_logs(system_account_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_trace_created ON juhe_dataset.audit_logs(system_account_id, trace_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_client_ip_created ON juhe_dataset.audit_logs(system_account_id, client_ip, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_api_key_created ON juhe_dataset.audit_logs(system_account_id, api_key_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_group_created ON juhe_dataset.audit_logs(system_account_id, group_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_id_created ON juhe_dataset.audit_logs(system_account_id, account_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_error_group_created ON juhe_dataset.audit_logs(error_group_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_trace_c_created_sort ON juhe_dataset.audit_logs(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_system_client_ip_c_created_sort ON juhe_dataset.audit_logs(system_account_id, (client_ip COLLATE "C"), created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_attempts_log_index ON juhe_dataset.audit_log_attempts(audit_log_id, attempt_index);
CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_payload_blobs_unique ON juhe_dataset.audit_payload_blobs(sha256, raw_size_bytes, content_type);
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

-- +goose Down
-- no-op: audit tables are retained for the shared Node writer schema.
