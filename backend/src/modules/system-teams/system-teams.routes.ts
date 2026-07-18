import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, parseOrBadRequest, sendBadRequest, sendNotFound } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  addSystemTeamMembersAsync,
  createSystemTeamAsync,
  findSystemTeamSummaryAsync,
  listSystemTeamsPageAsync,
  removeSystemTeamMemberAsync,
  updateSystemTeamAsync
} from '../../storage/repositories.js'
import { maxSystemTeamMemberBatchSize } from '../../storage/system-team-limits.js'
import type { SystemTeamSummary } from '../../domain/types.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope, getRequestAuthContext } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, sortedTextValues } from '../deduplication/mutation-guard.middleware.js'
import { diffSafeFields, operationMode, ownerTarget, runLoggedOperationAsync, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import { publishAccountStaticReset } from '../page-data/page-data-change.publisher.js'

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
  status: z.enum(['active', 'disabled']).optional()
}).strict()

const teamMembersSchema = z.object({
  systemAccountIds: z.array(z.string().trim().min(1)).min(1, '请至少选择一个团队成员').max(maxSystemTeamMemberBatchSize, `单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`)
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
    const team = await findSystemTeamSummaryAsync(paramsParsed.data.id, currentUserTeamScope())
    if (!team) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(team))
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
    const team = await findSystemTeamSummaryAsync(paramsParsed.data.id, getRequestAccessScope(scopeQuery.data.systemAccountId))
    if (!team) {
      sendNotFound(res, '团队不存在')
      return
    }
    res.json(ok(team))
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

systemTeamsRouter.post('/', requireAdmin, mutationGuard({
  operationKey: 'system_teams.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name'))
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
          ],
          targets: teamMemberTargets(team),
          viewers: teamMemberViewers(team)
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
    let authorizationAffectedOwnerSystemAccountIds: string[] = []
    const team = await runLoggedOperationAsync(async () => {
      const before = await findSystemTeamSummaryAsync(paramsParsed.data.id, requestAccess)
      const team = await updateSystemTeamAsync(paramsParsed.data.id, parsed.data, requestAccess)
      if (!team) {
        throw new Error('团队不存在')
      }
      if (Object.prototype.hasOwnProperty.call(parsed.data, 'status')) {
        authorizationAffectedOwnerSystemAccountIds = [...new Set([
          ...(before?.members ?? []).map((member) => member.systemAccountId),
          ...(team.members ?? []).map((member) => member.systemAccountId)
        ])]
      }
      return {
        result: team,
        log: {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'update',
          operationKey: 'system_teams.update',
          resourceType: 'system_team',
          resourceId: team.id,
          resourceName: team.name,
          summary: `更新系统团队：${team.name}`,
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, team as unknown as Record<string, unknown>, {
            name: '团队名称',
            description: '说明',
            status: '状态'
          }),
          targets: teamMemberTargets(team),
          viewers: teamMemberViewers(team)
        }
      }
    }, req)
    if (authorizationAffectedOwnerSystemAccountIds.length > 0) {
      await publishAccountStaticReset(authorizationAffectedOwnerSystemAccountIds)
    }
    res.json(ok(team))
  } catch (error) {
    if (error instanceof Error && error.message === '团队不存在') {
      sendNotFound(res, '团队不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新团队失败'))
  }
})

