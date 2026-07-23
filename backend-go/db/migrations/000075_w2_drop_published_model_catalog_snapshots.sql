-- +goose Up
DROP TABLE IF EXISTS juhe_business.model_catalog_snapshot_rebuild_requests;
DROP TABLE IF EXISTS juhe_business.gateway_model_catalog_snapshots;

-- +goose Down
CREATE TABLE IF NOT EXISTS juhe_business.gateway_model_catalog_snapshots (
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  protocol text NOT NULL CHECK (protocol IN ('openai', 'anthropic', 'gemini')),
  variant text NOT NULL CHECK (variant IN ('default', 'codex') OR variant LIKE 'chat_list:%' OR variant LIKE 'chat_model:%'),
  payload_json jsonb NOT NULL CHECK (jsonb_typeof(payload_json) = 'object'),
  model_count integer NOT NULL DEFAULT 0 CHECK (model_count >= 0),
  revision text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, protocol, variant)
);

CREATE INDEX IF NOT EXISTS idx_gateway_model_catalog_snapshots_updated
  ON juhe_business.gateway_model_catalog_snapshots(updated_at, system_account_id);

CREATE TABLE IF NOT EXISTS juhe_business.model_catalog_snapshot_rebuild_requests (
  scope text NOT NULL CHECK (scope IN ('all', 'personal')),
  system_account_id text NOT NULL DEFAULT '',
  generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (scope, system_account_id),
  CHECK ((scope = 'all' AND system_account_id = '') OR (scope = 'personal' AND system_account_id <> ''))
);

CREATE INDEX IF NOT EXISTS idx_model_catalog_snapshot_rebuild_requests_updated
  ON juhe_business.model_catalog_snapshot_rebuild_requests(updated_at, scope, system_account_id);
