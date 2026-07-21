-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.account_usage_snapshots (
  system_account_id text NOT NULL,
  account_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('openai_codex', 'relay_balance')),
  source text,
  snapshot_json text NOT NULL,
  refresh_status text,
  last_attempt_at timestamptz,
  last_success_at timestamptz,
  next_refresh_after timestamptz,
  last_error_message text,
  updated_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, account_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind
  ON juhe_stats.account_usage_snapshots (kind, updated_at);
CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind_account
  ON juhe_stats.account_usage_snapshots (kind, account_id);
CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_updated
  ON juhe_stats.account_usage_snapshots (updated_at);

CREATE TABLE IF NOT EXISTS juhe_business.account_test_tasks (
  id text PRIMARY KEY,
  account_id text NOT NULL,
  account_name text NOT NULL,
  provider_code text NOT NULL,
  provider_protocol_profile_id text NOT NULL,
  protocol_code text NOT NULL,
  protocol_version text NOT NULL,
  account_type text NOT NULL,
  request_system_account_id text NOT NULL,
  request_role text NOT NULL,
  request_system_account_filter_id text,
  diagnostics text NOT NULL DEFAULT 'full',
  model text,
  test_endpoint_mode text,
  draft_account_encrypted text,
  status text NOT NULL DEFAULT 'queued',
  status_message text,
  result_json text,
  error_message text,
  cancel_requested boolean NOT NULL DEFAULT false,
  queued_at timestamptz NOT NULL,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.account_test_sessions (
  id text PRIMARY KEY,
  request_system_account_id text NOT NULL,
  request_role text NOT NULL,
  request_system_account_filter_id text,
  status text NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'canceled', 'expired', 'completed')),
  cancel_reason text,
  last_heartbeat_at timestamptz NOT NULL,
  cancel_requested_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.account_test_session_tasks (
  session_id text NOT NULL,
  task_id text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (session_id, task_id),
  FOREIGN KEY (session_id) REFERENCES juhe_business.account_test_sessions(id) ON DELETE CASCADE,
  FOREIGN KEY (task_id) REFERENCES juhe_business.account_test_tasks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_account_test_tasks_request_updated
  ON juhe_business.account_test_tasks (request_system_account_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_account_test_tasks_status_queued
  ON juhe_business.account_test_tasks (status, queued_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_account_test_tasks_finished_cleanup
  ON juhe_business.account_test_tasks (finished_at ASC, id ASC)
  WHERE finished_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_account_test_sessions_request_updated
  ON juhe_business.account_test_sessions (request_system_account_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_account_test_sessions_status_heartbeat
  ON juhe_business.account_test_sessions (status, last_heartbeat_at ASC, id ASC);

-- +goose Down
-- no-op: account test history and usage snapshots are current runtime state and remain readable by the previous release.
