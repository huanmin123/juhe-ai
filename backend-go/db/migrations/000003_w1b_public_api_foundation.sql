-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.external_integration_sources (
  id text PRIMARY KEY,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  scopes_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(scopes_json::jsonb) = 'array'),
  rate_limits_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(rate_limits_json::jsonb) = 'array'),
  expires_at timestamptz,
  notes text,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower
  ON juhe_business.external_integration_sources (lower(name));
CREATE INDEX IF NOT EXISTS idx_external_integration_sources_updated
  ON juhe_business.external_integration_sources (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status_updated
  ON juhe_business.external_integration_sources (status, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS juhe_business.external_integration_source_tokens (
  id text PRIMARY KEY,
  source_ref_id text NOT NULL REFERENCES juhe_business.external_integration_sources(id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  token_secret_encrypted text NOT NULL,
  token_prefix text NOT NULL,
  token_suffix text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'revoked')),
  scopes_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(scopes_json::jsonb) = 'array'),
  expires_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source
  ON juhe_business.external_integration_source_tokens (source_ref_id, status, expires_at);

CREATE TABLE IF NOT EXISTS juhe_dataset.public_api_logs (
  id text PRIMARY KEY,
  trace_id text,
  source_ref_id text,
  source_name text,
  token_id text,
  token_name text,
  token_prefix text,
  is_test_token boolean NOT NULL DEFAULT false,
  method text NOT NULL,
  path text NOT NULL,
  query_string text,
  client_ip text,
  user_agent text,
  status_code integer,
  success boolean NOT NULL DEFAULT false,
  duration_ms bigint,
  request_size_bytes bigint NOT NULL DEFAULT 0,
  response_size_bytes bigint NOT NULL DEFAULT 0,
  request_capture_status text NOT NULL DEFAULT 'empty' CHECK (request_capture_status IN ('complete', 'truncated', 'empty', 'dropped')),
  response_capture_status text NOT NULL DEFAULT 'empty' CHECK (response_capture_status IN ('complete', 'truncated', 'empty', 'dropped')),
  request_data_json text NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(request_data_json::jsonb) = 'object'),
  response_data_json text NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(response_data_json::jsonb) = 'object'),
  error_code text,
  error_message text,
  started_at timestamptz NOT NULL,
  ended_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_public_api_logs_created
  ON juhe_dataset.public_api_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created
  ON juhe_dataset.public_api_logs (source_ref_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_public_api_logs_path_created
  ON juhe_dataset.public_api_logs (path, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_public_api_logs_status_created
  ON juhe_dataset.public_api_logs (status_code, created_at DESC, id DESC);

-- +goose Down
-- no-op: W1b public API tables contain business credentials and operational logs.
