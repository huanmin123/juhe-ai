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
  pricingTemplateModel?: string
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  maxOutputTokens?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
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
    defaultReasoningEffort: normalizedDefaultReasoningEffort(
      record.defaultReasoningEffort,
      record.supportedReasoningEfforts
    ),
    releaseDate: record.releaseDate,
    shutdownDate: record.shutdownDate,
    contextWindowTokens: record.contextWindowTokens,
    maxOutputTokens: record.maxOutputTokens,
    inputUsdPer1M: record.inputUsdPer1M,
    outputUsdPer1M: record.outputUsdPer1M,
    cachedInputUsdPer1M: record.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: record.cacheWriteUsdPer1M,
    cacheWrite1hUsdPer1M: record.cacheWrite1hUsdPer1M,
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
  options: { includeRequestCapabilities?: boolean; includePrices?: boolean } = {}
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
    maxOutputTokens: numberToNull(form.maxOutputTokens)
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
    payload.defaultReasoningEffort = normalizedDefaultReasoningEffort(
      form.defaultReasoningEffort,
      supportedReasoningEfforts
    ) ?? null
  }
  return payload
}

export function clearCustomModelGptCapabilities(form: CustomModelForm): void {
  form.supportedServiceTiers = []
  form.supportedReasoningEfforts = []
  form.defaultReasoningEffort = undefined
}

export function normalizeCustomModelDefaultReasoningEffort(form: CustomModelForm): void {
  form.supportedServiceTiers = normalizeServiceTiers(form.supportedServiceTiers)
  form.supportedReasoningEfforts = normalizeReasoningEfforts(form.supportedReasoningEfforts)
  form.defaultReasoningEffort = normalizedDefaultReasoningEffort(
    form.defaultReasoningEffort,
    form.supportedReasoningEfforts
  )
}

export function applyPricingTemplateToCustomModelForm(
  form: CustomModelForm,
  providerModels: ProviderModelPricing[],
  model?: string
): void {
  const templateModel = trimToUndefined(model)
  if (!templateModel) return
  const template = findProviderModelByName(providerModels, templateModel)
  if (!template) return
  const category = categoryFromModeOrModel(form.mode, form.model)
  if (getModelCategory(template) !== category) return
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  for (const field of directPriceFieldKeys) {
    form[field] = visibleFields.has(field) ? template[field] : undefined
  }
  form.serviceTierPrices = cloneServiceTierPrices(template.serviceTierPrices)
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

function findProviderModelByName(models: ProviderModelPricing[], model: string): ProviderModelPricing | undefined {
  const normalized = model.trim()
  return models.find((item) => item.model.trim() === normalized)
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
  return uniqueCapabilityTokens(value)
}

function normalizedDefaultReasoningEffort(
  value: unknown,
  supportedReasoningEfforts: unknown
): ProviderModelReasoningEffort | undefined {
  const supported = normalizeReasoningEfforts(supportedReasoningEfforts)
  return typeof value === 'string' && supported.includes(value as ProviderModelReasoningEffort)
    ? value as ProviderModelReasoningEffort
    : undefined
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
