import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import { rfc3339InstantSchema } from '../../shared/zod-rfc3339.js'
import { getAuthorizationTeamUsageRowsAsync, getAuthorizationTeamUsageSummaryAsync, getAuthorizationUserUsageRowsAsync, getAuthorizationUserUsageSummaryAsync } from '../../storage/authorization-usage.repository.js'
import {
  createResourceAuthorizationMutationAsync,
  findResourceAuthorizationAsync,
  getResourceAuthorizationUsageAsync,
  listResourceAuthorizationsPageAsync,
  patchResourceAuthorizationAsync,
  revokeResourceAuthorizationMutationAsync,
  returnResourceAuthorizationForGranteeMutationAsync
} from '../../storage/repositories.js'
import { normalizeAccountUsageStatsRange, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDefaultRange } from '../../storage/usage-stats-window-helpers.js'
import { getRequestAccessScope, getRequestAuthContext } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, textValue } from '../deduplication/mutation-guard.middleware.js'
import { diffSafeFields, operationMode, ownerTarget, runLoggedOperationAsync, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import { requestQuotaLimitsSchema } from '../request-quota-limit.schema.js'
import { isAdminRole, type ResourceAuthorizationListItem, type ResourceAuthorizationSummary } from '../../domain/types.js'

export const authorizationsRouter = Router()

const authorizationExpiresAtSchema = rfc3339InstantSchema('过期时间格式不正确')

const authorizationIdParamsSchema = z.object({
  id: z.string().trim().min(1, '授权记录 ID 不能为空')
})

const authorizationMutationVersionSchema = z.object({
  expectedUpdatedAt: rfc3339InstantSchema('授权配置版本格式不正确')
}).strict()

const authorizationsQuerySchema = z.object({
  keyword: z.string().trim().max(120, '搜索关键字最多 120 个字符').optional(),
  resourceType: z.enum(['account', 'group']).optional(),
  resourceId: z.string().trim().min(1, '授权资源 ID 不能为空').optional(),
  resourceOwnerSystemAccountId: z.string().trim().min(1, '资源归属用户 ID 不能为空').optional(),
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

const authorizationUsageCommonQueryShape = {
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional(),
  resourceType: z.enum(['account', 'group']).optional(),
  resourceId: z.string().trim().min(1, '授权资源 ID 不能为空').optional(),
  teamId: z.string().trim().min(1, '团队 ID 不能为空').optional(),
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional()
}

const authorizationUsageTeamRowsQuerySchema = z.object({
  ...authorizationUsageCommonQueryShape,
  page: z.coerce.number().int().min(1, '页码必须大于 0').optional(),
  pageSize: z.coerce.number().int().min(1, '每页数量必须大于 0').max(200, '每页最多 200 条').optional()
}).strict().refine(validAuthorizationUsageResourceFilter, { path: ['resourceId'], message: '按资源筛选时必须指定资源类型' })

const authorizationUsageUserRowsQuerySchema = z.object({
  ...authorizationUsageCommonQueryShape,
  granteeSystemAccountId: z.string().trim().min(1, '被授权用户 ID 不能为空').optional(),
  page: z.coerce.number().int().min(1, '页码必须大于 0').optional(),
  pageSize: z.coerce.number().int().min(1, '每页数量必须大于 0').max(200, '每页最多 200 条').optional()
}).strict().refine(validAuthorizationUsageResourceFilter, { path: ['resourceId'], message: '按资源筛选时必须指定资源类型' })

const authorizationUsageTeamSummaryQuerySchema = z.object(authorizationUsageCommonQueryShape)
  .strict()
  .refine(validAuthorizationUsageResourceFilter, { path: ['resourceId'], message: '按资源筛选时必须指定资源类型' })

const authorizationUsageUserSummaryQuerySchema = z.object({
  ...authorizationUsageCommonQueryShape,
  granteeSystemAccountId: z.string().trim().min(1, '被授权用户 ID 不能为空').optional()
}).strict().refine(validAuthorizationUsageResourceFilter, { path: ['resourceId'], message: '按资源筛选时必须指定资源类型' })

function validAuthorizationUsageResourceFilter(value: { resourceType?: string; resourceId?: string }): boolean {
  return !value.resourceId || Boolean(value.resourceType)
}

const createAuthorizationSchema = z.object({
  resourceType: z.enum(['account', 'group']),
  resourceId: z.string().trim().min(1, '授权资源不能为空'),
  granteeType: z.enum(['system_account', 'team']),
  granteeId: z.string().trim().min(1, '被授权对象不能为空'),
  targetGroupId: z.string().trim().min(1, '目标分组不能为空').optional(),
  remark: z.string().trim().max(200).optional(),
  expiresAt: authorizationExpiresAtSchema.optional(),
  limits: requestQuotaLimitsSchema.optional()
}).strict().superRefine((value, ctx) => {
  if (value.resourceType === 'account' && value.granteeType === 'system_account' && !value.targetGroupId) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['targetGroupId'],
      message: '授权 AI 账户给个人时必须选择目标分组'
    })
  }
  if (value.targetGroupId && (value.resourceType !== 'account' || value.granteeType !== 'system_account')) {
    ctx.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['targetGroupId'],
      message: '只有授权 AI 账户给个人时可以指定目标分组'
    })
  }
})

