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
