-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS authorization_instance_source_account_id text,
  ADD COLUMN IF NOT EXISTS authorization_instance_authorization_id text,
  ADD COLUMN IF NOT EXISTS authorization_instance_owner_system_account_id text;

CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_scope
  ON juhe_business.accounts(system_account_id, authorization_instance_authorization_id, id)
  WHERE authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source
  ON juhe_business.accounts(authorization_instance_source_account_id, authorization_instance_owner_system_account_id)
  WHERE authorization_instance_source_account_id IS NOT NULL AND deleted_at IS NULL;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'fk_accounts_authorization_instance_authorization'
      AND conrelid = 'juhe_business.accounts'::regclass
  ) THEN
    ALTER TABLE juhe_business.accounts
      ADD CONSTRAINT fk_accounts_authorization_instance_authorization
      FOREIGN KEY (authorization_instance_authorization_id)
      REFERENCES juhe_business.resource_authorizations(id)
      ON DELETE SET NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'fk_accounts_authorization_instance_owner'
      AND conrelid = 'juhe_business.accounts'::regclass
  ) THEN
    ALTER TABLE juhe_business.accounts
      ADD CONSTRAINT fk_accounts_authorization_instance_owner
      FOREIGN KEY (authorization_instance_owner_system_account_id)
      REFERENCES juhe_business.system_accounts(id)
      ON DELETE SET NULL;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- no-op: authorization instance account columns are business data.
