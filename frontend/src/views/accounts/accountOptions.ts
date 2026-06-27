import type { AccountStatus, ProviderDefinition } from '@/types/domain'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_MODELS_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  ANTHROPIC_PROVIDER_CODE,
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_PROVIDER_CODE,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY
} from '@/shared/providerProtocol'

export { GPT_VENDOR_CODE }

export const OPENAI_COMPATIBLE_PROVIDER: ProviderDefinition = {
  id: OPENAI_COMPATIBLE_PROVIDER_CODE,
  code: OPENAI_COMPATIBLE_PROVIDER_CODE,
  name: 'OpenAI 兼容',
  enabled: true,
  defaultProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: 'gpt-5.5',
  accountTypes: ['api_key'],
  capabilities: ['responses', 'chat', 'passthrough'],
  protocolProfiles: [
    {
      id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: 'OpenAI 兼容 / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.openai.com/v1',
      defaultTestModel: 'gpt-5.5',
      accountTypes: ['api_key'],
      capabilities: ['responses', 'chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' },
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const GPT_PROVIDER: ProviderDefinition = {
  id: GPT_VENDOR_CODE,
  code: GPT_VENDOR_CODE,
  name: 'GPT',
  parentCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  enabled: true,
  defaultProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: '',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['responses', 'chat'],
  protocolProfiles: [
    {
      id: GPT_OPENAI_V1_PROFILE_ID,
      providerCode: GPT_VENDOR_CODE,
      name: 'GPT / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.openai.com/v1',
      defaultTestModel: '',
      accountTypes: ['oauth', 'api_key'],
      capabilities: ['responses', 'chat'],
      endpointFamilies: [
        { code: OPENAI_RESPONSES_FAMILY, name: 'Responses' },
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const ANTHROPIC_PROVIDER: ProviderDefinition = {
  id: ANTHROPIC_PROVIDER_CODE,
  code: ANTHROPIC_PROVIDER_CODE,
  name: 'Anthropic',
  enabled: true,
  defaultProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  baseUrl: 'https://api.anthropic.com/v1',
  defaultTestModel: 'claude-fable-5',
  accountTypes: ['api_key'],
  capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
  protocolProfiles: [
    {
      id: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
      providerCode: ANTHROPIC_PROVIDER_CODE,
      name: 'Anthropic / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://api.anthropic.com/v1',
      defaultTestModel: 'claude-fable-5',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'models', 'count_tokens', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' },
        { code: ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY, name: 'Message Token Counting' }
      ]
    }
  ]
}

export const GEMINI_PROVIDER: ProviderDefinition = {
  id: GEMINI_PROVIDER_CODE,
  code: GEMINI_PROVIDER_CODE,
  name: 'Google Gemini',
  enabled: true,
  defaultProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
  protocolCode: GEMINI_PROTOCOL_CODE,
  protocolVersion: GEMINI_PROTOCOL_VERSION,
  baseUrl: 'https://generativelanguage.googleapis.com',
  defaultTestModel: 'gemini-3.5-flash',
  accountTypes: ['api_key'],
  capabilities: ['generate_content', 'count_tokens', 'embed_content', 'passthrough'],
  protocolProfiles: [
    {
      id: GEMINI_NATIVE_V1BETA_PROFILE_ID,
      providerCode: GEMINI_PROVIDER_CODE,
      name: 'Google Gemini / Gemini v1beta',
      enabled: true,
      protocolCode: GEMINI_PROTOCOL_CODE,
      protocolVersion: GEMINI_PROTOCOL_VERSION,
      baseUrl: 'https://generativelanguage.googleapis.com',
      defaultTestModel: 'gemini-3.5-flash',
      accountTypes: ['api_key'],
      capabilities: ['generate_content', 'count_tokens', 'embed_content', 'passthrough'],
      endpointFamilies: [
        { code: GEMINI_GENERATE_CONTENT_FAMILY, name: 'Generate Content' },
        { code: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, name: 'Stream Generate Content' },
        { code: GEMINI_COUNT_TOKENS_FAMILY, name: 'Count Tokens' },
        { code: GEMINI_EMBED_CONTENT_FAMILY, name: 'Embed Content' }
      ]
    },
    {
      id: GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
      providerCode: GEMINI_PROVIDER_CODE,
      name: 'Google Gemini / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai',
      defaultTestModel: 'gemini-3.5-flash',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    }
  ]
}

export const HYBRID_PROVIDER: ProviderDefinition = {
  id: HYBRID_PROVIDER_CODE,
  code: HYBRID_PROVIDER_CODE,
  name: '混合供应商',
  enabled: true,
  defaultProtocolProfileId: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: 'gpt-5.5',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'messages', 'generate_content', 'stream_generate_content', 'bridge'],
  protocolProfiles: [
    {
      id: HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
      providerCode: HYBRID_PROVIDER_CODE,
      name: '混合供应商 / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.openai.com/v1',
      defaultTestModel: 'gpt-5.5',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'bridge'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
      providerCode: HYBRID_PROVIDER_CODE,
      name: '混合供应商 / Anthropic Messages',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://api.anthropic.com/v1',
      defaultTestModel: 'claude-fable-5',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'bridge'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' }
      ]
    },
    {
      id: HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID,
      providerCode: HYBRID_PROVIDER_CODE,
      name: '混合供应商 / Gemini native',
      enabled: true,
      protocolCode: GEMINI_PROTOCOL_CODE,
      protocolVersion: GEMINI_PROTOCOL_VERSION,
      baseUrl: 'https://generativelanguage.googleapis.com',
      defaultTestModel: 'gemini-3.5-flash',
      accountTypes: ['api_key'],
      capabilities: ['generate_content', 'stream_generate_content', 'bridge'],
      endpointFamilies: [
        { code: GEMINI_GENERATE_CONTENT_FAMILY, name: 'Generate Content' },
        { code: GEMINI_STREAM_GENERATE_CONTENT_FAMILY, name: 'Stream Generate Content' }
      ]
    }
  ]
}

export const DEEPSEEK_PROVIDER: ProviderDefinition = {
  id: DEEPSEEK_PROVIDER_CODE,
  code: DEEPSEEK_PROVIDER_CODE,
  name: 'DeepSeek',
  enabled: true,
  defaultProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://api.deepseek.com',
  defaultTestModel: 'deepseek-v4-flash',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'messages', 'passthrough'],
  protocolProfiles: [
    {
      id: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      providerCode: DEEPSEEK_PROVIDER_CODE,
      name: 'DeepSeek / OpenAI v1',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://api.deepseek.com',
      defaultTestModel: 'deepseek-v4-flash',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'messages', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
      providerCode: DEEPSEEK_PROVIDER_CODE,
      name: 'DeepSeek / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://api.deepseek.com/anthropic',
      defaultTestModel: 'deepseek-v4-flash',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'models', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' }
      ]
    }
  ]
}

export const GLM_PROVIDER: ProviderDefinition = {
  id: GLM_PROVIDER_CODE,
  code: GLM_PROVIDER_CODE,
  name: '智谱 GLM',
  enabled: true,
  defaultProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
  defaultTestModel: 'glm-5.2',
  accountTypes: ['api_key'],
  capabilities: ['chat', 'messages', 'passthrough'],
  protocolProfiles: [
    {
      id: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM 通用 / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/paas/v4/',
      defaultTestModel: 'glm-5.2',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'messages', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: GLM_CODING_OPENAI_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM Coding / OpenAI Chat',
      enabled: true,
      protocolCode: OPENAI_PROTOCOL_CODE,
      protocolVersion: OPENAI_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
      defaultTestModel: 'glm-5.2',
      accountTypes: ['api_key'],
      capabilities: ['chat', 'passthrough'],
      endpointFamilies: [
        { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' }
      ]
    },
    {
      id: GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
      providerCode: GLM_PROVIDER_CODE,
      name: '智谱 GLM Coding / Anthropic v1',
      enabled: true,
      protocolCode: ANTHROPIC_PROTOCOL_CODE,
      protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
      baseUrl: 'https://open.bigmodel.cn/api/anthropic',
      defaultTestModel: 'glm-5.2',
      accountTypes: ['api_key'],
      capabilities: ['messages', 'models', 'passthrough'],
      endpointFamilies: [
        { code: ANTHROPIC_MESSAGES_FAMILY, name: 'Messages' },
        { code: ANTHROPIC_MODELS_FAMILY, name: 'Models' }
      ]
    }
  ]
}

export const FALLBACK_PROVIDERS: ProviderDefinition[] = [GPT_PROVIDER, GLM_PROVIDER, DEEPSEEK_PROVIDER, HYBRID_PROVIDER, OPENAI_COMPATIBLE_PROVIDER, ANTHROPIC_PROVIDER, GEMINI_PROVIDER]

export const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
export const ACCOUNT_PAGE_SIZE = 20

export const statusOptions: Array<{ label: string; value: AccountStatus }> = [
  { label: '正常', value: 'active' },
  { label: '待测试', value: 'pending_test' },
  { label: '停用', value: 'disabled' },
  { label: '异常', value: 'error' },
  { label: '限流中', value: 'rate_limited' },
  { label: '临时不可调用', value: 'temporary_unavailable' }
]
