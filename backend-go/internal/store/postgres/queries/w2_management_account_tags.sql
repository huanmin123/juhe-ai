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
    AND visible_authorizations.resource_type = 'account'
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

-- name: LockManagementAccountForTagUpdate :one
SELECT accounts.id
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.resource_authorizations AS authorizations
  ON authorizations.id = accounts.authorization_instance_authorization_id
  AND authorizations.resource_type = 'account'
  AND authorizations.grantee_system_account_id = accounts.system_account_id
  AND authorizations.resource_id = accounts.authorization_instance_source_account_id
WHERE accounts.id = sqlc.arg(account_id)::text
  AND accounts.system_account_id = sqlc.arg(system_account_id)::text
  AND accounts.deleted_at IS NULL
  AND (
    accounts.authorization_instance_authorization_id IS NULL
    OR authorizations.status IN ('active', 'paused', 'expired')
  )
FOR UPDATE OF accounts;

-- name: DeleteManagementAccountTagBindingsForAccount :exec
DELETE FROM juhe_business.account_tag_bindings
WHERE account_id = sqlc.arg(account_id)::text
  AND system_account_id = sqlc.arg(system_account_id)::text;

-- name: UpsertManagementAccountTagForAccount :one
INSERT INTO juhe_business.account_tags (
  id, system_account_id, name, created_at, updated_at
) VALUES (
  sqlc.arg(tag_id)::text,
  sqlc.arg(system_account_id)::text,
  sqlc.arg(name)::text,
  now(),
  now()
)
ON CONFLICT (system_account_id, name) DO UPDATE SET
  name = EXCLUDED.name
RETURNING id, name, created_at, updated_at;

-- name: InsertManagementAccountTagBindingForAccount :exec
INSERT INTO juhe_business.account_tag_bindings (
  account_id, tag_id, system_account_id, created_at
) VALUES (
  sqlc.arg(account_id)::text,
  sqlc.arg(tag_id)::text,
  sqlc.arg(system_account_id)::text,
  now()
)
ON CONFLICT (account_id, tag_id) DO NOTHING;

-- name: GetManagementAccountTagUpdateAccount :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  COALESCE(accounts.authorization_instance_owner_system_account_id, accounts.system_account_id) AS owner_system_account_id,
  COALESCE(accounts.authorization_instance_authorization_id, '') AS account_authorization_id
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.resource_authorizations AS authorizations
  ON authorizations.id = accounts.authorization_instance_authorization_id
  AND authorizations.resource_type = 'account'
  AND authorizations.grantee_system_account_id = accounts.system_account_id
  AND authorizations.resource_id = accounts.authorization_instance_source_account_id
WHERE accounts.id = sqlc.arg(account_id)::text
  AND accounts.system_account_id = sqlc.arg(system_account_id)::text
  AND accounts.deleted_at IS NULL
  AND (
    accounts.authorization_instance_authorization_id IS NULL
    OR authorizations.status IN ('active', 'paused', 'expired')
  )
LIMIT 1;

-- name: ListManagementAccountTagsForAccount :many
SELECT
  account_tags.id,
  account_tags.name,
  account_tags.created_at,
  account_tags.updated_at
FROM juhe_business.account_tag_bindings AS account_tag_bindings
INNER JOIN juhe_business.account_tags AS account_tags
  ON account_tags.id = account_tag_bindings.tag_id
  AND account_tags.system_account_id = account_tag_bindings.system_account_id
WHERE account_tag_bindings.account_id = sqlc.arg(account_id)::text
  AND account_tag_bindings.system_account_id = sqlc.arg(system_account_id)::text
ORDER BY account_tags.name ASC, account_tags.id ASC;
