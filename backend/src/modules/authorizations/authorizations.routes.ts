import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import { getAuthorizationTeamUsageOverview, getAuthorizationUserUsageOverview } from '../../storage/authorization-usage.repository.js'
import {
  createResourceAuthorization,
  findResourceAuthorization,
  getResourceAuthorizationUsage,
  listResourceAuthorizations,
  listResourceAuthorizationsPage,
  revokeResourceAuthorization,
  returnResourceAuthorizationForGrantee,
  updateResourceAuthorization
} from '../../storage/repositories.js'
import { getDatabase } from '../../storage/database.js'
import { normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'
import { getRequestAccessScope, getRequestAuthContext } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, textValue } from '../deduplication/mutation-guard.middleware.js'
import { diffSafeFields, operationMode, ownerTarget, runLoggedOperation, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import type { ResourceAuthorizationSummary } from '../../domain/types.js'

export const authorizationsRouter = Router()

const authorizationIdParamsSchema = z.object({
  id: z.string().trim().min(1, '授权记录 ID 不能为空')
})

const authorizationsQuerySchema = z.object({
  resourceType: z.enum(['account', 'group']).optional(),
  resourceId: z.string().trim().min(1, '授权资源 ID 不能为空').optional(),
  granteeSystemAccountId: z.string().trim().min(1, '被授权用户 ID 不能为空').optional(),
  teamId: z.string().trim().min(1, '团队 ID 不能为空').optional(),
  status: z.enum(['active', 'paused', 'expired', 'revoked', 'returned', 'all']).optional(),
  direction: z.enum(['all', 'outbound', 'inbound']).optional(),
  sourceType: z.enum(['all', 'manual', 'team']).optional(),
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional(),
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional(),
  page: z.coerce.number().int().min(1, '页码必须大于 0').optional(),
  pageSize: z.coerce.number().int().min(1, '每页数量必须大于 0').max(500, '每页最多 500 条').optional()
})

const authorizationUsageQuerySchema = z.object({
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional(),
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional(),
  page: z.coerce.number().int().min(1, '页码必须大于 0').optional(),
  pageSize: z.coerce.number().int().min(1, '每页数量必须大于 0').optional()
})

const authorizationUsageOverviewQuerySchema = z.object({
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional(),
  resourceType: z.enum(['account', 'group']).optional(),
  resourceId: z.string().trim().min(1, '授权资源 ID 不能为空').optional(),
  granteeSystemAccountId: z.string().trim().min(1, '被授权用户 ID 不能为空').optional(),
  teamId: z.string().trim().min(1, '团队 ID 不能为空').optional(),
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional(),
  page: z.coerce.number().int().min(1, '页码必须大于 0').optional(),
  pageSize: z.coerce.number().int().min(1, '每页数量必须大于 0').max(200, '每页最多 200 条').optional()
})

const createAuthorizationSchema = z.object({
  resourceType: z.enum(['account', 'group']),
  resourceId: z.string().trim().min(1, '授权资源不能为空'),
  granteeType: z.enum(['system_account', 'team']),
  granteeId: z.string().trim().min(1, '被授权对象不能为空'),
  targetGroupId: z.string().trim().min(1, '目标分组不能为空').optional(),
  remark: z.string().trim().max(200).optional(),
  expiresAt: z.string().trim().refine((value) => !Number.isNaN(Date.parse(value)), '过期时间格式不正确').optional(),
  limits: z.record(z.string(), z.unknown()).optional(),
  modelPolicy: z.record(z.string(), z.unknown()).optional()
})

const updateAuthorizationSchema = z.object({
  status: z.enum(['active', 'paused']).optional(),
  expiresAt: z.union([
    z.string().trim().refine((value) => !Number.isNaN(Date.parse(value)), '过期时间格式不正确'),
    z.null()
  ]).optional(),
  limits: z.record(z.string(), z.unknown()).nullable().optional()
}).refine((value) => Object.prototype.hasOwnProperty.call(value, 'status') || Object.prototype.hasOwnProperty.call(value, 'expiresAt') || Object.prototype.hasOwnProperty.call(value, 'limits'), {
  message: '请提供要修改的授权内容'
})

const updateAuthorizationExpireSchema = z.object({
  expiresAt: z.union([
    z.string().trim().refine((value) => !Number.isNaN(Date.parse(value)), '过期时间格式不正确'),
    z.null()
  ]),
  limits: z.record(z.string(), z.unknown()).nullable().optional()
})

const revokeAuthorizationSchema = z.object({
  sourceType: z.enum(['manual', 'team']).optional(),
  sourceTeamId: z.string().trim().min(1).optional(),
  revokeAll: z.boolean().optional()
}).superRefine((value, ctx) => {
  if (value.revokeAll) {
    return
  }
  if (value.sourceType === 'team' && !value.sourceTeamId) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['sourceTeamId'],
      message: '回收团队来源授权时必须提供团队 ID'
    })
  }
})

