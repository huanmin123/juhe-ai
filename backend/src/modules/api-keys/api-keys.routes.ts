import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  createApiKeyRecordAsync,
  deleteApiKeyWithRelatedCleanupAsync,
  findApiKeySecretAsync,
  findApiKeySummaryAsync,
  listApiKeysPageAsync,
  refreshApiKeySecretAsync,
  updateApiKeyAsync,
  type ApiKeyListOptions
} from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { requestQuotaLimitsSchema } from '../request-quota-limit.schema.js'
import { apiKeyAvailabilityScheduleSchema } from './api-key-availability-schedule.schema.js'
import { submitApiKeyRelatedCleanup } from './api-key-cleanup.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const apiKeysRouter = Router()

const apiKeyMutationSchema = z.object({
  name: z.string().trim().min(1, '请填写 API Key 名称'),
  description: z.string().trim().max(200).nullable().optional(),
  routeStrategyId: z.string().trim().min(1, '请选择策略路由').optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().nullable().optional(),
  quotaLimits: requestQuotaLimitsSchema.nullable().optional(),
  availabilitySchedule: apiKeyAvailabilityScheduleSchema.nullable().optional(),
  availabilityScheduleActive: z.boolean().optional()
}).strict()

const apiKeyCreateSchema = apiKeyMutationSchema.refine((value) => Boolean(value.routeStrategyId?.trim()), {
  message: 'API Key 必须绑定策略路由'
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
      if (!apiKey) throw new Error('API Key 不存在')
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

apiKeysRouter.post('/', mutationGuard({
  operationKey: 'api_keys.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name'))
  })
}), async (req, res) => {
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
            safeChange('routeStrategyId', '策略路由', undefined, apiKey.routeStrategyId),
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
  try {
    const apiKey = await runLoggedOperationAsync(async () => {
      const apiKey = await updateApiKeyAsync(req.params.id, parsed.data as Record<string, unknown>, requestAccess)
      if (!apiKey) throw new Error('API Key 不存在')
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
            routeStrategyId: '策略路由',
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
      if (!deleteResult.deleted) throw new Error('API Key 不存在')
      return {
        result: true,
        afterCommit: () => {
          if (deleteResult.cleanupTarget) {
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
    if (error instanceof Error && error.message.includes('默认 API Key 不允许删除')) {
      res.status(409).json(badRequest(error.message))
      return
    }
    next(error)
    return
  }
  res.status(204).send()
})

function parseApiKeyListOptions(query: Record<string, unknown>): ApiKeyListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    status: apiKeyStatusQueryValue(query.status),
    routeStrategyId: optionalQueryText(query.routeStrategyId)
  }
}

function apiKeyStatusQueryValue(value: unknown): ApiKeyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}
