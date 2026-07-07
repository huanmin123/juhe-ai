-- name: ListManagementGroupOptions :many
SELECT
  groups.id,
  groups.system_account_id,
  system_accounts.display_name AS system_account_name,
  groups.name,
  groups.provider_code,
  groups.enabled,
  groups.is_default,
  groups.group_type,
  groups.scheduling_policy_json
FROM juhe_business.groups AS groups
LEFT JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = groups.system_account_id
WHERE (sqlc.arg(system_account_id)::text = '' OR groups.system_account_id = sqlc.arg(system_account_id)::text)
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
      (
        groups.name COLLATE "C" >= sqlc.arg(keyword)::text
        AND groups.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      )
      OR (
        groups.provider_code COLLATE "C" >= sqlc.arg(keyword)::text
        AND groups.provider_code COLLATE "C" < sqlc.arg(keyword_upper)::text
      )
    )
  )
ORDER BY
  CASE WHEN sqlc.arg(prefer_default)::boolean THEN groups.is_default ELSE false END DESC,
  groups.updated_at DESC,
  groups.id DESC
LIMIT sqlc.arg(row_limit)::int;
