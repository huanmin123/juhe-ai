-- name: FindPublicAPIAuthTokenByHash :one
SELECT
  sources.id AS source_ref_id,
  sources.name AS source_name,
  sources.status AS source_status,
  sources.scopes_json AS source_scopes_json,
  sources.rate_limits_json AS source_rate_limits_json,
  sources.expires_at AS source_expires_at,
  sources.last_used_at AS source_last_used_at,
  tokens.id AS token_id,
  tokens.name AS token_name,
  tokens.token_prefix AS token_prefix,
  tokens.status AS token_status,
  tokens.scopes_json AS token_scopes_json,
  tokens.expires_at AS token_expires_at,
  tokens.last_used_at AS token_last_used_at
FROM juhe_business.external_integration_source_tokens AS tokens
JOIN juhe_business.external_integration_sources AS sources ON sources.id = tokens.source_ref_id
WHERE tokens.token_hash = $1
LIMIT 1;

-- name: TouchPublicAPIAuthSourceLastUsed :exec
UPDATE juhe_business.external_integration_sources
SET last_used_at = $2,
    updated_at = $2
WHERE id = $1;

-- name: TouchPublicAPIAuthTokenLastUsed :exec
UPDATE juhe_business.external_integration_source_tokens
SET last_used_at = $2,
    updated_at = $2
WHERE id = $1;

-- name: InsertPublicAPILog :exec
INSERT INTO juhe_dataset.public_api_logs (
  id, trace_id, source_ref_id, source_name, token_id, token_name, token_prefix, is_test_token,
  method, path, query_string, client_ip, user_agent, status_code, success, duration_ms,
  request_size_bytes, response_size_bytes, request_capture_status, response_capture_status,
  request_data_json, response_data_json, error_code, error_message, started_at, ended_at, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15, $16,
  $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27
)
ON CONFLICT(id) DO NOTHING;

-- name: GetPublicAPILogRetentionDays :one
SELECT value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key = 'publicApiLogRetentionDays'
LIMIT 1;

-- name: CleanupPublicAPILogsBefore :execrows
WITH stale_public_api_logs AS (
  SELECT id
  FROM juhe_dataset.public_api_logs
  WHERE created_at < sqlc.arg(cutoff_created_at)::timestamptz
  ORDER BY created_at ASC, id ASC
  LIMIT sqlc.arg(row_limit)::int
)
DELETE FROM juhe_dataset.public_api_logs
WHERE id IN (SELECT id FROM stale_public_api_logs);
