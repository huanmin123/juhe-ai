-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS cooldown_retest_failure_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS cooldown_retest_observation_started_at timestamptz,
  ADD COLUMN IF NOT EXISTS cooldown_retest_last_at timestamptz,
  ADD COLUMN IF NOT EXISTS cooldown_retest_last_status_code integer,
  ADD COLUMN IF NOT EXISTS last_health_check_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_health_success_at timestamptz,
  ADD COLUMN IF NOT EXISTS stream_failure_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS stream_failure_window_started_at timestamptz;

-- +goose Down
-- no-op: these runtime diagnostics are current account state and remain readable by the previous release.
