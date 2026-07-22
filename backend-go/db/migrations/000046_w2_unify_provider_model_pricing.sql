-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.provider_model_catalog (
  id text PRIMARY KEY,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  model text NOT NULL CHECK (btrim(model) <> ''),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  mode text,
  catalog_order integer,
  release_date text,
  shutdown_date text,
  supported_api_protocols_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_api_protocols_json::jsonb) = 'array'),
  supported_service_tiers_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_service_tiers_json::jsonb) = 'array'),
  supported_reasoning_efforts_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array'),
  default_reasoning_effort text,
  codex_supported_reasoning_levels_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(codex_supported_reasoning_levels_json::jsonb) = 'array'),
  codex_default_reasoning_level text,
  codex_multi_agent_version text,
  pricing_model text,
  context_window_tokens integer,
  max_input_tokens integer,
  max_output_tokens integer,
  max_tokens integer,
  input_usd_per_1m double precision,
  output_usd_per_1m double precision,
  cached_input_usd_per_1m double precision,
  cache_write_usd_per_1m double precision,
  cache_write_1h_usd_per_1m double precision,
  priority_input_usd_per_1m double precision,
  priority_output_usd_per_1m double precision,
  priority_cached_input_usd_per_1m double precision,
  priority_cache_write_usd_per_1m double precision,
  priority_cache_write_1h_usd_per_1m double precision,
  flex_input_usd_per_1m double precision,
  flex_output_usd_per_1m double precision,
  flex_cached_input_usd_per_1m double precision,
  flex_cache_write_usd_per_1m double precision,
  flex_cache_write_1h_usd_per_1m double precision,
  long_context_input_token_threshold integer,
  long_context_input_cost_multiplier double precision,
  long_context_output_cost_multiplier double precision,
  image_input_usd_per_1m double precision,
  image_output_usd_per_1m double precision,
  audio_input_usd_per_1m double precision,
  audio_output_usd_per_1m double precision,
  output_usd_per_image double precision,
  supports_prompt_caching boolean NOT NULL DEFAULT false,
  supports_service_tier boolean NOT NULL DEFAULT false,
  catalog_visible boolean NOT NULL DEFAULT true,
  source text NOT NULL DEFAULT 'go-w2-seed',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider_code, model)
);

CREATE INDEX IF NOT EXISTS idx_provider_model_catalog_lookup
  ON juhe_business.provider_model_catalog(provider_code, status, catalog_visible, catalog_order, model);
CREATE INDEX IF NOT EXISTS idx_provider_model_catalog_release
  ON juhe_business.provider_model_catalog(provider_code, release_date DESC, model);

CREATE TABLE IF NOT EXISTS juhe_business.custom_provider_models (
  id text PRIMARY KEY,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  model text NOT NULL CHECK (btrim(model) <> ''),
  scope text NOT NULL DEFAULT 'personal' CHECK (scope IN ('personal', 'global')),
  system_account_id text REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('draft', 'active', 'disabled')),
  mode text,
  supported_api_protocols_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_api_protocols_json::jsonb) = 'array'),
  supported_service_tiers_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_service_tiers_json::jsonb) = 'array'),
  supported_reasoning_efforts_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array'),
  default_reasoning_effort text,
  pricing_model text,
  release_date text,
  shutdown_date text,
  context_window_tokens integer,
  max_output_tokens integer,
  input_usd_per_1m double precision,
  output_usd_per_1m double precision,
  cached_input_usd_per_1m double precision,
  cache_write_usd_per_1m double precision,
  image_input_usd_per_1m double precision,
  image_output_usd_per_1m double precision,
  audio_input_usd_per_1m double precision,
  audio_output_usd_per_1m double precision,
  output_usd_per_image double precision,
  currency text NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
  pricing_notes text,
  capability_notes text,
  notes text,
  created_by text NOT NULL REFERENCES juhe_business.system_accounts(id),
  updated_by text REFERENCES juhe_business.system_accounts(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((scope = 'personal' AND system_account_id IS NOT NULL) OR (scope = 'global' AND system_account_id IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique
  ON juhe_business.custom_provider_models(provider_code, system_account_id, model) WHERE scope = 'personal';
CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique
  ON juhe_business.custom_provider_models(provider_code, model) WHERE scope = 'global';
CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
  ON juhe_business.custom_provider_models(provider_code, status, scope, system_account_id, model);

ALTER TABLE juhe_business.provider_model_catalog
  ADD COLUMN IF NOT EXISTS pricing_model text,
  ADD COLUMN IF NOT EXISTS priority_input_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS priority_output_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS priority_cached_input_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS priority_cache_write_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS priority_cache_write_1h_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS flex_input_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS flex_output_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS flex_cached_input_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS flex_cache_write_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS flex_cache_write_1h_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS service_tier_prices_json text NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(service_tier_prices_json::jsonb) = 'object');

ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS pricing_model text,
  ADD COLUMN IF NOT EXISTS cache_write_1h_usd_per_1m double precision,
  ADD COLUMN IF NOT EXISTS service_tier_prices_json text NOT NULL DEFAULT '{}'
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

-- +goose Down
-- no-op: current schema intentionally has no legacy pricing aliases or expanded GPT tier columns.
