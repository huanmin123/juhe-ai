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

-- name: ListManagementProxyOptions :many
SELECT id, name, type, enabled
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
LIMIT sqlc.arg(row_limit)::int;
