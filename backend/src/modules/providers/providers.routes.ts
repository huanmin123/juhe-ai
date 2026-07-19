import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { listProvidersAsync } from '../../storage/repositories.js'
import { isAdminRole, type ProviderDefinition, type ProviderModelPricing } from '../../domain/types.js'
import {
  listAnthropicProtocolProviderCodesAsync,
  listGeminiProtocolProviderCodesAsync,
  listOpenAIProtocolProviderCodesAsync
} from '../../storage/provider.repository.js'
import { isHybridProviderCode } from '../../domain/provider-protocol.js'
import {
  clearProviderDefaultHealthCheckModelPreferenceIfModelAsync,
  listProviderDefaultHealthCheckModelPreferencesAsync,
  upsertProviderDefaultHealthCheckModelPreferenceAsync
} from '../../storage/provider-default-health-check-model.repository.js'
import {
  clearProviderSystemDefaultHealthCheckModelIfModelAsync,
  listProviderSystemDefaultHealthCheckModelsAsync,
  upsertProviderSystemDefaultHealthCheckModelAsync
} from '../../storage/provider-system-default-health-check-model.repository.js'
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
import {
  findBuiltInProviderModelByIdAsync,
  updateBuiltInProviderModelConfigurationAsync
} from '../../storage/provider-model-catalog.repository.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'
import { createPageDataDomainReadCache, pageDataReadCacheKey } from '../page-data/page-data-read-cache.service.js'
import { publishPageDataDomainReset } from '../page-data/page-data-change.publisher.js'

export const providersRouter = Router()

const providerOptionsReadCache = createPageDataDomainReadCache<ProviderDefinition[]>('providers.catalog', {
  max: 128,
  ttlMs: 24 * 60 * 60 * 1000
})
const providerModelOptionsReadCache = createPageDataDomainReadCache<ProviderModelOption[]>('providers.catalog', {
  max: 128,
  ttlMs: 24 * 60 * 60 * 1000
})

interface ProviderModelOption {
  providerCode: string
  model: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
  supportedServiceTiers?: string[]
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort: string | null
}

type ProviderModelOptionInput = Omit<ProviderModelOption, 'defaultReasoningEffort'> & {
  defaultReasoningEffort?: string | null
}

providersRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const providers = await providerOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/providers',
      query: { enabledOnly: false }
    }), () => listProvidersForRequestAsync())
    res.json(ok(providers))
  } catch (error) {
    next(error)
  }
})

providersRouter.get('/options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = providerModelRequestSystemAccountId(access)
    const providers = await providerOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/providers/options',
      query: { systemAccountId }
    }), async () => (await listProvidersForRequestAsync(systemAccountId)).filter((provider) => provider.enabled))
    res.json(ok(providers))
  } catch (error) {
    next(error)
  }
})

providersRouter.get('/models/options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const systemAccountId = providerModelRequestSystemAccountId(access)
    const protocol = Array.isArray(req.query.protocol) ? req.query.protocol[0] : req.query.protocol
    const options = await providerModelOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/providers/models/options',
      query: { protocol, systemAccountId }
    }), async () => {
      const providerCodes = await providerModelOptionProviderCodesAsync(protocol)
      const providers = (await listProvidersAsync()).filter((provider) => provider.enabled && providerCodes.has(provider.code))
      const catalogs = await Promise.all(providers.map((provider) => listProviderModelCatalogAsync({
        providerCode: provider.code,
        systemAccountId,
        includeUnpriced: true
      })))
      return dedupeProviderModelOptions(
        catalogs.flatMap((catalog) => catalog.map((item) => ({
          providerCode: item.providerCode,
          model: item.model,
          supportedApiProtocols: item.supportedApiProtocols,
          supportedServiceTiers: item.supportedServiceTiers,
          supportedReasoningEfforts: item.supportedReasoningEfforts,
          defaultReasoningEffort: item.defaultReasoningEffort
        })))
      )
    })
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

const defaultHealthCheckModelSchema = z.object({
  model: z.string().trim().min(1)
}).strict()

