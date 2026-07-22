-- +goose Up
ALTER TABLE juhe_business.gateway_model_catalog_snapshots
  DROP CONSTRAINT IF EXISTS gateway_model_catalog_snapshots_variant_check;

DELETE FROM juhe_business.gateway_model_catalog_snapshots
WHERE variant = 'chat';

ALTER TABLE juhe_business.gateway_model_catalog_snapshots
  ADD CONSTRAINT gateway_model_catalog_snapshots_variant_check
  CHECK (
    variant IN ('default', 'codex')
    OR variant LIKE 'chat_list:%'
    OR variant LIKE 'chat_model:%'
  );

INSERT INTO juhe_business.model_catalog_snapshot_rebuild_requests (
  scope, system_account_id, generation, updated_at
) VALUES ('all', '', 1, now())
ON CONFLICT (scope, system_account_id) DO UPDATE SET
  generation = juhe_business.model_catalog_snapshot_rebuild_requests.generation + 1,
  updated_at = excluded.updated_at;

-- +goose Down
-- Intentionally non-destructive: the runtime at schema 69 already reads and writes
-- the split chat variants, so restoring the historical constraint would recreate
-- the production outage and discard rebuildable-but-required published snapshots.
SELECT 1;
