import { Router } from 'express'
import { z } from 'zod'

import { isAdminRole } from '../../domain/types.js'
import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { listProviders } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope, getRequestAuthContext } from '../auth/request-context.js'
import {
  findCustomProviderModel,
  customProviderModelBindings,
  listProviderModelCatalog,
  removeCustomProviderModel,
  saveCustomProviderModel,
  compareProviderModelCatalogItems,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'

export const providersRouter = Router()

interface ProviderModelOption {
  providerCode: string
  model: string
}

providersRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listProviders()))
})

providersRouter.get('/options', (_req, res) => {
  res.json(ok(listProviders().filter((provider) => provider.enabled)))
})

providersRouter.get('/models/options', (_req, res) => {
  const context = getRequestAuthContext()
  const options = dedupeProviderModelOptions(
    listProviders()
      .filter((provider) => provider.enabled)
      .flatMap((provider) => listProviderModelCatalog({
        providerCode: provider.code,
        systemAccountId: context?.systemAccountId
      }).map((item) => ({
        providerCode: item.providerCode,
        model: item.model
      })))
  )
  res.json(ok(options))
})

providersRouter.get('/:code/models', (req, res) => {
  const context = getRequestAuthContext()
  const access = getRequestAccessScope(req.query.systemAccountId)
  const provider = listProviders().find((item) => item.code === req.params.code)
  if (!provider) {
    res.status(404).json({ message: '供应商不存在' })
    return
  }

  res.json(ok(listProviderModelsForRequest({
    providerCode: provider.code,
    systemAccountId: modelCatalogSystemAccountId(access),
    admin: Boolean(context && isAdminRole(context.role)),
    includeInactive: booleanQueryValue(req.query.includeInactive),
    includeUnpriced: booleanQueryValue(req.query.includeUnpriced)
  })))
})

const nullableTrimmedStringSchema = z.string().trim().nullable().optional()
const nullableDateSchema = z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/).nullable().optional()
const nullableIntegerSchema = z.number().int().min(0).nullable().optional()
const nullableNumberSchema = z.number().min(0).nullable().optional()
const nullableModelModeSchema = z.enum(['text', 'image', 'audio']).nullable().optional()

const customModelSchema = z.object({
  model: z.string().trim().min(1),
  scope: z.enum(['global', 'personal']).default('personal'),
  status: z.enum(['draft', 'active', 'disabled']).optional(),
  mode: nullableModelModeSchema,
  supportedApiProtocols: z.array(z.enum(['chat_completions', 'responses', 'completions', 'images', 'audio', 'realtime'])).optional(),
  pricingModel: nullableTrimmedStringSchema,
  releaseDate: nullableDateSchema,
  shutdownDate: nullableDateSchema,
  contextWindowTokens: nullableIntegerSchema,
  maxOutputTokens: nullableIntegerSchema,
  inputUsdPer1M: nullableNumberSchema,
  outputUsdPer1M: nullableNumberSchema,
  cachedInputUsdPer1M: nullableNumberSchema,
  cacheWriteUsdPer1M: nullableNumberSchema,
  imageInputUsdPer1M: nullableNumberSchema,
  imageOutputUsdPer1M: nullableNumberSchema,
  audioInputUsdPer1M: nullableNumberSchema,
  audioOutputUsdPer1M: nullableNumberSchema,
  outputUsdPerImage: nullableNumberSchema,
  pricingNotes: nullableTrimmedStringSchema,
  capabilityNotes: nullableTrimmedStringSchema,
  notes: nullableTrimmedStringSchema
}).strict()
const customModelPatchSchema = customModelSchema.partial().refine((value) => Object.keys(value).length > 0, {
  message: '请提供要修改的模型内容'
})

providersRouter.post('/:code/models', (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  const provider = listProviders().find((item) => item.code === req.params.code)
  if (!provider) {
    sendNotFound(res, '供应商不存在')
    return
  }
  const parsed = customModelSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('自定义模型参数无效'))
    return
  }
  if (parsed.data.scope === 'global' && !isAdminRole(context.role)) {
    res.status(403).json({ message: '只有管理员可以维护全局模型' })
    return
  }
  const ownerSystemAccountId = parsed.data.scope === 'personal' ? context.systemAccountId : undefined
  const validation = validateCustomModelPricing({
    providerCode: provider.code,
    ownerSystemAccountId,
    input: parsed.data
  })
  if (!validation.success) {
    res.status(400).json(badRequest(validation.message))
    return
  }
  try {
    const saved = saveCustomProviderModel({
      ...parsed.data,
      providerCode: provider.code,
      systemAccountId: ownerSystemAccountId,
      actorSystemAccountId: context.systemAccountId
    })
    res.status(201).json(ok(saved))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '自定义模型保存失败'))
  }
})

