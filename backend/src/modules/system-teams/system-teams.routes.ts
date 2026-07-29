import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  addSystemTeamMembersAsync,
  createSystemTeamAsync,
  findSystemTeamDetailAsync,
  listSystemTeamMemberHistoryAsync,
  listSystemTeamMembersAsync,
  listSystemTeamsPageAsync,
  removeSystemTeamMemberAsync,
  updateSystemTeamAsync
} from '../../storage/repositories.js'
import { maxSystemTeamMemberBatchSize } from '../../storage/system-team-limits.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope, getRequestAuthContext } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, sortedTextValues } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, ownerTarget, runLoggedOperationAsync, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'

export const systemTeamsRouter = Router()
export const myTeamsRouter = Router()

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
}).strict()

const updateTeamSchema = z.object({
  name: z.string().trim().min(1, '团队名称不能为空').max(100, '团队名称不能超过 100 个字符').optional(),
  description: z.string().trim().max(200).nullable().optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expectedUpdatedAt: z.string().datetime({ message: '团队版本格式不正确' })
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedUpdatedAt'), {
  message: '请至少提交一个团队变更字段'
})

const teamMembersSchema = z.object({
  systemAccountIds: z.array(z.string().trim().min(1)).min(1, '请至少选择一个团队成员').max(maxSystemTeamMemberBatchSize, `单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`),
  expectedUpdatedAt: z.string().datetime({ message: '团队版本格式不正确' })
}).strict()

const teamMemberMutationSchema = z.object({
  expectedUpdatedAt: z.string().datetime({ message: '团队版本格式不正确' })
}).strict()

function currentUserTeamScope() {
  const context = getRequestAuthContext()
  return getRequestAccessScope(context?.systemAccountId)
}

myTeamsRouter.get('/', async (req, res, next) => {
  try {
    res.json(ok(await listSystemTeamsPageAsync(currentUserTeamScope(), parseSystemTeamListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

myTeamsRouter.get('/:id', async (req, res, next) => {
  const paramsParsed = parseOrBadRequest(teamIdParamsSchema, req.params, '团队 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  try {
    const team = await findSystemTeamDetailAsync(paramsParsed.data.id, currentUserTeamScope())
    if (!team) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(team))
  } catch (error) {
    next(error)
  }
})

myTeamsRouter.get('/:id/members', async (req, res, next) => {
  const paramsParsed = parseOrBadRequest(teamIdParamsSchema, req.params, '团队 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  try {
    const members = await listSystemTeamMembersAsync(paramsParsed.data.id, parseSystemTeamMemberListOptions(req.query), currentUserTeamScope())
    if (!members) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(members))
  } catch (error) {
    next(error)
  }
})

myTeamsRouter.get('/:id/members/history', async (req, res, next) => {
  const paramsParsed = parseOrBadRequest(teamIdParamsSchema, req.params, '团队 ID 不合法')
  if (!paramsParsed.success) {
    sendBadRequest(res, paramsParsed.message)
    return
  }
  try {
    const history = await listSystemTeamMemberHistoryAsync(paramsParsed.data.id, parseSystemTeamMemberHistoryOptions(req.query), currentUserTeamScope())
    if (!history) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(history))
  } catch (error) {
    next(error)
  }
})

systemTeamsRouter.get('/', requireAdmin, async (req, res, next) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    sendBadRequest(res, scopeQuery.message)
    return
  }
  try {
    res.json(ok(await listSystemTeamsPageAsync(getRequestAccessScope(scopeQuery.data.systemAccountId), parseSystemTeamListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

systemTeamsRouter.get('/:id', requireAdmin, async (req, res, next) => {
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
  try {
    const team = await findSystemTeamDetailAsync(paramsParsed.data.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!team) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(team))
  } catch (error) {
    next(error)
  }
})

systemTeamsRouter.get('/:id/members', requireAdmin, async (req, res, next) => {
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
  try {
    const members = await listSystemTeamMembersAsync(
      paramsParsed.data.id,
      parseSystemTeamMemberListOptions(req.query),
      getRequestAccessScope(scopeQuery.data.systemAccountId)
    )
    if (!members) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(members))
  } catch (error) {
    next(error)
  }
})

systemTeamsRouter.get('/:id/members/history', requireAdmin, async (req, res, next) => {
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
  try {
    const history = await listSystemTeamMemberHistoryAsync(
      paramsParsed.data.id,
      parseSystemTeamMemberHistoryOptions(req.query),
      getRequestAccessScope(scopeQuery.data.systemAccountId)
    )
    if (!history) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(history))
  } catch (error) {
    next(error)
  }
})

function parseSystemTeamListOptions(query: Record<string, unknown>) {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword)
  }
}

function parseSystemTeamMemberHistoryOptions(query: Record<string, unknown>) {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize)
  }
}

function parseSystemTeamMemberListOptions(query: Record<string, unknown>) {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize)
  }
}