providersRouter.put('/:code/default-health-check-model', async (req, res, next) => {
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
    const parsed = defaultHealthCheckModelSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('默认检查模型参数无效'))
      return
    }
    const saveAsSystemDefault = isAdminRole(context.role)
    const access = getRequestAccessScope(req.query.systemAccountId)
    const targetSystemAccountId = saveAsSystemDefault
      ? undefined
      : providerModelRequestSystemAccountId(access)
    if (!saveAsSystemDefault && !targetSystemAccountId) {
      res.status(400).json(badRequest('请选择要设置默认检查模型的系统账户'))
      return
    }
    const model = parsed.data.model
    const validation = await validateDefaultHealthCheckModelSelection({
      providerCode: provider.code,
      systemAccountId: targetSystemAccountId,
      model
    })
    if (!validation.success) {
      res.status(400).json(badRequest(validation.message))
      return
    }
    const saved = saveAsSystemDefault
      ? await upsertProviderSystemDefaultHealthCheckModelAsync({
          providerCode: provider.code,
          model: validation.model
        })
      : await upsertProviderDefaultHealthCheckModelPreferenceAsync({
          providerCode: provider.code,
          systemAccountId: targetSystemAccountId!,
          model: validation.model
        })
    const affectedOwnerSystemAccountIds = targetSystemAccountId ? [targetSystemAccountId] : []
    const allScopes = affectedOwnerSystemAccountIds.length === 0
    await Promise.all([
      publishPageDataDomainReset('providers.catalog', affectedOwnerSystemAccountIds, allScopes),
      publishPageDataDomainReset('accounts.options', affectedOwnerSystemAccountIds, allScopes)
    ])
    res.json(ok({
      providerCode: saved.providerCode,
      defaultHealthCheckModel: saved.model
    }))
  } catch (error) {
    next(error)
  }
})

const nullableTrimmedStringSchema = z.string().trim().nullable().optional()
const nullableDateSchema = z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/).nullable().optional()
const nullableIntegerSchema = z.number().int().min(0).nullable().optional()
const nullableNumberSchema = z.number().min(0).nullable().optional()
const modelPriceSetSchema = z.object({
  inputUsdPer1M: nullableNumberSchema,
  outputUsdPer1M: nullableNumberSchema,
  cachedInputUsdPer1M: nullableNumberSchema,
  cacheWriteUsdPer1M: nullableNumberSchema,
  cacheWrite1hUsdPer1M: nullableNumberSchema,
  imageInputUsdPer1M: nullableNumberSchema,
  imageOutputUsdPer1M: nullableNumberSchema,
  audioInputUsdPer1M: nullableNumberSchema,
  audioOutputUsdPer1M: nullableNumberSchema,
  outputUsdPerImage: nullableNumberSchema
}).strict()
const serviceTierPricesSchema = z.record(z.string().trim().min(1).max(64), modelPriceSetSchema).nullable().optional()
const nullableModelModeSchema = z.enum(['text', 'image', 'audio']).nullable().optional()
const customModelCapabilityTokenSchema = z.string().regex(/^[a-z0-9][a-z0-9._-]{0,63}$/i)

