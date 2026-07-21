-- name: UpdateManagementAccount :one
-- First-pass account PATCH query. The Go adapter executes the equivalent CTE in
-- managementaccountupdate.go so this slice can remain independent of router wiring.
WITH current_target AS MATERIALIZED (
  SELECT accounts.*
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.providers AS providers
    ON providers.code = accounts.provider_code AND providers.enabled = true
  INNER JOIN juhe_business.provider_protocol_profiles AS profiles
    ON profiles.id = accounts.provider_protocol_profile_id
    AND profiles.provider_code = accounts.provider_code
    AND profiles.enabled = true
  WHERE accounts.id = sqlc.arg(account_id)::text
    AND accounts.system_account_id = sqlc.arg(system_account_id)::text
    AND accounts.config_revision = sqlc.arg(expected_config_revision)::int
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
  FOR UPDATE OF accounts
)
UPDATE juhe_business.accounts AS accounts
SET credentials_encrypted = CASE
      WHEN sqlc.arg(has_credentials)::boolean THEN sqlc.arg(credentials_encrypted)::text
      ELSE accounts.credentials_encrypted
    END,
    config_revision = config_revision + 1,
    updated_at = sqlc.arg(updated_at)::timestamptz
FROM current_target
WHERE accounts.id = current_target.id
RETURNING accounts.id, accounts.system_account_id, accounts.config_revision;
