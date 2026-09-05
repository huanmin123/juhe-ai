import { Router } from 'express'
import { z } from 'zod'

import type { GroupListItem } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { rfc3339InstantSchema } from '../../shared/zod-rfc3339.js'
import { DefaultGroupReadonlyError, GroupPatchConflictError, createGroupWithReceiptAsync, deleteGroupAsync, findGroupEditDetailAsync, findGroupSummaryAsync, findProviderOptionByCodeAsync, listAccountGroupOptionsAsync, listGroupAuthorizationOptionsAsync, listGroupItemsPageAsync, listGroupOptionsAsync, listGroupSelectOptionsAsync, listRouteStrategyGroupOptionsAsync, patchGroupAsync, returnGroupAuthorizationForGranteeAsync, type DeletedGroupRouteStrategyChange, type GroupCreateStorageReceipt } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, runLoggedOperationAsync, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import { hydrateGroupListPage } from './group-status-snapshot.service.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().trim().min(1),
  providerCode: z.string().trim().min(1),
  description: z.string().trim().optional(),
  enabled: z.boolean().optional(),
  groupType: z.enum(['personal', 'high_concurrency']).optional(),
  schedulingPolicy: z.object({
    defaultSoftConcurrency: z.number().int().min(1).max(1_000_000).optional(),
    maxQueueWaitMs: z.number().int().min(1).max(3_600_000).optional(),
    clientIpConcurrencyLimit: z.number().int().min(0).max(1_000_000).optional(),
    clientIpConcurrencyOverflowMode: z.enum(['reject', 'queue']).optional(),
    imageLaneMaxConcurrency: z.number().int().min(0).max(1_000_000).optional()
  }).strict().optional()
}).strict()
const groupPatchSchema = groupSchema.partial().extend({
  expectedUpdatedAt: rfc3339InstantSchema('分组版本格式不正确')
}).refine((value) => Object.keys(value).some((key) => key !== 'expectedUpdatedAt'), {
  message: '请提供要修改的分组内容'
})

groupsRouter.get('/', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const page = await listGroupItemsPageAsync(access, parseGroupListOptions(req.query))
    res.json(ok(await hydrateGroupListPage(access, page)))
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

groupsRouter.get('/options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = parseGroupOptionListOptions(req.query)
    if (!query.purpose) {
      res.status(400).json(badRequest('分组选项 purpose 仅支持 select 或 account'))
      return
    }
    const options = query.purpose === 'account'
      ? await listGroupOptionsAsync(access, query)
      : await listGroupSelectOptionsAsync(access, query)
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/authorization-options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = parseGroupOptionListOptions(req.query)
    const options = await listGroupAuthorizationOptionsAsync(access, query)
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/account-options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = parseGroupOptionListOptions(req.query)
    const options = await listAccountGroupOptionsAsync(access, query)
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/route-strategy-options', async (req, res, next) => {
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const options = await listRouteStrategyGroupOptionsAsync(access, parseRouteStrategyGroupOptionListOptions(req.query))
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/:id/edit-basic', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    const group = await findGroupEditDetailAsync(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!group) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    res.json(ok(group))
  } catch (error) {
    next(error)
  }
})

groupsRouter.get('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    const group = await findGroupSummaryAsync(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!group) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    res.json(ok(group))
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
    preferDefault: booleanQueryValue(query.preferDefault),
    purpose: groupOptionPurpose(query.purpose)
  }
}

function parseRouteStrategyGroupOptionListOptions(query: Record<string, unknown>) {
  return {
    ids: queryTextList(query.ids, 50),
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    limit: optionLimitValue(integerQueryValue(query.limit))
  }
}

