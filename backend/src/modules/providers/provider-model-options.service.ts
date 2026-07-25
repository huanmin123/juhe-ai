import { isHybridProviderCode } from '../../domain/provider-protocol.js'
import { listCustomProviderModelOptionsAsync } from '../../storage/custom-provider-models.repository.js'
import { listBuiltInProviderModelOptionsAsync } from '../../storage/provider-model-catalog.repository.js'
import { listProvidersAsync } from '../../storage/provider.repository.js'
import {
  listAnthropicProtocolProviderCodesAsync,
  listGeminiProtocolProviderCodesAsync,
  listOpenAIProtocolProviderCodesAsync
} from '../../storage/provider.repository.js'
import {
  modelCatalogBuiltInSourceProviderCodes,
  modelCatalogSourceProviderCodesAsync
} from '../model-pricing/model-catalog.service.js'

export interface ProviderModelOptionQuery {
  providerCode?: string
  protocol?: 'openai' | 'anthropic' | 'gemini'
  keyword?: string
  limit: number
  selectedIds: string[]
}

export interface ProviderModelOptionRow {
  id: string
  providerCode: string
  model: string
  scope: 'built_in' | 'global' | 'personal'
  mode?: string
  releaseDate?: string
  supportedApiProtocols?: string[]
  supportedServiceTiers?: string[]
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort?: string
}

export interface ProviderModelSelectionOption {
  id: string
  name: string
  providerCode?: string
  supportedApiProtocols: string[]
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
}

export function normalizeProviderModelOptionQuery(query: Record<string, unknown>): ProviderModelOptionQuery {
  const providerCode = optionalText(firstQueryValue(query.providerCode))
  const protocolText = optionalText(firstQueryValue(query.protocol))
  if (protocolText && protocolText !== 'openai' && protocolText !== 'anthropic' && protocolText !== 'gemini') {
    throw new Error('protocol 必须是 openai、anthropic 或 gemini')
  }
  const protocol = protocolText === 'openai' || protocolText === 'anthropic' || protocolText === 'gemini'
    ? protocolText
    : undefined
  const keyword = optionalText(firstQueryValue(query.keyword))
  const limitText = optionalText(firstQueryValue(query.limit))
  const limit = limitText === undefined ? 50 : Number(limitText)
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) {
    throw new Error('limit 必须是 1 到 50 的整数')
  }
  const selectedIds = normalizedTextList(query.selectedIds, 50)
  return {
    ...(providerCode ? { providerCode } : {}),
    ...(protocol ? { protocol } : {}),
    ...(keyword ? { keyword } : {}),
    limit,
    selectedIds
  }
}

export async function listProviderModelSelectionOptionsAsync(input: ProviderModelOptionQuery & {
  systemAccountId?: string
}): Promise<ProviderModelSelectionOption[]> {
  return mergeProviderModelOptionRows(await listProviderModelOptionRowsAsync(input), input)
}

export async function listProviderModelOptionRowsAsync(input: ProviderModelOptionQuery & {
  systemAccountId?: string
}): Promise<ProviderModelOptionRow[]> {
  const providerCodes = await providerModelSourceCodesAsync(input.providerCode, input.protocol)
  if (!providerCodes.length) return []
  const builtInProviderCodes = input.providerCode
    ? modelCatalogBuiltInSourceProviderCodes(input.providerCode, providerCodes)
    : providerCodes
  const [builtIn, custom] = await Promise.all([
    listBuiltInProviderModelOptionsAsync({
      providerCodes: builtInProviderCodes,
      keyword: input.keyword,
      limit: input.limit,
      selectedIds: input.selectedIds
    }),
    listCustomProviderModelOptionsAsync({
      providerCodes,
      systemAccountId: input.systemAccountId,
      keyword: input.keyword,
      limit: input.limit,
      selectedIds: input.selectedIds
    })
  ])
  return [...builtIn, ...custom]
}

