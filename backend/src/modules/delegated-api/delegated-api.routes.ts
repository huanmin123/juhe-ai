import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { resolveEffectiveUserRequestLimits, type GlobalUserRequestLimitSettings, USER_REQUEST_LIMIT_WINDOWS } from '../../domain/user-request-limits.js'
import type { AccountSummary, GroupListItem, GroupSummary, RouteStrategySummary, SystemAccountSummary, UserRequestLimitWindow } from '../../domain/types.js'
import { runRedisOperationWithDeadline } from '../../shared/redis-client.js'
import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  ApiKeyRevisionConflictError,
  GroupPatchConflictError,
  RouteStrategyVersionConflictError,
  createGroupWithReceiptAsync,
  createRouteStrategyListItemAsync,
  deleteGroupAsync,
  deleteRouteStrategyAsync,
  findGroupEditDetailAsync,
  findGroupSummaryAsync,
  findProviderOptionByCodeAsync,
  findRouteStrategySummaryAsync,
  findSystemAccountByIdAsync,
  listAccountItemsPageAsync,
  listApiKeysPageAsync,
  listCompleteRouteStrategyListItemsPageAsync,
  listGroupItemsPageAsync,
  listRouteStrategiesPageAsync,
  patchApiKeyAsync,
  patchGroupAsync,
  patchRouteStrategyAsync,
  updateSystemAccountAsync
} from '../../storage/repositories.js'
import { getSettingsAsync } from '../../storage/settings.repository.js'
import { patchAccountManagementAsync, AccountManagementPatchRevisionConflictError } from '../../storage/account-management-patch.repository.js'
import type { RouteStrategyListOptions } from '../../storage/route-strategy.repository.js'
import { findAccessTokenContext, type OAuthAccessTokenContext } from '../oidc-provider/oidc-provider.repository.js'
import { delegatedOAuthRateLimit } from '../oidc-provider/oidc-rate-limit.middleware.js'

export const delegatedApiRouter = Router()

const delegatedPrefix = 'juhe:'
const redisReadDeadlineMs = 750

const groupMutationSchema = z.object({
  name: z.string().trim().min(1),
  providerCode: z.string().trim().min(1),
  description: z.string().trim().optional(),
  enabled: z.boolean().optional(),
  groupType: z.enum(['personal', 'high_concurrency']).optional(),
  schedulingPolicy: z.record(z.unknown()).optional()
}).strict()

const groupPatchSchema = groupMutationSchema.partial().extend({
  expectedUpdatedAt: z.string().datetime()
}).refine((value) => Object.keys(value).some((key) => key !== 'expectedUpdatedAt'), {
  message: '请提供要修改的分组内容'
})

const routeStrategyBindingSchema = z.object({
  groupId: z.string().trim().min(1),
  priority: z.number().int().positive().optional(),
  weight: z.number().int().min(1).max(100).optional(),
  status: z.enum(['active', 'disabled']).optional()
}).strict()

