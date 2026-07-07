-- name: ListManagementRouteStrategyOptions :many
SELECT
  route_strategies.id,
  route_strategies.system_account_id,
  system_accounts.display_name AS system_account_name,
  route_strategies.name,
  route_strategies.mode,
  route_strategies.status,
  route_strategies.is_default
FROM juhe_business.route_strategies AS route_strategies
LEFT JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = route_strategies.system_account_id
WHERE (sqlc.arg(system_account_id)::text = '' OR route_strategies.system_account_id = sqlc.arg(system_account_id)::text)
  AND (
    coalesce(array_length(sqlc.arg(ids)::text[], 1), 0) = 0
    OR route_strategies.id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      route_strategies.name COLLATE "C" >= sqlc.arg(keyword)::text
      AND route_strategies.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(route_strategies.name, sqlc.arg(keyword)::text)
    )
  )
  AND (sqlc.arg(active_only)::boolean = false OR route_strategies.status = 'active')
ORDER BY route_strategies.is_default DESC,
  route_strategies.updated_at DESC,
  route_strategies.name ASC,
  route_strategies.id ASC
LIMIT sqlc.arg(row_limit)::int;