const customModelSchema = z.object({
  configurationTemplateId: z.string().trim().min(1).optional(),
  scope: z.enum(['personal', 'global']).optional(),
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
    'interactions',
    'completions',
    'images',
    'audio',
    'realtime'
  ])).optional(),
  supportedServiceTiers: z.array(customModelCapabilityTokenSchema).max(16).optional(),
  supportedReasoningEfforts: z.array(customModelCapabilityTokenSchema).max(16).optional(),
  defaultReasoningEffort: nullableTrimmedStringSchema,
  releaseDate: nullableDateSchema,
  shutdownDate: nullableDateSchema,
  contextWindowTokens: nullableIntegerSchema,
  maxInputTokens: nullableIntegerSchema,
  maxOutputTokens: nullableIntegerSchema,
  inputUsdPer1M: nullableNumberSchema,
  outputUsdPer1M: nullableNumberSchema,
  cachedInputUsdPer1M: nullableNumberSchema,
  cacheWriteUsdPer1M: nullableNumberSchema,
  cacheWrite1hUsdPer1M: nullableNumberSchema,
  serviceTierPrices: serviceTierPricesSchema,
  imageInputUsdPer1M: nullableNumberSchema,
  imageOutputUsdPer1M: nullableNumberSchema,
  audioInputUsdPer1M: nullableNumberSchema,
  audioOutputUsdPer1M: nullableNumberSchema,
  outputUsdPerImage: nullableNumberSchema,
  pricingNotes: nullableTrimmedStringSchema,
  capabilityNotes: nullableTrimmedStringSchema,
  notes: nullableTrimmedStringSchema
}).strict()
const customModelPatchSchema = customModelSchema.omit({ configurationTemplateId: true }).partial().refine((value) => Object.keys(value).length > 0, {
  message: '请提供要修改的模型内容'
})
const builtInModelPatchSchema = customModelSchema.omit({
  configurationTemplateId: true,
  scope: true,
  model: true,
  status: true,
  pricingNotes: true,
  capabilityNotes: true,
  notes: true
}).extend({ status: z.enum(['active', 'disabled']).optional() }).partial().refine((value) => Object.keys(value).length > 0, {
  message: '请提供有效的内置模型配置'
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
    const scope = parsed.data.scope ?? 'personal'
    if (scope === 'global' && !isAdminRole(context.role)) {
      res.status(403).json({ message: '只有管理员可以创建全局模型' })
      return
    }
    const access = getRequestAccessScope(req.query.systemAccountId)
    const ownerSystemAccountId = scope === 'global'
      ? undefined
      : providerModelRequestSystemAccountId(access)
    if (scope === 'personal' && !ownerSystemAccountId) {
      res.status(400).json(badRequest('请选择模型归属的系统账户'))
      return
    }
    const { configurationTemplateId, ...submitted } = parsed.data
    let inherited: Partial<typeof submitted> = {}
    if (configurationTemplateId) {
      const template = (await listProviderModelCatalogAsync({
        providerCode: provider.code,
        systemAccountId: ownerSystemAccountId,
        includeInactive: true,
        includeUnpriced: true
      })).find((item) => item.id === configurationTemplateId && item.status === 'active')
      if (!template) {
        res.status(400).json(badRequest('配置模板不可用'))
        return
      }
      inherited = customModelInputFromConfigurationTemplate(template)
    }
    const effectiveInput = { ...inherited, ...submitted, defaultReasoningEffort: null }
    const validation = await validateCustomModelPricing({
      providerCode: provider.code,
      ownerSystemAccountId,
      input: effectiveInput
    })
    if (!validation.success) {
      res.status(400).json(badRequest(validation.message))
      return
    }
    // 自定义模型是中转目录，不替上游选择 reasoning 默认值。
    const saveInput = effectiveInput
    try {
      const saved = await saveCustomProviderModelAsync({
        ...saveInput,
        scope,
        providerCode: provider.code,
        systemAccountId: ownerSystemAccountId,
        actorSystemAccountId: context.systemAccountId
      })
      await publishProviderModelPageDataReset(saved)
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
    const builtIn = await findBuiltInProviderModelByIdAsync(req.params.id)
    if (builtIn) {
      if (builtIn.providerCode !== req.params.code) {
        sendNotFound(res, '模型不存在')
        return
      }
      if (!isAdminRole(context.role)) {
        res.status(403).json({ message: '只有管理员可以维护内置模型配置' })
        return
      }
      const parsedConfiguration = builtInModelPatchSchema.safeParse(req.body)
      if (!parsedConfiguration.success) {
        res.status(400).json(badRequest('内置模型配置参数无效'))
        return
      }
      const configurationPatch = builtIn.providerCode === 'gpt'
        ? { ...parsedConfiguration.data, defaultReasoningEffort: null }
        : parsedConfiguration.data
      const next = { ...builtIn, ...configurationPatch }
      const capabilityMessage = validateCustomModelCapabilities(builtIn.providerCode, next)
      if (capabilityMessage) {
        res.status(400).json(badRequest(capabilityMessage))
        return
      }
      const saved = await updateBuiltInProviderModelConfigurationAsync(builtIn.id, configurationPatch)
      if (!saved) {
        sendNotFound(res, '模型不存在')
        return
      }
      await recordOperationLogAsync({
        module: 'providers', action: 'update_model_configuration', operationKey: 'providers.update_model_configuration',
        resourceType: 'provider_model', resourceId: saved.id, resourceName: saved.model,
        summary: `更新模型配置：${saved.model}`, detailLevel: 'full', visibilityScope: 'admin_only',
        changes: [safeChange('configuration', '模型配置', providerModelConfigurationSnapshot(builtIn), providerModelConfigurationSnapshot(saved))]
      }, req)
      await publishProviderModelPageDataReset({ scope: 'global' })
      res.json(ok(saved))
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
      defaultReasoningEffort: null,
      scope: existing.scope
    }
    const validation = await validateCustomModelPricing({
      providerCode: existing.providerCode,
      ownerSystemAccountId: existing.scope === 'global' ? undefined : existing.systemAccountId,
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
        await clearProviderDefaultHealthCheckModelPreferenceIfModelAsync({
          providerCode: saved.providerCode,
          systemAccountId: saved.scope === 'global' ? undefined : saved.systemAccountId,
          model: saved.model
        })
        if (saved.scope === 'global') {
          await clearProviderSystemDefaultHealthCheckModelIfModelAsync({
            providerCode: saved.providerCode,
            model: saved.model
          })
        }
      }
      await publishProviderModelPageDataReset(saved)
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
      await clearProviderDefaultHealthCheckModelPreferenceIfModelAsync({
        providerCode: existing.providerCode,
        systemAccountId: existing.scope === 'global' ? undefined : existing.systemAccountId,
        model: existing.model
      })
      if (existing.scope === 'global') {
        await clearProviderSystemDefaultHealthCheckModelIfModelAsync({
          providerCode: existing.providerCode,
          model: existing.model
        })
      }
      await publishProviderModelPageDataReset(existing)
    }
    res.json(ok({ deleted }))
  } catch (error) {
    next(error)
  }
})

function providerModelRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId ?? access?.systemAccountId
}

async function publishProviderModelPageDataReset(model: { scope?: string; systemAccountId?: string }): Promise<void> {
  const ownerSystemAccountIds = model.scope === 'personal' && model.systemAccountId ? [model.systemAccountId] : []
  const allScopes = ownerSystemAccountIds.length === 0
  await Promise.all([
    publishPageDataDomainReset('providers.catalog', ownerSystemAccountIds, allScopes),
    publishPageDataDomainReset('accounts.options', ownerSystemAccountIds, allScopes)
  ])
}

async function listProvidersForRequestAsync(systemAccountId?: string): Promise<ProviderDefinition[]> {
  const providers = await listProvidersAsync()
  const providerCodes = providers.map((provider) => provider.code)
  const [preferences, systemDefaults] = await Promise.all([
    listProviderDefaultHealthCheckModelPreferencesAsync(systemAccountId, providerCodes),
    listProviderSystemDefaultHealthCheckModelsAsync(providerCodes)
  ])
  return providers.map((provider) => providerWithDefaultHealthCheckModelPreference(
    provider,
    preferences.get(provider.code),
    systemDefaults.get(provider.code)
  ))
}

function providerWithDefaultHealthCheckModelPreference(
  provider: ProviderDefinition,
  preferredModel?: string,
  configuredSystemDefaultModel?: string
): ProviderDefinition {
  const personalModel = preferredModel?.trim()
  const systemModel = configuredSystemDefaultModel?.trim()
  return {
    ...provider,
    defaultHealthCheckModel: personalModel || systemModel || provider.defaultHealthCheckModel,
    systemDefaultHealthCheckModel: systemModel
  }
}

