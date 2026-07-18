-- name: ListManagementExternalIntegrationSources :many
SELECT
  sources.id,
  sources.name,
  sources.status,
  sources.scopes_json,
  sources.rate_limits_json,
  sources.expires_at,
  sources.notes,
  sources.last_used_at,
  sources.created_at,
  sources.updated_at
FROM juhe_business.external_integration_sources AS sources
WHERE (
    sqlc.arg(status)::text = 'all'
    OR sources.status = sqlc.arg(status)::text
  )
  AND (
    sqlc.arg(keyword)::text = ''
    OR (
      lower(sources.name) COLLATE "C" >= sqlc.arg(keyword)::text
      AND lower(sources.name) COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(lower(sources.name), sqlc.arg(keyword)::text)
    )
  )
ORDER BY sources.updated_at DESC, sources.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementExternalIntegrationSourceTokenStats :many
SELECT
  tokens.source_ref_id,
  COUNT(*) AS token_count,
  COUNT(*) FILTER (WHERE tokens.status = 'active') AS active_token_count
FROM juhe_business.external_integration_source_tokens AS tokens
WHERE tokens.source_ref_id = ANY(sqlc.arg(source_ids)::text[])
GROUP BY tokens.source_ref_id;

-- name: ListManagementExternalIntegrationSourcePrimaryTokens :many
SELECT DISTINCT ON (tokens.source_ref_id)
  tokens.source_ref_id,
  tokens.id,
  tokens.name,
  tokens.token_prefix,
  tokens.token_suffix,
  tokens.status,
  tokens.scopes_json,
  tokens.expires_at,
  tokens.last_used_at,
  tokens.created_at,
  tokens.updated_at,
  tokens.revoked_at
FROM juhe_business.external_integration_source_tokens AS tokens
WHERE tokens.source_ref_id = ANY(sqlc.arg(source_ids)::text[])
ORDER BY
  tokens.source_ref_id ASC,
  CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC,
  tokens.created_at DESC,
  tokens.id DESC;

-- name: FindManagementExternalIntegrationSource :one
SELECT
  sources.id,
  sources.name,
  sources.status,
  sources.scopes_json,
  sources.rate_limits_json,
  sources.expires_at,
  sources.notes,
  sources.last_used_at,
  sources.created_at,
  sources.updated_at
FROM juhe_business.external_integration_sources AS sources
WHERE sources.id = sqlc.arg(source_id)::text;

-- name: ListManagementExternalIntegrationSourceTokens :many
SELECT
  tokens.source_ref_id,
  tokens.id,
  tokens.name,
  tokens.token_prefix,
  tokens.token_suffix,
  tokens.status,
  tokens.scopes_json,
  tokens.expires_at,
  tokens.last_used_at,
  tokens.created_at,
  tokens.updated_at,
  tokens.revoked_at
FROM juhe_business.external_integration_source_tokens AS tokens
WHERE tokens.source_ref_id = sqlc.arg(source_id)::text
ORDER BY tokens.created_at DESC, tokens.id DESC;

-- name: FindManagementExternalIntegrationSourceTokenSecret :one
SELECT tokens.token_secret_encrypted
FROM juhe_business.external_integration_source_tokens AS tokens
JOIN juhe_business.external_integration_sources AS sources
  ON sources.id = tokens.source_ref_id
WHERE sources.id = sqlc.arg(source_id)::text
  AND tokens.id = sqlc.arg(token_id)::text;

-- name: FindManagementExternalIntegrationSourceForUpdate :one
SELECT
  sources.id,
  sources.name,
  sources.status,
  sources.scopes_json,
  sources.rate_limits_json,
  sources.expires_at,
  sources.notes,
  sources.last_used_at,
  sources.created_at,
  sources.updated_at
FROM juhe_business.external_integration_sources AS sources
WHERE sources.id = sqlc.arg(source_id)::text
FOR UPDATE;

-- name: InsertManagementExternalIntegrationSource :one
INSERT INTO juhe_business.external_integration_sources (
  id,
  name,
  status,
  scopes_json,
  rate_limits_json,
  expires_at,
  notes,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(source_id)::text,
  sqlc.arg(name)::text,
  sqlc.arg(status)::text,
  sqlc.arg(scopes_json)::text,
  sqlc.arg(rate_limits_json)::text,
  sqlc.narg(expires_at)::timestamptz,
  sqlc.narg(notes)::text,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING
  id,
  name,
  status,
  scopes_json,
  rate_limits_json,
  expires_at,
  notes,
  last_used_at,
  created_at,
  updated_at;

-- name: InsertManagementExternalIntegrationSourceToken :one
INSERT INTO juhe_business.external_integration_source_tokens (
  id,
  source_ref_id,
  name,
  token_hash,
  token_secret_encrypted,
  token_prefix,
  token_suffix,
  status,
  scopes_json,
  expires_at,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(token_id)::text,
  sqlc.arg(source_id)::text,
  sqlc.arg(token_name)::text,
  sqlc.arg(token_hash)::text,
  sqlc.arg(token_secret_encrypted)::text,
  sqlc.arg(token_prefix)::text,
  sqlc.arg(token_suffix)::text,
  sqlc.arg(token_status)::text,
  sqlc.arg(token_scopes_json)::text,
  sqlc.narg(token_expires_at)::timestamptz,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING
  source_ref_id,
  id,
  name,
  token_prefix,
  token_suffix,
  status,
  scopes_json,
  expires_at,
  last_used_at,
  created_at,
  updated_at,
  revoked_at;

-- name: CountManagementExternalIntegrationSourceTokensForDelete :one
SELECT COUNT(*)::bigint
FROM juhe_business.external_integration_source_tokens AS tokens
WHERE tokens.source_ref_id = sqlc.arg(source_id)::text;

-- name: DeleteManagementExternalIntegrationSource :one
DELETE FROM juhe_business.external_integration_sources
WHERE id = sqlc.arg(source_id)::text
RETURNING id;

-- name: UpdateManagementExternalIntegrationSource :one
UPDATE juhe_business.external_integration_sources
SET
  name = sqlc.arg(name)::text,
  status = sqlc.arg(status)::text,
  scopes_json = sqlc.arg(scopes_json)::text,
  rate_limits_json = sqlc.arg(rate_limits_json)::text,
  expires_at = sqlc.narg(expires_at)::timestamptz,
  notes = sqlc.narg(notes)::text,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(source_id)::text
RETURNING
  id,
  name,
  status,
  scopes_json,
  rate_limits_json,
  expires_at,
  notes,
  last_used_at,
  created_at,
  updated_at;

-- name: SyncManagementExternalIntegrationSourceTokens :execrows
UPDATE juhe_business.external_integration_source_tokens
SET
  name = sqlc.arg(token_name)::text,
  status = CASE WHEN status = 'revoked' THEN status ELSE sqlc.arg(source_status)::text END,
  scopes_json = sqlc.arg(scopes_json)::text,
  expires_at = sqlc.narg(expires_at)::timestamptz,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE source_ref_id = sqlc.arg(source_id)::text;