authorizationsRouter.get('/', (req, res) => {
  const parsed = parseOrBadRequest(authorizationsQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  const { systemAccountId, direction, sourceType, startDate, endDate, page, pageSize, ...filters } = parsed.data
  const usageRange = normalizeAuthorizationListUsageRange({ startDate, endDate })
  const sourceTypeFilter = sourceType && sourceType !== 'all' ? { sourceType } : {}
  const routeFilters = req.baseUrl.endsWith('/my-authorizations') && direction && direction !== 'all'
    ? { ...filters, ...sourceTypeFilter, direction }
    : { ...filters, ...sourceTypeFilter }
  if (page !== undefined || pageSize !== undefined) {
    res.json(ok(listResourceAuthorizationsPage(routeFilters, getRequestAccessScope(systemAccountId), { usageRange, page, pageSize })))
    return
  }
  res.json(ok(listResourceAuthorizations(routeFilters, getRequestAccessScope(systemAccountId), { usageRange })))
})

authorizationsRouter.get('/usage/team-details', (req, res) => {
  const parsed = parseOrBadRequest(authorizationUsageOverviewQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  const { systemAccountId, startDate, endDate, page, pageSize, ...filters } = parsed.data
  const range = normalizeAuthorizationUsageRange({ startDate, endDate })
  res.json(ok(getAuthorizationTeamUsageOverview(filters, getRequestAccessScope(systemAccountId), range, { page, pageSize })))
})

authorizationsRouter.get('/usage/user-details', (req, res) => {
  const parsed = parseOrBadRequest(authorizationUsageOverviewQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  const { systemAccountId, startDate, endDate, page, pageSize, ...filters } = parsed.data
  const range = normalizeAuthorizationUsageRange({ startDate, endDate })
  res.json(ok(getAuthorizationUserUsageOverview(filters, getRequestAccessScope(systemAccountId), range, { page, pageSize })))
})

authorizationsRouter.post('/', mutationGuard({
  operationKey: 'authorizations.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    resourceType: textValue(bodyField(req, 'resourceType')),
    resourceId: textValue(bodyField(req, 'resourceId')),
    granteeType: textValue(bodyField(req, 'granteeType')),
    granteeId: textValue(bodyField(req, 'granteeId')),
    targetGroupId: textValue(bodyField(req, 'targetGroupId'))
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const parsed = parseOrBadRequest(createAuthorizationSchema, req.body, '授权参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  const authContext = getRequestAuthContext()
  if (authContext?.role === 'admin' && req.baseUrl.endsWith('/authorizations') && !scopeQuery.data.systemAccountId) {
    sendBadRequest(res, '管理员新增授权时必须指定授权人')
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const authorization = createResourceAuthorization(parsed.data, requestAccess)
      return {
        result: authorization,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'create',
          operationKey: 'authorizations.create',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `创建资源授权：${authorization.resourceName ?? authorization.resourceId} -> ${authorizationGranteeName(authorization)}`,
          changes: [
            safeChange('resourceType', '资源类型', undefined, authorization.resourceType),
            safeChange('resourceId', '授权资源', undefined, authorization.resourceName ?? authorization.resourceId),
            safeChange('grantee', '被授权目标', undefined, authorizationGranteeName(authorization)),
            safeChange('targetGroupId', '目标分组', undefined, parsed.data.targetGroupId),
            safeChange('status', '状态', undefined, authorization.status),
            safeChange('expiresAt', '过期时间', undefined, authorization.expiresAt),
            safeChange('limits', '额度限制', undefined, authorization.limits),
            safeChange('modelPolicy', '模型策略', undefined, authorization.modelPolicy)
          ],
          targets: authorizationTargets(authorization),
          viewers: authorizationViewers(authorization)
        }
      }
    }, req)
    res.status(201).json(ok(authorization))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建授权失败'))
  }
})

authorizationsRouter.delete('/:id/return', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(authorizationIdParamsSchema, req.params, '授权记录 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    runLoggedOperation(() => {
      const authorization = returnResourceAuthorizationForGrantee(paramsParsed.data.id, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: authorization.grantee_system_account_id,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'return',
          operationKey: 'authorizations.return',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resource_id,
          summary: `归还授权使用权：${authorization.resource_id}`,
          changes: [safeChange('returned', '归还授权', false, true)],
          targets: [
            ownerTarget({
              targetType: authorization.resource_type,
              targetId: authorization.resource_id,
              ownerSystemAccountId: authorization.resource_owner_system_account_id,
              relation: 'owner'
            }),
            ownerTarget({
              targetType: 'system_account',
              targetId: authorization.grantee_system_account_id,
              ownerSystemAccountId: authorization.grantee_system_account_id,
              relation: 'grantee'
            })
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
    if (error instanceof Error && error.message === '授权记录不存在') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权使用权失败'))
  }
})

authorizationsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(authorizationIdParamsSchema, req.params, '授权记录 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const payload = {
    sourceType: req.body?.sourceType ?? req.query.sourceType,
    sourceTeamId: req.body?.sourceTeamId ?? req.query.sourceTeamId,
    revokeAll: req.body?.revokeAll ?? (req.query.revokeAll === 'true' ? true : req.query.revokeAll === 'false' ? false : undefined)
  }
  const parsed = parseOrBadRequest(revokeAuthorizationSchema, payload, '回收授权参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const before = findResourceAuthorization(paramsParsed.data.id, requestAccess, { includeUsage: false })
      const authorization = revokeResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'revoke',
          operationKey: 'authorizations.revoke',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `回收资源授权：${authorization.resourceName ?? authorization.resourceId} -> ${authorizationGranteeName(authorization)}`,
          changes: [
            ...diffSafeFields(before as unknown as Record<string, unknown> | undefined, authorization as unknown as Record<string, unknown>, {
              status: '状态',
              expiresAt: '过期时间',
              limits: '额度限制'
            }),
            safeChange('revoked', '回收状态', false, true)
          ],
          targets: authorizationTargets(authorization),
          viewers: authorizationViewers(authorization)
        }
      }
    }, req)
    res.json(ok(authorization))
  } catch (error) {
    if (error instanceof Error && error.message === '授权记录不存在') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '回收授权失败'))
  }
})