providersRouter.patch('/:code/models/:id', (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  const existing = findCustomProviderModel(req.params.id)
  if (!existing || existing.providerCode !== req.params.code) {
    sendNotFound(res, '自定义模型不存在')
    return
  }
  if (!canMutateCustomModel(existing.scope, existing.systemAccountId, context)) {
    res.status(403).json({ message: '无权修改该自定义模型' })
    return
  }
  const parsed = customModelPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('自定义模型参数无效'))
    return
  }
  if (parsed.data.model !== undefined && parsed.data.model.trim() !== existing.model.trim()) {
    res.status(400).json(badRequest('模型 ID 创建后不能修改'))
    return
  }
  const next = {
    ...existing,
    ...parsed.data,
    scope: existing.scope
  }
  const validation = validateCustomModelPricing({
    providerCode: existing.providerCode,
    ownerSystemAccountId: existing.systemAccountId,
    input: next
  })
  if (!validation.success) {
    res.status(400).json(badRequest(validation.message))
    return
  }
  try {
    res.json(ok(saveCustomProviderModel({
      ...next,
      providerCode: existing.providerCode,
      systemAccountId: existing.systemAccountId,
      actorSystemAccountId: context.systemAccountId
    })))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '自定义模型保存失败'))
  }
})

providersRouter.delete('/:code/models/:id', (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  const existing = findCustomProviderModel(req.params.id)
  if (!existing || existing.providerCode !== req.params.code) {
    sendNotFound(res, '自定义模型不存在')
    return
  }
  if (!canMutateCustomModel(existing.scope, existing.systemAccountId, context)) {
    res.status(403).json({ message: '无权删除该自定义模型' })
    return
  }
  const bindings = customProviderModelBindings({
    providerCode: existing.providerCode,
    model: existing.model
  })
  if (bindings.totalAccountCount > 0) {
    res.status(409).json({
      message: customModelBoundToAccountMessage(bindings)
    })
    return
  }
  res.json(ok({ deleted: removeCustomProviderModel(existing.id) }))
})

function dedupeProviderModelOptions(options: ProviderModelOption[]): ProviderModelOption[] {
  const seenModels = new Set<string>()
  const result: ProviderModelOption[] = []
  for (const option of options) {
    const model = option.model.trim()
    if (!model) continue
    const normalizedModel = model.toLowerCase()
    if (seenModels.has(normalizedModel)) continue
    seenModels.add(normalizedModel)
    result.push({ providerCode: option.providerCode, model })
  }
  return result
}

function listProviderModelsForRequest(input: {
  providerCode: string
  systemAccountId?: string
  admin: boolean
  includeInactive?: boolean
  includeUnpriced?: boolean
}): ProviderModelCatalogItem[] {
  const maintenanceView = input.includeInactive === true || input.includeUnpriced === true
  if (!maintenanceView || input.admin || !input.systemAccountId) {
    return listProviderModelCatalog({
      providerCode: input.providerCode,
      systemAccountId: input.systemAccountId,
      includeInactive: input.admin ? input.includeInactive : undefined,
      includeUnpriced: input.admin ? input.includeUnpriced : undefined
    })
  }

  const publicCatalog = listProviderModelCatalog({
    providerCode: input.providerCode
  })
  const personalCatalog = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeInactive: input.includeInactive,
    includeUnpriced: input.includeUnpriced
  }).filter((item) => item.scope === 'personal')
  return mergeProviderModelsForRoute([...publicCatalog, ...personalCatalog])
}

function mergeProviderModelsForRoute(items: ProviderModelCatalogItem[]): ProviderModelCatalogItem[] {
  const merged = new Map<string, ProviderModelCatalogItem>()
  for (const item of items) {
    const key = item.model.trim().toLowerCase()
    if (!key) continue
    const previous = merged.get(key)
    if (!previous || routeModelPriority(item) >= routeModelPriority(previous)) {
      merged.set(key, item)
    }
  }
  return [...merged.values()].sort(compareProviderModelCatalogItems)
}

function routeModelPriority(item: ProviderModelCatalogItem): number {
  if (item.scope === 'personal') return 3
  if (item.scope === 'global') return 2
  return 1
}