const updateAuthorizationSchema = z.object({
  expectedUpdatedAt: rfc3339InstantSchema('授权配置版本格式不正确'),
  status: z.enum(['active', 'paused']).optional(),
  expiresAt: z.union([
    authorizationExpiresAtSchema,
    z.null()
  ]).optional(),
  limits: requestQuotaLimitsSchema.nullable().optional()
}).strict().refine((value) => Object.prototype.hasOwnProperty.call(value, 'status') || Object.prototype.hasOwnProperty.call(value, 'expiresAt') || Object.prototype.hasOwnProperty.call(value, 'limits'), {
  message: '请提供要修改的授权内容'
})

const updateAuthorizationExpireSchema = z.object({
  expectedUpdatedAt: rfc3339InstantSchema('授权配置版本格式不正确'),
  expiresAt: z.union([
    authorizationExpiresAtSchema,
    z.null()
  ]).optional(),
  limits: requestQuotaLimitsSchema.nullable().optional()
}).strict().refine((value) => Object.prototype.hasOwnProperty.call(value, 'expiresAt') || Object.prototype.hasOwnProperty.call(value, 'limits'), {
  message: '请提供要修改的授权内容'
})

authorizationsRouter.get('/', async (req, res, next) => {
  const parsed = parseOrBadRequest(authorizationsQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const { systemAccountId, direction, sourceType, startDate, endDate, page, pageSize, ...filters } = parsed.data
    void startDate
    void endDate
    const sourceTypeFilter = sourceType && sourceType !== 'all' ? { sourceType } : {}
    const routeFilters = req.baseUrl.endsWith('/my-authorizations') && direction && direction !== 'all'
      ? { ...filters, ...sourceTypeFilter, direction }
      : { ...filters, ...sourceTypeFilter }
    const result = await listResourceAuthorizationsPageAsync(routeFilters, getRequestAccessScope(systemAccountId), { includeUsage: false, page, pageSize })
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

authorizationsRouter.get('/usage/team-details', async (req, res, next) => {
  const parsed = parseOrBadRequest(authorizationUsageTeamRowsQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const { systemAccountId, startDate, endDate, page, pageSize, ...filters } = parsed.data
    const range = await normalizeAuthorizationUsageRangeAsync({ startDate, endDate })
    res.json(ok(await getAuthorizationTeamUsageRowsAsync(filters, getRequestAccessScope(systemAccountId), range, { page, pageSize })))
  } catch (error) {
    next(error)
  }
})

authorizationsRouter.get('/usage/team-summary', async (req, res, next) => {
  const parsed = parseOrBadRequest(authorizationUsageTeamSummaryQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) { sendBadRequest(res, parsed.message); return }
  try {
    const { systemAccountId, startDate, endDate, ...filters } = parsed.data
    const range = await normalizeAuthorizationUsageRangeAsync({ startDate, endDate })
    res.json(ok(await getAuthorizationTeamUsageSummaryAsync(filters, getRequestAccessScope(systemAccountId), range)))
  } catch (error) { next(error) }
})

authorizationsRouter.get('/usage/user-details', async (req, res, next) => {
  const parsed = parseOrBadRequest(authorizationUsageUserRowsQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const { systemAccountId, startDate, endDate, page, pageSize, ...filters } = parsed.data
    const range = await normalizeAuthorizationUsageRangeAsync({ startDate, endDate })
    res.json(ok(await getAuthorizationUserUsageRowsAsync(filters, getRequestAccessScope(systemAccountId), range, { page, pageSize })))
  } catch (error) {
    next(error)
  }
})

authorizationsRouter.get('/usage/user-summary', async (req, res, next) => {
  const parsed = parseOrBadRequest(authorizationUsageUserSummaryQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) { sendBadRequest(res, parsed.message); return }
  try {
    const { systemAccountId, startDate, endDate, ...filters } = parsed.data
    const range = await normalizeAuthorizationUsageRangeAsync({ startDate, endDate })
    res.json(ok(await getAuthorizationUserUsageSummaryAsync(filters, getRequestAccessScope(systemAccountId), range)))
  } catch (error) { next(error) }
})

authorizationsRouter.post('/', mutationGuard({
  operationKey: 'authorizations.create',
  succeededTtlMs: 0,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    resourceType: textValue(bodyField(req, 'resourceType')),
    resourceId: textValue(bodyField(req, 'resourceId')),
    granteeType: textValue(bodyField(req, 'granteeType')),
    granteeId: textValue(bodyField(req, 'granteeId')),
    targetGroupId: textValue(bodyField(req, 'targetGroupId')),
    remark: bodyField(req, 'remark'),
    expiresAt: bodyField(req, 'expiresAt'),
    limits: bodyField(req, 'limits')
  })
}), async (req, res) => {
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
  if (isAdminRole(authContext?.role) && req.baseUrl.endsWith('/authorizations') && !scopeQuery.data.systemAccountId) {
    sendBadRequest(res, '管理员新增授权时必须指定授权人')
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = await runLoggedOperationAsync(async () => {
      const mutation = await createResourceAuthorizationMutationAsync(parsed.data, requestAccess)
      const authorization = mutation.item
      return {
        result: mutation,
        log: mutation.created || mutation.previousStatus ? {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'create',
          operationKey: 'authorizations.create',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `${mutation.previousStatus ? '重新激活' : '创建'}资源授权：${authorization.resourceName ?? authorization.resourceId} -> ${authorizationGranteeName(authorization)}`,
          changes: [
            safeChange('resourceType', '资源类型', undefined, authorization.resourceType),
            safeChange('resourceId', '授权资源', undefined, authorization.resourceName ?? authorization.resourceId),
            safeChange('grantee', '被授权目标', undefined, authorizationGranteeName(authorization)),
            safeChange('targetGroupId', '目标分组', undefined, parsed.data.targetGroupId),
            safeChange('status', '状态', undefined, authorization.status),
            safeChange('expiresAt', '过期时间', undefined, authorization.expiresAt),
            safeChange('limits', '额度限制', undefined, authorization.limits)
          ],
          targets: authorizationTargets(authorization),
          viewers: authorizationViewers(authorization)
        } : undefined
      }
    }, req)
    res.status(authorization.created ? 201 : 200).json(ok(authorization))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建授权失败'))
  }
})

authorizationsRouter.get('/:id', async (req, res, next) => {
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
    const authorization = await findResourceAuthorizationAsync(
      paramsParsed.data.id,
      getRequestAccessScope(scopeQuery.data.systemAccountId),
      { includeUsage: false }
    )
    if (!authorization) {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.json(ok(authorization))
  } catch (error) {
    next(error)
  }
})

authorizationsRouter.delete('/:id/return', async (req, res) => {
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
  const bodyParsed = parseOrBadRequest(authorizationMutationVersionSchema, req.body, '归还授权参数不合法')
  if (!bodyParsed.success) {
    sendBadRequest(res, bodyParsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = await runLoggedOperationAsync(async () => {
      const outcome = await returnResourceAuthorizationForGranteeMutationAsync(
        paramsParsed.data.id,
        bodyParsed.data.expectedUpdatedAt,
        requestAccess
      )
      return {
        result: outcome,
        log: outcome.kind === 'updated' ? {
          operationScopeSystemAccountId: outcome.context.granteeSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'return',
          operationKey: 'authorizations.return',
          resourceType: 'authorization',
          resourceId: outcome.result.id,
          resourceName: outcome.context.resourceId,
          summary: `归还授权使用权：${outcome.context.resourceId}`,
          changes: [safeChange('returned', '归还授权', false, true)],
          targets: [
            ownerTarget({
              targetType: outcome.context.resourceType,
              targetId: outcome.context.resourceId,
              ownerSystemAccountId: outcome.context.resourceOwnerSystemAccountId,
              relation: 'owner'
            }),
            ownerTarget({
              targetType: 'system_account',
              targetId: outcome.context.granteeSystemAccountId,
              ownerSystemAccountId: outcome.context.granteeSystemAccountId,
              relation: 'grantee'
            })
          ],
          viewers: viewers(
            viewer(outcome.context.resourceOwnerSystemAccountId, 'authorization_owner'),
            viewer(outcome.context.granteeSystemAccountId, 'authorization_grantee')
          )
        } : undefined
      }
    }, req)
    if (authorization.kind === 'not_found') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    if (authorization.kind === 'conflict') {
      res.status(409).json({ message: '授权配置已被其他操作更新，请刷新后重试', currentUpdatedAt: authorization.currentUpdatedAt })
      return
    }
    res.status(204).send()
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权使用权失败'))
  }
})

authorizationsRouter.delete('/:id', async (req, res) => {
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
  const bodyParsed = parseOrBadRequest(authorizationMutationVersionSchema, req.body, '回收授权参数不合法')
  if (!bodyParsed.success) {
    sendBadRequest(res, bodyParsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = await runLoggedOperationAsync(async () => {
      const outcome = await revokeResourceAuthorizationMutationAsync(
        paramsParsed.data.id,
        bodyParsed.data.expectedUpdatedAt,
        requestAccess
      )
      return {
        result: outcome,
        log: outcome.kind === 'updated' ? {
          operationScopeSystemAccountId: outcome.context.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'revoke',
          operationKey: 'authorizations.revoke',
          resourceType: 'authorization',
          resourceId: outcome.result.id,
          resourceName: outcome.context.resourceId,
          summary: `回收资源授权：${outcome.context.resourceId}`,
          changes: [safeChange('status', '状态', outcome.previousStatus, 'revoked')],
          targets: authorizationPatchTargets(outcome.context),
          viewers: authorizationPatchViewers(outcome.context)
        } : undefined
      }
    }, req)
    if (authorization.kind === 'not_found') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    if (authorization.kind === 'conflict') {
      res.status(409).json({ message: '授权配置已被其他操作更新，请刷新后重试', currentUpdatedAt: authorization.currentUpdatedAt })
      return
    }
    res.json(ok(authorization.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '回收授权失败'))
  }
})

authorizationsRouter.patch('/:id', async (req, res) => {
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
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await patchResourceAuthorizationAsync(paramsParsed.data.id, parsed.data, requestAccess)
      return {
        result: outcome,
        log: outcome.kind === 'updated' ? {
          operationScopeSystemAccountId: outcome.context.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'update',
          operationKey: 'authorizations.update',
          resourceType: 'authorization',
          resourceId: outcome.result.id,
          resourceName: outcome.context.resourceId,
          summary: `更新资源授权：${outcome.context.resourceId}`,
          changes: diffSafeFields(outcome.previous as unknown as Record<string, unknown>, outcome.result as unknown as Record<string, unknown>, {
            status: '状态',
            expiresAt: '过期时间',
            limits: '额度限制'
          }),
          targets: authorizationPatchTargets(outcome.context),
          viewers: authorizationPatchViewers(outcome.context)
        } : undefined
      }
    }, req)
    if (outcome.kind === 'not_found') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    if (outcome.kind === 'conflict') {
      res.status(409).json({ message: '授权配置已被其他操作更新，请刷新后重试', currentUpdatedAt: outcome.currentUpdatedAt })
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '修改授权失败'))
  }
})

