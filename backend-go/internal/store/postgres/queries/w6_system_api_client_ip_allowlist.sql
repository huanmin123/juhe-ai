-- name: FindSystemAPIClientIPAllowlistPolicy :one
SELECT policies.id, policies.expires_at
FROM juhe_stats.client_ip_policies AS policies
INNER JOIN juhe_stats.client_ip_registry AS registry
  ON registry.ip_hash = policies.ip_hash
WHERE policies.ip_hash = sqlc.arg(ip_hash)::text
  AND policies.policy_type = 'allowlist'
  AND policies.status = 'active'
  AND (
    policies.expires_at IS NULL
    OR policies.expires_at > sqlc.arg(now_at)::text
  )
ORDER BY policies.created_at DESC, policies.id DESC
LIMIT 1;

-- name: LockManagementClientIPRegistry :one
SELECT ip_hash, client_ip
FROM juhe_stats.client_ip_registry
WHERE ip_hash = sqlc.arg(ip_hash)::text
FOR UPDATE;

-- name: DisableActiveManagementClientIPPolicies :execrows
UPDATE juhe_stats.client_ip_policies
SET status = 'disabled',
  disabled_at = sqlc.arg(disabled_at)::text,
  disabled_by_system_account_id = sqlc.arg(disabled_by_system_account_id)::text,
  disabled_reason = sqlc.arg(disabled_reason)::text,
  updated_at = sqlc.arg(updated_at)::text
WHERE ip_hash = sqlc.arg(ip_hash)::text
  AND status = 'active';

-- name: InsertManagementClientIPAllowlistPolicy :one
INSERT INTO juhe_stats.client_ip_policies (
  id,
  ip_hash,
  policy_type,
  status,
  reason,
  expires_at,
  created_by_system_account_id,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(ip_hash)::text,
  'allowlist',
  'active',
  sqlc.narg(reason)::text,
  NULL,
  sqlc.arg(created_by_system_account_id)::text,
  sqlc.arg(created_at)::text,
  sqlc.arg(updated_at)::text
)
RETURNING
  id,
  ip_hash,
  policy_type,
  status,
  reason,
  expires_at,
  created_by_system_account_id,
  created_at,
  updated_at,
  disabled_at,
  disabled_by_system_account_id,
  disabled_reason;

-- name: InsertManagementClientIPBlacklistPolicy :one
INSERT INTO juhe_stats.client_ip_policies (
  id,
  ip_hash,
  policy_type,
  status,
  reason,
  expires_at,
  created_by_system_account_id,
  created_at,
  updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(ip_hash)::text,
  'blacklist',
  'active',
  sqlc.narg(reason)::text,
  sqlc.narg(expires_at)::text,
  sqlc.arg(created_by_system_account_id)::text,
  sqlc.arg(created_at)::text,
  sqlc.arg(updated_at)::text
)
RETURNING
  id,
  ip_hash,
  policy_type,
  status,
  reason,
  expires_at,
  created_by_system_account_id,
  created_at,
  updated_at,
  disabled_at,
  disabled_by_system_account_id,
  disabled_reason;

-- name: DisableActiveManagementClientIPAllowlistPolicies :execrows
UPDATE juhe_stats.client_ip_policies
SET status = 'disabled',
  disabled_at = sqlc.arg(disabled_at)::text,
  disabled_by_system_account_id = sqlc.arg(disabled_by_system_account_id)::text,
  disabled_reason = sqlc.arg(disabled_reason)::text,
  updated_at = sqlc.arg(updated_at)::text
WHERE ip_hash = sqlc.arg(ip_hash)::text
  AND policy_type = 'allowlist'
  AND status = 'active';

-- name: DisableActiveManagementClientIPBlacklistPolicies :execrows
UPDATE juhe_stats.client_ip_policies
SET status = 'disabled',
  disabled_at = sqlc.arg(disabled_at)::text,
  disabled_by_system_account_id = sqlc.arg(disabled_by_system_account_id)::text,
  disabled_reason = sqlc.arg(disabled_reason)::text,
  updated_at = sqlc.arg(updated_at)::text
WHERE ip_hash = sqlc.arg(ip_hash)::text
  AND policy_type = 'blacklist'
  AND status = 'active';
