import type { AccountStatus, ProviderDefinition } from '@/types/domain'

export const OPENAI_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  defaultTestModel: '',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

export const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
export const ACCOUNT_PAGE_SIZE = 20
export const OPENAI_PROVIDER_CODE = 'openai'

export const statusOptions: Array<{ label: string; value: AccountStatus }> = [
  { label: '正常', value: 'active' },
  { label: '停用', value: 'disabled' },
  { label: '异常', value: 'error' },
  { label: '限流中', value: 'rate_limited' },
  { label: '临时不可调用', value: 'temporary_unavailable' }
]