authorizationsRouter.patch('/:id/expire', async (req, res) => {
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
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await patchResourceAuthorizationAsync(paramsParsed.data.id, parsed.data, requestAccess)
      return {
        result: outcome,
        log: outcome.kind === 'updated' ? {
          operationScopeSystemAccountId: outcome.context.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'update_expire',
          operationKey: 'authorizations.update_expire',
          resourceType: 'authorization',
          resourceId: outcome.result.id,
          resourceName: outcome.context.resourceId,
          summary: `更新授权有效期：${outcome.context.resourceId}`,
          changes: diffSafeFields(outcome.previous as unknown as Record<string, unknown>, outcome.result as unknown as Record<string, unknown>, {
            expiresAt: '过期时间',
            limits: '额度限制',
            status: '状态'
          }),
          targets: authorizationPatchTargets(outcome.context),
          viewers: authorizationPatchViewers(outcome.context)
        } : undefined
      }
    }, req)
    if (outcome.kind === 'not_found') {
      sendNotFound(res, '授权记录不存在')
      return
    }
    if (outcome.kind === 'conflict') {
      res.status(409).json({ message: '授权配置已被其他操作更新，请刷新后重试', currentUpdatedAt: outcome.currentUpdatedAt })
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '修改授权失败'))
  }
})

