import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { listProvidersAsync } from '../../storage/repositories.js'
import type { ProviderDefinition } from '../../domain/types.js'
import {
  listAnthropicProtocolProviderCodesAsync,
  listGeminiProtocolProviderCodesAsync,
  listOpenAIProtocolProviderCodesAsync
} from '../../storage/provider.repository.js'
import { isHybridProviderCode } from '../../domain/provider-protocol.js'
import {
  clearProviderDefaultTestModelPreferenceIfModelAsync,
  listProviderDefaultTestModelPreferencesAsync,
  upsertProviderDefaultTestModelPreferenceAsync
} from '../../storage/provider-default-test-model.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope, getRequestAuthContext, type RequestAccessScope } from '../auth/request-context.js'
import {
  findCustomProviderModelAsync,
  customProviderModelBindingsAsync,
  listProviderModelCatalogAsync,
  removeCustomProviderModelAsync,
  saveCustomProviderModelAsync,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'
import type { ProviderModelApiProtocol } from '../model-pricing/provider-driver.types.js'

export const providersRouter = Router()

interface ProviderModelOption {
  providerCode: string
  model: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

providersRouter.get('/', requireAdmin, async (_req, res, next) => {
  try {
    const context = getRequestAuthContext()
    res.json(ok(await listProvidersForRequestAsync(context?.systemAccountId)))
  } catch (error) {
    next(error)
  }
})

providersRouter.get('/options', async (_req, res, next) => {
  try {
    const context = getRequestAuthContext()
    res.json(ok((await listProvidersForRequestAsync(context?.systemAccountId)).filter((provider) => provider.enabled)))
  } catch (error) {
    next(error)
  }
})

providersRouter.get('/models/options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = providerModelRequestSystemAccountId(access)
    const providerCodes = await providerModelOptionProviderCodesAsync(req.query.protocol)
    const providers = (await listProvidersAsync()).filter((provider) => provider.enabled && providerCodes.has(provider.code))
    const catalogs = await Promise.all(providers.map((provider) => listProviderModelCatalogAsync({
      providerCode: provider.code,
      systemAccountId,
      includeUnpriced: true
    })))
    const options = dedupeProviderModelOptions(
      catalogs.flatMap((catalog) => catalog.map((item) => ({
        providerCode: item.providerCode,
        model: item.model,
        supportedApiProtocols: item.supportedApiProtocols
      })))
    )
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

async function providerModelOptionProviderCodesAsync(protocol: unknown): Promise<Set<string>> {
  const value = Array.isArray(protocol) ? protocol[0] : protocol
  if (value === 'openai') {
    return new Set(await listOpenAIProtocolProviderCodesAsync())
  }
  if (value === 'anthropic') {
    return new Set(await listAnthropicProtocolProviderCodesAsync())
  }
  if (value === 'gemini') {
    return new Set(await listGeminiProtocolProviderCodesAsync())
  }
  return new Set((await listProvidersAsync())
    .filter((provider) => provider.enabled && !isHybridProviderCode(provider.code))
    .map((provider) => provider.code))
}

providersRouter.get('/:code/models', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const provider = (await listProvidersAsync()).find((item) => item.code === req.params.code)
    if (!provider) {
      res.status(404).json({ message: '供应商不存在' })
      return
    }

    res.json(ok(await listProviderModelsForRequestAsync({
      providerCode: provider.code,
      systemAccountId: providerModelRequestSystemAccountId(access),
      includeInactive: booleanQueryValue(req.query.includeInactive),
      includeUnpriced: booleanQueryValue(req.query.includeUnpriced)
    })))
  } catch (error) {
    next(error)
  }
})

const defaultTestModelSchema = z.object({
  model: z.string().trim().min(1)
}).strict()

providersRouter.put('/:code/default-test-model', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const provider = (await listProvidersAsync()).find((item) => item.code === req.params.code)
    if (!provider) {
      sendNotFound(res, '供应商不存在')
      return
    }
    const parsed = defaultTestModelSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('默认测试模型参数无效'))
      return
    }
    const model = parsed.data.model
    const validation = await validateDefaultTestModelSelection({
      providerCode: provider.code,
      systemAccountId: context.systemAccountId,
      model
    })
    if (!validation.success) {
      res.status(400).json(badRequest(validation.message))
      return
    }
    const saved = await upsertProviderDefaultTestModelPreferenceAsync({
      providerCode: provider.code,
      systemAccountId: context.systemAccountId,
      model: validation.model
    })
    res.json(ok({
      providerCode: saved.providerCode,
      defaultTestModel: saved.model
    }))
  } catch (error) {
    next(error)
  }
})

