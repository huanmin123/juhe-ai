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
  ''::text AS id,
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
  pricing_model,
  context_window_tokens,
  max_input_tokens,
  max_output_tokens,
  max_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  supports_prompt_caching,
  supports_service_tier,
  catalog_visible,
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
  pricing_model,
  context_window_tokens,
  NULL::integer AS max_input_tokens,
  max_output_tokens,
  NULL::integer AS max_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  NULL::double precision AS cache_write_1h_usd_per_1m,
  image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  (cached_input_usd_per_1m IS NOT NULL) AS supports_prompt_caching,
  false AS supports_service_tier,
  true AS catalog_visible,
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
