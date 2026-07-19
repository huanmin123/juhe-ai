import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  createRouteStrategyAsync,
  deleteRouteStrategyAsync,
  findRouteStrategySummaryAsync,
  listRouteStrategyListItemsPageAsync,
  listRouteStrategyOptionsAsync,
  updateRouteStrategyAsync,
  type RouteStrategyListOptions
} from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { clearNormalRouteLatencyDegradationForRouteStrategyAsync } from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { createPageDataDomainReadCache, pageDataReadCacheKey } from '../page-data/page-data-read-cache.service.js'
import { publishPageDataDomainGlobalReset } from '../page-data/page-data-change.publisher.js'

export const routeStrategiesRouter = Router()

const routeStrategyOptionsReadCache = createPageDataDomainReadCache<Awaited<ReturnType<typeof listRouteStrategyOptionsAsync>>>('routeStrategies.options', {
  max: 512,
  ttlMs: 6 * 60 * 60 * 1000
})

const routeStrategyGroupBindingSchema = z.object({
  groupId: z.string().trim().min(1, '策略路由分组无效'),
  priority: z.number().int().positive().optional(),
  weight: z.number({ invalid_type_error: '分组权重必须是数字' }).int('分组权重必须是整数').min(1, '分组权重必须在 1-100 之间').max(100, '分组权重必须在 1-100 之间').optional(),
  status: z.enum(['active', 'disabled']).optional()
}).strict()

const hybridRoutingConfigSchema = z.object({
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
}).strict()

const speedFirstConfigSchema = z.object({
  firstByteThresholdMs: z.number({ invalid_type_error: '首字观察阈值必须是数字' })
    .int('首字观察阈值必须是整数')
    .min(10000, '首字观察阈值不能低于 10000 毫秒')
    .max(60000, '首字观察阈值不能高于 60000 毫秒')
    .optional(),
  slowTriggerCount: z.number({ invalid_type_error: '慢速触发次数必须是数字' })
    .int('慢速触发次数必须是整数')
    .min(2, '慢速触发次数不能低于 2 次')
    .max(10, '慢速触发次数不能高于 10 次')
    .optional(),
  slowWindowSeconds: z.number({ invalid_type_error: '慢速窗口期必须是数字' })
    .int('慢速窗口期必须是整数')
    .min(60, '慢速窗口期不能低于 60 秒')
    .max(600, '慢速窗口期不能高于 600 秒')
    .optional(),
  recoverySuccessCount: z.number({ invalid_type_error: '恢复成功次数必须是数字' })
    .int('恢复成功次数必须是整数')
    .min(3, '恢复成功次数不能低于 3 次')
    .max(10, '恢复成功次数不能高于 10 次')
    .optional(),
  probeIntervalSeconds: z.number({ invalid_type_error: '探针间隔必须是数字' })
    .int('探针间隔必须是整数')
    .min(10, '探针间隔不能低于 10 秒')
    .max(300, '探针间隔不能高于 300 秒')
    .optional(),
  degradedTtlSeconds: z.number({ invalid_type_error: '降级保留时间必须是数字' })
    .int('降级保留时间必须是整数')
    .min(60, '降级保留时间不能低于 60 秒')
    .max(3600, '降级保留时间不能高于 3600 秒')
    .optional(),
  maxFirstByteRetriesPerRequest: z.number({ invalid_type_error: '单请求切号次数必须是数字' })
    .int('单请求切号次数必须是整数')
    .min(1, '单请求切号次数不能低于 1 次')
    .max(3, '单请求切号次数不能高于 3 次')
    .optional()
}).strict()

const normalRoutingConfigSchema = z.object({
  schedulingPreference: z.enum(['cost_first', 'speed_first']).optional(),
  speedFirstConfig: speedFirstConfigSchema.optional()
}).strict()