const nullableTrimmedStringSchema = z.string().trim().nullable().optional()
const nullableDateSchema = z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/).nullable().optional()
const nullableIntegerSchema = z.number().int().min(0).nullable().optional()
const nullableNumberSchema = z.number().min(0).nullable().optional()
const nullableModelModeSchema = z.enum(['text', 'image', 'audio']).nullable().optional()

const customModelSchema = z.object({
  model: z.string().trim().min(1),
  status: z.enum(['draft', 'active', 'disabled']).optional(),
  mode: nullableModelModeSchema,
  supportedApiProtocols: z.array(z.enum([
    'chat_completions',
    'responses',
    'messages',
    'message_token_counting',
    'generate_content',
    'stream_generate_content',
    'count_tokens',
    'embed_content',
    'completions',
    'images',
    'audio',
    'realtime'
  ])).optional(),
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

providersRouter.post('/:code/models', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const provider = (await listProvidersAsync()).find((item) => item.code === req.params.code)
    if (!provider) {
      sendNotFound(res, '供应商不存在')
      return
    }
    const parsed = customModelSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('自定义模型参数无效'))
      return
    }
    const ownerSystemAccountId = context.systemAccountId
    const validation = await validateCustomModelPricing({
      providerCode: provider.code,
      ownerSystemAccountId,
      input: parsed.data
    })
    if (!validation.success) {
      res.status(400).json(badRequest(validation.message))
      return
    }
    try {
      const saved = await saveCustomProviderModelAsync({
        ...parsed.data,
        providerCode: provider.code,
        systemAccountId: ownerSystemAccountId,
        actorSystemAccountId: context.systemAccountId
      })
      res.status(201).json(ok(saved))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '自定义模型保存失败'))
    }
  } catch (error) {
    next(error)
  }
})

providersRouter.patch('/:code/models/:id', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const existing = await findCustomProviderModelAsync(req.params.id)
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
    const validation = await validateCustomModelPricing({
      providerCode: existing.providerCode,
      ownerSystemAccountId: existing.systemAccountId,
      input: next
    })
    if (!validation.success) {
      res.status(400).json(badRequest(validation.message))
      return
    }
    try {
      const saved = await saveCustomProviderModelAsync({
        ...next,
        providerCode: existing.providerCode,
        systemAccountId: existing.systemAccountId,
        actorSystemAccountId: context.systemAccountId
      })
      if (saved.status !== 'active') {
        await clearProviderDefaultTestModelPreferenceIfModelAsync({
          providerCode: saved.providerCode,
          systemAccountId: saved.systemAccountId,
          model: saved.model
        })
      }
      res.json(ok(saved))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '自定义模型保存失败'))
    }
  } catch (error) {
    next(error)
  }
})

providersRouter.delete('/:code/models/:id', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const existing = await findCustomProviderModelAsync(req.params.id)
    if (!existing || existing.providerCode !== req.params.code) {
      sendNotFound(res, '自定义模型不存在')
      return
    }
    if (!canMutateCustomModel(existing.scope, existing.systemAccountId, context)) {
      res.status(403).json({ message: '无权删除该自定义模型' })
      return
    }
    const bindings = await customProviderModelBindingsAsync({
      providerCode: existing.providerCode,
      model: existing.model,
      scope: existing.scope,
      systemAccountId: existing.systemAccountId
    })
    if (bindings.totalAccountCount > 0) {
      res.status(409).json({
        message: customModelBoundToAccountMessage(bindings)
      })
      return
    }
    const deleted = await removeCustomProviderModelAsync(existing.id)
    if (deleted) {
      await clearProviderDefaultTestModelPreferenceIfModelAsync({
        providerCode: existing.providerCode,
        systemAccountId: existing.systemAccountId,
        model: existing.model
      })
    }
    res.json(ok({ deleted }))
  } catch (error) {
    next(error)
  }
})

function providerModelRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

async function listProvidersForRequestAsync(systemAccountId?: string): Promise<ProviderDefinition[]> {
  const providers = await listProvidersAsync()
  const preferences = await listProviderDefaultTestModelPreferencesAsync(
    systemAccountId,
    providers.map((provider) => provider.code)
  )
  if (!preferences.size) return providers
  return providers.map((provider) => providerWithDefaultTestModelPreference(provider, preferences.get(provider.code)))
}

