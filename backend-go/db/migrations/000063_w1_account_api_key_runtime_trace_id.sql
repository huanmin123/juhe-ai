-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  account_id text NOT NULL REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
  key_fingerprint text NOT NULL,
  key_index integer NOT NULL DEFAULT 0,
  credential_revision text,
  status text NOT NULL DEFAULT 'active',
  failure_count integer NOT NULL DEFAULT 0,
  consecutive_failures integer NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  cooldown_until text,
  next_probe_at text,
  probe_backoff_seconds integer NOT NULL DEFAULT 0,
  recovery_started_at text,
  last_attempt_at text,
  last_success_at text,
  last_failure_at text,
  last_error_code text,
  last_error_message text,
  last_trace_id text,
  last_probe_at text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);

ALTER TABLE juhe_business.account_api_key_runtime_states
  ADD COLUMN IF NOT EXISTS last_trace_id text;

CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
  ON juhe_business.account_api_key_runtime_states(account_id, key_fingerprint);

CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
  ON juhe_business.account_api_key_runtime_states(account_id, status, cooldown_until);

CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe
  ON juhe_business.account_api_key_runtime_states(account_id, status, next_probe_at ASC, updated_at ASC, key_index ASC)
  WHERE next_probe_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_owner
  ON juhe_business.account_api_key_runtime_states(system_account_id, account_id);

-- +goose Down
ALTER TABLE juhe_business.account_api_key_runtime_states
  DROP COLUMN IF EXISTS last_trace_id;
