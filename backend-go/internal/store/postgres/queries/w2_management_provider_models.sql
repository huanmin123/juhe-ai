-- name: FindManagementProviderModelProvider :one
SELECT code, enabled, parent_code
FROM juhe_business.providers
WHERE code = sqlc.arg(code)
LIMIT 1;

-- name: ListManagementEnabledModelProviderCodes :many
SELECT code
FROM juhe_business.providers
WHERE enabled = true
  AND code <> 'hybrid'
ORDER BY name ASC, code ASC
LIMIT 50;

-- name: ListManagementProviderCodesByProtocol :many
SELECT DISTINCT p.code
FROM juhe_business.provider_protocol_profiles AS ppp
INNER JOIN juhe_business.providers AS p
  ON p.code = ppp.provider_code
WHERE p.enabled = true
  AND ppp.enabled = true
  AND ppp.protocol_code = sqlc.arg(protocol_code)
  AND ppp.protocol_version = sqlc.arg(protocol_version)
ORDER BY p.code ASC
LIMIT 50;

-- name: ListManagementProviderModelCatalog :many
SELECT
  id,
  provider_code,
  model,
  'built_in'::text AS scope,
  NULL::text AS system_account_id,
  status,
  mode,
  catalog_order,
  release_date,
  shutdown_date,
  supported_api_protocols_json,
  supported_service_tiers_json,
  supported_reasoning_efforts_json,
  default_reasoning_effort,
  codex_supported_reasoning_levels_json,
  codex_default_reasoning_level,
  codex_multi_agent_version,
  context_window_tokens,
  max_input_tokens,
  max_output_tokens,
  max_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  long_context_input_token_threshold,
  long_context_input_cost_multiplier,
  long_context_output_cost_multiplier,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  supports_prompt_caching,
  (jsonb_array_length(supported_service_tiers_json::jsonb) > 0) AS supports_service_tier,
  catalog_visible,
  NULL::text AS pricing_notes,
  NULL::text AS capability_notes,
  NULL::text AS notes,
  ''::text AS created_by,
  NULL::text AS updated_by,
  source,
  created_at,
  updated_at
FROM juhe_business.provider_model_catalog
WHERE provider_code = ANY(sqlc.arg(built_in_provider_codes)::text[])
  AND catalog_visible = true
  AND (sqlc.arg(include_inactive)::boolean OR status = 'active')
  AND (
    shutdown_date IS NULL
    OR btrim(shutdown_date) = ''
    OR shutdown_date > CURRENT_DATE::text
  )
UNION ALL
SELECT
  id,
  provider_code,
  model,
  scope,
  system_account_id,
  status,
  mode,
  NULL::integer AS catalog_order,
  release_date,
  shutdown_date,
  supported_api_protocols_json,
  supported_service_tiers_json,
  supported_reasoning_efforts_json,
  default_reasoning_effort,
  '[]'::text AS codex_supported_reasoning_levels_json,
  NULL::text AS codex_default_reasoning_level,
  NULL::text AS codex_multi_agent_version,
  context_window_tokens,
  NULL::integer AS max_input_tokens,
  max_output_tokens,
  NULL::integer AS max_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  NULL::integer AS long_context_input_token_threshold,
  NULL::double precision AS long_context_input_cost_multiplier,
  NULL::double precision AS long_context_output_cost_multiplier,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  (cached_input_usd_per_1m IS NOT NULL) AS supports_prompt_caching,
  (jsonb_array_length(supported_service_tiers_json::jsonb) > 0) AS supports_service_tier,
  true AS catalog_visible,
  pricing_notes,
  capability_notes,
  notes,
  created_by,
  updated_by,
  CASE WHEN scope = 'global' THEN 'custom-global' ELSE 'custom-personal' END AS source,
  created_at,
  updated_at
FROM juhe_business.custom_provider_models
WHERE provider_code = ANY(sqlc.arg(custom_provider_codes)::text[])
  AND (sqlc.arg(include_inactive)::boolean OR status = 'active')
  AND (
    (scope = 'global' AND system_account_id IS NULL)
    OR (
      sqlc.arg(system_account_id)::text <> ''
      AND scope = 'personal'
      AND system_account_id = sqlc.arg(system_account_id)
    )
  )