function providerWithDefaultTestModelPreference(provider: ProviderDefinition, preferredModel?: string): ProviderDefinition {
  const model = preferredModel?.trim()
  if (!model) return provider
  return {
    ...provider,
    defaultTestModel: model,
    protocolProfiles: provider.protocolProfiles.map((profile) => (
      profile.id === provider.defaultProtocolProfileId
        ? { ...profile, defaultTestModel: model }
        : profile
    ))
  }
}

export function dedupeProviderModelOptions(options: ProviderModelOption[]): ProviderModelOption[] {
  const seenProviderModels = new Map<string, ProviderModelOption>()
  const result: ProviderModelOption[] = []
  for (const option of options) {
    const providerCode = option.providerCode.trim()
    const model = option.model.trim()
    if (!providerCode || !model) continue
    const normalizedProviderCode = providerCode.toLowerCase()
    const providerModelKey = `${normalizedProviderCode}\n${model}`
    const existing = seenProviderModels.get(providerModelKey)
    const supportedApiProtocols = normalizedProviderModelApiProtocols([
      ...(existing?.supportedApiProtocols ?? []),
      ...(option.supportedApiProtocols ?? [])
    ])
    if (existing) {
      existing.supportedApiProtocols = supportedApiProtocols
      continue
    }
    const item: ProviderModelOption = supportedApiProtocols.length
      ? { providerCode, model, supportedApiProtocols }
      : { providerCode, model }
    seenProviderModels.set(providerModelKey, item)
    result.push(item)
  }
  return result
}

function normalizedProviderModelApiProtocols(value: readonly ProviderModelApiProtocol[]): ProviderModelApiProtocol[] {
  const seen = new Set<ProviderModelApiProtocol>()
  const output: ProviderModelApiProtocol[] = []
  for (const item of value) {
    if (!item || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output
}

async function listProviderModelsForRequestAsync(input: {
  providerCode: string
  systemAccountId?: string
  includeInactive?: boolean
  includeUnpriced?: boolean
}): Promise<ProviderModelCatalogItem[]> {
  if (isHybridProviderCode(input.providerCode)) {
    const providers = (await listProvidersAsync()).filter((provider) => provider.enabled && !isHybridProviderCode(provider.code))
    return (await Promise.all(providers.map((provider) => listProviderModelCatalogAsync({
      providerCode: provider.code,
      systemAccountId: input.systemAccountId,
      includeInactive: input.includeInactive,
      includeUnpriced: input.includeUnpriced
    })))).flat()
  }
  return listProviderModelCatalogAsync({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeInactive: input.includeInactive,
    includeUnpriced: input.includeUnpriced
  })
}

async function validateDefaultTestModelSelection(input: {
  providerCode: string
  systemAccountId: string
  model: string
}): Promise<{ success: true; model: string } | { success: false; message: string }> {
  const model = input.model.trim()
  const catalog = await listProviderModelsForRequestAsync({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeInactive: true,
    includeUnpriced: true
  })
  const item = catalog.find((entry) => entry.model.trim() === model)
  if (!item) {
    return { success: false, message: `模型不在当前用户可见目录中：${model}` }
  }
  if ((item.status ?? 'active') !== 'active') {
    return { success: false, message: '只能把启用模型设置为默认测试模型' }
  }
  if (!isProviderModelUsableForAccountTest(item)) {
    return { success: false, message: '默认测试模型只能选择文本生成模型' }
  }
  return { success: true, model: item.model }
}

function isProviderModelUsableForAccountTest(item: ProviderModelCatalogItem): boolean {
  if (item.mode === 'image' || item.mode === 'audio') return false
  const protocols = item.supportedApiProtocols ?? []
  if (!protocols.length) return true
  return protocols.some((protocol) => [
    'chat_completions',
    'responses',
    'messages',
    'generate_content',
    'stream_generate_content'
  ].includes(protocol))
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
  scope: 'personal',
  ownerSystemAccountId: string | undefined,
  context: { systemAccountId: string; role: string }
): boolean {
  return scope === 'personal' && ownerSystemAccountId === context.systemAccountId
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

async function validateCustomModelPricing(input: {
  providerCode: string
  ownerSystemAccountId?: string
  input: CustomModelPricingInput
}): Promise<{ success: true } | { success: false; message: string }> {
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
  if (model && model === pricingModel) {
    return { success: false, message: 'pricingModel 不能指向当前模型自身' }
  }
  const pricingTarget = (await listProviderModelCatalogAsync({
    providerCode: input.providerCode,
    systemAccountId: input.ownerSystemAccountId
  })).find((item) => item.model === pricingModel)
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
