import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { DefaultGroupReadonlyError, createGroup, deleteGroup, findGroupSummary, listAccountGroupOptions, listGroupOptions, listGroupsPage, listProviders, updateGroup, type DeletedGroupApiKeyRouteChange } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountConcurrencyToGroupList } from '../gateway/gateway-runtime-snapshot.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().trim().min(1),
  providerCode: z.string().trim().min(1).optional(),
  description: z.string().trim().optional(),
  enabled: z.boolean().optional(),
  groupType: z.enum(['personal', 'high_concurrency']).optional(),
  schedulingPolicy: z.record(z.unknown()).optional()
})

groupsRouter.get('/', async (req, res, next) => {
  try {
    const page = listGroupsPage(getRequestAccessScope(req.query.systemAccountId), parseGroupListOptions(req.query))
    res.json(ok(await applyServerAccountConcurrencyToGroupList(page)))
  } catch (error) {
    next(error)
  }
})

function parseGroupListOptions(query: Record<string, unknown>) {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize)
  }
}

groupsRouter.get('/options', (req, res, next) => {
  try {
    res.json(ok(listGroupOptions(getRequestAccessScope(req.query.systemAccountId), parseGroupOptionListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/account-options', (req, res, next) => {
  try {
    res.json(ok(listAccountGroupOptions(getRequestAccessScope(req.query.systemAccountId), parseGroupOptionListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

function parseGroupOptionListOptions(query: Record<string, unknown>) {
  const ids = queryTextList(query.ids, 50)
  return {
    ids,
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    limit: optionLimitValue(integerQueryValue(query.limit)),
    manageableOnly: booleanQueryValue(query.manageableOnly),
    preferDefault: booleanQueryValue(query.preferDefault)
  }
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
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

groupsRouter.post('/', mutationGuard({
  operationKey: 'groups.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')) || 'openai',
    name: normalizedText(bodyField(req, 'name'))
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = groupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('分组参数无效'))
    return
  }
  const providerCode = parsed.data.providerCode?.trim() || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
    return
  }
  try {
    const group = runLoggedOperation(() => {
      const group = createGroup({ ...parsed.data, providerCode }, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(group as unknown as Record<string, unknown>, requestAccess)
      return {
        result: group,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'groups',
          action: 'create',
          operationKey: 'groups.create',
          resourceType: 'group',
          resourceId: group.id,
          resourceName: group.name,
          summary: `创建分组：${group.name}`,
          changes: [
            safeChange('name', '名称', undefined, group.name),
            safeChange('providerCode', '供应商', undefined, group.providerCode),
            safeChange('groupType', '分组类型', undefined, group.groupType),
            safeChange('enabled', '启用状态', undefined, group.enabled)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(201).json(ok(group))
  } catch (error) {
    const message = error instanceof Error ? error.message : '创建分组失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

groupsRouter.patch('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const providerCode = typeof (req.body as Record<string, unknown>).providerCode === 'string'
    ? String((req.body as Record<string, unknown>).providerCode).trim()
    : undefined
  if (providerCode) {
    const provider = listProviders().find((item) => item.code === providerCode)
    if (!provider) {
      res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
      return
    }
    if (!provider.enabled) {
      res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
      return
    }
  }
  const before = findGroupSummary(req.params.id, requestAccess)
  try {
    const group = runLoggedOperation(() => {
      const group = updateGroup(req.params.id, req.body as Record<string, unknown>, requestAccess)
      if (!group) {
        throw new Error('分组不存在')
      }
      const ownerSystemAccountId = resolveOperationOwner(group as unknown as Record<string, unknown>, requestAccess)
      return {
        result: group,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'groups',
          action: 'update',
          operationKey: 'groups.update',
          resourceType: 'group',
          resourceId: group.id,
          resourceName: group.name,
          summary: `更新分组：${group.name}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, group as unknown as Record<string, unknown>, {
            name: '名称',
            providerCode: '供应商',
            description: '说明',
            groupType: '分组类型',
            schedulingPolicy: '调度策略',
            enabled: '启用状态'
          }),
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(group))
  } catch (error) {
    if (error instanceof DefaultGroupReadonlyError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (error instanceof Error && error.message === '分组不存在') {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新分组失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
    return
  }
})

groupsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findGroupSummary(req.params.id, requestAccess)
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    runLoggedOperation(() => {
      const deleteResult = deleteGroup(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('分组不存在')
      }
      const affectedApiKeyRoutes = deleteResult.affectedApiKeyRoutes
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'groups',
          action: 'delete',
          operationKey: 'groups.delete',
          resourceType: 'group',
          resourceId: req.params.id,
          resourceName: before?.name ?? req.params.id,
          summary: `删除分组：${before?.name ?? req.params.id}`,
          changes: [
            safeChange('deleted', '删除状态', false, true),
            ...(affectedApiKeyRoutes.length
              ? [safeChange('affectedApiKeyRoutes', '影响的 API Key 路由', undefined, summarizeDeletedGroupApiKeyRouteChanges(affectedApiKeyRoutes))]
              : [])
          ],
          targets: affectedApiKeyRoutes.slice(0, 20).map((route) => ({
            targetType: 'api_key',
            targetId: route.apiKeyId,
            targetName: route.apiKeyName,
            targetOwnerSystemAccountId: ownerSystemAccountId,
            relation: 'affected' as const
          })),
          metadata: affectedApiKeyRoutes.length ? {
            affectedApiKeyRouteCount: affectedApiKeyRoutes.length,
            affectedApiKeyRoutes: affectedApiKeyRoutes.slice(0, 20)
          } : undefined,
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(204).send()
  } catch (error) {
    if (error instanceof Error && error.message === '分组不存在') {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除分组失败'))
  }
})

function summarizeDeletedGroupApiKeyRouteChanges(changes: DeletedGroupApiKeyRouteChange[]): string {
  const sample = changes.slice(0, 3).map((change) => {
    const removedGroupName = change.removedGroupName || change.removedGroupId
    const removedText = change.removedBindingStatus === 'disabled'
      ? `移除停用号池 ${removedGroupName}`
      : `移除号池 ${removedGroupName}`
    return `${change.apiKeyName}：${removedText}`
  }).join('；')
  return changes.length > 3 ? `${sample}；另有 ${changes.length - 3} 个 API Key 受影响` : sample
}
