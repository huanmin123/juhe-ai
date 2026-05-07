import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { DuplicateAccountCredentialError, ProxyProfileUnavailableError, clearAccountFailureState, createAccount, deleteAccount, findAccountForTest, listAccountsPage, listGroups, listProviders, migrateAccountTraffic, setAccountGroup, updateAccount, type AccountListOptions, type AccountListSchedulableFilter, type AccountListSortDirection, type AccountListSortField } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/openai-gateway-session-affinity.service.js'
import { testOpenAIAccount } from './account-test.service.js'

export const accountsRouter = Router()

const accountCreateSchema = z.object({
  providerCode: z.string().min(1).optional(),
  name: z.string().min(1),
  type: z.string().min(1),
  credentials: z.record(z.unknown()).optional(),
  status: z.enum(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  account_expires_at: z.string().nullable().optional(),
  notes: z.string().optional()
})

const accountTestSchema = z.object({
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional()
}).optional()

const accountGroupSchema = z.object({
  groupId: z.string().trim().min(1, '分组不能为空')
})

const accountTrafficMigrationSchema = z.object({
  targetAccountId: z.string().trim().min(1, '目标账户不能为空'),
  sourceStatus: z.enum(['temporary_unavailable', 'disabled']).optional()
})

const accountListSortFields = new Set<AccountListSortField>([
  'priority',
  'superPriority',
  'qualityScore',
  'name',
  'type',
  'providerCode',
  'systemAccount',
  'concurrency',
  'status',
  'accountExpiresAt',
  'lastUsedAt',
  'notes'
])

accountsRouter.get('/', (req, res) => {
  res.json(ok(listAccountsPage(getRequestAccessScope(req.query.systemAccountId), parseAccountListOptions(req.query))))
})

function parseAccountListOptions(query: Record<string, unknown>): AccountListOptions {
  const rawSorts = query.sorts ?? query.sort
  const sorts = stringValues(rawSorts)
    .flatMap((value) => value.split(','))
    .map((value) => value.trim())
    .filter(Boolean)
    .map(parseAccountListSort)
    .filter((sort): sort is NonNullable<ReturnType<typeof parseAccountListSort>> => Boolean(sort))
  return {
    sorts,
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    limit: integerQueryValue(query.limit),
    keyword: optionalQueryText(query.keyword),
    type: optionalQueryText(query.type),
    status: optionalQueryText(query.status),
    schedulable: schedulableQueryValue(query.schedulable)
  }
}

function parseAccountListSort(value: string): { field: AccountListSortField; order: AccountListSortDirection } | undefined {
  const [field, order] = value.split(':').map((item) => item.trim())
  if (!accountListSortFields.has(field as AccountListSortField)) return undefined
  if (order !== 'asc' && order !== 'desc') return undefined
  return { field: field as AccountListSortField, order }
}

function stringValues(value: unknown): string[] {
  if (typeof value === 'string') return [value]
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === 'string')
  return []
}

function integerQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : typeof text === 'number' ? text : undefined
  return Number.isInteger(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

function schedulableQueryValue(value: unknown): AccountListSchedulableFilter | undefined {
  const text = optionalQueryText(value)
  return text === 'all' || text === 'enabled' || text === 'disabled' || text === 'cooling' ? text : undefined
}

accountsRouter.post('/', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户参数无效'))
    return
  }

  const providerCode = parsed.data.providerCode?.trim() || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    res.status(400).json(badRequest(`不支持的供应商：${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`供应商已停用：${providerCode}`))
    return
  }
  if (!provider.accountTypes.includes(parsed.data.type)) {
    res.status(400).json(badRequest(`供应商 ${providerCode} 不支持账户类型 ${parsed.data.type}`))
    return
  }
  const groupId = typeof parsed.data.groupId === 'string' && parsed.data.groupId ? parsed.data.groupId : undefined
  if (groupId) {
    const group = listGroups(requestAccess).find((item) => item.id === groupId)
    if (!group || group.providerCode !== providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }

  try {
    const account = createAccount({
      ...parsed.data,
      providerCode
    }, requestAccess)
    clearGatewayRuntimeCache()
    res.status(201).json(ok(account))
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '账户参数无效'))
  }
})

accountsRouter.post('/:id/group', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountGroupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('请选择要绑定的分组'))
    return
  }

  const account = setAccountGroup(req.params.id, parsed.data.groupId, requestAccess)
  if (!account) {
    res.status(400).json(badRequest('账户不存在、授权已失效或分组不可用'))
    return
  }
  clearGatewayRuntimeCache()
  res.json(ok(account))
})

accountsRouter.post('/:id/traffic-migration', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountTrafficMigrationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('迁移流量参数无效'))
    return
  }

  try {
    const migration = migrateAccountTraffic({
      sourceAccountId: req.params.id,
      targetAccountId: parsed.data.targetAccountId,
      sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
    }, requestAccess)
    if (!migration) {
      res.status(404).json({ message: '账户不存在或无权迁移' })
      return
    }
    const affinityResult = migrateOpenAIAccountSessionAffinity(req.params.id, parsed.data.targetAccountId)
    clearGatewayRuntimeCache()
    res.json(ok({
      ...migration,
      ...affinityResult,
      sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
    }))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '迁移流量失败'))
  }
})

accountsRouter.patch('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const body = req.body as Record<string, unknown>
  const existingAccount = findAccountForTest(req.params.id, requestAccess)
  if (!existingAccount) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  const hasGroupId = Object.prototype.hasOwnProperty.call(body, 'groupId')
  if (hasGroupId && (typeof body.groupId !== 'string' || !body.groupId)) {
    res.status(400).json(badRequest('账户分组不能为空'))
    return
  }
  if (hasGroupId) {
    const group = listGroups(requestAccess).find((item) => item.id === body.groupId)
    if (!group || group.providerCode !== existingAccount.providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  if (body.clearFailureState === true) {
    clearAccountFailureState(req.params.id, {}, requestAccess)
  }
  let account: ReturnType<typeof updateAccount>
  try {
    account = updateAccount(req.params.id, body, requestAccess)
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新账户失败'))
    return
  }
  if (!account) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  if (hasGroupId) {
    const nextAccount = setAccountGroup(account.id, body.groupId as string, requestAccess)
    if (!nextAccount) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  clearGatewayRuntimeCache()
  res.json(ok(account))
})

accountsRouter.post('/:id/test', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountTestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户测试参数无效'))
    return
  }
  const account = findAccountForTest(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  if (account.providerCode !== 'openai') {
    res.status(400).json({ message: '第一阶段仅支持测试 OpenAI 账户' })
    return
  }

  const abortController = new AbortController()
  req.once('aborted', () => abortController.abort())
  res.once('close', () => {
    if (!res.writableEnded) {
      abortController.abort()
    }
  })
  try {
    const result = await testOpenAIAccount(account, { ...(parsed.data ?? {}), signal: abortController.signal })
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    res.json(ok(result))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    throw error
  }
})

accountsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!deleteAccount(req.params.id, requestAccess)) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  clearGatewayRuntimeCache()
  res.status(204).send()
})
