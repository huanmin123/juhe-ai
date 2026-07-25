import type {
  ProviderModelApiProtocol,
  ProviderModelMode,
  ProviderModelPricing,
  ProviderModelPriceSet,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier,
  CustomProviderModelScope,
  ProviderModelStatus,
  ProviderModelUpsertPayload
} from '@/types/domain'

import {
  categoryFromModeOrModel,
  directPriceFieldKeys,
  directPriceFieldsByCategory,
  getModelCategory,
  modelModeOptions,
  modelStatusOptions,
  type DirectPriceFieldKey,
  type ModelCategoryKey
} from './providerModelFormatters'

export interface CustomModelForm {
  id?: string
  scope: CustomProviderModelScope
  model: string
  status: ProviderModelStatus
  mode: ProviderModelMode
  supportedApiProtocols: ProviderModelApiProtocol[]
  supportedServiceTiers: ProviderModelServiceTier[]
  supportedReasoningEfforts: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort
  configurationTemplateId?: string
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
  serviceTierPrices: Record<string, ProviderModelPriceSet>
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
}

export const emptyCustomModelForm: CustomModelForm = {
  scope: 'personal',
  model: '',
  status: 'active',
  mode: 'text',
  supportedApiProtocols: ['responses', 'chat_completions'],
  supportedServiceTiers: [],
  supportedReasoningEfforts: [],
  serviceTierPrices: {}
}

export function createCustomModelFormFromPricing(
  record: ProviderModelPricing,
  providerModels: ProviderModelPricing[]
): CustomModelForm {
  const form: CustomModelForm = {
    id: record.id,
    scope: record.scope === 'global' ? 'global' : 'personal',
    model: record.model,
    status: record.status ?? 'active',
    mode: categoryFromModeOrModel(record.mode, record.model),
    supportedApiProtocols: [...(record.supportedApiProtocols ?? [])],
    supportedServiceTiers: normalizeServiceTiers(record.supportedServiceTiers),
    supportedReasoningEfforts: normalizeReasoningEfforts(record.supportedReasoningEfforts),
    defaultReasoningEffort: record.defaultReasoningEffort ?? undefined,
    releaseDate: record.releaseDate,
    shutdownDate: record.shutdownDate,
    contextWindowTokens: record.contextWindowTokens,
    maxInputTokens: record.maxInputTokens,
    maxOutputTokens: record.maxOutputTokens,
    inputUsdPer1M: record.inputUsdPer1M,
    outputUsdPer1M: record.outputUsdPer1M,
    cachedInputUsdPer1M: record.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: record.cacheWriteUsdPer1M,
    cacheWrite1hUsdPer1M: record.cacheWrite1hUsdPer1M,
    cacheStorageUsdPer1MPerHour: record.cacheStorageUsdPer1MPerHour,
    serviceTierPrices: cloneServiceTierPrices(record.serviceTierPrices),
    imageInputUsdPer1M: record.imageInputUsdPer1M,
    imageOutputUsdPer1M: record.imageOutputUsdPer1M,
    audioInputUsdPer1M: record.audioInputUsdPer1M,
    audioOutputUsdPer1M: record.audioOutputUsdPer1M,
    outputUsdPerImage: record.outputUsdPerImage
  }

  const category = categoryFromModeOrModel(form.mode, form.model)
  clearCustomModelPricesOutsideCategory(form, category)
  return form
}

