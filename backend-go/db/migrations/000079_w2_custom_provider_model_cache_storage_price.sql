-- +goose Up
ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS cache_storage_usd_per_1m_per_hour double precision;

ALTER TABLE juhe_business.provider_model_catalog
  ADD COLUMN IF NOT EXISTS cached_image_input_usd_per_1m double precision;

-- Node snapshot 2026-07-24: cache_read_input_image_token_cost converted from USD/token to USD/1M tokens.
UPDATE juhe_business.provider_model_catalog AS catalog
SET cached_image_input_usd_per_1m = pricing.cached_image_input_usd_per_1m,
    updated_at = now()
FROM (VALUES
  ('gpt-image-2', 2::double precision),
  ('gpt-image-2-2026-04-21', 2::double precision),
  ('gpt-image-1.5', 2::double precision),
  ('gpt-image-1-mini', 0.25::double precision),
  ('gpt-image-1', 2.5::double precision)
) AS pricing(model, cached_image_input_usd_per_1m)
WHERE catalog.provider_code = 'gpt'
  AND catalog.model = pricing.model;

-- +goose Down
-- no-op: provider model pricing is current business state.
