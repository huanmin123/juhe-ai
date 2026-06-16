import type { AccountModelMapping } from '../domain/types.js'
import { listProviderModelCatalog } from '../modules/model-pricing/model-catalog.service.js'
import { normalizeAccountModelMappingsInput } from './account-model-mappings.repository.js'
import { normalizeAccountSupportedModelsInput } from './account-supported-models.repository.js'

export function normalizeAccountSupportedModelsForProvider(value: unknown, providerCode: string, systemAccountId: string): string[] | undefined {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models

  const providerModels = new Set(listProviderModelCatalog({
    providerCode,
    systemAccountId
  }).map((item) => item.model))
  const invalidModels = models.filter((model) => !providerModels.has(model))
  if (invalidModels.length > 0) {
    throw new Error(`账户支持模型不在供应商模型目录中：${invalidModels.slice(0, 5).join('、')}`)
  }
  return models
}

export function normalizeAccountModelMappingsForProvider(value: unknown, providerCode: string, systemAccountId: string): AccountModelMapping[] | undefined {
  const mappings = normalizeAccountModelMappingsInput(value)
  if (!mappings?.length) return mappings

  const requestableModels = new Set(listProviderModelCatalog({
    providerCode,
    systemAccountId
  }).map((item) => item.model))
  const invalidSourceModels = mappings
    .map((mapping) => mapping.sourceModel)
    .filter((model) => !requestableModels.has(model))
  if (invalidSourceModels.length > 0) {
    throw new Error(`映射下游模型不在可请求模型目录中：${invalidSourceModels.slice(0, 5).join('、')}`)
  }
  const invalidUpstreamModels = mappings
    .map((mapping) => mapping.upstreamModel)
    .filter((model) => !requestableModels.has(model))
  if (invalidUpstreamModels.length > 0) {
    throw new Error(`映射上游模型不在可请求模型目录中：${invalidUpstreamModels.slice(0, 5).join('、')}`)
  }
  return mappings
}
