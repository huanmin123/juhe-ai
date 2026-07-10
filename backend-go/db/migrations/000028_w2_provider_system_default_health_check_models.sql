-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.provider_system_default_health_check_models (
  provider_code text PRIMARY KEY REFERENCES juhe_business.providers(code) ON DELETE CASCADE,
  model text NOT NULL CHECK (btrim(model) <> ''),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_provider_system_default_health_check_models_model
  ON juhe_business.provider_system_default_health_check_models(model, provider_code);

-- +goose Down
DROP INDEX IF EXISTS juhe_business.idx_provider_system_default_health_check_models_model;
DROP TABLE IF EXISTS juhe_business.provider_system_default_health_check_models;