const routeStrategyMutationSchema = z.object({
  name: z.string().trim().min(1, '请填写策略路由名称'),
  description: z.string().trim().max(200).nullable().optional(),
  mode: z.enum(['normal', 'hybrid_smart', 'weighted', 'failover', 'round_robin']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  groupBindings: z.array(routeStrategyGroupBindingSchema).min(1, '策略路由至少需要绑定一个分组').max(20).optional(),
  normalRoutingConfig: normalRoutingConfigSchema.nullable().optional(),
  hybridRoutingConfig: hybridRoutingConfigSchema.nullable().optional()
}).strict()

const routeStrategyCreateSchema = routeStrategyMutationSchema.refine((value) => Boolean(value.groupBindings?.length), {
  message: '策略路由至少需要绑定一个分组'
})

const routeStrategyUpdateSchema = routeStrategyMutationSchema.partial().refine((value) => Object.keys(value).length > 0, {
  message: '请提供要修改的策略路由内容'
})

routeStrategiesRouter.get('/', async (req, res, next) => {
  try {
    res.json(ok(await listRouteStrategyListItemsPageAsync(getRequestAccessScope(req.query.systemAccountId), parseRouteStrategyListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

routeStrategiesRouter.get('/options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = {
      ids: queryTextList(req.query.ids, 50),
      keyword: optionalQueryText(req.query.keyword),
      limit: optionLimitValue(integerQueryValue(req.query.limit)),
      activeOnly: booleanQueryValue(req.query.activeOnly) ?? true
    }
    const options = await routeStrategyOptionsReadCache.load(pageDataReadCacheKey({
      scope: access,
      route: '/route-strategies/options',
      query
    }), () => listRouteStrategyOptionsAsync(access, query))
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

routeStrategiesRouter.get('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    const routeStrategy = await findRouteStrategySummaryAsync(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!routeStrategy) {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    res.json(ok(routeStrategy))
  } catch (error) {
    next(error)
  }
})

routeStrategiesRouter.post('/', mutationGuard({
  operationKey: 'route_strategies.create',
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
  const parsed = routeStrategyCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '策略路由参数无效')))
    return
  }
  try {
    const routeStrategy = await runLoggedOperationAsync(async () => {
      const routeStrategy = await createRouteStrategyAsync(parsed.data, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(routeStrategy as unknown as Record<string, unknown>, requestAccess)
      return {
        result: routeStrategy,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'route_strategies',
          action: 'create',
          operationKey: 'route_strategies.create',
          resourceType: 'route_strategy',
          resourceId: routeStrategy.id,
          resourceName: routeStrategy.name,
          summary: `创建策略路由：${routeStrategy.name}`,
          changes: [
            safeChange('name', '名称', undefined, routeStrategy.name),
            safeChange('mode', '路由模式', undefined, routeStrategy.mode),
            safeChange('status', '状态', undefined, routeStrategy.status),
            safeChange('groupBindings', '绑定分组', undefined, routeStrategy.groupBindings),
            safeChange('normalRoutingConfig', '普通路由调度配置', undefined, routeStrategy.normalRoutingConfig),
            safeChange('hybridRoutingConfig', '混合智能路由配置', undefined, routeStrategy.hybridRoutingConfig)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    await publishPageDataDomainGlobalReset('routeStrategies.options')
    res.status(201).json(ok(routeStrategy))
  } catch (error) {
    const message = error instanceof Error ? error.message : '创建策略路由失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

routeStrategiesRouter.patch('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  let before: Awaited<ReturnType<typeof findRouteStrategySummaryAsync>>
  try {
    before = await findRouteStrategySummaryAsync(req.params.id, requestAccess)
  } catch (error) {
    next(error)
    return
  }
  const parsed = routeStrategyUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '策略路由参数无效')))
    return
  }
  try {
    const routeStrategy = await runLoggedOperationAsync(async () => {
      const routeStrategy = await updateRouteStrategyAsync(req.params.id, parsed.data as Record<string, unknown>, requestAccess)
      if (!routeStrategy) throw new Error('策略路由不存在')
      const ownerSystemAccountId = resolveOperationOwner(routeStrategy as unknown as Record<string, unknown>, requestAccess)
      return {
        result: routeStrategy,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'route_strategies',
          action: 'update',
          operationKey: 'route_strategies.update',
          resourceType: 'route_strategy',
          resourceId: routeStrategy.id,
          resourceName: routeStrategy.name,
          summary: `更新策略路由：${routeStrategy.name}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, routeStrategy as unknown as Record<string, unknown>, {
            name: '名称',
            description: '说明',
            mode: '路由模式',
            status: '状态',
            groupBindings: '绑定分组',
            normalRoutingConfig: '普通路由调度配置',
            hybridRoutingConfig: '混合智能路由配置'
          }),
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    await clearNormalRouteSpeedFirstRuntime(routeStrategy.id, 'route_strategy_updated')
    await publishPageDataDomainGlobalReset('routeStrategies.options')
    res.json(ok(routeStrategy))
  } catch (error) {
    if (error instanceof Error && error.message === '策略路由不存在') {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新策略路由失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

routeStrategiesRouter.delete('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  let before: Awaited<ReturnType<typeof findRouteStrategySummaryAsync>>
  try {
    before = await findRouteStrategySummaryAsync(req.params.id, requestAccess)
  } catch (error) {
    next(error)
    return
  }
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    await runLoggedOperationAsync(async () => {
      const deleted = await deleteRouteStrategyAsync(req.params.id, requestAccess)
      if (!deleted) throw new Error('策略路由不存在')
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'route_strategies',
          action: 'delete',
          operationKey: 'route_strategies.delete',
          resourceType: 'route_strategy',
          resourceId: req.params.id,
          resourceName: before?.name ?? req.params.id,
          summary: `删除策略路由：${before?.name ?? req.params.id}`,
          changes: [safeChange('deleted', '删除状态', false, true)],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    await clearNormalRouteSpeedFirstRuntime(req.params.id, 'route_strategy_deleted')
  } catch (error) {
    if (error instanceof Error && error.message === '策略路由不存在') {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '删除策略路由失败'
    res.status(400).json(badRequest(message))
    return
  }
  await publishPageDataDomainGlobalReset('routeStrategies.options')
  res.status(204).send()
})

function parseRouteStrategyListOptions(query: Record<string, unknown>): RouteStrategyListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    mode: routeStrategyModeQueryValue(query.mode),
    status: routeStrategyStatusQueryValue(query.status)
  }
}

function routeStrategyModeQueryValue(value: unknown): RouteStrategyListOptions['mode'] {
  const text = optionalQueryText(value)
  return text === 'normal' || text === 'hybrid_smart' || text === 'weighted' || text === 'failover' || text === 'round_robin' || text === 'all'
    ? text
    : undefined
}

function routeStrategyStatusQueryValue(value: unknown): RouteStrategyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(100, Math.max(1, value)) : 50
}

function booleanQueryValue(value: unknown): boolean | undefined {
  const text = Array.isArray(value) ? value[0] : value
  if (typeof text === 'boolean') return text
  if (typeof text !== 'string') return undefined
  const normalized = text.trim().toLowerCase()
  if (['1', 'true', 'yes'].includes(normalized)) return true
  if (['0', 'false', 'no'].includes(normalized)) return false
  return undefined
}

async function clearNormalRouteSpeedFirstRuntime(routeStrategyId: string, reason: string): Promise<void> {
  try {
    const cleared = await clearNormalRouteLatencyDegradationForRouteStrategyAsync(routeStrategyId)
    if (cleared > 0) {
      logger.info({
        event: 'route_strategy_normal_route_speed_first_runtime_cleared',
        routeStrategyId,
        reason,
        cleared
      }, '策略路由速度优先运行态已清理')
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'route_strategy_normal_route_speed_first_runtime_clear_failed',
      routeStrategyId,
      reason
    }), '策略路由速度优先运行态清理失败')
  }
}
