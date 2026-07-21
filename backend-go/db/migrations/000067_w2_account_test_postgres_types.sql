-- +goose Up
ALTER TABLE juhe_business.account_test_tasks
  ALTER COLUMN cancel_requested DROP DEFAULT,
  ALTER COLUMN cancel_requested TYPE boolean
    USING lower(cancel_requested::text) IN ('1', 't', 'true'),
  ALTER COLUMN cancel_requested SET DEFAULT false,
  ALTER COLUMN queued_at TYPE timestamptz USING NULLIF(queued_at::text, '')::timestamptz,
  ALTER COLUMN started_at TYPE timestamptz USING NULLIF(started_at::text, '')::timestamptz,
  ALTER COLUMN finished_at TYPE timestamptz USING NULLIF(finished_at::text, '')::timestamptz,
  ALTER COLUMN created_at TYPE timestamptz USING NULLIF(created_at::text, '')::timestamptz,
  ALTER COLUMN updated_at TYPE timestamptz USING NULLIF(updated_at::text, '')::timestamptz;

ALTER TABLE juhe_business.account_test_sessions
  ALTER COLUMN last_heartbeat_at TYPE timestamptz USING NULLIF(last_heartbeat_at::text, '')::timestamptz,
  ALTER COLUMN cancel_requested_at TYPE timestamptz USING NULLIF(cancel_requested_at::text, '')::timestamptz,
  ALTER COLUMN finished_at TYPE timestamptz USING NULLIF(finished_at::text, '')::timestamptz,
  ALTER COLUMN created_at TYPE timestamptz USING NULLIF(created_at::text, '')::timestamptz,
  ALTER COLUMN updated_at TYPE timestamptz USING NULLIF(updated_at::text, '')::timestamptz;

ALTER TABLE juhe_business.account_test_session_tasks
  ALTER COLUMN created_at TYPE timestamptz USING NULLIF(created_at::text, '')::timestamptz;

ALTER TABLE juhe_stats.account_usage_snapshots
  ALTER COLUMN last_attempt_at TYPE timestamptz USING NULLIF(last_attempt_at::text, '')::timestamptz,
  ALTER COLUMN last_success_at TYPE timestamptz USING NULLIF(last_success_at::text, '')::timestamptz,
  ALTER COLUMN next_refresh_after TYPE timestamptz USING NULLIF(next_refresh_after::text, '')::timestamptz,
  ALTER COLUMN updated_at TYPE timestamptz USING NULLIF(updated_at::text, '')::timestamptz,
  ALTER COLUMN created_at TYPE timestamptz USING NULLIF(created_at::text, '')::timestamptz;

CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_task
  ON juhe_business.account_test_session_tasks (task_id, session_id);

-- +goose Down
-- no-op: account test and balance runtime tables keep the Go PostgreSQL types after rollback.
