-- +goose Up
ALTER TABLE juhe_business.provider_model_catalog
  ADD COLUMN service_tier_prices_json text NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object');

ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN cache_write_1h_usd_per_1m double precision,
  ADD COLUMN service_tier_prices_json text NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object');

UPDATE juhe_business.provider_model_catalog
SET service_tier_prices_json = (
  CASE WHEN priority_input_usd_per_1m IS NOT NULL
      OR priority_output_usd_per_1m IS NOT NULL
      OR priority_cached_input_usd_per_1m IS NOT NULL
      OR priority_cache_write_usd_per_1m IS NOT NULL
      OR priority_cache_write_1h_usd_per_1m IS NOT NULL
    THEN jsonb_build_object('priority', jsonb_strip_nulls(jsonb_build_object(
      'inputUsdPer1M', priority_input_usd_per_1m,
      'outputUsdPer1M', priority_output_usd_per_1m,
      'cachedInputUsdPer1M', priority_cached_input_usd_per_1m,
      'cacheWriteUsdPer1M', priority_cache_write_usd_per_1m,
      'cacheWrite1hUsdPer1M', priority_cache_write_1h_usd_per_1m
    ))) ELSE '{}'::jsonb END
  ||
  CASE WHEN flex_input_usd_per_1m IS NOT NULL
      OR flex_output_usd_per_1m IS NOT NULL
      OR flex_cached_input_usd_per_1m IS NOT NULL
      OR flex_cache_write_usd_per_1m IS NOT NULL
      OR flex_cache_write_1h_usd_per_1m IS NOT NULL
    THEN jsonb_build_object('flex', jsonb_strip_nulls(jsonb_build_object(
      'inputUsdPer1M', flex_input_usd_per_1m,
      'outputUsdPer1M', flex_output_usd_per_1m,
      'cachedInputUsdPer1M', flex_cached_input_usd_per_1m,
      'cacheWriteUsdPer1M', flex_cache_write_usd_per_1m,
      'cacheWrite1hUsdPer1M', flex_cache_write_1h_usd_per_1m
    ))) ELSE '{}'::jsonb END
)::text;

ALTER TABLE juhe_business.provider_model_catalog
  DROP COLUMN priority_input_usd_per_1m,
  DROP COLUMN priority_output_usd_per_1m,
  DROP COLUMN priority_cached_input_usd_per_1m,
  DROP COLUMN priority_cache_write_usd_per_1m,
  DROP COLUMN priority_cache_write_1h_usd_per_1m,
  DROP COLUMN flex_input_usd_per_1m,
  DROP COLUMN flex_output_usd_per_1m,
  DROP COLUMN flex_cached_input_usd_per_1m,
  DROP COLUMN flex_cache_write_usd_per_1m,
  DROP COLUMN flex_cache_write_1h_usd_per_1m,
  DROP COLUMN pricing_model;

ALTER TABLE juhe_business.custom_provider_models
  DROP COLUMN pricing_model;

-- +goose Down
-- no-op: current schema intentionally has no legacy pricing aliases or expanded GPT tier columns.
