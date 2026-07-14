-- +goose Up
CREATE TABLE juhe_dataset.api_key_record_cleanup_targets (
  api_key_id text PRIMARY KEY,
  system_account_id text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0,
  last_attempt_at timestamptz,
  last_blocked_reason text,
  last_error_message text,
  CHECK (attempt_count >= 0)
);

CREATE INDEX idx_api_key_record_cleanup_targets_attempt
  ON juhe_dataset.api_key_record_cleanup_targets (
    COALESCE(last_attempt_at, created_at),
    created_at,
    api_key_id
  );

-- +goose Down
-- no-op: API Key cleanup targets are part of the current fresh schema.
