-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS balance_query_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS balance_query_config_json text NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS balance_query_next_refresh_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_accounts_balance_refresh_due
  ON juhe_business.accounts (balance_query_next_refresh_at, id)
  WHERE deleted_at IS NULL
    AND authorization_instance_authorization_id IS NULL
    AND status = 'active'
    AND schedulable = true
    AND type = 'api_key'
    AND balance_query_enabled = true;

-- +goose Down
-- no-op: balance query configuration is current account state and remains readable by the previous release.
