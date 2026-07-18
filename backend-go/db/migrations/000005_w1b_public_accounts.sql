-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.protocols (
  id text PRIMARY KEY,
  code text NOT NULL,
  version text NOT NULL,
  name text NOT NULL,
  description text,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (code, version)
);

CREATE TABLE IF NOT EXISTS juhe_business.provider_protocol_profiles (
  id text PRIMARY KEY,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  name text NOT NULL,
  description text,
  enabled boolean NOT NULL DEFAULT true,
  protocol_code text NOT NULL,
  protocol_version text NOT NULL,
  base_url text NOT NULL,
  default_health_check_model text NOT NULL DEFAULT '',
  account_types_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(account_types_json::jsonb) = 'array'),
  capabilities_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(capabilities_json::jsonb) = 'array'),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (protocol_code, protocol_version)
    REFERENCES juhe_business.protocols(code, version)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_protocol_profiles_account_fk
  ON juhe_business.provider_protocol_profiles (id, provider_code, protocol_code, protocol_version);
CREATE INDEX IF NOT EXISTS idx_provider_protocol_profiles_provider_enabled
  ON juhe_business.provider_protocol_profiles (provider_code, enabled, id);

CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  provider_protocol_profile_id text NOT NULL,
  protocol_code text NOT NULL,
  protocol_version text NOT NULL,
  name text NOT NULL,
  type text NOT NULL DEFAULT 'api_key' CHECK (type = 'api_key'),
  status text NOT NULL DEFAULT 'pending_test'
    CHECK (status IN ('active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable')),
  credentials_encrypted text NOT NULL CHECK (btrim(credentials_encrypted) <> ''),
  credential_fingerprint text,
  credential_mask text NOT NULL DEFAULT '',
  concurrency_limit integer NOT NULL DEFAULT 20 CHECK (concurrency_limit BETWEEN 1 AND 100000),
  priority integer NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 100000),
  super_priority_enabled boolean NOT NULL DEFAULT false,
  fallback_enabled boolean NOT NULL DEFAULT false,
  client_compatibility text NOT NULL DEFAULT 'openai_standard'
    CHECK (client_compatibility IN ('openai_standard', 'codex_responses')),
  schedulable boolean NOT NULL DEFAULT true,
  availability_schedule_json text CHECK (
    availability_schedule_json IS NULL OR jsonb_typeof(availability_schedule_json::jsonb) = 'object'
  ),
  availability_schedule_next_check_at timestamptz,
  notes text,
  account_expires_at timestamptz,
  last_used_at timestamptz,
  cooldown_until timestamptz,
  last_error_code text,
  last_error_message text,
  health_check_model text NOT NULL,
  health_check_endpoint_mode text NOT NULL
    CHECK (health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse')),
  deleted_at timestamptz,
  deleted_by text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK (NOT (super_priority_enabled AND fallback_enabled)),
  FOREIGN KEY (provider_protocol_profile_id, provider_code, protocol_code, protocol_version)
    REFERENCES juhe_business.provider_protocol_profiles(id, provider_code, protocol_code, protocol_version)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_id_owner_unique
  ON juhe_business.accounts (id, system_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique_lower
  ON juhe_business.accounts (system_account_id, lower(name))
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_owner_updated
  ON juhe_business.accounts (system_account_id, updated_at DESC, created_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_owner_provider_updated
  ON juhe_business.accounts (system_account_id, provider_code, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_owner_profile_updated
  ON juhe_business.accounts (system_account_id, provider_protocol_profile_id, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_owner_status_updated
  ON juhe_business.accounts (system_account_id, status, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_lookup
  ON juhe_business.accounts (system_account_id, name COLLATE "C", id)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup
  ON juhe_business.accounts (name COLLATE "C", id)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint
  ON juhe_business.accounts (credential_fingerprint)
  WHERE credential_fingerprint IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
  account_id text NOT NULL REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  model text NOT NULL CHECK (btrim(model) <> ''),
  created_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, model)
);

CREATE INDEX IF NOT EXISTS idx_account_supported_models_provider_model
  ON juhe_business.account_supported_models (provider_code, model, account_id);

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'fk_group_accounts_account_owner'
      AND conrelid = 'juhe_business.group_accounts'::regclass
  ) THEN
    ALTER TABLE juhe_business.group_accounts
      ADD CONSTRAINT fk_group_accounts_account_owner
      FOREIGN KEY (account_id, system_account_id)
      REFERENCES juhe_business.accounts(id, system_account_id)
      ON DELETE CASCADE;
  END IF;
END $$;
-- +goose StatementEnd

INSERT INTO juhe_business.protocols (
  id, code, version, name, description, enabled, created_at, updated_at
) VALUES
  ('protocol_openai_v1', 'openai', 'v1', 'OpenAI v1', 'OpenAI v1 compatible protocol', true, now(), now()),
  ('protocol_anthropic_v1', 'anthropic', 'v1', 'Anthropic v1', 'Anthropic Messages protocol', true, now(), now()),
  ('protocol_gemini_v1beta', 'gemini', 'v1beta', 'Gemini v1beta', 'Google Gemini native protocol', true, now(), now())
ON CONFLICT (code, version) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.provider_protocol_profiles (
  id, provider_code, name, description, enabled, protocol_code, protocol_version,
  base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
) VALUES
  ('profile_openai_openai_v1', 'openai', 'OpenAI Compatible / OpenAI v1',
    '通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案',
    true, 'openai', 'v1', 'https://api.openai.com/v1', 'gpt-5.6-sol',
    '["api_key"]', '["responses","chat","passthrough"]', now(), now()),
  ('profile_gpt_openai_v1', 'gpt', 'GPT / OpenAI v1',
    'GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户接入方式',
    true, 'openai', 'v1', 'https://api.openai.com/v1', 'gpt-5.6-sol',
    '["oauth","api_key"]', '["responses","chat"]', now(), now()),
  ('profile_deepseek_openai_v1', 'deepseek', 'DeepSeek / OpenAI v1',
    'DeepSeek OpenAI-compatible Chat Completions 协议档案',
    true, 'openai', 'v1', 'https://api.deepseek.com', 'deepseek-v4-flash',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_deepseek_anthropic_v1', 'deepseek', 'DeepSeek / Anthropic v1',
    'DeepSeek Anthropic-compatible Messages 协议档案',
    true, 'anthropic', 'v1', 'https://api.deepseek.com/anthropic', 'deepseek-v4-flash',
    '["api_key"]', '["messages","models","passthrough"]', now(), now()),
  ('profile_anthropic_anthropic_v1', 'anthropic', 'Anthropic / Anthropic v1',
    'Anthropic 官方 API Key 协议档案',
    true, 'anthropic', 'v1', 'https://api.anthropic.com/v1', 'claude-opus-4-8',
    '["api_key"]', '["messages","models","count_tokens","passthrough"]', now(), now()),
  ('profile_gemini_native_v1beta', 'gemini', 'Gemini / Gemini v1beta',
    'Gemini 官方 API Key 原生 v1beta 协议档案',
    true, 'gemini', 'v1beta', 'https://generativelanguage.googleapis.com', 'gemini-3.5-flash',
    '["api_key"]', '["generate_content","stream_generate_content","count_tokens","embed_content","models","passthrough"]', now(), now()),
  ('profile_gemini_openai_chat_v1beta', 'gemini', 'Gemini / OpenAI Chat',
    'Gemini 官方 OpenAI Chat Completions 兼容档案',
    true, 'openai', 'v1', 'https://generativelanguage.googleapis.com/v1beta/openai', 'gemini-3.5-flash',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_glm_general_openai_v1', 'glm', 'GLM 通用 / OpenAI Chat',
    '智谱通用 GLM API Key 协议档案',
    true, 'openai', 'v1', 'https://open.bigmodel.cn/api/paas/v4/', 'glm-5.2',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_glm_coding_openai_v1', 'glm', 'GLM Coding / OpenAI Chat',
    'GLM Coding Plan Key 的 OpenAI Chat Completions 协议档案',
    true, 'openai', 'v1', 'https://open.bigmodel.cn/api/coding/paas/v4', 'glm-5.2',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_glm_coding_anthropic_v1', 'glm', 'GLM Coding / Anthropic v1',
    'GLM Coding Plan Key 的 Anthropic Messages 协议档案',
    true, 'anthropic', 'v1', 'https://open.bigmodel.cn/api/anthropic', 'glm-5.2',
    '["api_key"]', '["messages","models","passthrough"]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'hybrid', '混合供应商 / OpenAI Chat',
    '混合供应商通用 API Key 档案',
    true, 'openai', 'v1', '', '',
    '["api_key"]', '["chat","responses","messages","generate_content","stream_generate_content","bridge"]', now(), now()),
  ('profile_hybrid_anthropic_messages_v1', 'hybrid', '混合供应商 / Anthropic Messages',
    '混合供应商 Anthropic Messages API Key 档案',
    true, 'anthropic', 'v1', '', '',
    '["api_key"]', '["messages","bridge"]', now(), now())
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

-- +goose Down
-- no-op: W1b public account tables are business data.
