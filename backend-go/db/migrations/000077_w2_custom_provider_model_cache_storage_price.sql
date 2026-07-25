-- +goose Up
ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS cache_storage_usd_per_1m_per_hour double precision;

-- +goose Down
-- no-op: custom provider model pricing is current business state.