ORDER BY provider_code ASC, scope ASC, model ASC, id ASC;

-- name: UpdateManagementBuiltInProviderModelPrices :one
UPDATE juhe_business.provider_model_catalog
SET input_usd_per_1m = CASE WHEN sqlc.arg(input_usd_per_1m_present)::boolean THEN sqlc.narg(input_usd_per_1m)::double precision ELSE input_usd_per_1m END,
    output_usd_per_1m = CASE WHEN sqlc.arg(output_usd_per_1m_present)::boolean THEN sqlc.narg(output_usd_per_1m)::double precision ELSE output_usd_per_1m END,
    cached_input_usd_per_1m = CASE WHEN sqlc.arg(cached_input_usd_per_1m_present)::boolean THEN sqlc.narg(cached_input_usd_per_1m)::double precision ELSE cached_input_usd_per_1m END,
    cache_write_usd_per_1m = CASE WHEN sqlc.arg(cache_write_usd_per_1m_present)::boolean THEN sqlc.narg(cache_write_usd_per_1m)::double precision ELSE cache_write_usd_per_1m END,
    cache_write_1h_usd_per_1m = CASE WHEN sqlc.arg(cache_write_1h_usd_per_1m_present)::boolean THEN sqlc.narg(cache_write_1h_usd_per_1m)::double precision ELSE cache_write_1h_usd_per_1m END,
    service_tier_prices_json = CASE WHEN sqlc.arg(service_tier_prices_present)::boolean THEN sqlc.arg(service_tier_prices_json)::text ELSE service_tier_prices_json END,
    image_input_usd_per_1m = CASE WHEN sqlc.arg(image_input_usd_per_1m_present)::boolean THEN sqlc.narg(image_input_usd_per_1m)::double precision ELSE image_input_usd_per_1m END,
    image_output_usd_per_1m = CASE WHEN sqlc.arg(image_output_usd_per_1m_present)::boolean THEN sqlc.narg(image_output_usd_per_1m)::double precision ELSE image_output_usd_per_1m END,
    audio_input_usd_per_1m = CASE WHEN sqlc.arg(audio_input_usd_per_1m_present)::boolean THEN sqlc.narg(audio_input_usd_per_1m)::double precision ELSE audio_input_usd_per_1m END,
    audio_output_usd_per_1m = CASE WHEN sqlc.arg(audio_output_usd_per_1m_present)::boolean THEN sqlc.narg(audio_output_usd_per_1m)::double precision ELSE audio_output_usd_per_1m END,
    output_usd_per_image = CASE WHEN sqlc.arg(output_usd_per_image_present)::boolean THEN sqlc.narg(output_usd_per_image)::double precision ELSE output_usd_per_image END,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND provider_code = sqlc.arg(provider_code)
RETURNING
  id,
  provider_code,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  updated_at;

-- name: FindManagementCustomProviderModel :one
SELECT
  id,
  provider_code,
  model,
  scope,
  system_account_id,
  status,
  mode,
  supported_api_protocols_json,
  supported_service_tiers_json,
  supported_reasoning_efforts_json,
  default_reasoning_effort,
  release_date,
  shutdown_date,
  context_window_tokens,
  max_output_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  pricing_notes,
  capability_notes,
  notes,
  created_by,
  updated_by,
  created_at,
  updated_at
FROM juhe_business.custom_provider_models
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: FindManagementCustomProviderModelByScope :one
SELECT
  id,
  provider_code,
  model,
  scope,
  system_account_id,
  status,
  mode,
  supported_api_protocols_json,
  supported_service_tiers_json,
  supported_reasoning_efforts_json,
  default_reasoning_effort,
  release_date,
  shutdown_date,
  context_window_tokens,
  max_output_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  pricing_notes,
  capability_notes,
  notes,
  created_by,
  updated_by,
  created_at,
  updated_at
