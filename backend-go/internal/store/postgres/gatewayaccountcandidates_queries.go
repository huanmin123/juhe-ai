package postgres

const resolveGatewayGroupAccessSQL = `
SELECT
  groups.id,
  groups.system_account_id,
  groups.provider_code,
  CASE
    WHEN groups.system_account_id = $2::text THEN groups.group_type
    ELSE COALESCE(group_authorization_settings.group_type, groups.group_type)
  END AS effective_group_type,
  CASE
    WHEN groups.system_account_id = $2::text THEN groups.scheduling_policy_json
    ELSE COALESCE(group_authorization_settings.scheduling_policy_json, groups.scheduling_policy_json)
  END AS effective_scheduling_policy_json,
  CASE WHEN groups.system_account_id = $2::text THEN 'owner' ELSE 'authorized' END AS access_type,
  resource_authorizations.id,
  resource_authorizations.expires_at,
  resource_authorizations.limits_json,
  resource_authorizations.effective_source_type,
  resource_authorizations.effective_source_team_id
FROM juhe_business.groups AS groups
LEFT JOIN LATERAL (
  SELECT resource_authorizations.*
  FROM juhe_business.resource_authorizations AS resource_authorizations
  WHERE groups.system_account_id <> $2::text
    AND resource_authorizations.resource_type = 'group'
    AND resource_authorizations.resource_id = groups.id
    AND resource_authorizations.resource_owner_system_account_id = groups.system_account_id
    AND resource_authorizations.grantee_system_account_id = $2::text
    AND resource_authorizations.status = 'active'
    AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $3::timestamptz)
  ORDER BY resource_authorizations.updated_at DESC, resource_authorizations.id ASC
  LIMIT 1
) AS resource_authorizations ON true
LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
  ON group_authorization_settings.authorization_id = resource_authorizations.id
  AND group_authorization_settings.system_account_id = $2::text
  AND group_authorization_settings.group_id = groups.id
WHERE groups.id = $1::text
  AND groups.enabled = true
  AND (
    groups.system_account_id = $2::text
    OR (
      resource_authorizations.id IS NOT NULL
      AND COALESCE(group_authorization_settings.enabled, true) = true
    )
  )
LIMIT 1`

