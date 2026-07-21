-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.model_catalog_snapshot_rebuild_requests (
  scope text NOT NULL CHECK (scope IN ('all', 'personal')),
  system_account_id text NOT NULL DEFAULT '',
  generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (scope, system_account_id),
  CHECK (
    (scope = 'all' AND system_account_id = '')
    OR (scope = 'personal' AND system_account_id <> '')
  )
);

CREATE INDEX IF NOT EXISTS idx_model_catalog_snapshot_rebuild_requests_updated
  ON juhe_business.model_catalog_snapshot_rebuild_requests(updated_at, scope, system_account_id);

-- +goose Down
DROP TABLE IF EXISTS juhe_business.model_catalog_snapshot_rebuild_requests;
