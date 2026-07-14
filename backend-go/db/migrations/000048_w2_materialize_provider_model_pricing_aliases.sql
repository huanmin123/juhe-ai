-- +goose Up
UPDATE juhe_business.provider_model_catalog AS alias
SET input_usd_per_1m = COALESCE(alias.input_usd_per_1m, target.input_usd_per_1m),
    output_usd_per_1m = COALESCE(alias.output_usd_per_1m, target.output_usd_per_1m),
    cached_input_usd_per_1m = COALESCE(alias.cached_input_usd_per_1m, target.cached_input_usd_per_1m),
    cache_write_usd_per_1m = COALESCE(alias.cache_write_usd_per_1m, target.cache_write_usd_per_1m),
    cache_write_1h_usd_per_1m = COALESCE(alias.cache_write_1h_usd_per_1m, target.cache_write_1h_usd_per_1m),
    service_tier_prices_json = CASE
      WHEN alias.service_tier_prices_json::jsonb <> '{}'::jsonb THEN alias.service_tier_prices_json
      ELSE target.service_tier_prices_json
    END,
    image_input_usd_per_1m = COALESCE(alias.image_input_usd_per_1m, target.image_input_usd_per_1m),
    image_output_usd_per_1m = COALESCE(alias.image_output_usd_per_1m, target.image_output_usd_per_1m),
    audio_input_usd_per_1m = COALESCE(alias.audio_input_usd_per_1m, target.audio_input_usd_per_1m),
    audio_output_usd_per_1m = COALESCE(alias.audio_output_usd_per_1m, target.audio_output_usd_per_1m),
    output_usd_per_image = COALESCE(alias.output_usd_per_image, target.output_usd_per_image)
FROM juhe_business.provider_model_catalog AS target
WHERE alias.pricing_model IS NOT NULL
  AND target.provider_code = alias.provider_code
  AND target.model = alias.pricing_model;

WITH candidates AS (
  SELECT alias.id AS alias_id, 1 AS precedence,
         target.input_usd_per_1m, target.output_usd_per_1m, target.cached_input_usd_per_1m,
         target.cache_write_usd_per_1m, target.cache_write_1h_usd_per_1m, target.service_tier_prices_json,
         target.image_input_usd_per_1m, target.image_output_usd_per_1m,
         target.audio_input_usd_per_1m, target.audio_output_usd_per_1m, target.output_usd_per_image
  FROM juhe_business.custom_provider_models AS alias
  JOIN juhe_business.provider_model_catalog AS target
    ON target.provider_code = alias.provider_code AND target.model = alias.pricing_model
  WHERE alias.pricing_model IS NOT NULL
  UNION ALL
  SELECT alias.id AS alias_id,
         CASE WHEN target.scope = 'personal' THEN 3 ELSE 2 END AS precedence,
         target.input_usd_per_1m, target.output_usd_per_1m, target.cached_input_usd_per_1m,
         target.cache_write_usd_per_1m, target.cache_write_1h_usd_per_1m, target.service_tier_prices_json,
         target.image_input_usd_per_1m, target.image_output_usd_per_1m,
         target.audio_input_usd_per_1m, target.audio_output_usd_per_1m, target.output_usd_per_image
  FROM juhe_business.custom_provider_models AS alias
  JOIN juhe_business.custom_provider_models AS target
    ON target.provider_code = alias.provider_code
   AND target.model = alias.pricing_model
   AND (target.scope = 'global' OR (alias.scope = 'personal' AND target.scope = 'personal' AND target.system_account_id = alias.system_account_id))
  WHERE alias.pricing_model IS NOT NULL
), chosen AS (
  SELECT DISTINCT ON (alias_id) * FROM candidates ORDER BY alias_id, precedence DESC
)
UPDATE juhe_business.custom_provider_models AS alias
SET input_usd_per_1m = COALESCE(alias.input_usd_per_1m, chosen.input_usd_per_1m),
    output_usd_per_1m = COALESCE(alias.output_usd_per_1m, chosen.output_usd_per_1m),
    cached_input_usd_per_1m = COALESCE(alias.cached_input_usd_per_1m, chosen.cached_input_usd_per_1m),
    cache_write_usd_per_1m = COALESCE(alias.cache_write_usd_per_1m, chosen.cache_write_usd_per_1m),
    cache_write_1h_usd_per_1m = COALESCE(alias.cache_write_1h_usd_per_1m, chosen.cache_write_1h_usd_per_1m),
    service_tier_prices_json = CASE
      WHEN alias.service_tier_prices_json::jsonb <> '{}'::jsonb THEN alias.service_tier_prices_json
      ELSE chosen.service_tier_prices_json
    END,
    image_input_usd_per_1m = COALESCE(alias.image_input_usd_per_1m, chosen.image_input_usd_per_1m),
    image_output_usd_per_1m = COALESCE(alias.image_output_usd_per_1m, chosen.image_output_usd_per_1m),
    audio_input_usd_per_1m = COALESCE(alias.audio_input_usd_per_1m, chosen.audio_input_usd_per_1m),
    audio_output_usd_per_1m = COALESCE(alias.audio_output_usd_per_1m, chosen.audio_output_usd_per_1m),
    output_usd_per_image = COALESCE(alias.output_usd_per_image, chosen.output_usd_per_image)
