-- name: MarkManagementModelCatalogSnapshotRebuildDirty :exec
INSERT INTO juhe_business.model_catalog_snapshot_rebuild_requests (scope, system_account_id, generation, updated_at)
VALUES (
  sqlc.arg(scope)::text,
  sqlc.arg(system_account_id)::text,
  1,
  sqlc.arg(updated_at)::timestamptz
)
ON CONFLICT (scope, system_account_id) DO UPDATE SET
  generation = juhe_business.model_catalog_snapshot_rebuild_requests.generation + 1,
  updated_at = EXCLUDED.updated_at;

-- name: ListManagementModelCatalogSnapshotRebuildRequests :many
SELECT scope, system_account_id, generation, updated_at
FROM juhe_business.model_catalog_snapshot_rebuild_requests
ORDER BY CASE WHEN scope = 'all' THEN 0 ELSE 1 END, updated_at, system_account_id;

-- name: AckManagementModelCatalogSnapshotRebuild :execrows
DELETE FROM juhe_business.model_catalog_snapshot_rebuild_requests
WHERE scope = sqlc.arg(scope)::text
  AND system_account_id = sqlc.arg(system_account_id)::text
  AND generation = sqlc.arg(generation)::bigint;