authorizationsRouter.get('/:id/usage', async (req, res, next) => {
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
  try {
    const authorization = await getResourceAuthorizationUsageAsync(
      paramsParsed.data.id,
      getRequestAccessScope(queryParsed.data.systemAccountId),
      {
        range: await normalizeAuthorizationUsageRangeAsync({
          startDate: queryParsed.data.startDate,
          endDate: queryParsed.data.endDate
        }),
        page: queryParsed.data.page,
        pageSize: queryParsed.data.pageSize
      }
    )
    if (!authorization) {
      sendNotFound(res, '授权记录不存在')
      return
    }
    res.json(ok(authorization))
  } catch (error) {
    next(error)
  }
})

function authorizationTargets(authorization: ResourceAuthorizationSummary | ResourceAuthorizationListItem) {
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

interface AuthorizationPatchContext {
  resourceType: 'account' | 'group'
  resourceId: string
  resourceOwnerSystemAccountId: string
  granteeType: 'system_account' | 'team'
  granteeSystemAccountId?: string
  granteeTeamId?: string
}

function authorizationPatchTargets(context: AuthorizationPatchContext) {
  const targets = [ownerTarget({
    targetType: context.resourceType,
    targetId: context.resourceId,
    ownerSystemAccountId: context.resourceOwnerSystemAccountId,
    relation: 'owner'
  })]
  if (context.granteeType === 'team') {
    targets.push(ownerTarget({
      targetType: 'system_team',
      targetId: context.granteeTeamId,
      relation: 'grantee'
    }))
  } else {
    targets.push(ownerTarget({
      targetType: 'system_account',
      targetId: context.granteeSystemAccountId,
      ownerSystemAccountId: context.granteeSystemAccountId,
      relation: 'grantee'
    }))
  }
  return targets
}

function authorizationPatchViewers(context: AuthorizationPatchContext) {
  return viewers(
    viewer(context.resourceOwnerSystemAccountId, 'authorization_owner'),
    viewer(context.granteeType === 'system_account' ? context.granteeSystemAccountId : undefined, 'authorization_grantee')
  )
}

function authorizationViewers(authorization: ResourceAuthorizationSummary | ResourceAuthorizationListItem) {
  return viewers(
    viewer(authorization.resourceOwnerSystemAccountId, 'authorization_owner'),
    viewer(authorization.granteeType === 'system_account' ? authorization.granteeSystemAccountId : undefined, 'authorization_grantee')
  )
}

function authorizationGranteeName(authorization: ResourceAuthorizationSummary | ResourceAuthorizationListItem): string {
  if (authorization.granteeType === 'team') {
    return authorization.granteeTeamName ?? '团队'
  }
  return authorization.granteeSystemAccountName ?? '被授权用户'
}

async function normalizeAuthorizationUsageRangeAsync(input: { startDate?: string; endDate?: string }) {
  const timezone = await usageStatsTimezoneAsync()
  const defaultRange = fixedUsageStatsDefaultRange(timezone)
  const startDate = input.startDate ?? input.endDate ?? defaultRange.startDate
  const endDate = input.endDate ?? input.startDate ?? defaultRange.endDate
  return normalizeAccountUsageStatsRange({
    startDate,
    endDate
  }, timezone)
}
