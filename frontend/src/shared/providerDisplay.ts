import type { ProviderDefinition } from '@/types/domain'
import { ANTHROPIC_PROVIDER_CODE, DEEPSEEK_PROVIDER_CODE, GEMINI_PROVIDER_CODE, GLM_PROVIDER_CODE, GPT_VENDOR_CODE, HYBRID_PROVIDER_CODE, OPENAI_COMPATIBLE_PROVIDER_CODE, normalizeProviderToken } from './providerProtocol'

const builtinProviderNames = new Map<string, string>([
  [OPENAI_COMPATIBLE_PROVIDER_CODE, 'OpenAI 兼容'],
  [GPT_VENDOR_CODE, 'GPT'],
  [DEEPSEEK_PROVIDER_CODE, 'DeepSeek'],
  [GLM_PROVIDER_CODE, '智谱 GLM'],
  [ANTHROPIC_PROVIDER_CODE, 'Anthropic'],
  [GEMINI_PROVIDER_CODE, 'Google Gemini'],
  [HYBRID_PROVIDER_CODE, '混合供应商']
])

type ProviderDisplaySource = Pick<ProviderDefinition, 'code' | 'name'>

export function providerDisplayName(providerCode?: string, providers: ProviderDisplaySource[] = []): string {
  const code = normalizeProviderToken(providerCode)
  if (!code) return '未知供应商'
  const provider = providers.find((item) => normalizeProviderToken(item.code) === code)
  return provider?.name?.trim() || builtinProviderNames.get(code) || '未知供应商'
}