function modelCatalogSystemAccountId(access: ReturnType<typeof getRequestAccessScope>): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

function booleanQueryValue(value: unknown): boolean | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  if (typeof raw === 'boolean') return raw
  if (typeof raw !== 'string') return undefined
  const normalized = raw.trim().toLowerCase()
  if (['1', 'true', 'yes'].includes(normalized)) return true
  if (['0', 'false', 'no'].includes(normalized)) return false
  return undefined
}

function canMutateCustomModel(
  scope: 'global' | 'personal',
  ownerSystemAccountId: string | undefined,
  context: { systemAccountId: string; role: string }
): boolean {
  if (isAdminRole(context.role)) return true
  if (scope === 'global') return false
  return ownerSystemAccountId === context.systemAccountId
}

function customModelBoundToAccountMessage(input: {
  supportedModelAccountCount: number
  mappingSourceAccountCount: number
  mappingUpstreamAccountCount: number
  totalAccountCount: number
}): string {
  const details: string[] = []
  if (input.supportedModelAccountCount > 0) {
    details.push(`${input.supportedModelAccountCount} 个账户支持模型`)
  }
  if (input.mappingSourceAccountCount > 0) {
    details.push(`${input.mappingSourceAccountCount} 个账户映射下游模型`)
  }
  if (input.mappingUpstreamAccountCount > 0) {
    details.push(`${input.mappingUpstreamAccountCount} 个账户映射上游模型`)
  }
  return details.length
    ? `模型已绑定 AI 账户，不能删除；请先从${details.join('、')}中移除后再删除`
    : '模型已绑定 AI 账户，不能删除；请先解除账户绑定后再删除'
}

function validateCustomModelPricing(input: {
  providerCode: string
  ownerSystemAccountId?: string
  input: CustomModelPricingInput
}): { success: true } | { success: false; message: string } {
  const status = input.input.status ?? 'active'
  const model = input.input.model?.trim()
  const pricingModel = typeof input.input.pricingModel === 'string' ? input.input.pricingModel.trim() : undefined
  const hasDirectPrice = customInputHasDirectPrice(input.input)
  if (hasDirectPrice && pricingModel) {
    return { success: false, message: '自定义模型不能同时配置直接价格和 pricingModel' }
  }
  if (status === 'active' && !hasDirectPrice && !pricingModel) {
    return { success: false, message: '启用的自定义模型必须配置价格或 pricingModel' }
  }
  if (!pricingModel) {
    return { success: true }
  }
  if (model && model.toLowerCase() === pricingModel.toLowerCase()) {
    return { success: false, message: 'pricingModel 不能指向当前模型自身' }
  }
  const pricingTarget = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.ownerSystemAccountId
  }).find((item) => item.model.toLowerCase() === pricingModel.toLowerCase())
  if (!pricingTarget) {
    return { success: false, message: `pricingModel 不存在：${pricingModel}` }
  }
  if (pricingTarget.pricingModel) {
    return { success: false, message: 'pricingModel 只能指向有直接价格的模型，不能递归指向另一个 pricingModel' }
  }
  if (!customInputHasDirectPrice(pricingTarget)) {
    return { success: false, message: `pricingModel 缺少直接价格：${pricingModel}` }
  }
  return { success: true }
}

type CustomModelStatus = 'draft' | 'active' | 'disabled'
type CustomModelPricingInput = CustomModelPriceFields & {
  model?: string
  pricingModel?: string | null
  supportedApiProtocols?: ProviderModelCatalogItem['supportedApiProtocols']
  status?: CustomModelStatus
}

function customInputHasDirectPrice(input: CustomModelPriceFields): boolean {
  return typeof input.inputUsdPer1M === 'number'
    || typeof input.outputUsdPer1M === 'number'
    || typeof input.cachedInputUsdPer1M === 'number'
    || typeof input.cacheWriteUsdPer1M === 'number'
    || typeof input.imageInputUsdPer1M === 'number'
    || typeof input.imageOutputUsdPer1M === 'number'
    || typeof input.audioInputUsdPer1M === 'number'
    || typeof input.audioOutputUsdPer1M === 'number'
    || typeof input.outputUsdPerImage === 'number'
}

type CustomModelPriceFields = Partial<Record<
  'inputUsdPer1M'
  | 'outputUsdPer1M'
  | 'cachedInputUsdPer1M'
  | 'cacheWriteUsdPer1M'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'outputUsdPerImage',
  number | null
>>