systemTeamsRouter.post('/:id/members', requireAdmin, mutationGuard({
  operationKey: 'system_teams.add_members',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    teamId: req.params.id,
    systemAccountIds: sortedTextValues(bodyField(req, 'systemAccountIds'))
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
    let affectedOwnerSystemAccountIds: string[] = []
    const team = await runLoggedOperationAsync(async () => {
      const before = await findSystemTeamSummaryAsync(paramsParsed.data.id, requestAccess)
      const beforeMemberIds = new Set((before?.members ?? []).map((member) => member.systemAccountId))
      const team = await addSystemTeamMembersAsync(paramsParsed.data.id, parsed.data, requestAccess)
      if (!team) {
        throw new Error('团队不存在或已停用')
      }
      const addedMembers = (team.members ?? []).filter((member) => !beforeMemberIds.has(member.systemAccountId))
      affectedOwnerSystemAccountIds = addedMembers.map((member) => member.systemAccountId)
      return {
        result: team,
        log: {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'add_members',
          operationKey: 'system_teams.add_members',
          resourceType: 'system_team',
          resourceId: team.id,
          resourceName: team.name,
          summary: `添加团队成员：${team.name}`,
          changes: [safeChange('members', '新增成员', undefined, addedMembers.map((member) => member.systemAccountName).filter(Boolean).join('、'))],
          targets: [
            ...teamMemberTargets(team),
            ...addedMembers.map((member) => ownerTarget({
              targetType: 'system_account',
              targetId: member.systemAccountId,
              targetName: member.systemAccountName,
              ownerSystemAccountId: member.systemAccountId,
              relation: 'team_member'
            }))
          ],
          viewers: viewers(teamMemberViewers(team), ...addedMembers.map((member) => viewer(member.systemAccountId, 'team_member')))
        }
      }
    }, req)
    if (affectedOwnerSystemAccountIds.length > 0) await publishAccountStaticReset(affectedOwnerSystemAccountIds)
    res.json(ok(team))
  } catch (error) {
    if (error instanceof Error && error.message === '团队不存在或已停用') {
      sendNotFound(res, '团队不存在或已停用')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '添加团队成员失败'))
  }
})

systemTeamsRouter.delete('/:id/members/:memberId', requireAdmin, async (req, res) => {
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
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    let affectedOwnerSystemAccountId: string | undefined
    const team = await runLoggedOperationAsync(async () => {
      const before = await findSystemTeamSummaryAsync(paramsParsed.data.id, requestAccess)
      const removedMember = before?.members?.find((member) => member.id === paramsParsed.data.memberId)
      affectedOwnerSystemAccountId = removedMember?.systemAccountId
      const team = await removeSystemTeamMemberAsync(paramsParsed.data.id, paramsParsed.data.memberId, requestAccess)
      if (!team) {
        throw new Error('团队成员不存在')
      }
      return {
        result: team,
        log: {
          mode: operationMode(requestAccess),
          module: 'system_teams',
          action: 'remove_member',
          operationKey: 'system_teams.remove_member',
          resourceType: 'system_team',
          resourceId: team.id,
          resourceName: team.name,
          summary: `移除团队成员：${team.name}`,
          changes: [safeChange('member', '移除成员', removedMember?.systemAccountName, undefined)],
          targets: [
            ...teamMemberTargets(team),
            ...(removedMember ? [ownerTarget({
              targetType: 'system_account',
              targetId: removedMember.systemAccountId,
              targetName: removedMember.systemAccountName,
              ownerSystemAccountId: removedMember.systemAccountId,
              relation: 'team_member'
            })] : [])
          ],
          viewers: viewers(teamMemberViewers(team), removedMember ? viewer(removedMember.systemAccountId, 'team_member') : [])
        }
      }
    }, req)
    if (affectedOwnerSystemAccountId) await publishAccountStaticReset([affectedOwnerSystemAccountId])
    res.json(ok(team))
  } catch (error) {
    if (error instanceof Error && error.message === '团队成员不存在') {
      sendNotFound(res, '团队成员不存在')
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '移除团队成员失败'))
  }
})

function teamMemberTargets(team: SystemTeamSummary) {
  return (team.members ?? []).map((member) => ownerTarget({
    targetType: 'system_account',
    targetId: member.systemAccountId,
    targetName: member.systemAccountName,
    ownerSystemAccountId: member.systemAccountId,
    relation: 'team_member'
  }))
}

function teamMemberViewers(team: SystemTeamSummary) {
  return viewers(...(team.members ?? []).map((member) => viewer(member.systemAccountId, 'team_member')))
}