const listGatewayAccountCandidatesSQL = `
SELECT
  group_accounts.account_id,
  accounts.system_account_id,
  group_accounts.group_id,
  group_accounts.account_authorization_id,
  group_accounts.local_priority,
  group_accounts.local_super_priority_enabled,
  group_accounts.local_fallback_enabled,
  group_accounts.created_at,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.name,
  accounts.type,
  accounts.status,
  accounts.schedulable,
  accounts.concurrency_limit,
  accounts.priority,
  accounts.super_priority_enabled,
  accounts.fallback_enabled,
  accounts.client_compatibility,
  accounts.credentials_encrypted,
  accounts.proxy_profile_id,
  accounts.availability_schedule_json,
  accounts.cooldown_until,
  accounts.account_expires_at,
  accounts.config_revision,
  accounts.authorization_instance_source_account_id,
  accounts.authorization_instance_authorization_id,
  accounts.authorization_instance_owner_system_account_id,
  account_authorizations.expires_at,
  account_authorizations.limits_json,
  account_authorizations.effective_source_type,
  account_authorizations.effective_source_team_id,
  source_accounts.id,
  source_accounts.provider_code,
  source_accounts.provider_protocol_profile_id,
  source_accounts.protocol_code,
  source_accounts.protocol_version,
  source_accounts.type,
  source_accounts.status,
  source_accounts.schedulable,
  source_accounts.credentials_encrypted,
  source_accounts.proxy_profile_id,
  source_accounts.cooldown_until,
  source_accounts.account_expires_at,
  source_accounts.concurrency_limit,
  source_accounts.client_compatibility
FROM juhe_business.group_accounts AS group_accounts
INNER JOIN juhe_business.groups AS groups
  ON groups.id = group_accounts.group_id
  AND groups.system_account_id = group_accounts.system_account_id
INNER JOIN juhe_business.accounts AS accounts
  ON accounts.id = group_accounts.account_id
  AND accounts.system_account_id = group_accounts.system_account_id
LEFT JOIN juhe_business.resource_authorizations AS account_authorizations
  ON account_authorizations.id = accounts.authorization_instance_authorization_id
  AND account_authorizations.resource_type = 'account'
  AND account_authorizations.resource_id = accounts.authorization_instance_source_account_id
  AND account_authorizations.grantee_system_account_id = $3::text
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = accounts.authorization_instance_source_account_id
WHERE group_accounts.group_id = $1::text
  AND group_accounts.system_account_id = $2::text
  AND group_accounts.enabled = true
  AND groups.enabled = true
  AND groups.provider_code = $4::text
  AND (
    ($7::text = 'owner' AND groups.system_account_id = $3::text)
    OR (
      $7::text = 'authorized'
      AND EXISTS (
        SELECT 1
        FROM juhe_business.resource_authorizations AS group_authorizations
        LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorizations.id
          AND group_authorization_settings.system_account_id = $3::text
          AND group_authorization_settings.group_id = groups.id
        WHERE group_authorizations.id = $8::text
          AND group_authorizations.resource_type = 'group'
          AND group_authorizations.resource_id = groups.id
          AND group_authorizations.resource_owner_system_account_id = groups.system_account_id
          AND group_authorizations.grantee_system_account_id = $3::text
          AND group_authorizations.status = 'active'
          AND (group_authorizations.expires_at IS NULL OR group_authorizations.expires_at > $6::timestamptz)
          AND COALESCE(group_authorization_settings.enabled, true) = true
      )
    )
  )
  AND accounts.provider_code = $4::text
  AND accounts.deleted_at IS NULL
  AND accounts.status IN ('active', 'rate_limited', 'temporary_unavailable')
  AND ($5::boolean OR accounts.status = 'active')
  AND accounts.schedulable = true
  AND ($5::boolean OR accounts.cooldown_until IS NULL OR accounts.cooldown_until <= $6::timestamptz)
  AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > $6::timestamptz)
  AND (
    (
      accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL
      AND group_accounts.account_authorization_id IS NULL
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
    )
    OR (
      $7::text = 'owner'
      AND accounts.system_account_id = $3::text
      AND group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id
      AND account_authorizations.id IS NOT NULL
      AND account_authorizations.status = 'active'
      AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > $6::timestamptz)
      AND accounts.authorization_instance_owner_system_account_id = account_authorizations.resource_owner_system_account_id
      AND account_authorizations.resource_owner_system_account_id = source_accounts.system_account_id
      AND source_accounts.deleted_at IS NULL
      AND source_accounts.provider_code = $4::text
      AND source_accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND source_accounts.status IN ('active', 'rate_limited', 'temporary_unavailable')
      AND ($5::boolean OR source_accounts.status = 'active')
      AND source_accounts.schedulable = true
      AND ($5::boolean OR source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= $6::timestamptz)
      AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > $6::timestamptz)
      AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
    )
  )
  AND (
    $9::text = ''
    OR EXISTS (
      SELECT 1 FROM juhe_business.account_supported_models AS supported_models
      WHERE supported_models.account_id = COALESCE(source_accounts.id, accounts.id)
        AND supported_models.model = $9::text
    )
    OR NOT EXISTS (
      SELECT 1 FROM juhe_business.account_supported_models AS supported_models
      WHERE supported_models.account_id = COALESCE(source_accounts.id, accounts.id)
    )
    OR EXISTS (
      SELECT 1 FROM juhe_business.account_model_mappings AS model_mappings
      WHERE model_mappings.account_id = COALESCE(source_accounts.id, accounts.id)
        AND model_mappings.enabled = true
        AND model_mappings.source_model = $9::text
        AND model_mappings.upstream_model <> model_mappings.source_model
        AND ($10::text = '' OR model_mappings.source_endpoint_family = $10::text)
        AND EXISTS (
          SELECT 1 FROM juhe_business.account_supported_models AS mapped_models
          WHERE mapped_models.account_id = model_mappings.account_id
            AND mapped_models.model = model_mappings.upstream_model
        )
    )
  )
ORDER BY CASE
    WHEN $9::text = '' THEN 0
    WHEN EXISTS (
      SELECT 1
      FROM juhe_business.account_supported_models AS supported_models
      WHERE supported_models.account_id = COALESCE(source_accounts.id, accounts.id)
        AND supported_models.model = $9::text
    ) THEN 0
    WHEN EXISTS (
      SELECT 1
      FROM juhe_business.account_model_mappings AS model_mappings
      WHERE model_mappings.account_id = COALESCE(source_accounts.id, accounts.id)
        AND model_mappings.enabled = true
        AND model_mappings.source_model = $9::text
        AND model_mappings.upstream_model <> model_mappings.source_model
        AND ($10::text = '' OR model_mappings.source_endpoint_family = $10::text)
        AND EXISTS (
          SELECT 1 FROM juhe_business.account_supported_models AS mapped_models
          WHERE mapped_models.account_id = model_mappings.account_id
            AND mapped_models.model = model_mappings.upstream_model
        )
    ) THEN 1
    ELSE 2
  END ASC,
  group_accounts.local_fallback_enabled ASC,
  group_accounts.local_super_priority_enabled DESC,
  group_accounts.local_priority ASC,
  group_accounts.created_at ASC,
  group_accounts.account_id ASC
LIMIT $11`