export function dedupeProviderModelOptions(options: ProviderModelOptionInput[]): ProviderModelOption[] {
  const seenProviderModels = new Map<string, number>()
  const result: ProviderModelOption[] = []
  const defaultReasoningEffortCandidates: string[][] = []
  for (const option of options) {
    const providerCode = option.providerCode.trim()
    const model = option.model.trim()
    if (!providerCode || !model) continue
    const normalizedProviderCode = providerCode.toLowerCase()
    const providerModelKey = `${normalizedProviderCode}\n${model}`
    const supportedApiProtocols = normalizedProviderModelApiProtocols(option.supportedApiProtocols ?? [])
    const supportedServiceTiers = normalizedProviderModelCapabilities(
      option.supportedServiceTiers ?? [],
      providerModelCapabilityToken
    )
    const supportedReasoningEfforts = normalizedProviderModelCapabilities(
      option.supportedReasoningEfforts ?? [],
      providerModelCapabilityToken
    )
    const defaultReasoningEffort = normalizedProviderModelCapability(
      option.defaultReasoningEffort,
      providerModelCapabilityToken
    )
    const existingIndex = seenProviderModels.get(providerModelKey)
    if (existingIndex !== undefined) {
      const existing = result[existingIndex]
      assignProviderModelOptionCapabilities(existing, {
        supportedApiProtocols: normalizedProviderModelApiProtocols([
          ...(existing.supportedApiProtocols ?? []),
          ...supportedApiProtocols
        ]),
        supportedServiceTiers: normalizedProviderModelCapabilities([
          ...(existing.supportedServiceTiers ?? []),
          ...supportedServiceTiers
        ], providerModelCapabilityToken),
        supportedReasoningEfforts: normalizedProviderModelCapabilities([
          ...(existing.supportedReasoningEfforts ?? []),
          ...supportedReasoningEfforts
        ], providerModelCapabilityToken)
      })
      if (defaultReasoningEffort) {
        defaultReasoningEffortCandidates[existingIndex].push(defaultReasoningEffort)
      }
      continue
    }
    const item: ProviderModelOption = { providerCode, model, defaultReasoningEffort: null }
    assignProviderModelOptionCapabilities(item, {
      supportedApiProtocols,
      supportedServiceTiers,
      supportedReasoningEfforts
    })
    seenProviderModels.set(providerModelKey, result.length)
    result.push(item)
    defaultReasoningEffortCandidates.push(defaultReasoningEffort ? [defaultReasoningEffort] : [])
  }
  for (let index = 0; index < result.length; index += 1) {
    const supportedReasoningEfforts = new Set(result[index].supportedReasoningEfforts ?? [])
    const defaultReasoningEffort = defaultReasoningEffortCandidates[index]
      .find((candidate) => supportedReasoningEfforts.has(candidate))
    if (defaultReasoningEffort) {
      result[index].defaultReasoningEffort = defaultReasoningEffort
    }
  }
  return result
}

