-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.account_model_mappings (
  account_id text NOT NULL REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  source_model text NOT NULL CHECK (btrim(source_model) <> ''),
  source_endpoint_family text NOT NULL CHECK (btrim(source_endpoint_family) <> ''),
  upstream_model text NOT NULL CHECK (btrim(upstream_model) <> ''),
  upstream_endpoint_family text NOT NULL CHECK (btrim(upstream_endpoint_family) <> ''),
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, source_model, source_endpoint_family)
);

CREATE INDEX IF NOT EXISTS idx_account_model_mappings_source
  ON juhe_business.account_model_mappings(provider_code, source_model, source_endpoint_family, account_id);
CREATE INDEX IF NOT EXISTS idx_account_model_mappings_upstream
  ON juhe_business.account_model_mappings(provider_code, upstream_model, upstream_endpoint_family, account_id);

-- +goose Down
-- no-op: account model mappings are business metadata shared with Node schema.
