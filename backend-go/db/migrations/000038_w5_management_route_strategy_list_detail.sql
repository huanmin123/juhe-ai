-- +goose Up
CREATE INDEX IF NOT EXISTS idx_route_strategies_management_updated
  ON juhe_business.route_strategies (updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_management_updated
  ON juhe_business.route_strategies (system_account_id, updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_route_strategies_name_lookup
  ON juhe_business.route_strategies (name COLLATE "C", id);

-- +goose Down
-- no-op: management route strategy list/detail indexes are part of the current schema.
