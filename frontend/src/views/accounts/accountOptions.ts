import type { AccountStatus, AccountType, ProviderDefinition } from '@/types/domain'
import type { SchedulableFilter } from './accountFormatters'

export const FALLBACK_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

export const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
export const ACCOUNT_PAGE_SIZE = 20

export const typeOptions: Array<{ label: string; value: 'all' | AccountType }> = [
  { label: '全部类型', value: 'all' },
  { label: 'OAuth', value: 'oauth' },
  { label: 'API Key', value: 'api_key' }
]

export const schedulableOptions: Array<{ label: string; value: SchedulableFilter }> = [
  { label: '全部启停', value: 'all' },
  { label: '已启用', value: 'enabled' },
  { label: '已停用', value: 'disabled' },
  { label: '临时不可调用', value: 'cooling' }
]

export const statusOptions: Array<{ label: string; value: 'all' | AccountStatus }> = [
  { label: '全部状态', value: 'all' },
  { label: '正常', value: 'active' },
  { label: '停用', value: 'disabled' },
  { label: '错误', value: 'error' },
  { label: '限流中', value: 'rate_limited' },
  { label: '临时不可调用', value: 'temporary_unavailable' }
]

export const defaultTestModelOptions = [
  'gpt-5.5',
  'gpt-5.4',
  'gpt-5.4-mini',
  'gpt-5.3-codex',
  'gpt-5.2',
  'gpt-4.1',
  'gpt-4.1-mini'
]
