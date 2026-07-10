-- name: CreateManagementGroup :one
WITH target_account AS MATERIALIZED (
  SELECT id
  FROM juhe_business.system_accounts
  WHERE id = sqlc.arg(system_account_id)::text
  FOR KEY SHARE
),
target_provider AS MATERIALIZED (
  SELECT code, enabled
  FROM juhe_business.providers
  WHERE code = sqlc.arg(provider_code)::text
  FOR SHARE
),
inserted AS (
  INSERT INTO juhe_business.groups (
    id,
    system_account_id,
    name,
    provider_code,
    description,
    enabled,
    is_default,
    group_type,
    scheduling_policy_json,
    created_at,
    updated_at
  )
  SELECT
    sqlc.arg(id)::text,
    target_account.id,
    sqlc.arg(name)::text,
    target_provider.code,
    sqlc.narg(description)::text,
    sqlc.arg(enabled)::boolean,
    false,
    sqlc.arg(group_type)::text,
    sqlc.narg(scheduling_policy_json)::text,
    sqlc.arg(created_at)::timestamptz,
    sqlc.arg(updated_at)::timestamptz
  FROM target_account
  CROSS JOIN target_provider
  WHERE target_provider.enabled
  RETURNING
    id,
    system_account_id,
    name,
    provider_code,
    description,
    enabled,
    is_default,
    group_type,
    scheduling_policy_json
)
SELECT
  EXISTS (SELECT 1 FROM target_account)::boolean AS system_account_exists,
  EXISTS (SELECT 1 FROM target_provider)::boolean AS provider_exists,
  COALESCE((SELECT enabled FROM target_provider), false)::boolean AS provider_enabled,
  inserted.id,
  inserted.system_account_id,
  inserted.name,
  inserted.provider_code,
  inserted.description,
  inserted.enabled,
  inserted.is_default,
  inserted.group_type,
  inserted.scheduling_policy_json
FROM (VALUES (1)) AS sentinel(value)
LEFT JOIN inserted ON true;
