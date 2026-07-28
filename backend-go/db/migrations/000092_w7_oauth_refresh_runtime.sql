-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS oauth_access_token_expires_at text,
  ADD COLUMN IF NOT EXISTS oauth_refresh_token_present integer NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due
  ON juhe_business.accounts (
    provider_code,
    type,
    oauth_refresh_token_present,
    oauth_access_token_expires_at,
    status,
    id
  );

CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due
  ON juhe_business.accounts (
    provider_protocol_profile_id,
    type,
    oauth_refresh_token_present,
    (oauth_access_token_expires_at IS NOT NULL),
    oauth_access_token_expires_at ASC,
    updated_at ASC,
    id ASC
  )
  WHERE authorization_instance_authorization_id IS NULL
    AND deleted_at IS NULL;

-- +goose Down
-- Forward-only: Node and Go share these durable account fields and indexes.
SELECT 1;
