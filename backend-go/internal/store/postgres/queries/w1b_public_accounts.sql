-- name: FindPublicAccountTargetByUsername :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE lower(username) = lower($1)
LIMIT 1;

-- name: FindPublicAccountTargetByID :one
SELECT id, username, display_name, status
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1;

-- name: InsertPublicAccountSystemAccount :exec
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, description, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(username), sqlc.arg(display_name), sqlc.arg(description), 'user', 'active', sqlc.arg(password_hash),
  true, false, sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: FindPublicAccountProviderProfile :one
SELECT
  profiles.id,
  profiles.provider_code,
  profiles.name,
  profiles.enabled AS profile_enabled,
  providers.enabled AS provider_enabled,
  providers.default_supported_models_json,
  COALESCE(health_check_defaults.model, profiles.default_health_check_model) AS default_health_check_model,
  profiles.protocol_code,
  profiles.protocol_version,
  profiles.account_types_json
FROM juhe_business.provider_protocol_profiles AS profiles
JOIN juhe_business.providers AS providers
  ON providers.code = profiles.provider_code
LEFT JOIN juhe_business.provider_default_health_check_models AS health_check_defaults
  ON health_check_defaults.system_account_id = sqlc.arg(system_account_id)
  AND health_check_defaults.provider_code = profiles.provider_code
WHERE profiles.provider_code = sqlc.arg(provider_code)
  AND profiles.id = sqlc.arg(profile_id)
LIMIT 1;

-- name: FindExistingPublicAccountGroupByName :one
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  enabled,
  group_type
FROM juhe_business.groups
WHERE system_account_id = sqlc.arg(system_account_id)
  AND provider_code = sqlc.arg(provider_code)
  AND lower(name) = lower(sqlc.arg(name))
LIMIT 1;

-- name: FindPublicAccountGroupByID :one
SELECT
  id,
  system_account_id,
  name,
  provider_code,
  enabled,
  group_type
FROM juhe_business.groups
WHERE id = $1
LIMIT 1;

-- name: InsertPublicAccountGroup :one
INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(system_account_id), sqlc.arg(name), sqlc.arg(provider_code), sqlc.arg(description),
  sqlc.arg(enabled), false, sqlc.arg(group_type), NULL, sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  enabled,
  group_type;

-- name: ListPublicAccounts :many
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.credentials_encrypted,
  accounts.credential_fingerprint,
  accounts.credential_mask,
  accounts.client_compatibility,
  accounts.health_check_model,
  accounts.schedulable,
  accounts.availability_schedule_json,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.notes,
  accounts.created_at,
  accounts.updated_at,
  groups.id AS bound_group_id,
  groups.name AS bound_group_name
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.account_id = accounts.id
  AND group_accounts.system_account_id = accounts.system_account_id
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
WHERE accounts.system_account_id = sqlc.arg(system_account_id)
  AND accounts.deleted_at IS NULL
  AND (sqlc.arg(provider_code)::text = '' OR accounts.provider_code = sqlc.arg(provider_code))
  AND (sqlc.arg(provider_protocol_profile_id)::text = '' OR accounts.provider_protocol_profile_id = sqlc.arg(provider_protocol_profile_id))
  AND (sqlc.arg(group_id)::text = '' OR group_accounts.group_id = sqlc.arg(group_id))
  AND (sqlc.arg(account_type)::text = '' OR accounts.type = sqlc.arg(account_type))
  AND (sqlc.arg(status)::text = '' OR accounts.status = sqlc.arg(status))
  AND (
    sqlc.arg(schedulable)::text = ''
    OR sqlc.arg(schedulable)::text = 'all'
    OR (sqlc.arg(schedulable)::text = 'enabled' AND accounts.schedulable = true AND accounts.status = 'active')
    OR (sqlc.arg(schedulable)::text = 'disabled' AND (accounts.schedulable = false OR accounts.status IN ('pending_test', 'disabled', 'error')))
    OR (sqlc.arg(schedulable)::text = 'cooling' AND accounts.status IN ('rate_limited', 'temporary_unavailable'))
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (accounts.name COLLATE "C" >= sqlc.arg(keyword)::text AND accounts.name COLLATE "C" < sqlc.arg(keyword_upper)::text)
    OR (accounts.provider_code COLLATE "C" >= sqlc.arg(keyword)::text AND accounts.provider_code COLLATE "C" < sqlc.arg(keyword_upper)::text)
  )