export function mergeProviderModelOptionRows(
  rows: ProviderModelOptionRow[],
  query: ProviderModelOptionQuery
): ProviderModelSelectionOption[] {
  const selectedIds = new Set(query.selectedIds)
  const keyword = query.keyword?.toLowerCase()
  const byProviderModel = new Map<string, ProviderModelOptionRow>()
  for (const row of rows) {
    const providerCode = row.providerCode.trim()
    const model = row.model.trim()
    if (!providerCode || !model) continue
    if (keyword && !model.toLowerCase().includes(keyword) && !selectedIds.has(model)) continue
    const key = model
    const existing = byProviderModel.get(key)
    if (!existing || optionScopePriority(row.scope) > optionScopePriority(existing.scope)) {
      byProviderModel.set(key, { ...row, providerCode, model })
    }
  }
  const ordered = [...byProviderModel.values()].sort((left, right) => {
    const releaseDateOrder = compareModelReleaseDateDescending(left.releaseDate, right.releaseDate)
    if (releaseDateOrder !== 0) return releaseDateOrder
    const modelOrder = left.model.localeCompare(right.model, 'en')
    if (modelOrder !== 0) return modelOrder
    return left.providerCode.localeCompare(right.providerCode, 'en')
  })
  const visibleModels = new Set([
    ...query.selectedIds,
    ...ordered.filter((row) => !selectedIds.has(row.model)).slice(0, query.limit).map((row) => row.model)
  ])
  return ordered.filter((row) => visibleModels.has(row.model)).map((row) => ({
    id: row.model,
    name: row.model,
    supportedApiProtocols: [...(row.supportedApiProtocols ?? [])],
    supportedServiceTiers: [...(row.supportedServiceTiers ?? [])],
    supportedReasoningEfforts: [...(row.supportedReasoningEfforts ?? [])],
    ...(row.defaultReasoningEffort ? { defaultReasoningEffort: row.defaultReasoningEffort } : {})
  }))
}

function compareModelReleaseDateDescending(left?: string, right?: string): number {
  const leftDate = normalizedModelReleaseDate(left)
  const rightDate = normalizedModelReleaseDate(right)
  if (leftDate && rightDate && leftDate !== rightDate) return rightDate.localeCompare(leftDate, 'en')
  if (leftDate) return -1
  if (rightDate) return 1
  return 0
}

function normalizedModelReleaseDate(value?: string): string {
  const normalized = value?.trim().slice(0, 10) ?? ''
  if (!/^\d{4}-\d{2}-\d{2}$/.test(normalized)) return ''
  return Number.isFinite(Date.parse(`${normalized}T00:00:00.000Z`)) ? normalized : ''
}

async function providerModelSourceCodesAsync(
  providerCode?: string,
  protocol?: ProviderModelOptionQuery['protocol']
): Promise<string[]> {
  if (providerCode) return modelCatalogSourceProviderCodesAsync(providerCode)
  if (protocol === 'openai') return listOpenAIProtocolProviderCodesAsync()
  if (protocol === 'anthropic') return listAnthropicProtocolProviderCodesAsync()
  if (protocol === 'gemini') return listGeminiProtocolProviderCodesAsync()
  return [...new Set((await listProvidersAsync())
    .filter((provider) => provider.enabled && !isHybridProviderCode(provider.code))
    .map((provider) => provider.code.trim())
    .filter(Boolean))]
}

function optionScopePriority(scope: ProviderModelOptionRow['scope']): number {
  if (scope === 'personal') return 3
  if (scope === 'global') return 2
  return 1
}

function firstQueryValue(value: unknown): unknown {
  return Array.isArray(value) ? value[0] : value
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedTextList(value: unknown, max: number): string[] {
  const raw = Array.isArray(value) ? value : typeof value === 'string' ? value.split(',') : []
  return [...new Set(raw
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.trim())
    .filter(Boolean))].slice(0, max)
}
