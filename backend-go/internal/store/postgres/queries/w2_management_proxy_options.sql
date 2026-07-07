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