systemTeamsRouter.post('/', requireAdmin, mutationGuard({
  operationKey: 'system_teams.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name')),
    description: normalizedText(bodyField(req, 'description')),
    status: normalizedText(bodyField(req, 'status')) || 'active'
  })
}), async (req, res) => {
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
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const team = await runLoggedOperationAsync(async () => {
      const team = await createSystemTeamAsync(parsed.data, requestAccess)
      return {
        result: team,
        log: {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'create',
          operationKey: 'system_teams.create',
          resourceType: 'system_team',
          resourceId: team.id,
          resourceName: team.name,
          summary: `创建系统团队：${team.name}`,
          changes: [
            safeChange('name', '团队名称', undefined, team.name),
            safeChange('description', '说明', undefined, team.description),
            safeChange('status', '状态', undefined, team.status)
          ]
        }
      }
    }, req)
    res.status(201).json(ok(team))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建团队失败'))
  }
})

systemTeamsRouter.patch('/:id', requireAdmin, async (req, res) => {
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
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await updateSystemTeamAsync(paramsParsed.data.id, parsed.data, requestAccess)
      return {
        result: outcome,
        log: outcome.status === 'updated' ? {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'update',
          operationKey: 'system_teams.update',
          resourceType: 'system_team',
          resourceId: outcome.result.id,
          resourceName: outcome.name,
          summary: `更新系统团队：${outcome.name}`,
          changes: outcome.changes.map((change) => safeChange(change.field, systemTeamPatchFieldLabel(change.field), change.before, change.after))
        } : undefined
      }
    }, req)
    if (outcome.status === 'not_found') {
      sendNotFound(res, '团队不存在')
      return
    }
    if (outcome.status === 'conflict') {
      res.status(409).json(badRequest('团队已被其他操作更新，请刷新后重试'))
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新团队失败'))
  }
})

systemTeamsRouter.post('/:id/members', requireAdmin, mutationGuard({
  operationKey: 'system_teams.add_members',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    teamId: req.params.id,
    systemAccountIds: sortedTextValues(bodyField(req, 'systemAccountIds')),
    expectedUpdatedAt: normalizedText(bodyField(req, 'expectedUpdatedAt'))
  })
}), async (req, res) => {
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
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await addSystemTeamMembersAsync(paramsParsed.data.id, parsed.data, requestAccess)
      return {
        result: outcome,
        log: outcome.status === 'updated' ? {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'add_members',
          operationKey: 'system_teams.add_members',
          resourceType: 'system_team',
          resourceId: outcome.result.id,
          resourceName: outcome.name,
          summary: `添加团队成员：${outcome.name}`,
          changes: [safeChange('members', '新增成员', undefined, outcome.result.addedMembers.map((member) => member.systemAccountName).filter(Boolean).join('、'))],
          targets: outcome.result.addedMembers.map((member) => ownerTarget({
              targetType: 'system_account',
              targetId: member.systemAccountId,
              targetName: member.systemAccountName,
              ownerSystemAccountId: member.systemAccountId,
              relation: 'team_member'
            })),
          viewers: viewers(...(outcome.viewerSystemAccountIds ?? []).map((systemAccountId) => viewer(systemAccountId, 'team_member')))
        } : undefined
      }
    }, req)
    if (outcome.status === 'not_found') {
      sendNotFound(res, '团队不存在或已停用')
      return
    }
    if (outcome.status === 'conflict') {
      res.status(409).json(badRequest('团队已被其他操作更新，请刷新后重试'))
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '添加团队成员失败'))
  }
})

systemTeamsRouter.delete('/:id/members/:memberId', requireAdmin, mutationGuard({
  operationKey: 'system_teams.remove_member',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    teamId: req.params.id,
    memberId: req.params.memberId,
    expectedUpdatedAt: normalizedText(bodyField(req, 'expectedUpdatedAt'))
  })
}), async (req, res) => {
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
  const parsed = parseOrBadRequest(teamMemberMutationSchema, req.body, '团队成员参数不合法')
  if (!parsed.success) {
    sendBadRequest(res, parsed.message)
    return
  }
  try {
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const outcome = await runLoggedOperationAsync(async () => {
      const outcome = await removeSystemTeamMemberAsync(paramsParsed.data.id, paramsParsed.data.memberId, parsed.data, requestAccess)
      return {
        result: outcome,
        log: outcome.status === 'updated' ? {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'remove_member',
          operationKey: 'system_teams.remove_member',
          resourceType: 'system_team',
          resourceId: outcome.result.id,
          resourceName: outcome.name,
          summary: `移除团队成员：${outcome.name}`,
          changes: [safeChange('member', '移除成员', outcome.removedMember.systemAccountName, undefined)],
          targets: [ownerTarget({
              targetType: 'system_account',
              targetId: outcome.removedMember.systemAccountId,
              targetName: outcome.removedMember.systemAccountName,
              ownerSystemAccountId: outcome.removedMember.systemAccountId,
              relation: 'team_member'
            })],
          viewers: viewers(...outcome.viewerSystemAccountIds.map((systemAccountId) => viewer(systemAccountId, 'team_member')))
        } : undefined
      }
    }, req)
    if (outcome.status === 'not_found') {
      sendNotFound(res, '团队成员不存在')
      return
    }
    if (outcome.status === 'conflict') {
      res.status(409).json(badRequest('团队已被其他操作更新，请刷新后重试'))
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '移除团队成员失败'))
  }
})

function systemTeamPatchFieldLabel(field: string): string {
  return ({
    name: '团队名称',
    description: '说明',
    status: '状态'
  } as Record<string, string>)[field] ?? field
}