const routeStrategyMutationSchema = z.object({
  name: z.string().trim().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  mode: z.enum(['normal', 'hybrid_smart', 'weighted', 'failover', 'round_robin']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  groupBindings: z.array(routeStrategyBindingSchema).min(1).max(20).optional(),
  normalRoutingConfig: z.record(z.unknown()).nullable().optional(),
  hybridRoutingConfig: z.record(z.unknown()).nullable().optional()
}).strict()

const routeStrategyCreateSchema = routeStrategyMutationSchema.refine((value) => Boolean(value.groupBindings?.length), {
  message: '策略路由至少需要绑定一个分组'
})

const routeStrategyPatchSchema = routeStrategyMutationSchema.partial().extend({
  expectedUpdatedAt: z.string().datetime()
}).refine((value) => Object.keys(value).some((key) => key !== 'expectedUpdatedAt'), {
  message: '请提供要修改的策略路由内容'
})

const apiKeyPatchSchema = z.object({
  expectedRevision: z.string().trim().min(1),
  name: z.string().trim().min(1).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  routeStrategyId: z.string().trim().min(1).optional()
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedRevision'), {
  message: '请提供要修改的 API Key 内容'
})

const accountPatchSchema = z.object({
  expectedConfigRevision: z.number().int().min(1),
  name: z.string().trim().min(1).optional(),
  status: z.enum(['active', 'disabled']).optional()
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedConfigRevision'), {
  message: '请提供要修改的 AI 账户内容'
})

const profilePatchSchema = z.object({
  displayName: z.string().trim().min(1)
}).strict()

interface DelegatedRequest extends Request {
  delegatedAccessToken?: OAuthAccessTokenContext
}

interface RequestLimitWindowSnapshot {
  limit: number
  limitMode: 'limited' | 'unlimited'
  usageTracked: boolean
  used: number | null
  remaining: number | null
  source: 'global' | 'user'
  resetsAt: string
}

delegatedApiRouter.use(delegatedOAuthRateLimit, requireDelegatedAccess)

delegatedApiRouter.get('/profile', requireScope('profile.read'), async (req, res, next) => {
  try {
    const account = await currentAccount(req)
    if (!account) {
      res.status(404).json({ message: '用户不存在' })
      return
    }
    res.json(ok(profileDto(account)))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.patch('/profile', requireScope('profile.write'), async (req, res, next) => {
  const parsed = profilePatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('用户资料参数无效'))
    return
  }
  try {
    const account = await updateSystemAccountAsync(delegatedContext(req).systemAccountId, {
      displayName: parsed.data.displayName
    })
    if (!account) {
      res.status(404).json({ message: '用户不存在' })
      return
    }
    res.json(ok(profileDto(account)))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '修改显示名称失败' })
  }
})

