import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { createApiKeyRecord, deleteApiKeyWithRelatedCleanup, findApiKeySummary, listApiKeysPage, updateApiKey, type ApiKeyListOptions } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { submitApiKeyRelatedCleanup } from './api-key-cleanup.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const apiKeysRouter = Router()

const apiKeyMutationSchema = z.object({
  name: z.string().trim().min(1, '请填写 API Key 名称'),
  description: z.string().trim().max(200).nullable().optional(),
  groupId: z.string().trim().min(1, 'API Key 分组无效').optional(),
  groupBindings: z.array(z.object({
    groupId: z.string().trim().min(1, 'API Key 分组无效'),
    priority: z.number().int().positive().optional(),
    weight: z.number({ invalid_type_error: '分组权重必须是数字' }).int('分组权重必须是整数').min(1, '分组权重必须在 1-100 之间').max(100, '分组权重必须在 1-100 之间').optional(),
    status: z.enum(['active', 'disabled']).optional()
  })).min(1, 'API Key 至少需要绑定一个分组').max(20).optional(),
  groupRouteStrategy: z.string().optional().refine((value) => value === undefined || value === 'priority_failover' || value === 'round_robin' || value === 'weighted_round_robin', '分组路由策略无效'),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().optional(),
  quotaLimits: z.record(z.string(), z.unknown()).nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional()
})
const apiKeyCreateSchema = apiKeyMutationSchema.refine((value) => Boolean(value.groupId || value.groupBindings?.length), {
  message: 'API Key 至少需要绑定一个分组'
})
const apiKeyUpdateSchema = apiKeyMutationSchema.partial()

apiKeysRouter.get('/', (req, res) => {
  res.json(ok(listApiKeysPage(getRequestAccessScope(req.query.systemAccountId), parseApiKeyListOptions(req.query))))
})

function parseApiKeyListOptions(query: Record<string, unknown>): ApiKeyListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    limit: integerQueryValue(query.limit),
    keyword: optionalQueryText(query.keyword),
    status: apiKeyStatusQueryValue(query.status),
    groupId: optionalQueryText(query.groupId)
  }
}

function apiKeyStatusQueryValue(value: unknown): ApiKeyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

apiKeysRouter.post('/', mutationGuard({
  operationKey: 'api_keys.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name'))
  })
}), (req, res) => {
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
    const apiKey = runLoggedOperation(() => {
      const apiKey = createApiKeyRecord(parsed.data, requestAccess)
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
            safeChange('groupRouteStrategy', '分组路由策略', undefined, apiKey.groupRouteStrategy),
            safeChange('groupBindings', '绑定分组路由', undefined, apiKey.groupBindings),
            safeChange('availabilitySchedule', '自动启停计划', undefined, apiKey.availabilitySchedule),
            safeChange('key', '密钥', undefined, apiKey.keyPrefix)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(201).json(ok(apiKey, 'API Key 已创建，可在列表继续复制'))
  } catch (error) {
    const message = error instanceof Error ? error.message : 'API Key 参数无效'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

apiKeysRouter.patch('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findApiKeySummary(req.params.id, requestAccess)
  const parsed = apiKeyUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 参数无效')))
    return
  }
  try {
    const apiKey = runLoggedOperation(() => {
      const apiKey = updateApiKey(req.params.id, parsed.data as Record<string, unknown>, requestAccess)
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
            groupId: '主分组',
            groupRouteStrategy: '分组路由策略',
            groupBindings: '绑定分组路由',
            expiresAt: '过期时间',
            quotaLimits: '额度限制',
            availabilitySchedule: '自动启停计划'
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

apiKeysRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findApiKeySummary(req.params.id, requestAccess)
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    runLoggedOperation(() => {
      const deleteResult = deleteApiKeyWithRelatedCleanup(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('API Key 不存在')
      }
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
    throw error
  }
  res.status(204).send()
})
