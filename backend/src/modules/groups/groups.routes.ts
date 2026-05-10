import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { DefaultGroupReadonlyError, createGroup, deleteGroup, listGroups, listProviders, updateGroup } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { diffSafeFields, operationMode, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().min(1),
  providerCode: z.string().min(1).optional(),
  description: z.string().optional(),
  enabled: z.boolean().optional()
})

groupsRouter.get('/', (req, res) => {
  res.json(ok(listGroups(getRequestAccessScope(req.query.systemAccountId))))
})

groupsRouter.post('/', (req, res) => {
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
  const before = listGroups(requestAccess).find((item) => item.id === req.params.id)
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
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新分组失败'))
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
  const before = listGroups(requestAccess).find((item) => item.id === req.params.id)
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