authorizationsRouter.patch('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(authorizationIdParamsSchema, req.params, '授权记录 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const parsed = parseOrBadRequest(updateAuthorizationSchema, req.body, '修改授权参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const before = findResourceAuthorization(paramsParsed.data.id, requestAccess, { includeUsage: false })
      const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'update',
          operationKey: 'authorizations.update',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `更新资源授权：${authorization.resourceName ?? authorization.resourceId}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, authorization as unknown as Record<string, unknown>, {
            status: '状态',
            expiresAt: '过期时间',
            limits: '额度限制'
          }),
          targets: authorizationTargets(authorization),
          viewers: authorizationViewers(authorization)
        }
      }
    }, req)
    res.json(ok(authorization))
  } catch (error) {
    if (error instanceof Error && error.message === '授权记录不存在') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '修改授权失败'))
  }
})

authorizationsRouter.patch('/:id/expire', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(authorizationIdParamsSchema, req.params, '授权记录 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const parsed = parseOrBadRequest(updateAuthorizationExpireSchema, req.body, '修改授权参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const before = findResourceAuthorization(paramsParsed.data.id, requestAccess, { includeUsage: false })
      const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'update_expire',
          operationKey: 'authorizations.update_expire',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `更新授权有效期：${authorization.resourceName ?? authorization.resourceId}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, authorization as unknown as Record<string, unknown>, {
            expiresAt: '过期时间',
            limits: '额度限制',
            status: '状态'
          }),
          targets: authorizationTargets(authorization),
          viewers: authorizationViewers(authorization)
        }
      }
    }, req)
    res.json(ok(authorization))
  } catch (error) {
    if (error instanceof Error && error.message === '授权记录不存在') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '修改授权失败'))
  }
})

