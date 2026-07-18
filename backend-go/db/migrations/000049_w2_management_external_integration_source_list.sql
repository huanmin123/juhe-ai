-- +goose Up
CREATE INDEX IF NOT EXISTS idx_external_integration_sources_name_lower_c_prefix
  ON juhe_business.external_integration_sources ((lower(name) COLLATE "C"));

-- +goose Down
DROP INDEX IF EXISTS juhe_business.idx_external_integration_sources_name_lower_c_prefix;
