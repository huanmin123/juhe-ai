-- name: ListPageDataDirtyDomains :many
SELECT domain, generation
FROM juhe_business.page_data_dirty_domains
WHERE is_dirty = TRUE
ORDER BY domain ASC;

-- name: MarkPageDataDomainDirty :one
INSERT INTO juhe_business.page_data_dirty_domains(domain, generation, is_dirty, updated_at)
VALUES (sqlc.arg(domain)::text, 1, TRUE, NOW())
ON CONFLICT(domain) DO UPDATE SET
  generation = juhe_business.page_data_dirty_domains.generation + 1,
  is_dirty = TRUE,
  updated_at = EXCLUDED.updated_at
RETURNING generation;

-- name: ClearPageDataDomainDirty :execrows
UPDATE juhe_business.page_data_dirty_domains
SET is_dirty = FALSE, updated_at = NOW()
WHERE domain = sqlc.arg(domain)::text
  AND generation = sqlc.arg(generation)::bigint
  AND is_dirty = TRUE;