export function buildCustomModelPayload(
  form: CustomModelForm,
  category: ModelCategoryKey,
  options: { includeRequestCapabilities?: boolean; includePrices?: boolean; includeDefaultReasoningEffort?: boolean } = {}
): ProviderModelUpsertPayload | undefined {
  const model = form.model.trim()
  if (!model) return undefined
  const payload: ProviderModelUpsertPayload = {
    scope: form.scope,
    model,
    status: form.status,
    mode: form.mode,
    supportedApiProtocols: [...form.supportedApiProtocols],
    releaseDate: trimToNull(form.releaseDate),
    shutdownDate: trimToNull(form.shutdownDate),
    contextWindowTokens: numberToNull(form.contextWindowTokens),
    maxInputTokens: numberToNull(form.maxInputTokens),
    maxOutputTokens: numberToNull(form.maxOutputTokens)
  }
  if (form.configurationTemplateId) {
    payload.configurationTemplateId = form.configurationTemplateId
  }
  if (options.includePrices !== false) {
    payload.serviceTierPrices = cloneServiceTierPrices(form.serviceTierPrices)
    Object.assign(payload, buildCustomModelDirectPricePayload(form, category))
  }
  if (options.includeRequestCapabilities) {
    const supportedReasoningEfforts = category === 'text'
      ? normalizeReasoningEfforts(form.supportedReasoningEfforts)
      : []
    payload.supportedServiceTiers = category === 'text'
      ? normalizeServiceTiers(form.supportedServiceTiers)
      : []
    payload.supportedReasoningEfforts = supportedReasoningEfforts
    payload.defaultReasoningEffort = options.includeDefaultReasoningEffort === true && supportedReasoningEfforts.includes(form.defaultReasoningEffort ?? '')
      ? form.defaultReasoningEffort
      : null
  }
  return payload
}

export function clearCustomModelGptCapabilities(form: CustomModelForm): void {
  form.supportedServiceTiers = []
  form.supportedReasoningEfforts = []
  form.defaultReasoningEffort = undefined
}

export function normalizeCustomModelRequestCapabilities(form: CustomModelForm): void {
  form.supportedServiceTiers = normalizeServiceTiers(form.supportedServiceTiers)
  form.supportedReasoningEfforts = normalizeReasoningEfforts(form.supportedReasoningEfforts)
  if (!form.supportedReasoningEfforts.includes(form.defaultReasoningEffort ?? '')) {
    form.defaultReasoningEffort = undefined
  }
}

export function applyConfigurationTemplateToCustomModelForm(
  form: CustomModelForm,
  providerModels: ProviderModelPricing[],
  id?: string
): void {
  const templateID = trimToUndefined(id)
  if (!templateID) return
  const template = providerModels.find((item) => item.id === templateID)
  if (!template) return
  const category = getModelCategory(template)
  form.configurationTemplateId = templateID
  form.mode = category
  form.supportedApiProtocols = [...(template.supportedApiProtocols ?? [])]
  form.supportedServiceTiers = normalizeServiceTiers(template.supportedServiceTiers)
  form.supportedReasoningEfforts = normalizeReasoningEfforts(template.supportedReasoningEfforts)
  form.defaultReasoningEffort = undefined
  form.contextWindowTokens = template.contextWindowTokens
  form.maxInputTokens = template.maxInputTokens
  form.maxOutputTokens = template.maxOutputTokens
  form.releaseDate = template.releaseDate
  form.shutdownDate = template.shutdownDate
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  for (const field of directPriceFieldKeys) {
    form[field] = visibleFields.has(field) ? template[field] : undefined
  }
  form.serviceTierPrices = cloneServiceTierPrices(template.serviceTierPrices)
  reconcileCustomModelServiceTierPrices(form)
}

export function reconcileCustomModelServiceTierPrices(form: CustomModelForm): void {
  const tiers = normalizeServiceTiers(form.supportedServiceTiers)
  const current = form.serviceTierPrices
  form.supportedServiceTiers = tiers
  form.serviceTierPrices = Object.fromEntries(tiers.map((tier) => [tier, { ...(current[tier] ?? {}) }]))
}

export function availableCustomModelStatusOptions(_canManagePrices: boolean, _originalStatus?: ProviderModelStatus) {
  return modelStatusOptions
}

