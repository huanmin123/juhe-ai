-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS config_revision integer NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE juhe_business.accounts
  DROP COLUMN IF EXISTS config_revision;
