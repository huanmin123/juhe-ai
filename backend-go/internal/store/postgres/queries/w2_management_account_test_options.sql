-- name: GetManagementAccountTestOptionsSource :one
WITH test_options_sources AS (
  SELECT
    accounts.id,
    accounts.system_account_id AS owner_system_account_id,
    accounts.id AS model_mapping_account_id,
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
    source_accounts.id AS model_mapping_account_id,
    source_accounts.provider_code,
    source_accounts.provider_protocol_profile_id,
    source_accounts.protocol_code,
    source_accounts.protocol_version,
    source_accounts.type,
    source_accounts.client_compatibility,
    source_accounts.health_check_model,
    source_accounts.health_check_endpoint_mode,
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
  test_options_sources.model_mapping_account_id,
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

-- name: GetManagementAccountTestOptionListSource :one
WITH test_option_list_sources AS (
  SELECT
    accounts.id,
    accounts.system_account_id AS owner_system_account_id,
    accounts.provider_code,
    accounts.provider_protocol_profile_id,
    accounts.protocol_code,
    accounts.protocol_version,
    accounts.type,
    accounts.client_compatibility,
    accounts.health_check_model
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
    source_accounts.health_check_model
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
  test_option_list_sources.id,
  test_option_list_sources.owner_system_account_id,
  test_option_list_sources.provider_code,
  test_option_list_sources.provider_protocol_profile_id,
  test_option_list_sources.protocol_code,
  test_option_list_sources.protocol_version,
  test_option_list_sources.type,
  test_option_list_sources.client_compatibility,
  test_option_list_sources.health_check_model
FROM test_option_list_sources;

-- name: GetManagementAccountTestModelCapabilitiesSource :one
WITH test_model_capability_sources AS (
  SELECT
    accounts.id,
    accounts.system_account_id AS owner_system_account_id,
    accounts.id AS model_mapping_account_id,
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
    source_accounts.id AS model_mapping_account_id,
    source_accounts.provider_code,
    source_accounts.provider_protocol_profile_id,
    source_accounts.protocol_code,
    source_accounts.protocol_version,
    source_accounts.type,
    source_accounts.client_compatibility,
    source_accounts.health_check_model,
    source_accounts.health_check_endpoint_mode,
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
  test_model_capability_sources.id,
  test_model_capability_sources.owner_system_account_id,
  test_model_capability_sources.model_mapping_account_id,
  test_model_capability_sources.provider_code,
  test_model_capability_sources.provider_protocol_profile_id,
  test_model_capability_sources.protocol_code,
  test_model_capability_sources.protocol_version,
  test_model_capability_sources.type,
  test_model_capability_sources.client_compatibility,
  test_model_capability_sources.health_check_model,
  test_model_capability_sources.health_check_endpoint_mode,
  test_model_capability_sources.credentials_encrypted
FROM test_model_capability_sources;

-- name: ListManagementAccountTestModelCatalog :many
WITH source_provider_codes AS (
  SELECT sqlc.arg(provider_code)::text AS code
  WHERE sqlc.arg(provider_code)::text NOT IN ('openai', 'hybrid')

  UNION

  SELECT providers.code
  FROM juhe_business.providers AS providers
  WHERE sqlc.arg(provider_code)::text = 'openai'
    AND providers.enabled = true
    AND EXISTS (
      SELECT 1
      FROM juhe_business.provider_protocol_profiles AS profiles
      WHERE profiles.provider_code = providers.code
        AND profiles.enabled = true
        AND profiles.protocol_code = 'openai'
        AND profiles.protocol_version = 'v1'
    )

  UNION

  SELECT providers.code
  FROM juhe_business.providers AS providers
  WHERE sqlc.arg(provider_code)::text = 'hybrid'
    AND providers.enabled = true
    AND providers.code <> 'hybrid'
    AND EXISTS (
      SELECT 1
      FROM juhe_business.provider_protocol_profiles AS profiles
      WHERE profiles.provider_code = providers.code
        AND profiles.enabled = true
        AND (
          (profiles.protocol_code = 'openai' AND profiles.protocol_version = 'v1')
          OR (profiles.protocol_code = 'anthropic' AND profiles.protocol_version = 'v1')
          OR (profiles.protocol_code = 'gemini' AND profiles.protocol_version = 'v1beta')
        )
    )
), candidate_models AS (
  SELECT
    catalog.id,
    catalog.provider_code,
    catalog.model,
    'built_in'::text AS scope,
    COALESCE(catalog.mode, '')::text AS mode,
    catalog.supported_api_protocols_json
  FROM juhe_business.provider_model_catalog AS catalog
  INNER JOIN source_provider_codes AS sources ON sources.code = catalog.provider_code
  WHERE (sqlc.arg(provider_code)::text <> 'openai' OR catalog.provider_code <> 'openai')
    AND catalog.status = 'active'
    AND catalog.catalog_visible = true
    AND (
      catalog.shutdown_date IS NULL
      OR btrim(catalog.shutdown_date) = ''
      OR catalog.shutdown_date > CURRENT_DATE::text
    )
    AND (
      cardinality(sqlc.arg(model_ids)::text[]) = 0
      OR catalog.model = ANY(sqlc.arg(model_ids)::text[])
    )
    AND (
      sqlc.arg(keyword)::text = ''
      OR strpos(lower(catalog.model), lower(sqlc.arg(keyword)::text)) > 0
      OR catalog.model = ANY(sqlc.arg(selected_ids)::text[])
    )

  UNION ALL

  SELECT
    custom_models.id,
    custom_models.provider_code,
    custom_models.model,
    custom_models.scope,
    COALESCE(custom_models.mode, '')::text AS mode,
    custom_models.supported_api_protocols_json
  FROM juhe_business.custom_provider_models AS custom_models
  INNER JOIN source_provider_codes AS sources ON sources.code = custom_models.provider_code
  WHERE custom_models.status = 'active'
    AND custom_models.catalog_visible = true
    AND (
      custom_models.shutdown_date IS NULL
      OR btrim(custom_models.shutdown_date) = ''
      OR custom_models.shutdown_date > CURRENT_DATE::text
    )
    AND (
      (custom_models.scope = 'global' AND custom_models.system_account_id IS NULL)
      OR (
        sqlc.arg(owner_system_account_id)::text <> ''
        AND custom_models.scope = 'personal'
        AND custom_models.system_account_id = sqlc.arg(owner_system_account_id)::text
      )
    )
    AND (
      cardinality(sqlc.arg(model_ids)::text[]) = 0
      OR custom_models.model = ANY(sqlc.arg(model_ids)::text[])
    )
    AND (
      sqlc.arg(keyword)::text = ''
      OR strpos(lower(custom_models.model), lower(sqlc.arg(keyword)::text)) > 0
      OR custom_models.model = ANY(sqlc.arg(selected_ids)::text[])
    )
), ranked_models AS (
  SELECT
    candidate_models.*,
    row_number() OVER (
      PARTITION BY
        CASE
          WHEN sqlc.arg(provider_code)::text = 'hybrid'
          THEN candidate_models.provider_code
          ELSE ''
        END,
        candidate_models.model
      ORDER BY
        CASE candidate_models.scope
          WHEN 'personal' THEN 3
          WHEN 'global' THEN 2
          ELSE 1
        END DESC,
        candidate_models.provider_code ASC,
        candidate_models.id ASC
    ) AS scope_rank
  FROM candidate_models
), windowed_models AS (
  SELECT
    ranked_models.*,
    row_number() OVER (
      PARTITION BY
        CASE
          WHEN sqlc.arg(provider_code)::text = 'hybrid'
          THEN ranked_models.provider_code
          ELSE ''
        END
      ORDER BY
        (ranked_models.model = ANY(sqlc.arg(selected_ids)::text[])) DESC,
        ranked_models.model COLLATE "C" ASC,
        ranked_models.provider_code ASC,
        ranked_models.id ASC
    ) AS provider_window_rank
  FROM ranked_models
  WHERE ranked_models.scope_rank = 1
)
SELECT
  windowed_models.id,
  windowed_models.provider_code,
  windowed_models.model,
  windowed_models.scope,
  windowed_models.mode,
  windowed_models.supported_api_protocols_json
FROM windowed_models
WHERE windowed_models.provider_window_rank <=
  CASE
    WHEN cardinality(sqlc.arg(model_ids)::text[]) > 0
      THEN LEAST(500, GREATEST(1, cardinality(sqlc.arg(model_ids)::text[]) * 50))
    ELSE sqlc.arg(result_limit)::integer + cardinality(sqlc.arg(selected_ids)::text[])
  END
ORDER BY
  (windowed_models.model = ANY(sqlc.arg(selected_ids)::text[])) DESC,
  windowed_models.model COLLATE "C" ASC,
  windowed_models.provider_code ASC,
  windowed_models.id ASC;

-- name: ListManagementAccountTestOptionModelMappings :many
SELECT
  account_model_mappings.source_model,
  account_model_mappings.source_endpoint_family,
  account_model_mappings.upstream_model,
  account_model_mappings.upstream_endpoint_family,
  account_model_mappings.enabled
FROM juhe_business.account_model_mappings AS account_model_mappings
WHERE account_model_mappings.account_id = sqlc.arg(account_id)::text
ORDER BY
  account_model_mappings.source_model ASC,
  account_model_mappings.source_endpoint_family ASC;

-- name: ListManagementAccountTestOptionModelMappingsBySourceModel :many
SELECT
  account_model_mappings.source_model,
  account_model_mappings.source_endpoint_family,
  account_model_mappings.upstream_model,
  account_model_mappings.upstream_endpoint_family,
  account_model_mappings.enabled
FROM juhe_business.account_model_mappings AS account_model_mappings
WHERE account_model_mappings.account_id = sqlc.arg(account_id)::text
  AND account_model_mappings.source_model = sqlc.arg(source_model)::text
ORDER BY account_model_mappings.source_endpoint_family ASC;