const gptServiceTierValues: ProviderModelServiceTier[] = ['priority', 'flex']
const gptReasoningEffortValues: ProviderModelReasoningEffort[] = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export function buildCustomModelCapabilityOptions(
  providerCode: string,
  serviceTiers: ProviderModelServiceTier[],
  reasoningEfforts: ProviderModelReasoningEffort[]
) {
  const isGpt = providerCode.trim().toLowerCase() === 'gpt'
  return {
    serviceTiers: capabilityOptions([...(isGpt ? gptServiceTierValues : []), ...serviceTiers], formatServiceTier),
    reasoningEfforts: capabilityOptions(
      [...(isGpt ? gptReasoningEffortValues : []), ...reasoningEfforts].filter((value) => value !== 'none'),
      formatReasoningEffort
    )
  }
}

export function canManageModelPricesForView(isManagementView: boolean, isAdmin: boolean): boolean {
  void isManagementView
  void isAdmin
  return true
}

export function availableCustomModelModeOptions(providerCode: string, providerModels: ProviderModelPricing[]) {
  const code = providerCode.trim().toLowerCase()
  const categories = new Set<ModelCategoryKey>(['text'])
  for (const item of providerModels) categories.add(getModelCategory(item))
  if (code === 'gpt' || code === 'openai' || code === 'hybrid') {
    categories.add('image')
  }
  return modelModeOptions.filter((option) => categories.has(option.value))
}

function cloneServiceTierPrices(value?: Record<string, ProviderModelPriceSet>): Record<string, ProviderModelPriceSet> {
  return Object.fromEntries(Object.entries(value ?? {}).map(([tier, prices]) => [tier, { ...prices }]))
}

export function clearCustomModelPricesOutsideCategory(form: CustomModelForm, category: ModelCategoryKey): void {
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  for (const field of directPriceFieldKeys) {
    if (!visibleFields.has(field)) {
      form[field] = undefined
    }
  }
  if (category !== 'text') form.serviceTierPrices = {}
}

function buildCustomModelDirectPricePayload(form: CustomModelForm, category: ModelCategoryKey) {
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  const payload: Partial<Record<DirectPriceFieldKey, number | null>> = {}
  for (const field of directPriceFieldKeys) {
    payload[field] = !visibleFields.has(field)
      ? null
      : numberToNull(form[field])
  }
  return payload
}

function trimToUndefined(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function trimToNull(value: unknown): string | null {
  return trimToUndefined(value) ?? null
}

function numberToNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function normalizeServiceTiers(value: unknown): ProviderModelServiceTier[] {
  if (!Array.isArray(value)) return []
  return uniqueCapabilityTokens(value)
}

function normalizeReasoningEfforts(value: unknown): ProviderModelReasoningEffort[] {
  if (!Array.isArray(value)) return []
  return uniqueCapabilityTokens<ProviderModelReasoningEffort>(value).filter((effort) => effort !== 'none')
}

function uniqueCapabilityTokens<TValue extends string>(values: unknown[]): TValue[] {
  const output: TValue[] = []
  const seen = new Set<string>()
  for (const value of values) {
    if (typeof value !== 'string') continue
    const normalized = value.trim()
    if (!/^[a-z0-9][a-z0-9._-]{0,63}$/i.test(normalized) || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized as TValue)
  }
  return output
}

function capabilityOptions<TValue extends string>(values: TValue[], label: (value: TValue) => string) {
  return uniqueCapabilityTokens<TValue>(values).map((value) => ({ value, label: label(value) }))
}

function formatServiceTier(value: ProviderModelServiceTier): string {
  if (value === 'priority') return 'Priority'
  if (value === 'flex') return 'Flex'
  return value
}

function formatReasoningEffort(value: ProviderModelReasoningEffort): string {
  if (value === 'none') return '关闭'
  if (value === 'minimal') return 'Minimal'
  if (value === 'low') return 'Low'
  if (value === 'medium') return 'Medium'
  if (value === 'high') return 'High'
  if (value === 'xhigh') return 'XHigh'
  if (value === 'max') return 'Max'
  return value
}
