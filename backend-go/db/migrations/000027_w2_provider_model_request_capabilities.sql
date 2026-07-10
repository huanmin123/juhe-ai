-- +goose Up
ALTER TABLE juhe_business.provider_model_catalog
  ADD COLUMN IF NOT EXISTS supported_service_tiers_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(supported_service_tiers_json::jsonb) = 'array'),
  ADD COLUMN IF NOT EXISTS supported_reasoning_efforts_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array'),
  ADD COLUMN IF NOT EXISTS default_reasoning_effort text
    CHECK (
      default_reasoning_effort IS NULL
      OR default_reasoning_effort IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
    ),
  ADD COLUMN IF NOT EXISTS codex_supported_reasoning_levels_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(codex_supported_reasoning_levels_json::jsonb) = 'array'),
  ADD COLUMN IF NOT EXISTS codex_default_reasoning_level text
    CHECK (
      codex_default_reasoning_level IS NULL
      OR codex_default_reasoning_level IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra')
    ),
  ADD COLUMN IF NOT EXISTS codex_multi_agent_version text
    CHECK (
      codex_multi_agent_version IS NULL
      OR codex_multi_agent_version = 'v2'
    );

ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS supported_service_tiers_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(supported_service_tiers_json::jsonb) = 'array'),
  ADD COLUMN IF NOT EXISTS supported_reasoning_efforts_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(supported_reasoning_efforts_json::jsonb) = 'array'),
  ADD COLUMN IF NOT EXISTS default_reasoning_effort text
    CHECK (
      default_reasoning_effort IS NULL
      OR default_reasoning_effort IN ('none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max')
    );

UPDATE juhe_business.provider_model_catalog
SET
  supported_service_tiers_json = '["priority"]',
  supported_reasoning_efforts_json = '["none","low","medium","high","xhigh","max"]',
  default_reasoning_effort = NULL,
  codex_supported_reasoning_levels_json = '["low","medium","high","xhigh","max","ultra"]',
  codex_default_reasoning_level = 'low',
  codex_multi_agent_version = 'v2',
  updated_at = now()
WHERE provider_code = 'gpt'
  AND model = 'gpt-5.6-sol';

UPDATE juhe_business.provider_model_catalog
SET
  supported_service_tiers_json = '["priority"]',
  supported_reasoning_efforts_json = '["none","low","medium","high","xhigh","max"]',
  default_reasoning_effort = NULL,
  codex_supported_reasoning_levels_json = '["low","medium","high","xhigh","max","ultra"]',
  codex_default_reasoning_level = 'medium',
  codex_multi_agent_version = 'v2',
  updated_at = now()
WHERE provider_code = 'gpt'
  AND model = 'gpt-5.6-terra';

UPDATE juhe_business.provider_model_catalog
SET
  supported_service_tiers_json = '["priority"]',
  supported_reasoning_efforts_json = '["none","low","medium","high","xhigh","max"]',
  default_reasoning_effort = NULL,
  codex_supported_reasoning_levels_json = '["low","medium","high","xhigh","max"]',
  codex_default_reasoning_level = 'medium',
  codex_multi_agent_version = NULL,
  updated_at = now()
WHERE provider_code = 'gpt'
  AND model = 'gpt-5.6-luna';

UPDATE juhe_business.provider_model_catalog
SET
  supported_service_tiers_json = '["priority"]',
  supported_reasoning_efforts_json = '[]',
  default_reasoning_effort = NULL,
  codex_supported_reasoning_levels_json = '[]',
  codex_default_reasoning_level = NULL,
  codex_multi_agent_version = NULL,
  updated_at = now()
WHERE provider_code = 'gpt'
  AND model IN ('gpt-5.5', 'gpt-5.4', 'gpt-5.4-mini');

UPDATE juhe_business.provider_model_catalog
SET
  supported_service_tiers_json = '[]',
  supported_reasoning_efforts_json = '[]',
  default_reasoning_effort = NULL,
  codex_supported_reasoning_levels_json = '[]',
  codex_default_reasoning_level = NULL,
  codex_multi_agent_version = NULL,
  updated_at = now()
WHERE provider_code <> 'gpt'
   OR model = 'gpt-image-2';

ALTER TABLE juhe_business.provider_model_catalog
  DROP COLUMN IF EXISTS supports_service_tier;

-- +goose Down
-- no-op: provider model request capabilities are current business metadata.
