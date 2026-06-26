import { Router } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { createApiKeyRecordAsync, deleteApiKeyWithRelatedCleanupAsync, findApiKeySecretAsync, findApiKeySummaryAsync, listApiKeysPageAsync, listProviders, listProvidersAsync, refreshApiKeySecretAsync, updateApiKeyAsync, type ApiKeyListOptions } from '../../storage/repositories.js'
import { getRequestAccessScope, getRequestAuthContext, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { listProviderModelCatalog, listProviderModelCatalogAsync } from '../model-pricing/model-catalog.service.js'
import { requestQuotaLimitsSchema } from '../request-quota-limit.schema.js'
import { apiKeyAvailabilityScheduleSchema } from './api-key-availability-schedule.schema.js'
import { submitApiKeyRelatedCleanup } from './api-key-cleanup.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const apiKeysRouter = Router()

const apiKeyMutationSchema = z.object({
  name: z.string().trim().min(1, '请填写 API Key 名称'),
  description: z.string().trim().max(200).nullable().optional(),
  groupBindings: z.array(z.object({
    groupId: z.string().trim().min(1, 'API Key 分组无效'),
    priority: z.number().int().positive().optional(),
    weight: z.number({ invalid_type_error: '分组权重必须是数字' }).int('分组权重必须是整数').min(1, '分组权重必须在 1-100 之间').max(100, '分组权重必须在 1-100 之间').optional(),
    status: z.enum(['active', 'disabled']).optional()
  }).strict()).min(1, 'API Key 至少需要绑定一个分组').max(20).optional(),
  routeMode: z.enum(['normal', 'hybrid']).optional(),
  clientProfile: z.enum(['auto', 'generic_openai', 'codex', 'generic_anthropic', 'claude_code', 'generic_gemini', 'gemini_cli']).optional(),
  groupRouteStrategy: z.string().optional().refine((value) => value === undefined || value === 'priority_failover' || value === 'round_robin' || value === 'weighted_round_robin', '分组路由策略无效'),
  hybridRoutingConfig: z.object({
    scoringGroupId: z.string().trim().optional(),
    scoringModel: z.string().trim().min(1, '评分模型不能为空'),
    scoringContextMode: z.literal('full_request').optional(),
    qualityPreference: z.enum(['cost_first', 'balanced', 'quality_first']).optional(),
    scoringTimeoutMs: z.number().int().min(1000).max(60000).optional(),
    scoringFallbackMaxLevel: z.number().int().min(2).max(5).optional(),
    scoringCacheEnabled: z.boolean().optional(),
    scoringCacheTtlSeconds: z.number().int().min(1).max(3600).optional(),
    cacheAffinityEnabled: z.boolean().optional(),
    affinityTtlSeconds: z.number().int().min(1).max(86400).optional(),
    switchMinLevelDelta: z.number().int().min(0).max(9).optional(),
    downgradeConsecutiveLowCount: z.number().int().min(1).max(20).optional(),
    levelRoutes: z.array(z.object({
      minLevel: z.number().int().min(1).max(10),
      maxLevel: z.number().int().min(1).max(10),
      targetModel: z.string().trim().min(1, '目标模型不能为空'),
      enabled: z.boolean().optional()
    }).strict()).min(1, '等级范围不能为空'),
    qualityInspection: z.object({
      enabled: z.boolean().optional(),
      scoringGroupId: z.string().trim().optional(),
      scoringModel: z.string().trim().optional(),
      triggerMode: z.enum(['quality_first_only', 'risk_based', 'always_for_hybrid']).optional(),
      maxTriggerLevel: z.number().int().min(1).max(10).optional(),
      maxRetries: z.number().int().min(0).max(2).optional(),
      failureAction: z.enum(['repair_then_upgrade', 'upgrade_next_level', 'retry_same_model', 'return_error']).optional(),
      unavailableAction: z.enum(['pass_through', 'return_error']).optional()
    }).strict().optional()
  }).strict().nullable().optional(),
  explicitHybridRouteRules: z.array(z.object({
    id: z.string().trim().optional(),
    enabled: z.boolean().optional(),
    priority: z.number().int().positive().optional(),
    sourceClientProfile: z.enum(['auto', 'generic_openai', 'codex', 'generic_anthropic', 'claude_code', 'generic_gemini', 'gemini_cli']).optional(),
    sourceEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content']),
    sourceModel: z.string().trim().optional(),
    targetGroupId: z.string().trim().min(1, '目标分组不能为空'),
    targetAccountId: z.string().trim().optional(),
    targetProviderProtocolProfileId: z.string().trim().optional(),
    upstreamEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content']),
    upstreamModel: z.string().trim().min(1, '上游模型不能为空'),
    adapterMode: z.enum(['direct', 'bridge']).optional()
  }).strict()).max(100).nullable().optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().nullable().optional(),
  quotaLimits: requestQuotaLimitsSchema.nullable().optional(),
  availabilitySchedule: apiKeyAvailabilityScheduleSchema.nullable().optional(),
  availabilityScheduleActive: z.boolean().optional()
}).strict()
const apiKeyCreateSchema = apiKeyMutationSchema.refine((value) => Boolean(value.groupBindings?.length), {
  message: 'API Key 至少需要绑定一个分组'
})
const apiKeyUpdateSchema = apiKeyMutationSchema.partial().refine((value) => Object.keys(value).length > 0, {
  message: '请提供要修改的 API Key 内容'
})

apiKeysRouter.get('/', async (req, res, next) => {
  try {
    res.json(ok(await listApiKeysPageAsync(getRequestAccessScope(req.query.systemAccountId), parseApiKeyListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

apiKeysRouter.get('/:id/secret', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    const apiKey = await findApiKeySecretAsync(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!apiKey) {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    res.json(ok({ key: apiKey.key }))
  } catch (error) {
    next(error)
  }
})

apiKeysRouter.post('/:id/refresh-key', mutationGuard({
  operationKey: 'api_keys.refresh_key',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    id: normalizedText(req.params.id)
  })
}), async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  let before: Awaited<ReturnType<typeof findApiKeySummaryAsync>>
  try {
    before = await findApiKeySummaryAsync(req.params.id, requestAccess)
  } catch (error) {
    next(error)
    return
  }
  try {
    const apiKey = await runLoggedOperationAsync(async () => {
      const apiKey = await refreshApiKeySecretAsync(req.params.id, requestAccess)
      if (!apiKey) {
        throw new Error('API Key 不存在')
      }
      const ownerSystemAccountId = resolveOperationOwner(apiKey as unknown as Record<string, unknown>, requestAccess)
      return {
        result: apiKey,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'refresh_key',
          operationKey: 'api_keys.refresh_key',
          resourceType: 'api_key',
          resourceId: apiKey.id,
          resourceName: apiKey.name,
          summary: `刷新 API Key 密钥：${apiKey.name}`,
          changes: [
            safeChange('key', '密钥标识', before ? `${before.keyPrefix}...${before.keySuffix}` : undefined, `${apiKey.keyPrefix}...${apiKey.keySuffix}`)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(apiKey, 'API Key 密钥已刷新，请立即复制完整密钥'))
  } catch (error) {
    if (error instanceof Error && error.message === 'API Key 不存在') {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    next(error)
  }
})

function parseApiKeyListOptions(query: Record<string, unknown>): ApiKeyListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    status: apiKeyStatusQueryValue(query.status),
    groupId: optionalQueryText(query.groupId)
  }
}

function apiKeyStatusQueryValue(value: unknown): ApiKeyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

async function validateHybridRoutingCatalogModels(value: unknown, access: RequestAccessScope | undefined): Promise<string | undefined> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const config = value as Record<string, unknown>
  const catalogModels = runtimeConfig.databaseDriver === 'postgres'
    ? await availableHybridRoutingModelSetAsync(access)
    : availableHybridRoutingModelSet(access)
  const scoringModelMessage = validateCatalogModel(config.scoringModel, catalogModels, '评分模型')
  if (scoringModelMessage) return scoringModelMessage

  const qualityInspection = config.qualityInspection
  if (qualityInspection && typeof qualityInspection === 'object' && !Array.isArray(qualityInspection)) {
    const qualityConfig = qualityInspection as Record<string, unknown>
    if (qualityConfig.enabled !== false) {
      const qualityScoringModel = typeof qualityConfig.scoringModel === 'string' && qualityConfig.scoringModel.trim()
        ? qualityConfig.scoringModel
        : config.scoringModel
      const qualityModelMessage = validateCatalogModel(qualityScoringModel, catalogModels, '质量评分模型')
      if (qualityModelMessage) return qualityModelMessage
    }
  }

  if (Array.isArray(config.levelRoutes)) {
    for (let index = 0; index < config.levelRoutes.length; index += 1) {
      const route = config.levelRoutes[index]
      if (!route || typeof route !== 'object' || Array.isArray(route)) continue
      const record = route as Record<string, unknown>
      if (record.enabled === false) continue
      const routeMessage = validateCatalogModel(record.targetModel, catalogModels, `第 ${index + 1} 个等级区间目标模型`)
      if (routeMessage) return routeMessage
    }
  }
  return undefined
}

function validateCatalogModel(value: unknown, catalogModels: Set<string>, label: string): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  const model = value.trim()
  return catalogModels.has(model.toLowerCase()) ? undefined : `${label}不存在于模型目录：${model}`
}

function availableHybridRoutingModelSet(access: RequestAccessScope | undefined): Set<string> {
  const context = getRequestAuthContext()
  const systemAccountId = access?.systemAccountFilterId?.trim() || access?.systemAccountId || context?.systemAccountId
  const models = listProviders()
    .filter((provider) => provider.enabled)
    .flatMap((provider) => listProviderModelCatalog({
      providerCode: provider.code,
      systemAccountId,
      includeUnpriced: true
    }))
    .map((item) => item.model.trim().toLowerCase())
    .filter(Boolean)
  return new Set(models)
}

async function availableHybridRoutingModelSetAsync(access: RequestAccessScope | undefined): Promise<Set<string>> {
  const context = getRequestAuthContext()
  const systemAccountId = access?.systemAccountFilterId?.trim() || access?.systemAccountId || context?.systemAccountId
  const providers = (await listProvidersAsync()).filter((provider) => provider.enabled)
  const catalogs = await Promise.all(providers.map((provider) => listProviderModelCatalogAsync({
    providerCode: provider.code,
    systemAccountId,
    includeUnpriced: true
  })))
  const models = catalogs
    .flatMap((catalog) => catalog)
    .map((item) => item.model.trim().toLowerCase())
    .filter(Boolean)
  return new Set(models)
}

apiKeysRouter.post('/', mutationGuard({
  operationKey: 'api_keys.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name'))
  })
}), async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = apiKeyCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 参数无效')))
    return
  }
  const modelValidationMessage = await validateHybridRoutingCatalogModels(parsed.data.hybridRoutingConfig, requestAccess)
  if (modelValidationMessage) {
    res.status(400).json(badRequest(modelValidationMessage))
    return
  }
  try {
    const apiKey = await runLoggedOperationAsync(async () => {
      const apiKey = await createApiKeyRecordAsync(parsed.data, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(apiKey as unknown as Record<string, unknown>, requestAccess)
      return {
        result: apiKey,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'create',
          operationKey: 'api_keys.create',
          resourceType: 'api_key',
          resourceId: apiKey.id,
          resourceName: apiKey.name,
          summary: `创建 API Key：${apiKey.name}`,
          changes: [
            safeChange('name', '名称', undefined, apiKey.name),
            safeChange('status', '状态', undefined, apiKey.status),
            safeChange('clientProfile', '默认客户端画像', undefined, apiKey.clientProfile),
            safeChange('routeMode', '路由模式', undefined, apiKey.routeMode),
            safeChange('groupRouteStrategy', '分组路由策略', undefined, apiKey.groupRouteStrategy),
            safeChange('hybridRoutingConfig', '混合路由配置', undefined, apiKey.hybridRoutingConfig),
            safeChange('explicitHybridRouteRules', '显式混合路由规则', undefined, apiKey.explicitHybridRouteRules),
            safeChange('groupBindings', '绑定分组路由', undefined, apiKey.groupBindings),
            safeChange('availabilitySchedule', '时间计划', undefined, apiKey.availabilitySchedule),
            safeChange('key', '密钥标识', undefined, `${apiKey.keyPrefix}...${apiKey.keySuffix}`)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(201).json(ok(apiKey, 'API Key 已创建，请立即复制完整密钥'))
  } catch (error) {
    const message = error instanceof Error ? error.message : 'API Key 参数无效'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

apiKeysRouter.patch('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  let before: Awaited<ReturnType<typeof findApiKeySummaryAsync>>
  try {
    before = await findApiKeySummaryAsync(req.params.id, requestAccess)
  } catch (error) {
    next(error)
    return
  }
  const parsed = apiKeyUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 参数无效')))
    return
  }
  const modelValidationMessage = await validateHybridRoutingCatalogModels(parsed.data.hybridRoutingConfig, requestAccess)
  if (modelValidationMessage) {
    res.status(400).json(badRequest(modelValidationMessage))
    return
  }
  try {
    const apiKey = await runLoggedOperationAsync(async () => {
      const apiKey = await updateApiKeyAsync(req.params.id, parsed.data as Record<string, unknown>, requestAccess)
      if (!apiKey) {
        throw new Error('API Key 不存在')
      }
      const ownerSystemAccountId = resolveOperationOwner(apiKey as unknown as Record<string, unknown>, requestAccess)
      return {
        result: apiKey,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'update',
          operationKey: 'api_keys.update',
          resourceType: 'api_key',
          resourceId: apiKey.id,
          resourceName: apiKey.name,
          summary: `更新 API Key：${apiKey.name}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, apiKey as unknown as Record<string, unknown>, {
            name: '名称',
            description: '说明',
            status: '状态',
            clientProfile: '默认客户端画像',
            routeMode: '路由模式',
            groupRouteStrategy: '分组路由策略',
            hybridRoutingConfig: '混合路由配置',
            explicitHybridRouteRules: '显式混合路由规则',
            groupBindings: '绑定分组路由',
            expiresAt: '过期时间',
            quotaLimits: '额度限制',
            availabilitySchedule: '时间计划',
            availabilityScheduleActive: '时间计划派生状态'
          }),
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(apiKey))
  } catch (error) {
    if (error instanceof Error && error.message === 'API Key 不存在') {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新 API Key 失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

apiKeysRouter.delete('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  let before: Awaited<ReturnType<typeof findApiKeySummaryAsync>>
  try {
    before = await findApiKeySummaryAsync(req.params.id, requestAccess)
  } catch (error) {
    next(error)
    return
  }
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    await runLoggedOperationAsync(async () => {
      const deleteResult = await deleteApiKeyWithRelatedCleanupAsync(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('API Key 不存在')
      }
      return {
        result: true,
        afterCommit: () => {
          if (deleteResult.cleanupTarget && runtimeConfig.databaseDriver !== 'postgres') {
            submitApiKeyRelatedCleanup(deleteResult.cleanupTarget)
          }
        },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'delete',
          operationKey: 'api_keys.delete',
          resourceType: 'api_key',
          resourceId: req.params.id,
          resourceName: before?.name ?? req.params.id,
          summary: `删除 API Key：${before?.name ?? req.params.id}`,
          changes: [safeChange('deleted', '删除状态', false, true)],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
  } catch (error) {
    if (error instanceof Error && error.message === 'API Key 不存在') {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    next(error)
    return
  }
  res.status(204).send()
})
