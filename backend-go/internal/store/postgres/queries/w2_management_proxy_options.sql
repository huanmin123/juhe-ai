-- name: ListManagementProxies :many
SELECT
  id,
  name,
  description,
  type,
  host,
  port,
  username,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at
FROM juhe_business.proxy_profiles
WHERE (
  sqlc.arg(has_keyword)::boolean = false
  OR (
    name COLLATE "C" >= sqlc.arg(keyword)::text
    AND name COLLATE "C" < sqlc.arg(keyword_upper)::text
    AND starts_with(name, sqlc.arg(keyword)::text)
  )
)
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;

-- name: FindManagementProxy :one
SELECT
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  password_encrypted,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at
FROM juhe_business.proxy_profiles
WHERE id = sqlc.arg(id)::text
LIMIT 1;

-- name: FindManagementProxyForUpdate :one
SELECT
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  password_encrypted,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at
FROM juhe_business.proxy_profiles
WHERE id = sqlc.arg(id)::text
FOR UPDATE;

-- name: ListManagementProxyOptions :many
SELECT id, name, type, enabled
FROM (
  (
    SELECT id, name, type, enabled, updated_at
    FROM juhe_business.proxy_profiles
    WHERE enabled = true
      AND (
        sqlc.arg(has_keyword)::boolean = false
        OR (
          name COLLATE "C" >= sqlc.arg(keyword)::text
          AND name COLLATE "C" < sqlc.arg(keyword_upper)::text
          AND starts_with(name, sqlc.arg(keyword)::text)
        )
      )
    ORDER BY name ASC, updated_at DESC, id ASC
    LIMIT sqlc.arg(row_limit)::int
  )
  UNION
  (
    SELECT id, name, type, enabled, updated_at
    FROM juhe_business.proxy_profiles
    WHERE enabled = true
      AND sqlc.arg(has_selected_ids)::boolean = true
      AND id = ANY(sqlc.arg(selected_ids)::text[])
  )
) proxy_options
ORDER BY name ASC, updated_at DESC, id ASC;

-- name: CreateManagementProxy :one
INSERT INTO juhe_business.proxy_profiles (
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  password_encrypted,
  enabled,
  test_status,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(name)::text,
  sqlc.narg(description)::text,
  sqlc.arg(type)::text,
  sqlc.arg(host)::text,
  sqlc.arg(port)::int,
  sqlc.narg(username)::text,
  sqlc.narg(password_encrypted)::text,
  sqlc.arg(enabled)::bool,
  'unknown',
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at;

-- name: UpdateManagementProxy :one
UPDATE juhe_business.proxy_profiles
SET
  name = sqlc.arg(name)::text,
  description = sqlc.narg(description)::text,
  type = sqlc.arg(type)::text,
  host = sqlc.arg(host)::text,
  port = sqlc.arg(port)::int,
  username = sqlc.narg(username)::text,
  password_encrypted = sqlc.narg(password_encrypted)::text,
  enabled = sqlc.arg(enabled)::bool,
  test_status = CASE WHEN sqlc.arg(reset_test_state)::bool THEN 'unknown' ELSE test_status END,
  latency_ms = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE latency_ms END,
  outbound_ip = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE outbound_ip END,
  outbound_region = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE outbound_region END,
  last_test_message = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE last_test_message END,
  last_tested_at = CASE WHEN sqlc.arg(reset_test_state)::bool THEN NULL ELSE last_tested_at END,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(id)::text
RETURNING
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at;

-- name: UpdateManagementProxyTestState :one
UPDATE juhe_business.proxy_profiles
SET
  test_status = sqlc.arg(test_status)::text,
  latency_ms = sqlc.narg(latency_ms)::int,
  outbound_ip = CASE WHEN sqlc.arg(set_outbound_ip)::bool THEN sqlc.narg(outbound_ip)::text ELSE outbound_ip END,
  outbound_region = CASE WHEN sqlc.arg(set_outbound_region)::bool THEN sqlc.narg(outbound_region)::text ELSE outbound_region END,
  last_test_message = sqlc.arg(last_test_message)::text,
  last_tested_at = sqlc.arg(last_tested_at)::timestamptz,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(id)::text
RETURNING
  id,
  system_account_id,
  name,
  description,
  type,
  host,
  port,
  username,
  enabled,
  test_status,
  latency_ms,
  outbound_ip,
  outbound_region,
  last_test_message,
  last_tested_at;

-- name: DeleteManagementProxy :execrows
DELETE FROM juhe_business.proxy_profiles
WHERE id = sqlc.arg(id)::text;

-- name: ListManagementProxyAccountBindings :many
SELECT id, name
FROM juhe_business.accounts
WHERE proxy_profile_id = sqlc.arg(proxy_id)::text
  AND deleted_at IS NULL
ORDER BY id ASC
LIMIT sqlc.arg(row_limit)::int;
