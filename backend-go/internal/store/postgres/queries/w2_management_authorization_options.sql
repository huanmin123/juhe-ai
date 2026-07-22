-- name: ListManagementAuthorizationGranteeAccounts :many
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE (
    sqlc.arg(has_ids)::boolean = false
    OR id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      username COLLATE "C" >= sqlc.arg(keyword)::text
      AND username COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(username, sqlc.arg(keyword)::text)
    )
    OR (
      display_name COLLATE "C" >= sqlc.arg(keyword)::text
      AND display_name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(display_name, sqlc.arg(keyword)::text)
    )
  )
ORDER BY status ASC, display_name ASC, username ASC, id ASC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListManagementAuthorizationGranteeTeams :many
SELECT id, name, status
FROM juhe_business.system_teams
WHERE (
    sqlc.arg(has_ids)::boolean = false
    OR id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      name COLLATE "C" >= sqlc.arg(keyword)::text
      AND name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(name, sqlc.arg(keyword)::text)
    )
  )
ORDER BY status ASC, name ASC, id ASC
LIMIT sqlc.arg(row_limit)::int;

-- name: ListManagementAuthorizationGranteeGroups :many
WITH active_grantee AS (
  SELECT id
  FROM juhe_business.system_accounts
  WHERE id = sqlc.arg(grantee_system_account_id)::text
    AND status = 'active'
  LIMIT 1
)
SELECT
  groups.id,
  groups.name
FROM juhe_business.groups AS groups
INNER JOIN active_grantee
  ON active_grantee.id = groups.system_account_id
WHERE groups.enabled = true
  AND (
    coalesce(array_length(sqlc.arg(ids)::text[], 1), 0) = 0
    OR groups.id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(provider_code)::text = ''
    OR groups.provider_code = sqlc.arg(provider_code)::text
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      groups.name COLLATE "C" >= sqlc.arg(keyword)::text
      AND groups.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(groups.name, sqlc.arg(keyword)::text)
    )
  )
ORDER BY
  CASE WHEN sqlc.arg(prefer_default)::boolean THEN groups.is_default ELSE false END DESC,
  groups.updated_at DESC,
  groups.id DESC
LIMIT sqlc.arg(row_limit)::int;
