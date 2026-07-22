-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS last_error_trace_id text,
  ADD COLUMN IF NOT EXISTS last_health_check_trace_id text;

-- +goose Down
ALTER TABLE juhe_business.accounts
  DROP COLUMN IF EXISTS last_health_check_trace_id,
  DROP COLUMN IF EXISTS last_error_trace_id;
