import type {
  ProviderModelApiProtocol,
  ProviderModelMode,
  ProviderModelPricing,
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
  model: string
  status: ProviderModelStatus
  mode: ProviderModelMode
  supportedApiProtocols: ProviderModelApiProtocol[]
  pricingTemplateModel?: string
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  maxOutputTokens?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
}

export const emptyCustomModelForm: CustomModelForm = {
  model: '',
  status: 'active',
  mode: 'text',
  supportedApiProtocols: ['responses', 'chat_completions']
}

export function createCustomModelFormFromPricing(
  record: ProviderModelPricing,
  providerModels: ProviderModelPricing[]
): CustomModelForm {
  const form: CustomModelForm = {
    id: record.id,
    model: record.model,
    status: record.status ?? 'active',
    mode: categoryFromModeOrModel(record.mode, record.model),
    supportedApiProtocols: [...(record.supportedApiProtocols ?? [])],
    releaseDate: record.releaseDate,
    shutdownDate: record.shutdownDate,
    contextWindowTokens: record.contextWindowTokens,
    maxOutputTokens: record.maxOutputTokens,
    inputUsdPer1M: record.inputUsdPer1M,
    outputUsdPer1M: record.outputUsdPer1M,
    cachedInputUsdPer1M: record.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: record.cacheWriteUsdPer1M,
    imageInputUsdPer1M: record.imageInputUsdPer1M,
    imageOutputUsdPer1M: record.imageOutputUsdPer1M,
    audioInputUsdPer1M: record.audioInputUsdPer1M,
    audioOutputUsdPer1M: record.audioOutputUsdPer1M,
    outputUsdPerImage: record.outputUsdPerImage
  }

  const category = categoryFromModeOrModel(form.mode, form.model)
  clearCustomModelPricesOutsideCategory(form, category)
  if (record.pricingModel) {
    applyPricingTemplateToCustomModelForm(form, providerModels, record.pricingModel)
  }
  return form
}

export function buildCustomModelPayload(
  form: CustomModelForm,
  category: ModelCategoryKey
): ProviderModelUpsertPayload | undefined {
  const model = form.model.trim()
  if (!model) return undefined
  return {
    model,
    status: form.status,
    mode: form.mode,
    supportedApiProtocols: [...form.supportedApiProtocols],
    pricingModel: null,
    releaseDate: trimToNull(form.releaseDate),
    shutdownDate: trimToNull(form.shutdownDate),
    contextWindowTokens: numberToNull(form.contextWindowTokens),
    maxOutputTokens: numberToNull(form.maxOutputTokens),
    ...buildCustomModelDirectPricePayload(form, category)
  }
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
  const normalized = model.trim().toLowerCase()
  return models.find((item) => item.model.trim().toLowerCase() === normalized)
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