authorizationsRouter.get('/:id/usage', (req, res) => {
  const queryParsed = parseOrBadRequest(authorizationUsageQuerySchema, req.query, '查询参数不合法')
  if (!queryParsed.success) {
    sendBadRequest(res, queryParsed.message)
    return
  }
  const paramsParsed = parseOrBadRequest(authorizationIdParamsSchema, req.params, '授权记录 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const authorization = getResourceAuthorizationUsage(
    paramsParsed.data.id,
    getRequestAccessScope(queryParsed.data.systemAccountId),
    {
      range: normalizeAccountUsageStatsRange({
        startDate: queryParsed.data.startDate,
        endDate: queryParsed.data.endDate
      }, usageStatsTimezone()),
      page: queryParsed.data.page,
      pageSize: queryParsed.data.pageSize
    }
  )
  if (!authorization) {
    sendNotFound(res, '授权记录不存在')
    return
  }
  res.json(ok(authorization))
})

function authorizationTargets(authorization: ReturnType<typeof listResourceAuthorizations>[number]) {
  const targets = [
    ownerTarget({
      targetType: authorization.resourceType,
      targetId: authorization.resourceId,
      targetName: authorization.resourceName,
      ownerSystemAccountId: authorization.resourceOwnerSystemAccountId,
      relation: 'owner'
    })
  ]
  if (authorization.granteeType === 'team') {
    targets.push(ownerTarget({
      targetType: 'system_team',
      targetId: authorization.granteeTeamId,
      targetName: authorization.granteeTeamName,
      relation: 'grantee'
    }))
    return targets
  }
  targets.push(ownerTarget({
    targetType: 'system_account',
    targetId: authorization.granteeSystemAccountId,
    targetName: authorization.granteeSystemAccountName ?? authorization.granteeUsername,
    ownerSystemAccountId: authorization.granteeSystemAccountId,
    relation: 'grantee'
  }))
  return targets
}

function authorizationViewers(authorization: ReturnType<typeof listResourceAuthorizations>[number]) {
  return viewers(
    viewer(authorization.resourceOwnerSystemAccountId, 'authorization_owner'),
    viewer(authorization.granteeType === 'system_account' ? authorization.granteeSystemAccountId : undefined, 'authorization_grantee')
  )
}

function authorizationGranteeName(authorization: ResourceAuthorizationSummary): string {
  if (authorization.granteeType === 'team') {
    return authorization.granteeTeamName ?? '团队'
  }
  return authorization.granteeSystemAccountName ?? '被授权用户'
}

function normalizeAuthorizationListUsageRange(input: { startDate?: string; endDate?: string }) {
  const timezone = usageStatsTimezone()
  const today = todayDateKey(timezone)
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}

function normalizeAuthorizationUsageRange(input: { startDate?: string; endDate?: string }) {
  const timezone = usageStatsTimezone()
  const today = todayDateKey(timezone)
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({
    startDate,
    endDate
  }, timezone)
}
