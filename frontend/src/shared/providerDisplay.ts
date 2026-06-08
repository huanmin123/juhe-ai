import type { ProviderDefinition } from '@/types/domain'
import { GPT_VENDOR_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, normalizeProviderToken } from './providerProtocol'

const builtinProviderNames = new Map<string, string>([
  [OPENAI_COMPATIBLE_PROVIDER_CODE, 'OpenAI 兼容'],
  [GPT_VENDOR_CODE, 'GPT']
])

type ProviderDisplaySource = Pick<ProviderDefinition, 'code' | 'name'>

export function providerDisplayName(providerCode?: string, providers: ProviderDisplaySource[] = []): string {
  const code = normalizeProviderToken(providerCode)
  if (!code) return '未知供应商'
  const provider = providers.find((item) => normalizeProviderToken(item.code) === code)
  return provider?.name?.trim() || builtinProviderNames.get(code) || '未知供应商'
}
