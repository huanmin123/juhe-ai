-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.protocol_endpoint_families (
  id text PRIMARY KEY,
  protocol_code text NOT NULL,
  protocol_version text NOT NULL,
  family_code text NOT NULL,
  name text NOT NULL,
  description text,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (protocol_code, protocol_version, family_code),
  FOREIGN KEY (protocol_code, protocol_version)
    REFERENCES juhe_business.protocols(code, version)
);

CREATE TABLE IF NOT EXISTS juhe_business.provider_protocol_profile_families (
  profile_id text NOT NULL REFERENCES juhe_business.provider_protocol_profiles(id) ON DELETE CASCADE,
  family_code text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  default_test_model text,
  capabilities_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(capabilities_json::jsonb) = 'array'),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (profile_id, family_code)
);

CREATE TABLE IF NOT EXISTS juhe_business.provider_default_test_models (
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  model text NOT NULL CHECK (btrim(model) <> ''),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, provider_code)
);

CREATE INDEX IF NOT EXISTS idx_provider_default_test_models_model
  ON juhe_business.provider_default_test_models(provider_code, model, system_account_id);

INSERT INTO juhe_business.providers (
  id, code, name, parent_code, description, enabled, default_supported_models_json, created_at, updated_at
) VALUES
  ('openai', 'openai', 'OpenAI 兼容', NULL,
    '通用 OpenAI-compatible 供应商，用于接入兼容 OpenAI v1 协议的上游服务，默认只提供 API Key 透传能力',
    true, '["gpt-5.5","gpt-5.4","gpt-5.4-mini","gpt-image-2"]', now(), now()),
  ('gpt', 'gpt', 'GPT', 'openai',
    'GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力',
    true, '["gpt-5.5","gpt-5.4","gpt-5.4-mini","gpt-image-2"]', now(), now()),
  ('deepseek', 'deepseek', 'DeepSeek', NULL,
    'DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code',
    true, '["deepseek-v4-flash","deepseek-v4-pro","deepseek-ai-v4-flash","deepseek-ai-v4-pro"]', now(), now()),
  ('anthropic', 'anthropic', 'Anthropic', NULL,
    'Anthropic 官方供应商，当前支持官方 API Key 与 Anthropic Messages 原生协议直连',
    true, '["claude-opus-4-8","claude-sonnet-4-6","claude-haiku-4-5"]', now(), now()),
  ('gemini', 'gemini', 'Gemini', NULL,
    'Google Gemini 官方供应商，默认使用 Gemini v1beta 原生协议；Codex / OpenAI 客户端通过 Gemini OpenAI Chat 兼容档案接入',
    true, '["gemini-3.5-flash","gemini-3.1-pro-preview","gemini-2.5-pro","gemini-2.5-flash"]', now(), now()),
  ('glm', 'glm', '智谱 GLM', NULL,
    '智谱 GLM 官方供应商，支持通用 GLM API Key、GLM Coding Plan OpenAI Chat 档案，以及 GLM Coding Anthropic v1 Messages 档案',
    true, '["glm-5.2","glm-5.1","glm-5","glm-5-turbo","glm-4.7-flashx","glm-4.7-flash"]', now(), now()),
  ('hybrid', 'hybrid', '混合供应商', NULL,
    '混合供应商账户用于创建真实上游账户，并在账户内配置允许的下游协议入口和上游模型映射；不指向其他账户、分组或 API Key',
    true, '["gpt-5.5","claude-opus-4-8","gemini-3.5-flash","deepseek-v4-flash","glm-5.2"]', now(), now())
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
  base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
) VALUES
  ('profile_openai_openai_v1', 'openai', 'OpenAI 兼容 / OpenAI v1',
    '通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案，仅承载 API Key 透传、模型目录和通用协议策略',
    true, 'openai', 'v1', 'https://api.openai.com/v1', 'gpt-5.5',
    '["api_key"]', '["responses","chat","passthrough"]', now(), now()),
  ('profile_gpt_openai_v1', 'gpt', 'GPT / OpenAI v1',
    'GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户创建方式',
    true, 'openai', 'v1', 'https://api.openai.com/v1', 'gpt-5.5',
    '["oauth","api_key"]', '["responses","chat"]', now(), now()),
  ('profile_deepseek_anthropic_v1', 'deepseek', 'DeepSeek / Anthropic v1',
    'DeepSeek 供应商的 Anthropic v1 Messages 协议档案，承载 Claude Code 使用的 /v1/messages 与 /v1/models 直连',
    true, 'anthropic', 'v1', 'https://api.deepseek.com/anthropic', 'deepseek-v4-flash',
    '["api_key"]', '["messages","models","passthrough"]', now(), now()),
  ('profile_deepseek_openai_v1', 'deepseek', 'DeepSeek / OpenAI v1',
    'DeepSeek 供应商的 OpenAI-compatible v1 协议档案，承载 API Key、Chat Completions、DeepSeek 响应扩展字段与 Codex Responses 桥接',
    true, 'openai', 'v1', 'https://api.deepseek.com', 'deepseek-v4-flash',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_anthropic_anthropic_v1', 'anthropic', 'Anthropic / Anthropic v1',
    'Anthropic 官方 API Key 协议档案，仅承载 x-api-key、anthropic-version 与 Messages 原生协议',
    true, 'anthropic', 'v1', 'https://api.anthropic.com/v1', 'claude-opus-4-8',
    '["api_key"]', '["messages","models","count_tokens","passthrough"]', now(), now()),
  ('profile_gemini_openai_chat_v1beta', 'gemini', 'Gemini / OpenAI Chat',
    'Gemini 官方 OpenAI Chat Completions 兼容档案，仅用于 OpenAI Chat 直连和 Codex Responses 显式模型映射，不承载 Gemini 原生协议',
    true, 'openai', 'v1', 'https://generativelanguage.googleapis.com/v1beta/openai', 'gemini-3.5-flash',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_gemini_native_v1beta', 'gemini', 'Gemini / Gemini v1beta',
    'Gemini 官方 API Key 协议档案，承载 x-goog-api-key 与 Gemini v1beta 原生协议直连',
    true, 'gemini', 'v1beta', 'https://generativelanguage.googleapis.com', 'gemini-3.5-flash',
    '["api_key"]', '["generate_content","stream_generate_content","count_tokens","embed_content","models","passthrough"]', now(), now()),
  ('profile_glm_coding_openai_v1', 'glm', '智谱 GLM Coding / OpenAI Chat',
    '智谱 GLM Coding Plan Key 协议档案，使用 Coding Plan OpenAI Chat Completions 兼容端点',
    true, 'openai', 'v1', 'https://open.bigmodel.cn/api/coding/paas/v4', 'glm-5.2',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_glm_coding_anthropic_v1', 'glm', '智谱 GLM Coding / Anthropic v1',
    '智谱 GLM Coding Plan Key 的 Anthropic v1 Messages 协议档案，面向 Anthropic Messages 客户端直连',
    true, 'anthropic', 'v1', 'https://open.bigmodel.cn/api/anthropic', 'glm-5.2',
    '["api_key"]', '["messages","models","passthrough"]', now(), now()),
  ('profile_glm_general_openai_v1', 'glm', '智谱 GLM 通用 / OpenAI Chat',
    '智谱通用 GLM API Key 协议档案，使用智谱 OpenAI Chat Completions 兼容端点',
    true, 'openai', 'v1', 'https://open.bigmodel.cn/api/paas/v4/', 'glm-5.2',
    '["api_key"]', '["chat","passthrough"]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'hybrid', '混合供应商',
    '混合供应商通用 API Key 档案；真实上游 Base URL 和目标协议由账户模型映射显式声明',
    true, 'openai', 'v1', '', '',
    '["api_key"]', '["chat","responses","messages","generate_content","stream_generate_content","bridge"]', now(), now()),
  ('profile_hybrid_anthropic_messages_v1', 'hybrid', '混合供应商 Anthropic Messages',
    '混合供应商 Anthropic Messages API Key 档案；下游协议由账户模型映射显式声明',
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
  default_test_model = EXCLUDED.default_test_model,
  account_types_json = EXCLUDED.account_types_json,
  capabilities_json = EXCLUDED.capabilities_json,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.protocol_endpoint_families (
  id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
) VALUES
  ('openai_v1_chat_completions', 'openai', 'v1', 'chat_completions', 'Chat Completions', 'OpenAI v1 /chat/completions 接口族', true, now(), now()),
  ('openai_v1_responses', 'openai', 'v1', 'responses', 'Responses', 'OpenAI v1 /responses 接口族', true, now(), now()),
  ('anthropic_v1_messages', 'anthropic', 'v1', 'messages', 'Messages', 'Anthropic v1 /messages 接口族', true, now(), now()),
  ('anthropic_v1_models', 'anthropic', 'v1', 'models', 'Models', 'Anthropic v1 /models 接口族', true, now(), now()),
  ('anthropic_v1_message_token_counting', 'anthropic', 'v1', 'message_token_counting', 'Message Token Counting', 'Anthropic v1 /messages/count_tokens 接口族', true, now(), now()),
  ('gemini_v1beta_models', 'gemini', 'v1beta', 'models', 'Models', 'Gemini v1beta /models 接口族', true, now(), now()),
  ('gemini_v1beta_generate_content', 'gemini', 'v1beta', 'generate_content', 'generateContent', 'Gemini v1beta :generateContent 接口族', true, now(), now()),
  ('gemini_v1beta_stream_generate_content', 'gemini', 'v1beta', 'stream_generate_content', 'streamGenerateContent', 'Gemini v1beta :streamGenerateContent SSE 接口族', true, now(), now()),
  ('gemini_v1beta_count_tokens', 'gemini', 'v1beta', 'count_tokens', 'countTokens', 'Gemini v1beta :countTokens 接口族', true, now(), now()),
  ('gemini_v1beta_embed_content', 'gemini', 'v1beta', 'embed_content', 'embedContent', 'Gemini v1beta :embedContent 接口族', true, now(), now())
ON CONFLICT (protocol_code, protocol_version, family_code) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at;

INSERT INTO juhe_business.provider_protocol_profile_families (
  profile_id, family_code, enabled, capabilities_json, created_at, updated_at
) VALUES
  ('profile_openai_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_openai_openai_v1', 'responses', true, '[]', now(), now()),
  ('profile_gpt_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_gpt_openai_v1', 'responses', true, '[]', now(), now()),
  ('profile_deepseek_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_deepseek_anthropic_v1', 'messages', true, '[]', now(), now()),
  ('profile_deepseek_anthropic_v1', 'models', true, '[]', now(), now()),
  ('profile_anthropic_anthropic_v1', 'messages', true, '[]', now(), now()),
  ('profile_anthropic_anthropic_v1', 'models', true, '[]', now(), now()),
  ('profile_anthropic_anthropic_v1', 'message_token_counting', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'models', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'generate_content', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'stream_generate_content', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'count_tokens', true, '[]', now(), now()),
  ('profile_gemini_native_v1beta', 'embed_content', true, '[]', now(), now()),
  ('profile_gemini_openai_chat_v1beta', 'chat_completions', true, '[]', now(), now()),
  ('profile_glm_general_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_glm_coding_openai_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_glm_coding_anthropic_v1', 'messages', true, '[]', now(), now()),
  ('profile_glm_coding_anthropic_v1', 'models', true, '[]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'chat_completions', true, '[]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'responses', true, '[]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'messages', true, '[]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'generate_content', true, '[]', now(), now()),
  ('profile_hybrid_openai_chat_v1', 'stream_generate_content', true, '[]', now(), now()),
  ('profile_hybrid_anthropic_messages_v1', 'messages', true, '[]', now(), now())
ON CONFLICT (profile_id, family_code) DO UPDATE SET
  enabled = EXCLUDED.enabled,
  capabilities_json = EXCLUDED.capabilities_json,
  updated_at = EXCLUDED.updated_at;

-- +goose Down
-- no-op: provider option catalog tables are business metadata.