function normalizedProviderModelApiProtocols(value: readonly ProviderModelApiProtocol[]): ProviderModelApiProtocol[] {
  const seen = new Set<ProviderModelApiProtocol>()
  const output: ProviderModelApiProtocol[] = []
  for (const item of value) {
    const normalized = item.trim() as ProviderModelApiProtocol
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

const providerModelCapabilityToken = {
  has(value: string): boolean {
    return /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value)
  }
} as ReadonlySet<string>

function normalizedProviderModelCapabilities<TValue extends string>(
  value: readonly TValue[],
  allowedValues: ReadonlySet<TValue>
): TValue[] {
  const seen = new Set<TValue>()
  const output: TValue[] = []
  for (const item of value) {
    const normalized = item.trim() as TValue
    if (!allowedValues.has(normalized) || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function normalizedProviderModelCapability<TValue extends string>(
  value: TValue | null | undefined,
  allowedValues: ReadonlySet<TValue>
): TValue | undefined {
  const normalized = value?.trim() as TValue | undefined
  return normalized && allowedValues.has(normalized) ? normalized : undefined
}

function assignProviderModelOptionCapabilities(
  option: ProviderModelOption,
  capabilities: Pick<
    ProviderModelOption,
    'supportedApiProtocols' | 'supportedServiceTiers' | 'supportedReasoningEfforts'
  >
): void {
  if (capabilities.supportedApiProtocols?.length) {
    option.supportedApiProtocols = capabilities.supportedApiProtocols
  } else {
    delete option.supportedApiProtocols
  }
  if (capabilities.supportedServiceTiers?.length) {
    option.supportedServiceTiers = capabilities.supportedServiceTiers
  } else {
    delete option.supportedServiceTiers
  }
  if (capabilities.supportedReasoningEfforts?.length) {
    option.supportedReasoningEfforts = capabilities.supportedReasoningEfforts
  } else {
    delete option.supportedReasoningEfforts
  }
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

async function validateDefaultHealthCheckModelSelection(input: {
  providerCode: string
  systemAccountId?: string
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
    return { success: false, message: '只能把启用模型设置为默认检查模型' }
  }
  if (!isProviderModelUsableForAccountTest(item)) {
    return { success: false, message: '默认检查模型只能选择文本生成模型' }
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
  scope: 'global' | 'personal',
  ownerSystemAccountId: string | undefined,
  context: { systemAccountId: string; role: string }
): boolean {
  if (scope === 'global') return isAdminRole(context.role)
  return ownerSystemAccountId === context.systemAccountId || isAdminRole(context.role)
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

function customModelInputFromConfigurationTemplate(template: ProviderModelCatalogItem) {
  return {
    mode: customModelModeFromCatalog(template),
    supportedApiProtocols: [...(template.supportedApiProtocols ?? [])],
    supportedServiceTiers: [...(template.supportedServiceTiers ?? [])],
    supportedReasoningEfforts: [...(template.supportedReasoningEfforts ?? [])],
    defaultReasoningEffort: null,
    releaseDate: template.releaseDate ?? null,
    shutdownDate: template.shutdownDate ?? null,
    contextWindowTokens: template.contextWindowTokens ?? null,
    maxInputTokens: template.maxInputTokens ?? null,
    maxOutputTokens: template.maxOutputTokens ?? null,
    inputUsdPer1M: template.inputUsdPer1M ?? null,
    outputUsdPer1M: template.outputUsdPer1M ?? null,
    cachedInputUsdPer1M: template.cachedInputUsdPer1M ?? null,
    cacheWriteUsdPer1M: template.cacheWriteUsdPer1M ?? null,
    cacheWrite1hUsdPer1M: template.cacheWrite1hUsdPer1M ?? null,
    serviceTierPrices: structuredClone(template.serviceTierPrices ?? {}),
    imageInputUsdPer1M: template.imageInputUsdPer1M ?? null,
    imageOutputUsdPer1M: template.imageOutputUsdPer1M ?? null,
    audioInputUsdPer1M: template.audioInputUsdPer1M ?? null,
    audioOutputUsdPer1M: template.audioOutputUsdPer1M ?? null,
    outputUsdPerImage: template.outputUsdPerImage ?? null,
    pricingNotes: template.pricingNotes ?? null,
    capabilityNotes: template.capabilityNotes ?? null,
    notes: template.notes ?? null
  }
}

function customModelModeFromCatalog(template: ProviderModelCatalogItem): 'text' | 'image' | 'audio' {
  if (template.mode === 'image' || template.mode === 'audio') return template.mode
  if (template.supportedApiProtocols.includes('images')) return 'image'
  if (template.supportedApiProtocols.includes('audio')) return 'audio'
  return 'text'
}

async function validateCustomModelPricing(input: {
  providerCode: string
  ownerSystemAccountId?: string
  input: CustomModelPricingInput
}): Promise<{ success: true } | { success: false; message: string }> {
  const status = input.input.status ?? 'active'
  const hasDirectPrice = customInputHasDirectPrice(input.input)
  const capabilityValidationMessage = validateCustomModelCapabilities(input.providerCode, input.input)
  if (capabilityValidationMessage) {
    return { success: false, message: capabilityValidationMessage }
  }
  if (status === 'active' && !hasDirectPrice) return { success: false, message: '启用的自定义模型必须配置完整当前价格' }
  return { success: true }
}

type CustomModelStatus = 'draft' | 'active' | 'disabled'
type CustomModelPricingInput = CustomModelPriceFields & {
  model?: string
  mode?: string | null
  supportedApiProtocols?: ProviderModelCatalogItem['supportedApiProtocols']
  supportedServiceTiers?: ProviderModelCatalogItem['supportedServiceTiers']
  supportedReasoningEfforts?: ProviderModelCatalogItem['supportedReasoningEfforts']
  defaultReasoningEffort?: ProviderModelCatalogItem['defaultReasoningEffort'] | null
  status?: CustomModelStatus
  serviceTierPrices?: unknown
}

function validateCustomModelCapabilities(providerCode: string, input: CustomModelPricingInput): string | undefined {
  const serviceTiers = input.supportedServiceTiers ?? []
  const reasoningEfforts = input.supportedReasoningEfforts ?? []
  const defaultReasoningEffort = input.defaultReasoningEffort ?? undefined
  const isTextModel = input.mode === undefined || input.mode === null || input.mode === 'text'
  const tierPriceMessage = validateServiceTierPriceKeys(input.mode, serviceTiers, input.serviceTierPrices)
  if (tierPriceMessage) return tierPriceMessage
  if (!isTextModel && (serviceTiers.length || reasoningEfforts.length || defaultReasoningEffort)) {
    return '只有文本自定义模型支持服务等级和思考能力配置'
  }
  if (providerCode === 'gpt') {
    const gptServiceTiers = new Set(['priority', 'flex'])
    const gptReasoningEfforts = new Set(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
    if (serviceTiers.length > 2 || reasoningEfforts.length > 7
      || serviceTiers.some((value) => !gptServiceTiers.has(value))
      || reasoningEfforts.some((value) => !gptReasoningEfforts.has(value))) {
      return '自定义模型参数无效'
    }
  }
  if (defaultReasoningEffort && !reasoningEfforts.includes(defaultReasoningEffort)) {
    return '默认思考级别必须属于支持的思考级别'
  }
  return undefined
}

function customInputHasDirectPrice(input: CustomModelPriceFields): boolean {
  const mode = 'mode' in input && typeof input.mode === 'string' ? input.mode : 'text'
  if (mode === 'image') {
    return typeof input.imageInputUsdPer1M === 'number' || typeof input.imageOutputUsdPer1M === 'number' || typeof input.outputUsdPerImage === 'number'
  }
  if (mode === 'audio') {
    return typeof input.audioInputUsdPer1M === 'number' || typeof input.audioOutputUsdPer1M === 'number'
  }
  return typeof input.inputUsdPer1M === 'number'
    || typeof input.outputUsdPer1M === 'number'
    || typeof input.cachedInputUsdPer1M === 'number'
    || typeof input.cacheWriteUsdPer1M === 'number'
    || typeof input.cacheWrite1hUsdPer1M === 'number'
    || serviceTierPriceKeys(input.serviceTierPrices).length > 0
}

type CustomModelPriceFields = Partial<Record<
  'inputUsdPer1M'
  | 'outputUsdPer1M'
  | 'cachedInputUsdPer1M'
  | 'cacheWriteUsdPer1M'
  | 'cacheWrite1hUsdPer1M'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'outputUsdPerImage',
  number | null
>> & { mode?: string | null; serviceTierPrices?: unknown }

function validateServiceTierPriceKeys(mode: unknown, supportedServiceTiers: readonly string[], value: unknown): string | undefined {
  const keys = serviceTierPriceKeys(value)
  if (!keys.length) return undefined
  if (mode === 'image' || mode === 'audio') return '只有文本自定义模型支持服务档位价格'
  if (keys.some((tier) => !supportedServiceTiers.includes(tier))) return '服务档位价格必须属于模型支持的服务等级'
  return undefined
}

function serviceTierPriceKeys(value: unknown): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value)
    .filter(([, prices]) => prices && typeof prices === 'object' && !Array.isArray(prices)
      && Object.values(prices).some((price) => typeof price === 'number' && Number.isFinite(price) && price >= 0))
    .map(([tier]) => tier.trim())
    .filter(Boolean)
}

function providerModelConfigurationSnapshot(value: ProviderModelPricing): Record<string, unknown> {
  return {
    status: value.status, mode: value.mode, supportedApiProtocols: value.supportedApiProtocols,
    supportedServiceTiers: value.supportedServiceTiers, supportedReasoningEfforts: value.supportedReasoningEfforts,
    defaultReasoningEffort: value.defaultReasoningEffort, releaseDate: value.releaseDate, shutdownDate: value.shutdownDate,
    contextWindowTokens: value.contextWindowTokens, maxInputTokens: value.maxInputTokens, maxOutputTokens: value.maxOutputTokens,
    inputUsdPer1M: value.inputUsdPer1M, outputUsdPer1M: value.outputUsdPer1M,
    cachedInputUsdPer1M: value.cachedInputUsdPer1M, cacheWriteUsdPer1M: value.cacheWriteUsdPer1M,
    cacheWrite1hUsdPer1M: value.cacheWrite1hUsdPer1M, serviceTierPrices: value.serviceTierPrices,
    imageInputUsdPer1M: value.imageInputUsdPer1M, imageOutputUsdPer1M: value.imageOutputUsdPer1M,
    audioInputUsdPer1M: value.audioInputUsdPer1M, audioOutputUsdPer1M: value.audioOutputUsdPer1M,
    outputUsdPerImage: value.outputUsdPerImage
  }
}