FROM chosen
WHERE alias.id = chosen.alias_id;

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM juhe_business.provider_model_catalog
    WHERE pricing_model IS NOT NULL
      AND input_usd_per_1m IS NULL AND output_usd_per_1m IS NULL AND cached_input_usd_per_1m IS NULL
      AND cache_write_usd_per_1m IS NULL AND cache_write_1h_usd_per_1m IS NULL
      AND service_tier_prices_json::jsonb = '{}'::jsonb
      AND image_input_usd_per_1m IS NULL AND image_output_usd_per_1m IS NULL
      AND audio_input_usd_per_1m IS NULL AND audio_output_usd_per_1m IS NULL AND output_usd_per_image IS NULL
  ) THEN
    RAISE EXCEPTION 'provider_model_catalog contains unresolved pricing_model aliases';
  END IF;
  IF EXISTS (
    SELECT 1 FROM juhe_business.custom_provider_models
    WHERE pricing_model IS NOT NULL
      AND input_usd_per_1m IS NULL AND output_usd_per_1m IS NULL AND cached_input_usd_per_1m IS NULL
      AND cache_write_usd_per_1m IS NULL AND cache_write_1h_usd_per_1m IS NULL
      AND service_tier_prices_json::jsonb = '{}'::jsonb
      AND image_input_usd_per_1m IS NULL AND image_output_usd_per_1m IS NULL
      AND audio_input_usd_per_1m IS NULL AND audio_output_usd_per_1m IS NULL AND output_usd_per_image IS NULL
  ) THEN
    RAISE EXCEPTION 'custom_provider_models contains unresolved pricing_model aliases';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE juhe_business.provider_model_catalog
  DROP COLUMN IF EXISTS priority_input_usd_per_1m,
  DROP COLUMN IF EXISTS priority_output_usd_per_1m,
  DROP COLUMN IF EXISTS priority_cached_input_usd_per_1m,
  DROP COLUMN IF EXISTS priority_cache_write_usd_per_1m,
  DROP COLUMN IF EXISTS priority_cache_write_1h_usd_per_1m,
  DROP COLUMN IF EXISTS flex_input_usd_per_1m,
  DROP COLUMN IF EXISTS flex_output_usd_per_1m,
  DROP COLUMN IF EXISTS flex_cached_input_usd_per_1m,
  DROP COLUMN IF EXISTS flex_cache_write_usd_per_1m,
  DROP COLUMN IF EXISTS flex_cache_write_1h_usd_per_1m,
  DROP COLUMN IF EXISTS pricing_model;

ALTER TABLE juhe_business.custom_provider_models
  DROP COLUMN IF EXISTS pricing_model;

-- +goose Down
-- no-op: pricing aliases are materialized into current model rows before legacy columns are removed.
