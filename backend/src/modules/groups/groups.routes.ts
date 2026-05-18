import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { DefaultGroupReadonlyError, createGroup, deleteGroup, findGroupSummary, listAccountGroupOptions, listGroupOptions, listGroups, listProviders, updateGroup } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { applyServerAccountConcurrencyToGroups } from '../gateway/gateway-runtime-snapshot.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().trim().min(1),
  providerCode: z.string().trim().min(1).optional(),
  description: z.string().trim().optional(),
  enabled: z.boolean().optional()
})

groupsRouter.get('/', async (req, res, next) => {
  try {
    const groups = listGroups(getRequestAccessScope(req.query.systemAccountId))
    res.json(ok(await applyServerAccountConcurrencyToGroups(groups)))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/options', (req, res, next) => {
  try {
    res.json(ok(listGroupOptions(getRequestAccessScope(req.query.systemAccountId))))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/account-options', (req, res, next) => {
  try {
    res.json(ok(listAccountGroupOptions(getRequestAccessScope(req.query.systemAccountId))))
  } catch (error) {
    next(error)
  }
})

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
        afterCommit: clearGatewayRuntimeCache,
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
        afterCommit: clearGatewayRuntimeCache,
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
      if (!deleteGroup(req.params.id, requestAccess)) {
        throw new Error('分组不存在')
      }
      return {
        result: true,
        afterCommit: clearGatewayRuntimeCache,
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
          changes: [safeChange('deleted', '删除状态', false, true)],
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
