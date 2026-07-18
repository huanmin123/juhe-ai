-- +goose Up
UPDATE juhe_business.providers
SET
  id = 'deepseek',
  name = 'DeepSeek',
  parent_code = NULL,
  description = 'DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code',
  enabled = true,
  default_supported_models_json = '["deepseek-v4-flash","deepseek-v4-pro"]',
  updated_at = now()
WHERE code = 'deepseek';

UPDATE juhe_business.provider_protocol_profiles AS target
SET
  provider_code = 'deepseek',
  name = source.name,
  description = source.description,
  enabled = true,
  protocol_code = source.protocol_code,
  protocol_version = 'v1',
  base_url = source.base_url,
  default_health_check_model = 'deepseek-v4-flash',
  account_types_json = '["api_key"]',
  capabilities_json = source.capabilities_json,
  updated_at = now()
FROM (
  VALUES
    (
      'profile_deepseek_openai_v1',
      'DeepSeek / OpenAI v1',
      'DeepSeek 供应商的 OpenAI-compatible v1 协议档案，承载 API Key、Chat Completions、DeepSeek 响应扩展字段与 Codex Responses 桥接',
      'openai',
      'https://api.deepseek.com',
      '["chat","passthrough"]'
    ),
    (
      'profile_deepseek_anthropic_v1',
      'DeepSeek / Anthropic v1',
      'DeepSeek 供应商的 Anthropic v1 Messages 协议档案，承载 Claude Code 使用的 /v1/messages 与 /v1/models 直连',
      'anthropic',
      'https://api.deepseek.com/anthropic',
      '["messages","models","passthrough"]'
    )
) AS source(id, name, description, protocol_code, base_url, capabilities_json)
WHERE target.id = source.id;

-- +goose Down
-- no-op: DeepSeek provider options are current business metadata.
