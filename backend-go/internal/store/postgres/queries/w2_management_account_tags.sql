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

-- name: LockManagementAccountTagForDelete :one
SELECT id
FROM juhe_business.account_tags
WHERE id = sqlc.arg(tag_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text
FOR UPDATE;

-- name: ManagementAccountTagHasActiveBindings :one
SELECT EXISTS (
  SELECT 1
  FROM juhe_business.account_tag_bindings AS account_tag_bindings
  INNER JOIN juhe_business.accounts AS accounts
    ON accounts.id = account_tag_bindings.account_id
    AND accounts.system_account_id = account_tag_bindings.system_account_id
    AND accounts.deleted_at IS NULL
  LEFT JOIN juhe_business.resource_authorizations AS visible_authorizations
    ON visible_authorizations.id = accounts.authorization_instance_authorization_id
    AND visible_authorizations.grantee_system_account_id = accounts.system_account_id
    AND visible_authorizations.status IN ('active', 'paused', 'expired')
  WHERE account_tag_bindings.tag_id = sqlc.arg(tag_id)::text
    AND account_tag_bindings.system_account_id = sqlc.arg(system_account_id)::text
    AND (
      accounts.authorization_instance_authorization_id IS NULL
      OR visible_authorizations.id IS NOT NULL
    )
  LIMIT 1
)::boolean;

-- name: DeleteManagementAccountTag :execrows
DELETE FROM juhe_business.account_tags
WHERE id = sqlc.arg(tag_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text;
