import { type Response, Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  ApiKeyRevisionConflictError,
  createApiKeyRecordAsync,
  deleteApiKeyWithRelatedCleanupAsync,
  findApiKeySecretAsync,
  listApiKeysPageAsync,
  patchApiKeyAsync,
  refreshApiKeySecretForManagementAsync,
  type ApiKeyListOptions
} from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { requestQuotaLimitsSchema } from '../request-quota-limit.schema.js'
import { apiKeyAvailabilityScheduleSchema } from './api-key-availability-schedule.schema.js'
import { submitApiKeyRelatedCleanupAsync } from './api-key-cleanup.service.js'
import { diffSafeFields, operationMode, recordOperationLogAsync, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const apiKeysRouter = Router()

const apiKeyMutationSchema = z.object({
  name: z.string().trim().min(1, '请填写 API Key 名称'),
  description: z.string().trim().max(200).nullable().optional(),
  routeStrategyId: z.string().trim().min(1, '请选择策略路由').optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().nullable().optional(),
  quotaLimits: requestQuotaLimitsSchema.nullable().optional(),
  availabilitySchedule: apiKeyAvailabilityScheduleSchema.nullable().optional()
}).strict()

const apiKeyCreateSchema = apiKeyMutationSchema

const apiKeyUpdateSchema = apiKeyMutationSchema.partial().extend({
  expectedRevision: z.string().trim().min(1, '缺少 API Key revision')
}).refine((value) => Object.keys(value).some((key) => key !== 'expectedRevision'), {
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
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const apiKey = await findApiKeySecretAsync(req.params.id, requestAccess)
    if (!apiKey) {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    if (!apiKey.key) {
      throw new Error('API Key 密钥读取失败')
    }
    const ownerSystemAccountId = resolveOperationOwner(apiKey as unknown as Record<string, unknown>, requestAccess)
    const operationLog = {
      operationScopeSystemAccountId: ownerSystemAccountId,
      mode: operationMode(requestAccess),
      module: 'api_keys',
      action: 'reveal_secret',
      operationKey: 'api_keys.reveal_secret',
      resourceType: 'api_key',
      resourceId: apiKey.id,
      resourceName: apiKey.name,
      summary: `查看 API Key 完整密钥：${apiKey.name}`,
      changes: [
        safeChange('key', '密钥标识', undefined, `${apiKey.keyPrefix}...${apiKey.keySuffix}`)
      ],
      viewers: viewer(ownerSystemAccountId, 'resource_owner')
    }
    setNoStoreHeaders(res)
    res.json(ok({ key: apiKey.key }))
    void recordOperationLogAsync(operationLog, req)
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
  try {
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await refreshApiKeySecretForManagementAsync(req.params.id, requestAccess)
      if (!outcome) throw new Error('API Key 不存在')
      return {
        result: outcome,
        log: {
          operationScopeSystemAccountId: outcome.ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'refresh_key',
          operationKey: 'api_keys.refresh_key',
          resourceType: 'api_key',
          resourceId: outcome.result.id,
          resourceName: outcome.resourceName,
          summary: `刷新 API Key 密钥：${outcome.resourceName}`,
          statusCode: outcome.validationCacheError ? 500 : 200,
          changes: [
            safeChange(
              'key',
              '密钥标识',
              `${outcome.previousKeyPrefix}...${outcome.previousKeySuffix}`,
              `${outcome.result.keyPrefix}...${outcome.result.keySuffix}`
            )
          ],
          viewers: viewer(outcome.ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (outcome.validationCacheError) {
      next(outcome.validationCacheError)
      return
    }
    setNoStoreHeaders(res)
    res.json(ok(outcome.result, 'API Key 密钥已刷新，请立即复制完整密钥'))
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
  try {
    const apiKey = await runLoggedOperationAsync(async () => {
      const created = await createApiKeyRecordAsync(parsed.data, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(created as unknown as Record<string, unknown>, requestAccess)
      return {
        result: {
          id: created.id,
          key: created.key,
          keyPrefix: created.keyPrefix,
          keySuffix: created.keySuffix,
          revision: created.revision
        },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'create',
          operationKey: 'api_keys.create',
          resourceType: 'api_key',
          resourceId: created.id,
          resourceName: created.name,
          summary: `创建 API Key：${created.name}`,
          statusCode: 201,
          changes: [
            safeChange('name', '名称', undefined, created.name),
            safeChange('status', '状态', undefined, created.status),
            safeChange('routeStrategyId', '策略路由', undefined, created.routeStrategyId),
            safeChange('availabilitySchedule', '时间计划', undefined, created.availabilitySchedule),
            safeChange('key', '密钥标识', undefined, `${created.keyPrefix}...${created.keySuffix}`)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    setNoStoreHeaders(res)
    res.status(201).json(ok(apiKey, 'API Key 已创建，请立即复制完整密钥'))
  } catch (error) {
    const clientError = apiKeyMutationClientError(error)
    if (clientError) {
      res.status(clientError.status).json(badRequest(clientError.message))
      return
    }
    next(error)
  }
})

apiKeysRouter.patch('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = apiKeyUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 参数无效')))
    return
  }
  try {
    const outcome = await runLoggedOperationAsync(async () => {
      const { expectedRevision, ...mutation } = parsed.data
      const outcome = await patchApiKeyAsync(
        req.params.id,
        mutation as Record<string, unknown>,
        expectedRevision,
        requestAccess
      )
      if (!outcome) throw new Error('API Key 不存在')
      const changes = diffSafeFields(
        outcome.before as Record<string, unknown>,
        outcome.after as Record<string, unknown>,
        {
          name: '名称',
          description: '说明',
          status: '状态',
          routeStrategyId: '策略路由',
          expiresAt: '过期时间',
          quotaLimits: '额度限制',
          availabilitySchedule: '时间计划'
        }
      )
      return {
        result: outcome,
        log: outcome.result.changedFields.length ? {
          operationScopeSystemAccountId: outcome.ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'update',
          operationKey: 'api_keys.update',
          resourceType: 'api_key',
          resourceId: outcome.result.id,
          resourceName: outcome.resourceName,
          summary: `更新 API Key：${outcome.resourceName}`,
          statusCode: outcome.validationCacheError ? 500 : 200,
          changes,
          viewers: viewer(outcome.ownerSystemAccountId, 'resource_owner')
        } : undefined
      }
    }, req)
    if (outcome.validationCacheError) {
      next(outcome.validationCacheError)
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    if (error instanceof ApiKeyRevisionConflictError) {
      res.status(409).json({ message: error.message, currentRevision: error.currentRevision })
      return
    }
    if (error instanceof Error && error.message === 'API Key 不存在') {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    const clientError = apiKeyMutationClientError(error)
    if (clientError) {
      res.status(clientError.status).json(badRequest(clientError.message))
      return
    }
    next(error)
  }
})

apiKeysRouter.delete('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  try {
    const deleteResult = await runLoggedOperationAsync(async () => {
      const deleteResult = await deleteApiKeyWithRelatedCleanupAsync(req.params.id, requestAccess)
      if (!deleteResult.deleted) throw new Error('API Key 不存在')
      return {
        result: deleteResult,
        afterCommit: async () => {
          if (deleteResult.cleanupTarget) {
            await submitApiKeyRelatedCleanupAsync(deleteResult.cleanupTarget)
          }
        },
        log: {
          operationScopeSystemAccountId: deleteResult.ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'api_keys',
          action: 'delete',
          operationKey: 'api_keys.delete',
          resourceType: 'api_key',
          resourceId: req.params.id,
          resourceName: deleteResult.resourceName,
          summary: `删除 API Key：${deleteResult.resourceName}`,
          statusCode: deleteResult.validationCacheError ? 500 : 204,
          changes: [safeChange('deleted', '删除状态', false, true)],
          viewers: viewer(deleteResult.ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (deleteResult.validationCacheError) {
      next(deleteResult.validationCacheError)
      return
    }
  } catch (error) {
    if (error instanceof Error && error.message === 'API Key 不存在') {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    if (error instanceof Error && (error.message.includes('默认 API Key 不允许删除') || error.message.includes('AI 对话 API Key 不允许删除'))) {
      res.status(409).json(badRequest(error.message))
      return
    }
    next(error)
    return
  }
  setNoStoreHeaders(res)
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

function apiKeyMutationClientError(error: unknown): { status: 400 | 409; message: string } | undefined {
  if (!(error instanceof Error)) return undefined
  if (error.message.startsWith('API Key 名称已存在：')) {
    return { status: 409, message: error.message }
  }
  const badRequestMessages = new Set([
    '当前用户缺少可用的默认策略路由',
    'API Key 绑定的策略路由不存在或不属于当前用户',
    'API Key 只能绑定启用状态的策略路由',
    '默认 API Key 不允许更换策略路由',
    'AI 对话 API Key 不允许修改名称',
    '默认 API Key 不允许修改名称',
    'API Key 过期时间必须是有效时间字符串'
  ])
  return badRequestMessages.has(error.message)
    ? { status: 400, message: error.message }
    : undefined
}

function apiKeyStatusQueryValue(value: unknown): ApiKeyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

function setNoStoreHeaders(res: Response): void {
  res.set('Cache-Control', 'no-store')
  res.set('Pragma', 'no-cache')
}
