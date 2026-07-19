-- +goose Up
ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS catalog_visible boolean NOT NULL DEFAULT true;

DROP INDEX IF EXISTS juhe_business.idx_custom_provider_models_catalog_lookup;
CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
  ON juhe_business.custom_provider_models(provider_code, status, catalog_visible, scope, system_account_id, model);

CREATE TABLE IF NOT EXISTS juhe_business.gateway_model_catalog_snapshots (
  system_account_id text NOT NULL,
  protocol text NOT NULL CHECK (protocol IN ('openai', 'anthropic', 'gemini')),
  variant text NOT NULL CHECK (variant IN ('default', 'codex', 'chat')),
  payload_json text NOT NULL CHECK (jsonb_typeof(payload_json::jsonb) = 'object'),
  model_count integer NOT NULL DEFAULT 0 CHECK (model_count >= 0),
  revision text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, protocol, variant)
);

CREATE INDEX IF NOT EXISTS idx_gateway_model_catalog_snapshots_updated
  ON juhe_business.gateway_model_catalog_snapshots(updated_at, system_account_id);

-- +goose Down
DROP TABLE IF EXISTS juhe_business.gateway_model_catalog_snapshots;
DROP INDEX IF EXISTS juhe_business.idx_custom_provider_models_catalog_lookup;
CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
  ON juhe_business.custom_provider_models(provider_code, status, scope, system_account_id, model);
ALTER TABLE juhe_business.custom_provider_models DROP COLUMN IF EXISTS catalog_visible;