function groupOptionPurpose(value: unknown): 'select' | 'account' | undefined {
  const purpose = optionalQueryText(value)
  if (!purpose || purpose === 'select') return 'select'
  return purpose === 'account' ? 'account' : undefined
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
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    name: normalizedText(bodyField(req, 'name'))
  })
}), async (req, res, next) => {
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
  const providerCode = parsed.data.providerCode.trim()
  let provider: Awaited<ReturnType<typeof findProviderOptionByCodeAsync>>
  try {
    provider = await findProviderOptionByCodeAsync(providerCode)
  } catch (error) {
    next(error)
    return
  }
  if (!provider) {
    res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
    return
  }
  try {
    const group = await runLoggedOperationAsync(async () => {
      const receipt = await createGroupWithReceiptAsync({ ...parsed.data, providerCode }, requestAccess)
      const group = receipt.group
      const ownerSystemAccountId = receipt.ownerSystemAccountId
      return {
        result: groupCreateListItem(receipt),
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

function groupCreateListItem(receipt: GroupCreateStorageReceipt): GroupListItem {
  const { group, ownerSystemAccountId, updatedAt } = receipt
  return {
    id: group.id,
    systemAccountId: group.systemAccountId,
    systemAccountName: group.systemAccountName,
    ownerSystemAccountId,
    ownerSystemAccountName: group.systemAccountName,
    name: group.name,
    providerCode: group.providerCode,
    description: group.description,
    enabled: group.enabled,
    isDefault: false,
    groupType: group.groupType,
    accessType: 'owner',
    updatedAt,
    accountStats: {
      total: group.accountStats.total,
      available: group.accountStats.available,
      active: group.accountStats.active,
      disabled: group.accountStats.disabled,
      error: group.accountStats.error,
      rateLimited: group.accountStats.rateLimited,
      concurrencyLimit: group.accountStats.concurrencyLimit,
      currentConcurrency: 0
    },
    canEdit: true,
    canDelete: true,
    canReturn: false
  }
}

groupsRouter.patch('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = groupPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('分组参数无效'))
    return
  }
  try {
    const mutation = await runLoggedOperationAsync(async () => {
      const mutation = await patchGroupAsync(req.params.id, parsed.data as Record<string, unknown>, requestAccess)
      if (!mutation) {
        throw new Error('分组不存在')
      }
      const operationScopeSystemAccountId = mutation.accessType === 'authorized'
        ? effectiveRequestSystemAccountId(requestAccess)
        : mutation.ownerSystemAccountId
      return {
        result: mutation,
        log: mutation.changedFields.length ? {
          operationScopeSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'groups',
          action: 'update',
          operationKey: 'groups.update',
          resourceType: 'group',
          resourceId: mutation.id,
          resourceName: mutation.name,
          summary: mutation.accessType === 'authorized' ? `更新授权分组使用配置：${mutation.name}` : `更新分组：${mutation.name}`,
          changes: mutation.changes.map((change) => safeChange(change.field, groupPatchFieldLabel(change.field), change.before, change.after)),
          viewers: viewer(operationScopeSystemAccountId, mutation.accessType === 'authorized' ? 'authorization_grantee' : 'resource_owner')
        } : undefined
      }
    }, req)
    res.json(ok({ id: mutation.id, changedFields: mutation.changedFields, updatedAt: mutation.updatedAt }))
  } catch (error) {
    if (error instanceof DefaultGroupReadonlyError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (error instanceof GroupPatchConflictError) {
      res.status(409).json(badRequest(error.message))
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

function groupPatchFieldLabel(field: string): string {
  return ({
    name: '名称',
    providerCode: '供应商',
    description: '说明',
    groupType: '分组类型',
    schedulingPolicy: '调度策略',
    enabled: '启用状态'
  } as Record<string, string>)[field] ?? field
}

groupsRouter.post('/:id/return-authorization', mutationGuard({
  operationKey: 'groups.return_authorization',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    groupId: normalizedText(req.params.id),
    grantee: normalizedText(queryField(req, 'systemAccountId'))
  })
}), async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  try {
    await runLoggedOperationAsync(async () => {
      const authorization = await returnGroupAuthorizationForGranteeAsync(req.params.id, requestAccess)
      if (!authorization) {
        throw new Error('授权分组不存在或不可归还')
      }
      const resourceName = authorization.resource_name
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: authorization.grantee_system_account_id,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'return',
          operationKey: 'groups.return_authorization',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName,
          summary: `归还授权分组：${resourceName}`,
          changes: [safeChange('returned', '归还授权分组', false, true)],
          targets: [
            {
              targetType: authorization.resource_type,
              targetId: authorization.resource_id,
              targetName: resourceName,
              targetOwnerSystemAccountId: authorization.resource_owner_system_account_id,
              relation: 'owner' as const
            },
            {
              targetType: 'system_account',
              targetId: authorization.grantee_system_account_id,
              targetOwnerSystemAccountId: authorization.grantee_system_account_id,
              relation: 'grantee' as const
            }
          ],
          viewers: viewers(
            viewer(authorization.resource_owner_system_account_id, 'authorization_owner'),
            viewer(authorization.grantee_system_account_id, 'authorization_grantee')
          )
        }
      }
    }, req)
    res.status(204).send()
  } catch (error) {
    if (error instanceof Error && error.message === '授权分组不存在或不可归还') {
      res.status(404).json({ message: '授权分组不存在或不可归还' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权分组失败'))
  }
})

groupsRouter.delete('/:id', async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  try {
    await runLoggedOperationAsync(async () => {
      const deleteResult = await deleteGroupAsync(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('分组不存在')
      }
      const ownerSystemAccountId = deleteResult.ownerSystemAccountId
      const resourceName = deleteResult.name ?? req.params.id
      const affectedRouteStrategies = deleteResult.affectedRouteStrategies
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
          resourceName,
          summary: `删除分组：${resourceName}`,
          changes: [
            safeChange('deleted', '删除状态', false, true),
            ...(affectedRouteStrategies.length
              ? [safeChange('affectedRouteStrategies', '影响的策略路由', undefined, summarizeDeletedGroupRouteStrategyChanges(affectedRouteStrategies))]
              : [])
          ],
          targets: affectedRouteStrategies.slice(0, 20).map((route) => ({
            targetType: 'route_strategy',
            targetId: route.routeStrategyId,
            targetName: route.routeStrategyName,
            targetOwnerSystemAccountId: ownerSystemAccountId,
            relation: 'affected' as const
          })),
          metadata: affectedRouteStrategies.length ? {
            affectedRouteStrategyCount: affectedRouteStrategies.length,
            affectedRouteStrategies: affectedRouteStrategies.slice(0, 20)
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

function summarizeDeletedGroupRouteStrategyChanges(changes: DeletedGroupRouteStrategyChange[]): string {
  const sample = changes.slice(0, 3).map((change) => {
    const removedGroupName = change.removedGroupName || change.removedGroupId
    const removedText = change.removedBindingStatus === 'disabled'
      ? `移除停用分组 ${removedGroupName}`
      : `移除分组 ${removedGroupName}`
    return `${change.routeStrategyName}：${removedText}`
  }).join('；')
  return changes.length > 3 ? `${sample}；另有 ${changes.length - 3} 个策略路由受影响` : sample
}

function effectiveRequestSystemAccountId(access: ReturnType<typeof getRequestAccessScope>): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}