FROM juhe_business.custom_provider_models
WHERE provider_code = sqlc.arg(provider_code)
  AND model = sqlc.arg(model)
  AND (
    (sqlc.arg(scope)::text = 'global' AND scope = 'global' AND system_account_id IS NULL)
    OR (
      sqlc.arg(scope)::text = 'personal'
      AND scope = 'personal'
      AND system_account_id = sqlc.arg(system_account_id)
    )
  )
LIMIT 1;

-- name: UpsertManagementCustomProviderModel :one
INSERT INTO juhe_business.custom_provider_models (
  id, provider_code, model, scope, system_account_id, status,
  mode, supported_api_protocols_json, supported_service_tiers_json,
  supported_reasoning_efforts_json, default_reasoning_effort,
  release_date, shutdown_date, context_window_tokens, max_output_tokens,
  input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, service_tier_prices_json,
  image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
  output_usd_per_image, currency, pricing_notes, capability_notes, notes,
  created_by, updated_by, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(provider_code), sqlc.arg(model), sqlc.arg(scope), sqlc.narg(system_account_id), sqlc.arg(status),
  sqlc.narg(mode), sqlc.arg(supported_api_protocols_json), sqlc.arg(supported_service_tiers_json),
  sqlc.arg(supported_reasoning_efforts_json), sqlc.narg(default_reasoning_effort),
  sqlc.narg(release_date), sqlc.narg(shutdown_date), sqlc.narg(context_window_tokens), sqlc.narg(max_output_tokens),
  sqlc.narg(input_usd_per_1m), sqlc.narg(output_usd_per_1m), sqlc.narg(cached_input_usd_per_1m), sqlc.narg(cache_write_usd_per_1m), sqlc.narg(cache_write_1h_usd_per_1m), sqlc.arg(service_tier_prices_json),
  sqlc.narg(image_input_usd_per_1m), sqlc.narg(image_output_usd_per_1m), sqlc.narg(audio_input_usd_per_1m), sqlc.narg(audio_output_usd_per_1m),
  sqlc.narg(output_usd_per_image), 'USD', sqlc.narg(pricing_notes), sqlc.narg(capability_notes), sqlc.narg(notes),
  sqlc.arg(actor_system_account_id), sqlc.arg(actor_system_account_id), now(), now()
)
ON CONFLICT (id) DO UPDATE SET
  provider_code = EXCLUDED.provider_code,
  model = EXCLUDED.model,
  scope = EXCLUDED.scope,
  system_account_id = EXCLUDED.system_account_id,
  status = EXCLUDED.status,
  mode = EXCLUDED.mode,
  supported_api_protocols_json = EXCLUDED.supported_api_protocols_json,
  supported_service_tiers_json = EXCLUDED.supported_service_tiers_json,
  supported_reasoning_efforts_json = EXCLUDED.supported_reasoning_efforts_json,
  default_reasoning_effort = EXCLUDED.default_reasoning_effort,
  release_date = EXCLUDED.release_date,
  shutdown_date = EXCLUDED.shutdown_date,
  context_window_tokens = EXCLUDED.context_window_tokens,
  max_output_tokens = EXCLUDED.max_output_tokens,
  input_usd_per_1m = EXCLUDED.input_usd_per_1m,
  output_usd_per_1m = EXCLUDED.output_usd_per_1m,
  cached_input_usd_per_1m = EXCLUDED.cached_input_usd_per_1m,
  cache_write_usd_per_1m = EXCLUDED.cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m = EXCLUDED.cache_write_1h_usd_per_1m,
  service_tier_prices_json = EXCLUDED.service_tier_prices_json,
  image_input_usd_per_1m = EXCLUDED.image_input_usd_per_1m,
  image_output_usd_per_1m = EXCLUDED.image_output_usd_per_1m,
  audio_input_usd_per_1m = EXCLUDED.audio_input_usd_per_1m,
  audio_output_usd_per_1m = EXCLUDED.audio_output_usd_per_1m,
  output_usd_per_image = EXCLUDED.output_usd_per_image,
  pricing_notes = EXCLUDED.pricing_notes,
  capability_notes = EXCLUDED.capability_notes,
  notes = EXCLUDED.notes,
  updated_by = EXCLUDED.updated_by,
  updated_at = EXCLUDED.updated_at
