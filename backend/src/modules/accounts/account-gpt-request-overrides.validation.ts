import type { AccountType } from '../../domain/types.js'
import {
  listProviderModelCatalog,
  listProviderModelCatalogAsync,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'
import {
  readGptAccountRequestOverrides,
  type GptAccountRequestOverrides
} from '../providers/drivers/gpt/request-overrides.js'

export function assertAccountGptRequestOverridesSupported(input: {
  providerCode: string
  accountType: AccountType
  credentials: Record<string, unknown> | undefined
  supportedModels: readonly string[]
  systemAccountId?: string
}): void {
  const overrides = readGptAccountRequestOverrides(input.credentials)
  if (!overrides.serviceTier && !overrides.reasoningEffort) return
  const catalog = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeUnpriced: true
  })
  assertAccountGptRequestOverridesSupportedByCatalog({
    providerCode: input.providerCode,
    accountType: input.accountType,
    overrides,
    supportedModels: input.supportedModels,
    catalog
  })
}

export async function assertAccountGptRequestOverridesSupportedAsync(input: {
  providerCode: string
  accountType: AccountType
  credentials: Record<string, unknown> | undefined
  supportedModels: readonly string[]
  systemAccountId?: string
}): Promise<void> {
  const overrides = readGptAccountRequestOverrides(input.credentials)
  if (!overrides.serviceTier && !overrides.reasoningEffort) return
  const catalog = await listProviderModelCatalogAsync({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeUnpriced: true
  })
  assertAccountGptRequestOverridesSupportedByCatalog({
    providerCode: input.providerCode,
    accountType: input.accountType,
    overrides,
    supportedModels: input.supportedModels,
    catalog
  })
}

export function assertAccountGptRequestOverridesSupportedByCatalog(input: {
  providerCode?: string
  accountType: AccountType
  overrides: GptAccountRequestOverrides
  supportedModels: readonly string[]
  catalog: readonly ProviderModelCatalogItem[]
}): void {
  assertProviderSupportsAccountRequestOverrides(input.providerCode ?? 'gpt', input.overrides)
  const supportedModels = uniqueTextList(input.supportedModels)
  if (!supportedModels.length) {
    throw new Error('请求覆盖要求账户至少配置一个支持模型')
  }
  const catalogByModel = new Map(input.catalog.map((item) => [item.model.trim(), item]))
  const modelItems = supportedModels
    .map((model) => catalogByModel.get(model))
    .filter((item): item is ProviderModelCatalogItem => item !== undefined)

  if (input.overrides.serviceTier) {
    const requiredTier = input.overrides.serviceTier === 'default'
      ? undefined
      : input.overrides.serviceTier
    const supported = modelItems.some((item) => requiredTier
      ? item.supportedServiceTiers.includes(requiredTier)
      : item.supportedServiceTiers.length > 0)
    if (!supported) {
      if (requiredTier) throw new Error(`所选支持模型中没有模型支持服务等级 ${requiredTier}`)
      throw new Error('所选支持模型中没有模型声明服务等级覆盖')
    }
  }

  if (input.overrides.reasoningEffort) {
    const supported = modelItems.some((item) => item.supportedReasoningEfforts.includes(input.overrides.reasoningEffort!))
    if (!supported) {
      throw new Error(`所选支持模型中没有模型支持思考级别 ${input.overrides.reasoningEffort}`)
    }
  }
}

function uniqueTextList(values: readonly string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const normalized = value.trim()
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function assertProviderSupportsAccountRequestOverrides(
  providerCode: string,
  overrides: GptAccountRequestOverrides
): void {
  if (!new Set(['gpt', 'openai', 'anthropic', 'gemini']).has(providerCode)) {
    throw new Error(`供应商 ${providerCode} 没有可确认的账户请求覆盖 wire 映射`)
  }
  if (providerCode === 'gemini' && overrides.serviceTier) {
    throw new Error('Gemini 原生请求没有可确认的服务等级 wire 字段，不能保存账户服务等级覆盖')
  }
}
