-- +goose Up
ALTER TABLE juhe_business.account_api_key_runtime_states
  ADD COLUMN IF NOT EXISTS last_trace_id text;

-- +goose Down
ALTER TABLE juhe_business.account_api_key_runtime_states
  DROP COLUMN IF EXISTS last_trace_id;
