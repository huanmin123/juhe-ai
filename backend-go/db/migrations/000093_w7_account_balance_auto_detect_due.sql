-- +goose Up
CREATE INDEX IF NOT EXISTS idx_accounts_balance_auto_detect_due
  ON juhe_business.accounts (balance_query_next_refresh_at ASC, id ASC)
  WHERE status = 'active'
    AND schedulable = true
    AND type = 'api_key'
    AND balance_query_enabled = false
    AND balance_query_config_json = '{}'
    AND deleted_at IS NULL
    AND authorization_instance_authorization_id IS NULL
    AND balance_query_next_refresh_at IS NOT NULL;

-- +goose Down
-- Forward-only: this additive index is safe to retain for older binaries.
SELECT 1;
