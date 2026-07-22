-- name: LockManagementAccountDeleteTarget :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.authorization_instance_authorization_id
FROM juhe_business.accounts AS accounts
WHERE accounts.id = sqlc.arg(account_id)::text
  AND accounts.deleted_at IS NULL
  AND (
    sqlc.arg(can_access_all)::boolean
    OR accounts.system_account_id = sqlc.arg(effective_system_account_id)::text
  )
FOR UPDATE OF accounts;

-- name: ListManagementAccountDeleteInstances :many
SELECT accounts.id, accounts.system_account_id
FROM juhe_business.accounts AS accounts
WHERE accounts.authorization_instance_source_account_id = sqlc.arg(source_account_id)::text
  AND accounts.deleted_at IS NULL
ORDER BY accounts.created_at ASC, accounts.id ASC
FOR UPDATE OF accounts;

-- name: ListManagementAccountDeleteAuthorizationIDs :many
SELECT resource_authorizations.id
FROM juhe_business.resource_authorizations AS resource_authorizations
WHERE resource_authorizations.resource_type = 'account'
  AND resource_authorizations.resource_id = sqlc.arg(account_id)::text
  AND resource_authorizations.status <> 'returned'
ORDER BY resource_authorizations.id ASC
FOR UPDATE OF resource_authorizations;

-- name: RevokeManagementAccountDeleteGrants :exec
UPDATE juhe_business.resource_authorization_grants
SET status = 'revoked',
    revoked_by = COALESCE(revoked_by, sqlc.arg(revoked_by)::text),
    revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at)::timestamptz),
    updated_at = sqlc.arg(updated_at)::timestamptz
WHERE resource_type = 'account'
  AND resource_id = sqlc.arg(account_id)::text
  AND status NOT IN ('revoked', 'returned');

-- name: RevokeManagementAccountDeleteSources :exec
UPDATE juhe_business.resource_authorization_sources
SET status = 'revoked',
    ended_at = COALESCE(ended_at, sqlc.arg(ended_at)::timestamptz),
    ended_reason = COALESCE(ended_reason, 'account_deleted'),
    revoked_by = sqlc.arg(revoked_by)::text,
    revoked_at = sqlc.arg(revoked_at)::timestamptz,
    updated_at = sqlc.arg(updated_at)::timestamptz
WHERE authorization_id = ANY(sqlc.arg(authorization_ids)::text[])
  AND status IN ('active', 'superseded');

-- name: RevokeManagementAccountDeleteAuthorizations :exec
UPDATE juhe_business.resource_authorizations
SET status = 'revoked',
    effective_source_type = NULL,
    effective_source_team_id = NULL,
    revoked_by = COALESCE(revoked_by, sqlc.arg(revoked_by)::text),
    revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at)::timestamptz),
    revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
    last_source_changed_at = sqlc.arg(last_source_changed_at)::timestamptz,
    updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = ANY(sqlc.arg(authorization_ids)::text[])
  AND status <> 'returned';

-- name: LogicallyDeleteManagementAccounts :many
UPDATE juhe_business.accounts
SET status = 'disabled',
    schedulable = false,
    cooldown_until = NULL,
    deleted_at = sqlc.arg(deleted_at)::timestamptz,
    deleted_by = sqlc.arg(deleted_by)::text,
    updated_at = sqlc.arg(updated_at)::timestamptz
WHERE deleted_at IS NULL
  AND id = ANY(sqlc.arg(account_ids)::text[])
RETURNING id;

-- name: DeleteManagementAccountTagBindings :exec
DELETE FROM juhe_business.account_tag_bindings
WHERE account_id = ANY(sqlc.arg(account_ids)::text[]);

-- name: DeleteManagementAccountSearchTerms :exec
DELETE FROM juhe_business.account_name_search_terms
WHERE account_id = ANY(sqlc.arg(account_ids)::text[]);

-- name: DeleteManagementAccountSearchDocuments :exec
DELETE FROM juhe_business.account_name_search_documents
WHERE account_id = ANY(sqlc.arg(account_ids)::text[]);
