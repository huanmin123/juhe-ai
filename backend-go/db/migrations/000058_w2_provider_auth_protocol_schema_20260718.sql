-- +goose Up
-- Databases already migrated through 000057 do not rerun the rewritten baseline
-- migrations. Keep this catch-up independently executable on both upgrade and
-- fresh histories before the generated model catalog is synchronized.
ALTER TABLE juhe_business.provider_model_catalog
  ADD COLUMN IF NOT EXISTS long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false;

ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_type_check,
  ADD CONSTRAINT accounts_type_check CHECK (type IN ('api_key', 'oauth', 'google_oauth')) NOT VALID;
ALTER TABLE juhe_business.accounts
  VALIDATE CONSTRAINT accounts_type_check;

ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_health_check_endpoint_mode_check,
  ADD CONSTRAINT accounts_health_check_endpoint_mode_check CHECK (
    health_check_endpoint_mode IN (
      'chat_json', 'chat_sse',
      'responses_json', 'responses_sse',
      'messages_json', 'messages_sse',
      'generate_content_json', 'generate_content_sse',
      'interactions_json', 'interactions_sse'
    )
  ) NOT VALID;
ALTER TABLE juhe_business.accounts
  VALIDATE CONSTRAINT accounts_health_check_endpoint_mode_check;

INSERT INTO juhe_business.providers (
  id, code, name, parent_code, description, enabled, default_supported_models_json, created_at, updated_at
) VALUES (
  'xai', 'xai', 'xAI', NULL,
  'xAI 官方供应商，支持官方 API Key、OpenAI Chat Completions 与 Responses 兼容协议',
  true, '["grok-4.3"]', now(), now()
)
ON CONFLICT (code) DO UPDATE SET
  id = EXCLUDED.id,
  name = EXCLUDED.name,
  parent_code = EXCLUDED.parent_code,
  description = EXCLUDED.description,
  enabled = EXCLUDED.enabled,
  default_supported_models_json = EXCLUDED.default_supported_models_json,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.provider_protocol_profiles (
  id, provider_code, name, description, enabled, protocol_code, protocol_version,
  base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
) VALUES
  ('profile_xai_openai_v1', 'xai', 'xAI / OpenAI v1',
    'xAI 官方 API Key 的 OpenAI v1 Chat Completions 与 Responses 协议档案',
    true, 'openai', 'v1', 'https://api.x.ai/v1', 'grok-4.3',
    '["api_key"]', '["responses","chat","passthrough"]', now(), now()),
  ('profile_gemini_native_v1beta', 'gemini', 'Gemini / Gemini v1beta',
    'Gemini 官方 API Key 与 Google OAuth 原生 v1beta 协议档案',
    true, 'gemini', 'v1beta', 'https://generativelanguage.googleapis.com', 'gemini-3.5-flash',
    '["api_key","google_oauth"]',
    '["generate_content","stream_generate_content","count_tokens","embed_content","interactions","models","passthrough"]',
    now(), now())
ON CONFLICT (id) DO UPDATE SET
  provider_code = EXCLUDED.provider_code,
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  enabled = EXCLUDED.enabled,
  protocol_code = EXCLUDED.protocol_code,
  protocol_version = EXCLUDED.protocol_version,
  base_url = EXCLUDED.base_url,
  default_health_check_model = EXCLUDED.default_health_check_model,
  account_types_json = EXCLUDED.account_types_json,
  capabilities_json = EXCLUDED.capabilities_json,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.protocol_endpoint_families (
  id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
) VALUES (
  'gemini_v1beta_interactions', 'gemini', 'v1beta', 'interactions',
  'Interactions', 'Gemini Interactions API 接口族（JSON 与 SSE）', true, now(), now()
)
ON CONFLICT (protocol_code, protocol_version, family_code) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.provider_protocol_profile_families (
  profile_id, family_code, enabled, capabilities_json, created_at, updated_at
) VALUES
  ('profile_xai_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_xai_openai_v1', 'responses', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'interactions', true, '[]', now(), now())
ON CONFLICT (profile_id, family_code) DO UPDATE SET
  enabled = EXCLUDED.enabled,
  capabilities_json = EXCLUDED.capabilities_json,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, description,
  enabled, is_default, created_at, updated_at
)
SELECT
  'grp_default_xai_sys_admin', 'sys_admin', '默认 xAI 分组', 'xai', '',
  true, true, now(), now()
WHERE EXISTS (
  SELECT 1
  FROM juhe_business.system_accounts
  WHERE id = 'sys_admin'
)
AND NOT EXISTS (
  SELECT 1
  FROM juhe_business.groups
  WHERE system_account_id = 'sys_admin'
    AND provider_code = 'xai'
    AND is_default = true
)
ON CONFLICT DO NOTHING;

-- +goose Down
-- no-op: provider authentication/protocol metadata and catalog columns are current business state.