ORDER BY accounts.updated_at DESC, accounts.created_at DESC, accounts.id DESC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: FindPublicAccountByID :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.credentials_encrypted,
  accounts.credential_fingerprint,
  accounts.credential_mask,
  accounts.client_compatibility,
  accounts.health_check_model,
  accounts.schedulable,
  accounts.availability_schedule_json,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.notes,
  accounts.created_at,
  accounts.updated_at,
  groups.id AS bound_group_id,
  groups.name AS bound_group_name
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.account_id = accounts.id
  AND group_accounts.system_account_id = accounts.system_account_id
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
WHERE accounts.id = $1
  AND accounts.deleted_at IS NULL
LIMIT 1;

-- name: FindPublicAccountByIDForUpdate :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.credentials_encrypted,
  accounts.credential_fingerprint,
  accounts.credential_mask,
  accounts.client_compatibility,
  accounts.health_check_model,
  accounts.schedulable,
  accounts.availability_schedule_json,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.notes,
  accounts.created_at,
  accounts.updated_at,
  groups.id AS bound_group_id,
  groups.name AS bound_group_name
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.account_id = accounts.id
  AND group_accounts.system_account_id = accounts.system_account_id
LEFT JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
WHERE accounts.id = $1
  AND accounts.deleted_at IS NULL
LIMIT 1
FOR UPDATE OF accounts;

-- name: FindExistingPublicAccountByNameInGroup :one
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.credentials_encrypted,
  accounts.credential_fingerprint,
  accounts.credential_mask,
  accounts.client_compatibility,
  accounts.health_check_model,
  accounts.schedulable,
  accounts.availability_schedule_json,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.notes,
  accounts.created_at,
  accounts.updated_at,
  groups.id AS bound_group_id,
  groups.name AS bound_group_name
FROM juhe_business.accounts AS accounts
JOIN juhe_business.group_accounts AS group_accounts
  ON group_accounts.account_id = accounts.id
  AND group_accounts.system_account_id = accounts.system_account_id
JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
WHERE accounts.system_account_id = sqlc.arg(system_account_id)
  AND accounts.provider_code = sqlc.arg(provider_code)
  AND accounts.provider_protocol_profile_id = sqlc.arg(provider_protocol_profile_id)
  AND group_accounts.group_id = sqlc.arg(group_id)
  AND lower(accounts.name) = lower(sqlc.arg(name))
  AND accounts.deleted_at IS NULL
LIMIT 1;

-- name: InsertPublicAccount :one
INSERT INTO juhe_business.accounts (
  id,
  system_account_id,
  provider_code,
  provider_protocol_profile_id,
  protocol_code,
  protocol_version,
  name,
  type,
  status,
  credentials_encrypted,
  credential_fingerprint,
  credential_mask,
  concurrency_limit,
  priority,
  client_compatibility,
  health_check_model,
  schedulable,
  availability_schedule_json,
  notes,
  last_error_message,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(id),
  sqlc.arg(system_account_id),
  sqlc.arg(provider_code),
  sqlc.arg(provider_protocol_profile_id),
  sqlc.arg(protocol_code),
  sqlc.arg(protocol_version),
  sqlc.arg(name),
  sqlc.arg(account_type),
  sqlc.arg(status),
  sqlc.arg(credentials_encrypted),
  sqlc.arg(credential_fingerprint),
  sqlc.arg(credential_mask),
  sqlc.arg(concurrency_limit),
  sqlc.arg(priority),
  sqlc.arg(client_compatibility),
  sqlc.arg(health_check_model),
  sqlc.arg(schedulable),
  sqlc.arg(availability_schedule_json),
  sqlc.arg(notes),
  sqlc.arg(last_error_message),
  sqlc.arg(created_at),
  sqlc.arg(updated_at)
)
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  provider_protocol_profile_id,
  protocol_code,
  protocol_version,
  type,
  status,
  credentials_encrypted,
  credential_fingerprint,
  credential_mask,
  client_compatibility,
  health_check_model,
  schedulable,
  availability_schedule_json,
  concurrency_limit,
  priority,
  notes,
  created_at,
  updated_at;

