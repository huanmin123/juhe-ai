-- +goose Up
ALTER TABLE juhe_business.api_keys
  ADD COLUMN IF NOT EXISTS key_secret_encrypted text;

-- +goose Down
-- no-op: key_secret_encrypted is part of the current api_keys contract.