RETURNING
  id,
  provider_code,
  model,
  scope,
  system_account_id,
  status,
  mode,
  supported_api_protocols_json,
  supported_service_tiers_json,
  supported_reasoning_efforts_json,
  default_reasoning_effort,
  release_date,
  shutdown_date,
  context_window_tokens,
  max_output_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  service_tier_prices_json,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  pricing_notes,
  capability_notes,
  notes,
  created_by,
  updated_by,
  created_at,
  updated_at;

-- name: DeleteManagementCustomProviderModel :execrows
DELETE FROM juhe_business.custom_provider_models
WHERE id = sqlc.arg(id);

-- name: GetManagementCustomProviderModelBindingSummary :one
WITH account_owner_scope AS (
  SELECT sqlc.arg(scope)::text AS scope, sqlc.arg(system_account_id)::text AS system_account_id
),
supported_model_accounts AS (
  SELECT asm.account_id
  FROM juhe_business.account_supported_models AS asm
  INNER JOIN juhe_business.accounts AS accounts
    ON accounts.id = asm.account_id
    AND accounts.deleted_at IS NULL
  CROSS JOIN account_owner_scope
  WHERE asm.provider_code = sqlc.arg(provider_code)
    AND asm.model = sqlc.arg(model)
    AND (
      account_owner_scope.scope = 'global'
      OR accounts.system_account_id = account_owner_scope.system_account_id
    )
),
mapping_source_accounts AS (
  SELECT amm.account_id
  FROM juhe_business.account_model_mappings AS amm
  INNER JOIN juhe_business.accounts AS accounts
    ON accounts.id = amm.account_id
    AND accounts.deleted_at IS NULL
  CROSS JOIN account_owner_scope
  WHERE amm.source_model = sqlc.arg(model)
    AND amm.provider_code = sqlc.arg(provider_code)
    AND (
      account_owner_scope.scope = 'global'
      OR accounts.system_account_id = account_owner_scope.system_account_id
    )
),
mapping_upstream_accounts AS (
  SELECT amm.account_id
  FROM juhe_business.account_model_mappings AS amm
  INNER JOIN juhe_business.accounts AS accounts
    ON accounts.id = amm.account_id
    AND accounts.deleted_at IS NULL
  CROSS JOIN account_owner_scope
  WHERE amm.upstream_model = sqlc.arg(model)
    AND amm.provider_code = sqlc.arg(provider_code)
    AND (
      account_owner_scope.scope = 'global'
      OR accounts.system_account_id = account_owner_scope.system_account_id
    )
),
all_bound_accounts AS (
  SELECT account_id FROM supported_model_accounts
  UNION
  SELECT account_id FROM mapping_source_accounts
  UNION
  SELECT account_id FROM mapping_upstream_accounts
)
SELECT
  (SELECT COUNT(DISTINCT account_id) FROM supported_model_accounts)::integer AS supported_model_account_count,
  (SELECT COUNT(DISTINCT account_id) FROM mapping_source_accounts)::integer AS mapping_source_account_count,
  (SELECT COUNT(DISTINCT account_id) FROM mapping_upstream_accounts)::integer AS mapping_upstream_account_count,
  (SELECT COUNT(DISTINCT account_id) FROM all_bound_accounts)::integer AS total_account_count;

-- name: ClearManagementProviderDefaultHealthCheckModelIfModel :execrows
DELETE FROM juhe_business.provider_default_health_check_models
WHERE provider_code = sqlc.arg(provider_code)
  AND model = sqlc.arg(model)
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR system_account_id = sqlc.arg(system_account_id)
  );

-- name: ClearManagementProviderSystemDefaultHealthCheckModelIfModel :execrows
DELETE FROM juhe_business.provider_system_default_health_check_models
WHERE provider_code = sqlc.arg(provider_code)
  AND model = sqlc.arg(model);
