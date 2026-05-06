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
import { clearAuthorizationQuotaCache } from '../gateway/authorization-quota.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

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
  const { systemAccountId, ...filters } = parsed.data
  res.json(ok(listResourceAuthorizations(filters, getRequestAccessScope(systemAccountId))))
})

authorizationsRouter.post('/', (req, res) => {
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
    const authorization = createResourceAuthorization(parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    clearGatewayRuntimeCache()
    clearAuthorizationQuotaCache()
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
    const authorization = revokeResourceAuthorization(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!authorization) {
      sendNotFound(res, '授权记录不存在')
      return
    }
    clearGatewayRuntimeCache()
    clearAuthorizationQuotaCache()
    res.json(ok(authorization))
  } catch (error) {
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
    const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!authorization) {
      sendNotFound(res, '授权记录不存在')
      return
    }
    clearGatewayRuntimeCache()
    clearAuthorizationQuotaCache()
    res.json(ok(authorization))
  } catch (error) {
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
    const authorization = updateResourceAuthorization(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!authorization) {
      sendNotFound(res, '授权记录不存在')
      return
    }
    clearGatewayRuntimeCache()
    clearAuthorizationQuotaCache()
    res.json(ok(authorization))
  } catch (error) {
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
