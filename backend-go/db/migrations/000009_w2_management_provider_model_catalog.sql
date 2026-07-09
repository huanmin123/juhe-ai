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
  pricing_model text,
  context_window_tokens integer,
  input_usd_per_1m double precision,
  output_usd_per_1m double precision,
  cached_input_usd_per_1m double precision,
  cache_write_usd_per_1m double precision,
  cache_write_1h_usd_per_1m double precision,
  image_input_usd_per_1m double precision,
  image_output_usd_per_1m double precision,
  audio_input_usd_per_1m double precision,
  audio_output_usd_per_1m double precision,
  output_usd_per_image double precision,
  max_input_tokens integer,
  max_output_tokens integer,
  max_tokens integer,
  supports_prompt_caching boolean NOT NULL DEFAULT false,
  supports_service_tier boolean NOT NULL DEFAULT false,
  catalog_visible boolean NOT NULL DEFAULT true,
  source text NOT NULL DEFAULT 'go-w2-seed',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
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
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK (
    (scope = 'personal' AND system_account_id IS NOT NULL)
    OR (scope = 'global' AND system_account_id IS NULL)
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique
  ON juhe_business.custom_provider_models(provider_code, system_account_id, model)
  WHERE scope = 'personal';
CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique
  ON juhe_business.custom_provider_models(provider_code, model)
  WHERE scope = 'global';
CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
  ON juhe_business.custom_provider_models(provider_code, status, scope, system_account_id, model);

INSERT INTO juhe_business.provider_model_catalog (
  id, provider_code, model, mode, catalog_order, release_date, supported_api_protocols_json,
  input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
  image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m,
  max_input_tokens, max_output_tokens, max_tokens,
  supports_prompt_caching, supports_service_tier, source, created_at, updated_at
) VALUES
  ('provider_model_gpt_gpt_5_6_sol', 'gpt', 'gpt-5.6-sol', 'chat', 0, '2026-06-26', '["chat_completions","responses"]',
    5, 30, 0.5, 6.25, NULL, NULL, NULL, NULL, NULL, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_5_6_terra', 'gpt', 'gpt-5.6-terra', 'chat', 1, '2026-06-26', '["chat_completions","responses"]',
    2.5, 15, 0.25, 3.125, NULL, NULL, NULL, NULL, NULL, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_5_6_luna', 'gpt', 'gpt-5.6-luna', 'chat', 2, '2026-06-26', '["chat_completions","responses"]',
    1, 6, 0.1, 1.25, NULL, NULL, NULL, NULL, NULL, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_5_5', 'gpt', 'gpt-5.5', 'chat', 10, '2026-04-23', '["chat_completions","responses"]',
    5, 30, 0.5, NULL, NULL, NULL, NULL, 1050000, 128000, 128000, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_5_4', 'gpt', 'gpt-5.4', 'chat', 20, '2026-03-05', '["chat_completions","responses"]',
    2.5, 15, 0.25, NULL, NULL, NULL, NULL, 1050000, 128000, 128000, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_5_4_mini', 'gpt', 'gpt-5.4-mini', 'chat', 30, '2026-03-17', '["chat_completions","responses"]',
    0.75, 4.5, 0.075, NULL, NULL, NULL, NULL, 400000, 128000, 128000, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gpt_gpt_image_2', 'gpt', 'gpt-image-2', 'image_generation', 40, '2026-04-21', '["images"]',
    5, 10, 1.25, NULL, 8, 30, NULL, NULL, NULL, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_deepseek_v4_flash', 'deepseek', 'deepseek-v4-flash', 'chat', 10, '2026-06-20', '["chat_completions"]',
    0.14, 0.28, 0.0028, NULL, NULL, NULL, NULL, 1000000, 384000, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_deepseek_ai_v4_flash', 'deepseek', 'deepseek-ai-v4-flash', 'chat', 30, '2026-06-20', '["chat_completions"]',
    0.14, 0.28, 0.0028, NULL, NULL, NULL, NULL, 1000000, 384000, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_deepseek_v4_pro', 'deepseek', 'deepseek-v4-pro', 'chat', 20, '2026-06-20', '["chat_completions"]',
    0.435, 0.87, 0.003625, NULL, NULL, NULL, NULL, 1000000, 384000, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_deepseek_ai_v4_pro', 'deepseek', 'deepseek-ai-v4-pro', 'chat', 40, '2026-06-20', '["chat_completions"]',
    0.435, 0.87, 0.003625, NULL, NULL, NULL, NULL, 1000000, 384000, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_anthropic_claude_opus_4_8', 'anthropic', 'claude-opus-4-8', 'chat', 40, NULL, '["messages","message_token_counting"]',
    5, 25, 0.5, 6.25, NULL, NULL, NULL, 1000000, 128000, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_anthropic_claude_sonnet_4_6', 'anthropic', 'claude-sonnet-4-6', 'chat', 120, NULL, '["messages","message_token_counting"]',
    3, 15, 0.3, 3.75, NULL, NULL, NULL, 1000000, NULL, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_anthropic_claude_haiku_4_5', 'anthropic', 'claude-haiku-4-5', 'chat', 160, NULL, '["messages","message_token_counting"]',
    1, 5, 0.1, 1.25, NULL, NULL, NULL, NULL, NULL, NULL, true, true, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gemini_3_5_flash', 'gemini', 'gemini-3.5-flash', 'chat', 10, NULL, '["generate_content","stream_generate_content","count_tokens"]',
    1.5, 9, 0.15, NULL, NULL, NULL, NULL, NULL, NULL, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gemini_3_1_pro_preview', 'gemini', 'gemini-3.1-pro-preview', 'chat', 20, NULL, '["generate_content","stream_generate_content","count_tokens"]',
    2, 12, 0.2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gemini_2_5_pro', 'gemini', 'gemini-2.5-pro', 'chat', 60, NULL, '["generate_content","stream_generate_content","count_tokens"]',
    1.25, 10, 0.125, NULL, NULL, NULL, NULL, NULL, NULL, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_gemini_2_5_flash', 'gemini', 'gemini-2.5-flash', 'chat', 70, NULL, '["generate_content","stream_generate_content","count_tokens"]',
    0.3, 2.5, 0.03, NULL, NULL, NULL, 1, NULL, NULL, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_5_2', 'glm', 'glm-5.2', 'chat', 10, NULL, '["chat_completions"]',
    1.4, 4.4, 0.26, NULL, NULL, NULL, NULL, 1000000, 131072, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_5_1', 'glm', 'glm-5.1', 'chat', 20, NULL, '["chat_completions"]',
    1.4, 4.4, 0.26, NULL, NULL, NULL, NULL, 200000, 98304, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_5', 'glm', 'glm-5', 'chat', 30, NULL, '["chat_completions"]',
    1, 3.2, 0.2, NULL, NULL, NULL, NULL, 128000, 98304, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_5_turbo', 'glm', 'glm-5-turbo', 'chat', 40, NULL, '["chat_completions"]',
    1.2, 4, 0.24, NULL, NULL, NULL, NULL, 200000, 98304, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_4_7_flashx', 'glm', 'glm-4.7-flashx', 'chat', 60, NULL, '["chat_completions"]',
    0.07, 0.4, 0.01, NULL, NULL, NULL, NULL, 200000, 131072, NULL, true, false, 'node-pricing-snapshot', now(), now()),
  ('provider_model_glm_4_7_flash', 'glm', 'glm-4.7-flash', 'chat', 70, NULL, '["chat_completions"]',
    0, 0, NULL, NULL, NULL, NULL, NULL, 200000, 131072, NULL, false, false, 'node-pricing-snapshot', now(), now())
ON CONFLICT (provider_code, model) DO UPDATE SET
  mode = EXCLUDED.mode,
  catalog_order = EXCLUDED.catalog_order,
  release_date = EXCLUDED.release_date,
  supported_api_protocols_json = EXCLUDED.supported_api_protocols_json,
  input_usd_per_1m = EXCLUDED.input_usd_per_1m,
  output_usd_per_1m = EXCLUDED.output_usd_per_1m,
  cached_input_usd_per_1m = EXCLUDED.cached_input_usd_per_1m,
  cache_write_usd_per_1m = EXCLUDED.cache_write_usd_per_1m,
  image_input_usd_per_1m = EXCLUDED.image_input_usd_per_1m,
  image_output_usd_per_1m = EXCLUDED.image_output_usd_per_1m,
  audio_input_usd_per_1m = EXCLUDED.audio_input_usd_per_1m,
  max_input_tokens = EXCLUDED.max_input_tokens,
  max_output_tokens = EXCLUDED.max_output_tokens,
  max_tokens = EXCLUDED.max_tokens,
  supports_prompt_caching = EXCLUDED.supports_prompt_caching,
  supports_service_tier = EXCLUDED.supports_service_tier,
  catalog_visible = EXCLUDED.catalog_visible,
  source = EXCLUDED.source,
  updated_at = EXCLUDED.updated_at;

-- +goose Down
-- no-op: model catalog tables are business metadata.
