-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS proxy_profile_id text REFERENCES juhe_business.proxy_profiles(id);

CREATE INDEX IF NOT EXISTS idx_accounts_proxy_profile
  ON juhe_business.accounts(proxy_profile_id, id)
  WHERE proxy_profile_id IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
-- no-op: proxy profile bindings are part of current account routing configuration.
