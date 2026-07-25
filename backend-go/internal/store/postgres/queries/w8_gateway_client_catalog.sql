-- name: ListGatewayClientCatalogProviders :many
SELECT code, enabled
FROM juhe_business.providers
ORDER BY name ASC, code ASC
LIMIT 50;

-- name: ListGatewayClientCatalogModels :many
WITH requested_providers AS (
  SELECT DISTINCT lower(btrim(requested.code)) AS requested_provider_code
  FROM unnest(sqlc.arg(logical_provider_codes)::text[]) AS requested(code)
  WHERE btrim(requested.code) <> ''
  ORDER BY requested_provider_code ASC
  LIMIT 50
),
protocol_provider_code_candidates AS (
  SELECT DISTINCT
    providers.code AS source_provider_code,
    profiles.protocol_code,
    profiles.protocol_version
  FROM juhe_business.provider_protocol_profiles AS profiles
  INNER JOIN juhe_business.providers AS providers
    ON providers.code = profiles.provider_code
   AND providers.enabled = true
  WHERE profiles.enabled = true
    AND (
      (profiles.protocol_code = 'openai' AND profiles.protocol_version = 'v1')
      OR (profiles.protocol_code = 'anthropic' AND profiles.protocol_version = 'v1')
      OR (profiles.protocol_code = 'gemini' AND profiles.protocol_version = 'v1beta')
    )
),
protocol_provider_codes AS (
  SELECT
    source_provider_code,
    protocol_code,
    protocol_version,
    ROW_NUMBER() OVER (
      PARTITION BY protocol_code, protocol_version
      ORDER BY source_provider_code ASC
    ) AS protocol_rank
  FROM protocol_provider_code_candidates
),
protocol_sources AS (
  SELECT
    requested.requested_provider_code,
    protocols.source_provider_code
  FROM requested_providers AS requested
  INNER JOIN protocol_provider_codes AS protocols
    ON protocols.protocol_rank <= 50
   AND (
      (
        requested.requested_provider_code = 'openai'
        AND protocols.protocol_code = 'openai'
        AND protocols.protocol_version = 'v1'
      ) OR (
        requested.requested_provider_code = 'hybrid'
        AND (
          (protocols.protocol_code = 'openai' AND protocols.protocol_version = 'v1')
          OR (protocols.protocol_code = 'anthropic' AND protocols.protocol_version = 'v1')
          OR (protocols.protocol_code = 'gemini' AND protocols.protocol_version = 'v1beta')
        )
      )
    )
),
source_providers AS (
  SELECT
    requested_provider_code,
    requested_provider_code AS source_provider_code,
    true AS include_built_in
  FROM requested_providers
  WHERE requested_provider_code NOT IN ('openai', 'hybrid')

  UNION

  SELECT requested_provider_code, source_provider_code, true AS include_built_in
  FROM protocol_sources
  WHERE requested_provider_code = 'openai'
    AND source_provider_code <> 'openai'

  UNION

  SELECT requested_provider_code, 'openai'::text AS source_provider_code, false AS include_built_in
  FROM requested_providers
  WHERE requested_provider_code = 'openai'

  UNION

  SELECT requested_provider_code, source_provider_code, true AS include_built_in
  FROM protocol_sources
  WHERE requested_provider_code = 'hybrid'
    AND source_provider_code <> 'hybrid'
),
catalog AS (
  SELECT
    sources.requested_provider_code,
    built_in.provider_code,
    built_in.model,
    'built_in'::text AS scope,
    NULL::text AS system_account_id,
    built_in.status,
    built_in.catalog_visible,
    built_in.release_date,
    built_in.created_at,
    built_in.supported_api_protocols_json,
    built_in.supported_service_tiers_json,
    built_in.codex_supported_reasoning_levels_json,
    built_in.codex_default_reasoning_level,
    built_in.codex_multi_agent_version,
    built_in.context_window_tokens,
    built_in.max_input_tokens,
    built_in.max_output_tokens,
    built_in.input_usd_per_1m,
    built_in.output_usd_per_1m,
    built_in.cached_input_usd_per_1m,
    built_in.cache_write_usd_per_1m,
    built_in.cache_write_1h_usd_per_1m,
    built_in.cache_storage_usd_per_1m_per_hour,
    built_in.image_input_usd_per_1m,
    built_in.cached_image_input_usd_per_1m,
    built_in.image_output_usd_per_1m,
    built_in.audio_input_usd_per_1m,
    built_in.audio_output_usd_per_1m,
    built_in.output_usd_per_image,
    built_in.service_tier_prices_json,
    NULL::text AS pricing_notes,
    NULL::text AS capability_notes,
    NULL::text AS notes
  FROM source_providers AS sources
  INNER JOIN juhe_business.provider_model_catalog AS built_in
    ON built_in.provider_code = sources.source_provider_code
   AND sources.include_built_in = true
  WHERE built_in.status = 'active'
    AND built_in.catalog_visible = true
    AND (
      built_in.shutdown_date IS NULL
      OR btrim(built_in.shutdown_date) = ''
      OR built_in.shutdown_date > CURRENT_DATE::text
    )
    AND (
      built_in.input_usd_per_1m IS NOT NULL
      OR built_in.output_usd_per_1m IS NOT NULL
      OR built_in.cached_input_usd_per_1m IS NOT NULL
      OR built_in.cache_write_usd_per_1m IS NOT NULL
      OR built_in.cache_write_1h_usd_per_1m IS NOT NULL
      OR built_in.cache_storage_usd_per_1m_per_hour IS NOT NULL
      OR built_in.image_input_usd_per_1m IS NOT NULL
      OR built_in.cached_image_input_usd_per_1m IS NOT NULL
      OR built_in.image_output_usd_per_1m IS NOT NULL
      OR built_in.audio_input_usd_per_1m IS NOT NULL
      OR built_in.audio_output_usd_per_1m IS NOT NULL
      OR built_in.output_usd_per_image IS NOT NULL
      OR COALESCE(NULLIF(btrim(built_in.service_tier_prices_json), ''), '{}')::jsonb <> '{}'::jsonb
    )

  UNION ALL

  SELECT
    sources.requested_provider_code,
    custom.provider_code,
    custom.model,
    custom.scope,
    custom.system_account_id,
    custom.status,
    custom.catalog_visible,
    custom.release_date,
    custom.created_at,
    custom.supported_api_protocols_json,
    custom.supported_service_tiers_json,
    '[]'::text AS codex_supported_reasoning_levels_json,
    NULL::text AS codex_default_reasoning_level,
    NULL::text AS codex_multi_agent_version,
    custom.context_window_tokens,
    custom.max_input_tokens,
    custom.max_output_tokens,
    custom.input_usd_per_1m,
    custom.output_usd_per_1m,
    custom.cached_input_usd_per_1m,
    custom.cache_write_usd_per_1m,
    custom.cache_write_1h_usd_per_1m,
    custom.cache_storage_usd_per_1m_per_hour,
    custom.image_input_usd_per_1m,
    NULL::double precision AS cached_image_input_usd_per_1m,
    custom.image_output_usd_per_1m,
    custom.audio_input_usd_per_1m,
    custom.audio_output_usd_per_1m,
    custom.output_usd_per_image,
    custom.service_tier_prices_json,
    custom.pricing_notes,
    custom.capability_notes,
    custom.notes
  FROM source_providers AS sources
  INNER JOIN juhe_business.custom_provider_models AS custom
    ON custom.provider_code = sources.source_provider_code
  WHERE custom.status = 'active'
    AND custom.catalog_visible = true
    AND (
      (custom.scope = 'global' AND custom.system_account_id IS NULL)
      OR (
        sqlc.arg(system_account_id)::text <> ''
        AND custom.scope = 'personal'
        AND custom.system_account_id = sqlc.arg(system_account_id)
      )
    )
    AND (
      custom.input_usd_per_1m IS NOT NULL
      OR custom.output_usd_per_1m IS NOT NULL
      OR custom.cached_input_usd_per_1m IS NOT NULL
      OR custom.cache_write_usd_per_1m IS NOT NULL
      OR custom.cache_write_1h_usd_per_1m IS NOT NULL
      OR custom.cache_storage_usd_per_1m_per_hour IS NOT NULL
      OR custom.image_input_usd_per_1m IS NOT NULL
      OR custom.image_output_usd_per_1m IS NOT NULL
      OR custom.audio_input_usd_per_1m IS NOT NULL
      OR custom.audio_output_usd_per_1m IS NOT NULL
      OR custom.output_usd_per_image IS NOT NULL
      OR COALESCE(NULLIF(btrim(custom.service_tier_prices_json), ''), '{}')::jsonb <> '{}'::jsonb
    )
)
SELECT
  requested_provider_code,
  provider_code,
  model,
  scope,
  system_account_id,
  status,
  catalog_visible,
  release_date,
  created_at,
  supported_api_protocols_json,
  supported_service_tiers_json,
  codex_supported_reasoning_levels_json,
  codex_default_reasoning_level,
  codex_multi_agent_version,
  context_window_tokens,
  max_input_tokens,
  max_output_tokens,
  input_usd_per_1m,
  output_usd_per_1m,
  cached_input_usd_per_1m,
  cache_write_usd_per_1m,
  cache_write_1h_usd_per_1m,
  cache_storage_usd_per_1m_per_hour,
  image_input_usd_per_1m,
  cached_image_input_usd_per_1m,
  image_output_usd_per_1m,
  audio_input_usd_per_1m,
  audio_output_usd_per_1m,
  output_usd_per_image,
  service_tier_prices_json,
  pricing_notes,
  capability_notes,
  notes
FROM catalog
ORDER BY
  requested_provider_code ASC,
  CASE scope WHEN 'personal' THEN 0 WHEN 'global' THEN 1 ELSE 2 END ASC,
  release_date DESC NULLS LAST,
  provider_code ASC,
  model ASC
LIMIT 20001;
