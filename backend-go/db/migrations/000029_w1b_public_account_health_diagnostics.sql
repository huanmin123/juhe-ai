-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS next_health_check_at timestamptz,
  ADD COLUMN IF NOT EXISTS health_check_failure_count integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS last_health_check_status_code integer,
  ADD COLUMN IF NOT EXISTS last_health_check_error_code text,
  ADD COLUMN IF NOT EXISTS last_health_check_error_message text;

-- +goose Down
ALTER TABLE juhe_business.accounts
  DROP COLUMN IF EXISTS last_health_check_error_message,
  DROP COLUMN IF EXISTS last_health_check_error_code,
  DROP COLUMN IF EXISTS last_health_check_status_code,
  DROP COLUMN IF EXISTS health_check_failure_count,
  DROP COLUMN IF EXISTS next_health_check_at;
