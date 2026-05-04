import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import {
  createResourceAuthorization,
  getResourceAuthorizationUsage,
  listResourceAuthorizations,
  revokeResourceAuthorization
} from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
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
  status: z.enum(['active', 'revoked', 'all']).optional(),
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional()
})

const accessScopeQuerySchema = z.object({
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

function firstIssueMessage(error: z.ZodError, fallback: string): string {
  return error.issues[0]?.message ?? fallback
}

function parseScopeQuery(query: unknown): { systemAccountId?: string } | { message: string } {
  const parsed = accessScopeQuerySchema.safeParse(query)
  if (!parsed.success) {
    return { message: firstIssueMessage(parsed.error, '查询参数不合法') }
  }
  return parsed.data
}

authorizationsRouter.get('/', (req, res) => {
  const parsed = authorizationsQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '查询参数不合法')))
    return
  }
  const { systemAccountId, ...filters } = parsed.data
  res.json(ok(listResourceAuthorizations(filters, getRequestAccessScope(systemAccountId))))
})

authorizationsRouter.post('/', (req, res) => {
  const scopeQuery = parseScopeQuery(req.query)
  if ('message' in scopeQuery) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = createAuthorizationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '授权参数不合法')))
    return
  }
  try {
    const authorization = createResourceAuthorization(parsed.data, getRequestAccessScope(scopeQuery.systemAccountId))
    clearGatewayRuntimeCache()
    res.status(201).json(ok(authorization))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建授权失败'))
  }
})

authorizationsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseScopeQuery(req.query)
  if ('message' in scopeQuery) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const paramsParsed = authorizationIdParamsSchema.safeParse(req.params)
  if (!paramsParsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(paramsParsed.error, '授权记录 ID 不合法')))
    return
  }
  const payload = {
    sourceType: req.body?.sourceType ?? req.query.sourceType,
    sourceTeamId: req.body?.sourceTeamId ?? req.query.sourceTeamId,
    revokeAll: req.body?.revokeAll ?? (req.query.revokeAll === 'true' ? true : req.query.revokeAll === 'false' ? false : undefined)
  }
  const parsed = revokeAuthorizationSchema.safeParse(payload)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '撤销授权参数不合法')))
    return
  }
  try {
    const authorization = revokeResourceAuthorization(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.systemAccountId))
    if (!authorization) {
      res.status(404).json({ message: '授权记录不存在' })
      return
    }
    clearGatewayRuntimeCache()
    res.json(ok(authorization))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '撤销授权失败'))
  }
})

authorizationsRouter.get('/:id/usage', (req, res) => {
  const scopeQuery = parseScopeQuery(req.query)
  if ('message' in scopeQuery) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const paramsParsed = authorizationIdParamsSchema.safeParse(req.params)
  if (!paramsParsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(paramsParsed.error, '授权记录 ID 不合法')))
    return
  }
  const authorization = getResourceAuthorizationUsage(paramsParsed.data.id, getRequestAccessScope(scopeQuery.systemAccountId))
  if (!authorization) {
    res.status(404).json({ message: '授权记录不存在' })
    return
  }
  res.json(ok(authorization))
})