-- name: InsertPublicAccountGroupBinding :exec
INSERT INTO juhe_business.group_accounts (
  system_account_id,
  group_id,
  account_id,
  local_priority,
  local_super_priority_enabled,
  local_fallback_enabled,
  enabled,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(system_account_id),
  sqlc.arg(group_id),
  sqlc.arg(account_id),
  sqlc.arg(local_priority),
  false,
  false,
  true,
  sqlc.arg(created_at),
  sqlc.arg(updated_at)
);

-- name: UpdatePublicAccountAllFields :one
UPDATE juhe_business.accounts
SET name = sqlc.arg(name),
    status = sqlc.arg(status),
    credentials_encrypted = sqlc.arg(credentials_encrypted),
    credential_fingerprint = sqlc.arg(credential_fingerprint),
    credential_mask = sqlc.arg(credential_mask),
    concurrency_limit = sqlc.arg(concurrency_limit),
    priority = sqlc.arg(priority),
    schedulable = sqlc.arg(schedulable),
    availability_schedule_json = sqlc.arg(availability_schedule_json),
    notes = sqlc.arg(notes),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id)
  AND deleted_at IS NULL
RETURNING
  id,
  system_account_id,
  name,
  provider_code,
  provider_protocol_profile_id,
  protocol_code,
  protocol_version,
  type,
  status,
  credentials_encrypted,
  credential_fingerprint,
  credential_mask,
  client_compatibility,
  health_check_model,
  schedulable,
  availability_schedule_json,
  concurrency_limit,
  priority,
  notes,
  created_at,
  updated_at;

-- name: UpdatePublicAccountGroupBindingDispatch :exec
UPDATE juhe_business.group_accounts
SET local_priority = sqlc.arg(local_priority),
    updated_at = sqlc.arg(updated_at)
WHERE account_id = sqlc.arg(account_id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: SoftDeletePublicAccount :execrows
UPDATE juhe_business.accounts
SET status = 'disabled',
    schedulable = false,
    cooldown_until = NULL,
    deleted_at = sqlc.arg(deleted_at),
    deleted_by = sqlc.arg(deleted_by),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND system_account_id = sqlc.arg(system_account_id)
  AND deleted_at IS NULL;

-- name: DeletePublicAccountGroupBindings :exec
DELETE FROM juhe_business.group_accounts
WHERE account_id = sqlc.arg(account_id)
  AND system_account_id = sqlc.arg(system_account_id);

-- name: DeletePublicAccountSupportedModels :exec
DELETE FROM juhe_business.account_supported_models
WHERE account_id = $1;

-- name: InsertPublicAccountSupportedModel :exec
INSERT INTO juhe_business.account_supported_models (
  account_id, provider_code, model, created_at
) VALUES (
  sqlc.arg(account_id), sqlc.arg(provider_code), sqlc.arg(model), sqlc.arg(created_at)
);

-- name: ListPublicAccountSupportedModelsByAccountIDs :many
SELECT account_id, model
FROM juhe_business.account_supported_models
WHERE account_id = ANY(sqlc.arg(account_ids)::text[])
ORDER BY account_id ASC, model ASC;
