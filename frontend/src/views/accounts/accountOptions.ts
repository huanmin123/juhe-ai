import type { AccountStatus, ProviderDefinition } from '@/types/domain'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY
} from '@/shared/providerProtocol'

export { GPT_VENDOR_CODE }

export const OPENAI_PROVIDER: ProviderDefinition = {
  id: GPT_VENDOR_CODE,
  code: GPT_VENDOR_CODE,
  name: 'GPT',
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
