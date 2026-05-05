import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import {
  addSystemTeamMembers,
  createSystemTeam,
  listSystemTeams,
  removeSystemTeamMember,
  updateSystemTeam
} from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

export const systemTeamsRouter = Router()

const teamIdParamsSchema = z.object({
  id: z.string().trim().min(1, '团队 ID 不能为空')
})

const teamMemberParamsSchema = z.object({
  id: z.string().trim().min(1, '团队 ID 不能为空'),
  memberId: z.string().trim().min(1, '团队成员 ID 不能为空')
})

const createTeamSchema = z.object({
  name: z.string().trim().min(1, '团队名称不能为空').max(100, '团队名称不能超过 100 个字符'),
  description: z.string().trim().max(200).nullable().optional(),
  status: z.enum(['active', 'disabled']).optional()
})

const updateTeamSchema = z.object({
  name: z.string().trim().min(1, '团队名称不能为空').max(100, '团队名称不能超过 100 个字符').optional(),
  description: z.string().trim().max(200).nullable().optional(),
  status: z.enum(['active', 'disabled']).optional()
})

const teamMembersSchema = z.object({
  systemAccountIds: z.array(z.string().trim().min(1)).min(1, '请至少选择一个团队成员')
})

systemTeamsRouter.get('/', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  res.json(ok(listSystemTeams(getRequestAccessScope(scopeQuery.data.systemAccountId))))
})

systemTeamsRouter.post('/', requireAdmin, (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const parsed = parseOrBadRequest(createTeamSchema, req.body, '团队参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const team = createSystemTeam(parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    clearGatewayRuntimeCache()
    res.status(201).json(ok(team))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建团队失败'))
  }
})

systemTeamsRouter.patch('/:id', requireAdmin, (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(teamIdParamsSchema, req.params, '团队 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const parsed = parseOrBadRequest(updateTeamSchema, req.body, '团队参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const team = updateSystemTeam(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!team) {
      sendNotFound(res, '团队不存在')
      return
    }
    clearGatewayRuntimeCache()
    res.json(ok(team))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新团队失败'))
  }
})

systemTeamsRouter.post('/:id/members', requireAdmin, (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(teamIdParamsSchema, req.params, '团队 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  const parsed = parseOrBadRequest(teamMembersSchema, req.body, '团队成员参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const team = addSystemTeamMembers(paramsParsed.data.id, parsed.data, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!team) {
      sendNotFound(res, '团队不存在或已停用')
      return
    }
    clearGatewayRuntimeCache()
    res.json(ok(team))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '添加团队成员失败'))
  }
})

systemTeamsRouter.delete('/:id/members/:memberId', requireAdmin, (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  const paramsParsed = parseOrBadRequest(teamMemberParamsSchema, req.params, '团队成员参数不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  try {
    const team = removeSystemTeamMember(paramsParsed.data.id, paramsParsed.data.memberId, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!team) {
      sendNotFound(res, '团队成员不存在')
      return
    }
    clearGatewayRuntimeCache()
    res.json(ok(team))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '移除团队成员失败'))
  }
})
