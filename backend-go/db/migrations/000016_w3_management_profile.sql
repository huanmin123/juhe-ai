-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower
  ON juhe_business.system_accounts (lower(display_name));

-- +goose Down
-- no-op: display_name uniqueness is part of the current system account contract.