delegatedApiRouter.get('/groups', requireScope('groups.read'), async (req, res, next) => {
  try {
    const page = await listGroupItemsPageAsync(delegatedAccess(req), {
      page: positiveQueryInteger(req.query.page),
      pageSize: positiveQueryInteger(req.query.pageSize),
      manageableOnly: true
    })
    res.json(ok({ ...page, items: page.items.map(groupDto) }))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.get('/groups/:id', requireScope('groups.read'), async (req, res, next) => {
  try {
    const group = await ownGroup(req.params.id, delegatedAccess(req))
    if (!group) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    const detail = await findGroupEditDetailAsync(req.params.id, delegatedAccess(req))
    res.json(ok({ ...groupDto(group), ...(detail ? { updatedAt: detail.updatedAt } : {}) }))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.post('/groups', requireScope('groups.write'), async (req, res, next) => {
  const parsed = groupMutationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组参数无效')))
    return
  }
  try {
    const provider = await findProviderOptionByCodeAsync(parsed.data.providerCode)
    if (!provider || !provider.enabled) {
      res.status(400).json(badRequest('供应商不存在或已停用'))
      return
    }
    const created = await createGroupWithReceiptAsync(parsed.data, delegatedAccess(req))
    res.status(201).json(ok({ ...groupDto(created.group), updatedAt: created.updatedAt }))
  } catch (error) {
    const message = error instanceof Error ? error.message : '创建分组失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

delegatedApiRouter.patch('/groups/:id', requireScope('groups.write'), async (req, res, next) => {
  const parsed = groupPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组参数无效')))
    return
  }
  try {
    if (!await ownGroup(req.params.id, delegatedAccess(req))) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    const changed = await patchGroupAsync(req.params.id, parsed.data, delegatedAccess(req))
    if (!changed) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    const group = await ownGroup(req.params.id, delegatedAccess(req))
    res.json(ok(group
      ? { ...groupDto(group), updatedAt: changed.updatedAt }
      : { id: changed.id, changedFields: changed.changedFields, updatedAt: changed.updatedAt }))
  } catch (error) {
    if (error instanceof GroupPatchConflictError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新分组失败'))
  }
})

delegatedApiRouter.delete('/groups/:id', requireScope('groups.write'), async (req, res, next) => {
  try {
    const access = delegatedAccess(req)
    if (!await ownGroup(req.params.id, access)) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    if (!hasScope(req, 'route_strategies.write') && await hasRouteStrategyBinding(req.params.id, access)) {
      sendInsufficientScope(res, 'juhe:route_strategies.write')
      return
    }
    const deleted = await deleteGroupAsync(req.params.id, access)
    if (!deleted.deleted) {
      res.status(404).json({ message: '分组不存在' })
      return
    }
    res.status(204).send()
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除分组失败'))
  }
})

delegatedApiRouter.get('/route-strategies', requireScope('route_strategies.read'), async (req, res, next) => {
  try {
    const page = await listCompleteRouteStrategyListItemsPageAsync(delegatedAccess(req), routeStrategyListOptions(req))
    res.json(ok({ ...page, items: page.items.map(routeStrategyListDto) }))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.get('/route-strategies/:id', requireScope('route_strategies.read'), async (req, res, next) => {
  try {
    const strategy = await findRouteStrategySummaryAsync(req.params.id, delegatedAccess(req))
    if (!strategy) {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    res.json(ok(routeStrategyDto(strategy)))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.post('/route-strategies', requireScope('route_strategies.write'), async (req, res) => {
  const parsed = routeStrategyCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '策略路由参数无效')))
    return
  }
  try {
    if (!await ownRouteStrategyGroups(parsed.data.groupBindings ?? [], delegatedAccess(req))) {
      res.status(400).json(badRequest('策略路由只能绑定自己的分组'))
      return
    }
    const created = await createRouteStrategyListItemAsync(parsed.data, delegatedAccess(req))
    res.status(201).json(ok(routeStrategyListDto(created)))
  } catch (error) {
    const message = error instanceof Error ? error.message : '创建策略路由失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

delegatedApiRouter.patch('/route-strategies/:id', requireScope('route_strategies.write'), async (req, res) => {
  const parsed = routeStrategyPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '策略路由参数无效')))
    return
  }
  try {
    const access = delegatedAccess(req)
    if (!await findRouteStrategySummaryAsync(req.params.id, access)) {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    if (parsed.data.groupBindings && !await ownRouteStrategyGroups(parsed.data.groupBindings, access)) {
      res.status(400).json(badRequest('策略路由只能绑定自己的分组'))
      return
    }
    const outcome = await patchRouteStrategyAsync(req.params.id, parsed.data, access)
    if (!outcome) {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    const strategy = await findRouteStrategySummaryAsync(req.params.id, access)
    res.json(ok(strategy ? routeStrategyDto(strategy) : outcome.result))
  } catch (error) {
    if (error instanceof RouteStrategyVersionConflictError) {
      res.status(409).json({ message: error.message, currentUpdatedAt: error.currentUpdatedAt })
      return
    }
    const message = error instanceof Error ? error.message : '更新策略路由失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

delegatedApiRouter.delete('/route-strategies/:id', requireScope('route_strategies.write'), async (req, res) => {
  try {
    const deleted = await deleteRouteStrategyAsync(req.params.id, delegatedAccess(req))
    if (!deleted) {
      res.status(404).json({ message: '策略路由不存在' })
      return
    }
    res.status(204).send()
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除策略路由失败'))
  }
})

delegatedApiRouter.get('/api-keys', requireScope('api_keys.read'), async (req, res, next) => {
  try {
    const page = await listApiKeysPageAsync(delegatedAccess(req), {
      page: positiveQueryInteger(req.query.page),
      pageSize: positiveQueryInteger(req.query.pageSize)
    })
    res.json(ok({ ...page, items: page.items.map(apiKeyDto) }))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.patch('/api-keys/:id', requireScope('api_keys.write'), async (req, res) => {
  const parsed = apiKeyPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 参数无效')))
    return
  }
  try {
    const outcome = await patchApiKeyAsync(req.params.id, omit(parsed.data, 'expectedRevision'), parsed.data.expectedRevision, delegatedAccess(req))
    if (!outcome) {
      res.status(404).json({ message: 'API Key 不存在' })
      return
    }
    if (outcome.validationCacheError) {
      res.status(500).json({ message: 'API Key 已更新，但 validation cache 失效失败' })
      return
    }
    res.json(ok(outcome.result))
  } catch (error) {
    if (error instanceof ApiKeyRevisionConflictError) {
      res.status(409).json({ message: error.message, currentRevision: error.currentRevision })
      return
    }
    const message = error instanceof Error ? error.message : '更新 API Key 失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

delegatedApiRouter.get('/ai-accounts', requireScope('ai_accounts.read'), async (req, res, next) => {
  try {
    const page = await listAccountItemsPageAsync(delegatedAccess(req), {
      page: positiveQueryInteger(req.query.page),
      pageSize: positiveQueryInteger(req.query.pageSize)
    })
    const items = page.items.filter(isOwnedPhysicalAccount).map(aiAccountDto)
    res.json(ok({ ...page, items, total: items.length, hasMore: false }))
  } catch (error) {
    next(error)
  }
})

delegatedApiRouter.patch('/ai-accounts/:id', requireScope('ai_accounts.write'), async (req, res) => {
  const parsed = accountPatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'AI 账户参数无效')))
    return
  }
  try {
    const access = delegatedAccess(req)
    const current = await ownedPhysicalAccount(req.params.id, access)
    if (!current) {
      res.status(404).json({ message: 'AI 账户不存在' })
      return
    }
    const changed = await patchAccountManagementAsync(req.params.id, parsed.data, access)
    if (!changed) {
      res.status(404).json({ message: 'AI 账户不存在' })
      return
    }
    const account = await ownedPhysicalAccount(req.params.id, access)
    res.json(ok(account ? aiAccountDto(account) : {
      id: changed.id,
      configRevision: changed.configRevision,
      changedFields: changed.changedFields,
      authorizationInstancesAffected: changed.authorizationInstancesAffected
    }))
  } catch (error) {
    if (error instanceof AccountManagementPatchRevisionConflictError) {
      res.status(409).json({ message: '账户配置已被其他操作更新，请刷新后重试' })
      return
    }
    const message = error instanceof Error ? error.message : '更新 AI 账户失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

delegatedApiRouter.get('/request-limits', requireScope('request_limits.read'), async (req, res, next) => {
  try {
    const account = await currentAccount(req)
    if (!account) {
      res.status(404).json({ message: '用户不存在' })
      return
    }
    res.json(ok(await requestLimitSnapshot(account)))
  } catch (error) {
    next(error)
  }
})

export function requireDelegatedAccess(req: DelegatedRequest, res: Response, next: NextFunction): void {
  const token = bearerToken(req)
  const context = token ? findAccessTokenContext(token) : undefined
  if (!context) {
    res.setHeader('WWW-Authenticate', 'Bearer error="invalid_token"')
    res.status(401).json({ error: 'invalid_token', error_description: '访问令牌无效或已过期' })
    return
  }
  req.delegatedAccessToken = context
  next()
}

export function requireScope(scope: string) {
  return (req: DelegatedRequest, res: Response, next: NextFunction): void => {
    if (hasScope(req, scope)) {
      next()
      return
    }
    sendInsufficientScope(res, `${delegatedPrefix}${scope}`)
  }
}

function delegatedContext(req: DelegatedRequest): OAuthAccessTokenContext {
  if (!req.delegatedAccessToken) throw new Error('个人委托访问上下文缺失')
  return req.delegatedAccessToken
}

function delegatedAccess(req: DelegatedRequest) {
  return { systemAccountId: delegatedContext(req).systemAccountId, role: 'user' as const }
}

function hasScope(req: DelegatedRequest, scope: string): boolean {
  return delegatedContext(req).scopes.includes(`${delegatedPrefix}${scope}`)
}

function sendInsufficientScope(res: Response, scope: string): void {
  res.setHeader('WWW-Authenticate', `Bearer error="insufficient_scope", scope="${scope}"`)
  res.status(403).json({ error: 'insufficient_scope', error_description: '访问令牌缺少所需权限' })
}

function bearerToken(req: Request): string | undefined {
  const value = req.headers.authorization
  if (Array.isArray(value) || typeof value !== 'string') return undefined
  const match = /^Bearer\s+([^\s]+)$/i.exec(value.trim())
  return match?.[1]
}

async function currentAccount(req: DelegatedRequest): Promise<SystemAccountSummary | undefined> {
  return findSystemAccountByIdAsync(delegatedContext(req).systemAccountId)
}

async function ownGroup(id: string, access: ReturnType<typeof delegatedAccess>): Promise<GroupSummary | undefined> {
  const group = await findGroupSummaryAsync(id, access)
  return group?.accessType === 'owner' ? group : undefined
}

async function ownRouteStrategyGroups(bindings: Array<{ groupId: string }>, access: ReturnType<typeof delegatedAccess>): Promise<boolean> {
  for (const binding of bindings) {
    if (!await ownGroup(binding.groupId, access)) return false
  }
  return true
}

async function hasRouteStrategyBinding(groupId: string, access: ReturnType<typeof delegatedAccess>): Promise<boolean> {
  let page = 1
  while (true) {
    const result = await listRouteStrategiesPageAsync(access, { page, pageSize: 500 })
    if (result.items.some((item) => item.groupBindings.some((binding) => binding.groupId === groupId))) return true
    if (!result.hasMore) return false
    page += 1
  }
}

async function ownedPhysicalAccount(id: string, access: ReturnType<typeof delegatedAccess>): Promise<AccountSummary | undefined> {
  const page = await listAccountItemsPageAsync(access, { page: 1, pageSize: 1, ids: [id] })
  const account = page.items[0]
  return account && isOwnedPhysicalAccount(account) ? account : undefined
}

function isOwnedPhysicalAccount(account: AccountSummary): boolean {
  return account.accessType === 'owner' && !account.authorizationInstanceSourceAccountId
}

function profileDto(account: SystemAccountSummary) {
  return { username: account.username, displayName: account.displayName }
}

function groupDto(group: Pick<GroupListItem, 'id' | 'name' | 'providerCode' | 'description' | 'enabled' | 'groupType' | 'updatedAt'> | GroupSummary) {
  return {
    id: group.id,
    name: group.name,
    providerCode: group.providerCode,
    ...(group.description ? { description: group.description } : {}),
    enabled: group.enabled,
    groupType: group.groupType,
    ...('updatedAt' in group ? { updatedAt: group.updatedAt } : {})
  }
}

function routeStrategyListDto(strategy: {
  id: string
  name: string
  description?: string
  mode: string
  status: string
  isDefault: boolean
  bindingCount?: number
  apiKeyCount?: number
  groupBindingPreview?: unknown
  createdAt: string
  updatedAt: string
}) {
  return {
    id: strategy.id,
    name: strategy.name,
    ...(strategy.description ? { description: strategy.description } : {}),
    mode: strategy.mode,
    status: strategy.status,
    isDefault: strategy.isDefault,
    bindingCount: strategy.bindingCount ?? 0,
    apiKeyCount: strategy.apiKeyCount ?? 0,
    groupBindings: strategy.groupBindingPreview ?? [],
    createdAt: strategy.createdAt,
    updatedAt: strategy.updatedAt
  }
}

function routeStrategyDto(strategy: RouteStrategySummary) {
  return {
    id: strategy.id,
    name: strategy.name,
    ...(strategy.description ? { description: strategy.description } : {}),
    mode: strategy.mode,
    status: strategy.status,
    isDefault: strategy.isDefault,
    normalRoutingConfig: strategy.normalRoutingConfig,
    hybridRoutingConfig: strategy.hybridRoutingConfig,
    groupBindings: strategy.groupBindings.map((binding) => ({
      id: binding.id,
      groupId: binding.groupId,
      groupName: binding.groupName,
      providerCode: binding.providerCode,
      priority: binding.priority,
      weight: binding.weight,
      status: binding.status,
      groupEnabled: binding.groupEnabled
    })),
    apiKeyCount: strategy.apiKeyCount,
    createdAt: strategy.createdAt,
    updatedAt: strategy.updatedAt
  }
}

function apiKeyDto(key: {
  id: string
  name: string
  description?: string
  keyPrefix: string
  keySuffix: string
  status: 'active' | 'disabled'
  routeStrategyId: string
  routeStrategyName?: string
  routeStrategyMode?: string
  routeStrategyStatus?: string
  revision: string
}) {
  return {
    id: key.id,
    name: key.name,
    ...(key.description ? { description: key.description } : {}),
    keyPrefix: key.keyPrefix,
    keySuffix: key.keySuffix,
    status: key.status,
    routeStrategyId: key.routeStrategyId,
    ...(key.routeStrategyName ? { routeStrategyName: key.routeStrategyName } : {}),
    ...(key.routeStrategyMode ? { routeStrategyMode: key.routeStrategyMode } : {}),
    ...(key.routeStrategyStatus ? { routeStrategyStatus: key.routeStrategyStatus } : {}),
    revision: key.revision
  }
}

function aiAccountDto(account: AccountSummary) {
  return {
    id: account.id,
    configRevision: account.configRevision,
    providerCode: account.providerCode,
    ...(account.providerProtocolProfileId ? { providerProtocolProfileId: account.providerProtocolProfileId } : {}),
    ...(account.protocolCode ? { protocolCode: account.protocolCode } : {}),
    ...(account.protocolVersion ? { protocolVersion: account.protocolVersion } : {}),
    name: account.name,
    type: account.type,
    status: account.status,
    schedulable: account.schedulable,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    ...(account.supportedModels ? { supportedModels: account.supportedModels } : {}),
    ...(account.modelMappings ? { modelMappings: account.modelMappings } : {}),
    ...(account.tags ? { tags: account.tags.map((tag) => ({ id: tag.id, name: tag.name })) } : {}),
    healthCheckModel: account.healthCheckModel,
    healthCheckEndpointMode: account.healthCheckEndpointMode
  }
}

async function requestLimitSnapshot(account: SystemAccountSummary) {
  const settings = await getSettingsAsync() as unknown as GlobalUserRequestLimitSettings
  const limits = resolveEffectiveUserRequestLimits(settings, account.requestLimits)
  const finite = USER_REQUEST_LIMIT_WINDOWS.filter((window) => limits[window].limit > 0)
  const nowMs = Date.now()
  const buckets = finite.map((window) => requestLimitBucket(window, limits.timezone, account.id, nowMs))
  const usage = finite.length === 0
    ? { status: 'not_tracked' as const, totals: new Map<UserRequestLimitWindow, number>() }
    : await readRequestLimitTotals(buckets)
  const unavailable = usage.status === 'unavailable'
  const windows = Object.fromEntries(USER_REQUEST_LIMIT_WINDOWS.map((window) => {
    const effective = limits[window]
    const bucket = requestLimitBucket(window, limits.timezone, account.id, nowMs)
    const unlimited = effective.limit === 0
    const used = unlimited || unavailable ? null : usage.totals.get(window) ?? 0
    return [window, {
      limit: effective.limit,
      limitMode: unlimited ? 'unlimited' : 'limited',
      usageTracked: !unlimited,
      used,
      remaining: used === null ? null : Math.max(0, effective.limit - used),
      source: effective.source,
      resetsAt: new Date(bucket.resetsAtMs).toISOString()
    } satisfies RequestLimitWindowSnapshot]
  })) as Record<UserRequestLimitWindow, RequestLimitWindowSnapshot>
  return {
    windows,
    usageStatus: usage.status,
    asOf: new Date().toISOString(),
    timezone: limits.timezone,
    overrideActive: limits.overrideActive,
    ...(limits.overrideExpiresOn ? { overrideExpiresOn: limits.overrideExpiresOn } : {})
  }
}

async function readRequestLimitTotals(buckets: RequestLimitBucket[]): Promise<{
  status: 'estimated' | 'unavailable'
  totals: Map<UserRequestLimitWindow, number>
}> {
  const redisUrl = runtimeConfig.runtimeStateDriver === 'redis' ? runtimeConfig.redis.stateUrl : undefined
  if (!redisUrl) return { status: 'unavailable', totals: new Map() }
  try {
    const replies = await runRedisOperationWithDeadline(redisUrl, {
      operationName: '个人委托请求限制读取',
      timeoutMs: redisReadDeadlineMs
    }, async (client) => {
      // node-redis MULTI queues the four read-only HGET commands into one pipeline and never writes state.
      const pipeline = (client as unknown as RedisPipelineClient).multi()
      for (const bucket of buckets) pipeline.hGet(bucket.redisKey, '__total')
      return pipeline.exec()
    })
    if (replies.some((reply) => reply instanceof Error)) {
      return { status: 'unavailable', totals: new Map() }
    }
    const totals = new Map<UserRequestLimitWindow, number>()
    for (const [index, bucket] of buckets.entries()) {
      totals.set(bucket.window, nonNegativeInteger(replies[index]))
    }
    return { status: 'estimated', totals }
  } catch {
    return { status: 'unavailable', totals: new Map() }
  }
}

interface RedisPipeline {
  hGet(key: string, field: string): RedisPipeline
  exec(): Promise<unknown[]>
}

interface RedisPipelineClient {
  multi(): RedisPipeline
}

interface RequestLimitBucket {
  window: UserRequestLimitWindow
  bucket: string
  resetsAtMs: number
  redisKey: string
}

function requestLimitBucket(window: UserRequestLimitWindow, timezone: string, systemAccountId: string, nowMs: number): RequestLimitBucket {
  const date = new Date(nowMs)
  const parts = dateParts(timezone, nowMs)
  const dayEpoch = Date.UTC(parts.year, parts.month - 1, parts.day)
  const mondayEpoch = dayEpoch - ((new Date(dayEpoch).getUTCDay() + 6) % 7) * 86_400_000
  const minute = Math.floor(nowMs / 60_000)
  const bucket = window === 'perMinute'
    ? String(minute)
    : window === 'perDay'
      ? `${parts.year}-${pad(parts.month)}-${pad(parts.day)}`
      : window === 'perWeek'
        ? dateKey(new Date(mondayEpoch))
        : `${parts.year}-${pad(parts.month)}`
  const resetsAtMs = window === 'perMinute'
    ? (minute + 1) * 60_000
    : nowMs + (window === 'perDay' ? 86_400_000 : window === 'perWeek' ? 7 * 86_400_000 : 31 * 86_400_000)
  return {
    window,
    bucket,
    resetsAtMs,
    redisKey: `${runtimeConfig.redis.namespace}:gateway:user-request-limit:${window}:${bucket}:${systemAccountId}`
  }
}

function dateParts(timezone: string, nowMs: number): { year: number; month: number; day: number } {
  const formatter = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone || 'UTC',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  })
  let year = 0
  let month = 0
  let day = 0
  for (const part of formatter.formatToParts(nowMs)) {
    if (part.type === 'year') year = Number(part.value)
    else if (part.type === 'month') month = Number(part.value)
    else if (part.type === 'day') day = Number(part.value)
  }
  return { year, month, day }
}

function dateKey(value: Date): string {
  return `${value.getUTCFullYear()}-${pad(value.getUTCMonth() + 1)}-${pad(value.getUTCDate())}`
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

function nonNegativeInteger(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? Math.floor(parsed) : 0
}

function routeStrategyListOptions(req: Request): RouteStrategyListOptions {
  return {
    page: positiveQueryInteger(req.query.page),
    pageSize: positiveQueryInteger(req.query.pageSize),
    keyword: textQuery(req.query.keyword),
    mode: routeStrategyModeQuery(req.query.mode),
    status: routeStrategyStatusQuery(req.query.status)
  }
}

function routeStrategyModeQuery(value: unknown): RouteStrategyListOptions['mode'] {
  const text = textQuery(value)
  return text === 'normal' || text === 'hybrid_smart' || text === 'weighted' || text === 'failover' || text === 'round_robin' || text === 'all'
    ? text
    : undefined
}

function routeStrategyStatusQuery(value: unknown): RouteStrategyListOptions['status'] {
  const text = textQuery(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

function positiveQueryInteger(value: unknown): number | undefined {
  const text = textQuery(value)
  if (!text || !/^\d+$/.test(text)) return undefined
  const parsed = Number(text)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : undefined
}

function textQuery(value: unknown): string | undefined {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
}

function omit<T extends Record<string, unknown>, K extends keyof T>(value: T, key: K): Omit<T, K> {
  const { [key]: _removed, ...result } = value
  return result
}
