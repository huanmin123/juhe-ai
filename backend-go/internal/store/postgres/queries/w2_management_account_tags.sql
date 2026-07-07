-- name: ListManagementAccountTags :many
SELECT
  account_tags.id,
  account_tags.name,
  count(accounts.id)::bigint AS account_count,
  account_tags.created_at,
  account_tags.updated_at
FROM juhe_business.account_tags AS account_tags
LEFT JOIN juhe_business.account_tag_bindings AS account_tag_bindings
  ON account_tag_bindings.tag_id = account_tags.id
  AND account_tag_bindings.system_account_id = account_tags.system_account_id
LEFT JOIN juhe_business.accounts AS accounts
  ON accounts.id = account_tag_bindings.account_id
  AND accounts.system_account_id = account_tag_bindings.system_account_id
  AND accounts.deleted_at IS NULL
WHERE account_tags.system_account_id = sqlc.arg(system_account_id)::text
GROUP BY
  account_tags.id,
  account_tags.name,
  account_tags.created_at,
  account_tags.updated_at
ORDER BY account_tags.name ASC, account_tags.id ASC;
