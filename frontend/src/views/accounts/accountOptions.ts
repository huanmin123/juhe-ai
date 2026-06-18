import type { AccountStatus, ProviderDefinition } from '@/types/domain'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_MESSAGE_TOKEN_COUNTING_FAMILY,
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_MODELS_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  ANTHROPIC_PROVIDER_CODE,
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
  defaultTestModel: 'claude-haiku-4-5',
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
      defaultTestModel: 'claude-haiku-4-5',
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

export const FALLBACK_PROVIDERS: ProviderDefinition[] = [GPT_PROVIDER, OPENAI_COMPATIBLE_PROVIDER, ANTHROPIC_PROVIDER]

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
