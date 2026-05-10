import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import {
  createResourceAuthorization,
  getResourceAuthorizationUsage,
  listResourceAuthorizations,
  revokeResourceAuthorization,
  updateResourceAuthorization
} from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, textValue } from '../deduplication/mutation-guard.middleware.js'
import { clearAuthorizationQuotaCache } from '../gateway/authorization-quota.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { diffSafeFields, operationMode, ownerTarget, runLoggedOperation, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'

export const authorizationsRouter = Router()

const authorizationIdParamsSchema = z.object({
  id: z.string().trim().min(1, '授权记录 ID 不能为空')
})

const authorizationsQuerySchema = z.object({
  resourceType: z.enum(['account', 'group']).optional(),
  resourceId: z.string().trim().min(1, '授权资源 ID 不能为空').optional(),
  granteeSystemAccountId: z.string().trim().min(1, '被授权用户 ID 不能为空').optional(),
  teamId: z.string().trim().min(1, '团队 ID 不能为空').optional(),
  status: z.enum(['active', 'paused', 'expired', 'revoked', 'all']).optional(),
  direction: z.enum(['all', 'outbound', 'inbound']).optional(),
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional()
})

const createAuthorizationSchema = z.object({
  resourceType: z.enum(['account', 'group']),
  resourceId: z.string().trim().min(1, '授权资源不能为空'),
  granteeType: z.enum(['system_account', 'team']),
  granteeId: z.string().trim().min(1, '被授权对象不能为空'),
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
      message: '撤销团队来源授权时必须提供团队 ID'
    })
  }
})

authorizationsRouter.get('/', (req, res) => {
  const parsed = parseOrBadRequest(authorizationsQuerySchema, req.query, '查询参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  const { systemAccountId, direction, ...filters } = parsed.data
  const routeFilters = req.baseUrl.endsWith('/my-authorizations') && direction && direction !== 'all'
    ? { ...filters, direction }
    : filters
  res.json(ok(listResourceAuthorizations(routeFilters, getRequestAccessScope(systemAccountId))))
})

authorizationsRouter.post('/', mutationGuard({
  operationKey: 'authorizations.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    resourceType: textValue(bodyField(req, 'resourceType')),
    resourceId: textValue(bodyField(req, 'resourceId')),
    granteeType: textValue(bodyField(req, 'granteeType')),
    granteeId: textValue(bodyField(req, 'granteeId'))
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
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const authorization = createResourceAuthorization(parsed.data, requestAccess)
      return {
        result: authorization,
        afterCommit: clearAuthorizationRuntimeCaches,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'create',
          operationKey: 'authorizations.create',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `创建资源授权：${authorization.resourceName ?? authorization.resourceId} -> ${authorization.granteeSystemAccountName ?? authorization.granteeUsername ?? authorization.granteeSystemAccountId}`,
          changes: [
            safeChange('resourceType', '资源类型', undefined, authorization.resourceType),
            safeChange('resourceId', '授权资源', undefined, authorization.resourceName ?? authorization.resourceId),
            safeChange('granteeSystemAccountId', '被授权用户', undefined, authorization.granteeSystemAccountName ?? authorization.granteeSystemAccountId),
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
  const parsed = parseOrBadRequest(revokeAuthorizationSchema, payload, '撤销授权参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const authorization = runLoggedOperation(() => {
      const before = listResourceAuthorizations({ status: 'all' }, requestAccess).find((item) => item.id === paramsParsed.data.id)
      const authorization = revokeResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        afterCommit: clearAuthorizationRuntimeCaches,
        log: {
          operationScopeSystemAccountId: authorization.resourceOwnerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'revoke',
          operationKey: 'authorizations.revoke',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName: authorization.resourceName ?? authorization.resourceId,
          summary: `撤销资源授权：${authorization.resourceName ?? authorization.resourceId} -> ${authorization.granteeSystemAccountName ?? authorization.granteeUsername ?? authorization.granteeSystemAccountId}`,
          changes: [
            ...diffSafeFields(before as unknown as Record<string, unknown> | undefined, authorization as unknown as Record<string, unknown>, {
              status: '状态',
              expiresAt: '过期时间',
              limits: '额度限制'
            }),
            safeChange('revoked', '撤销状态', false, true)
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
    res.status(400).json(badRequest(error instanceof Error ? error.message : '撤销授权失败'))
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
      const before = listResourceAuthorizations({ status: 'all' }, requestAccess).find((item) => item.id === paramsParsed.data.id)
      const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        afterCommit: clearAuthorizationRuntimeCaches,
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
      const before = listResourceAuthorizations({ status: 'all' }, requestAccess).find((item) => item.id === paramsParsed.data.id)
      const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, requestAccess)
      if (!authorization) {
        throw new Error('授权记录不存在')
      }
      return {
        result: authorization,
        afterCommit: clearAuthorizationRuntimeCaches,
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
  const authorization = getResourceAuthorizationUsage(paramsParsed.data.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
  if (!authorization) {
    sendNotFound(res, '授权记录不存在')
    return
  }
  res.json(ok(authorization))
})

function authorizationTargets(authorization: ReturnType<typeof listResourceAuthorizations>[number]) {
  return [
    ownerTarget({
      targetType: authorization.resourceType,
      targetId: authorization.resourceId,
      targetName: authorization.resourceName,
      ownerSystemAccountId: authorization.resourceOwnerSystemAccountId,
      relation: 'owner'
    }),
    ownerTarget({
      targetType: 'system_account',
      targetId: authorization.granteeSystemAccountId,
      targetName: authorization.granteeSystemAccountName ?? authorization.granteeUsername,
      ownerSystemAccountId: authorization.granteeSystemAccountId,
      relation: 'grantee'
    })
  ]
}

function authorizationViewers(authorization: ReturnType<typeof listResourceAuthorizations>[number]) {
  return viewers(
    viewer(authorization.resourceOwnerSystemAccountId, 'authorization_owner'),
    viewer(authorization.granteeSystemAccountId, 'authorization_grantee')
  )
}

function clearAuthorizationRuntimeCaches(): void {
  clearGatewayRuntimeCache()
  clearAuthorizationQuotaCache()
}
