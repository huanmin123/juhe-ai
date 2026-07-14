-- name: GetManagementAccountTestOptionsSource :one
WITH test_options_sources AS (
  SELECT
    accounts.id,
    accounts.system_account_id AS owner_system_account_id,
    accounts.provider_code,
    accounts.provider_protocol_profile_id,
    accounts.protocol_code,
    accounts.protocol_version,
    accounts.type,
    accounts.client_compatibility,
    accounts.health_check_model,
    accounts.health_check_endpoint_mode,
    accounts.credentials_encrypted
  FROM juhe_business.accounts AS accounts
  WHERE accounts.id = sqlc.arg(account_id)::text
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_source_account_id IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
    AND accounts.authorization_instance_owner_system_account_id IS NULL
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR accounts.system_account_id = sqlc.arg(system_account_id)::text
    )

  UNION ALL

  SELECT
    accounts.id,
    COALESCE(
      accounts.authorization_instance_owner_system_account_id,
      resource_authorizations.resource_owner_system_account_id
    ) AS owner_system_account_id,
    source_accounts.provider_code,
    source_accounts.provider_protocol_profile_id,
    source_accounts.protocol_code,
    source_accounts.protocol_version,
    source_accounts.type,
    source_accounts.client_compatibility,
    accounts.health_check_model,
    accounts.health_check_endpoint_mode,
    source_accounts.credentials_encrypted
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.accounts AS source_accounts
    ON source_accounts.id = accounts.authorization_instance_source_account_id
    AND source_accounts.deleted_at IS NULL
    AND source_accounts.authorization_instance_source_account_id IS NULL
    AND source_accounts.authorization_instance_authorization_id IS NULL
    AND source_accounts.authorization_instance_owner_system_account_id IS NULL
  INNER JOIN juhe_business.resource_authorizations AS resource_authorizations
    ON resource_authorizations.id = accounts.authorization_instance_authorization_id
    AND resource_authorizations.resource_type = 'account'
    AND resource_authorizations.resource_id = accounts.authorization_instance_source_account_id
    AND resource_authorizations.resource_owner_system_account_id = source_accounts.system_account_id
    AND (
      accounts.authorization_instance_owner_system_account_id IS NULL
      OR accounts.authorization_instance_owner_system_account_id = resource_authorizations.resource_owner_system_account_id
    )
    AND resource_authorizations.grantee_system_account_id = accounts.system_account_id
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
  WHERE accounts.id = sqlc.arg(account_id)::text
    AND accounts.deleted_at IS NULL
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR accounts.system_account_id = sqlc.arg(system_account_id)::text
    )
)
SELECT
  test_options_sources.id,
  test_options_sources.owner_system_account_id,
  test_options_sources.provider_code,
  test_options_sources.provider_protocol_profile_id,
  test_options_sources.protocol_code,
  test_options_sources.protocol_version,
  test_options_sources.type,
  test_options_sources.client_compatibility,
  test_options_sources.health_check_model,
  test_options_sources.health_check_endpoint_mode,
  test_options_sources.credentials_encrypted
FROM test_options_sources;
