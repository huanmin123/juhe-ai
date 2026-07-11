-- +goose Up
CREATE INDEX IF NOT EXISTS idx_api_keys_default_updated
  ON juhe_business.api_keys (is_default DESC, updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_api_keys_status_default_updated
  ON juhe_business.api_keys (status, is_default DESC, updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_api_keys_route_default_updated
  ON juhe_business.api_keys (route_strategy_id, is_default DESC, updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_api_keys_name_c_lookup
  ON juhe_business.api_keys (name COLLATE "C", id);

CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_scope_lookup
  ON juhe_stats.usage_stats_totals (scope_type, scope_id);

-- +goose Down
-- no-op: management API Key list indexes are part of the current schema.
