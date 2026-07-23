-- name: LockManagementAPIKeyDeleteTarget :one
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.name,
  api_keys.is_default,
  api_keys.purpose
FROM juhe_business.api_keys AS api_keys
WHERE api_keys.id = sqlc.arg(api_key_id)::text
  AND (
    sqlc.arg(owner_system_account_id)::text = ''
    OR api_keys.system_account_id = sqlc.arg(owner_system_account_id)::text
  )
FOR UPDATE OF api_keys;

-- name: HardDeleteManagementAPIKey :one
DELETE FROM juhe_business.api_keys
WHERE id = sqlc.arg(api_key_id)::text
  AND system_account_id = sqlc.arg(owner_system_account_id)::text
RETURNING id;
