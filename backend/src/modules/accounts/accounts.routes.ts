import { Router } from 'express'
import { z } from 'zod'

import type { AccountSummary } from '../../domain/types.js'
import { badRequest, ok } from '../../shared/http.js'
import { DuplicateAccountCredentialError, ProxyProfileUnavailableError, clearAccountFailureState, createAccount, deleteAccount, findAccountForTest, listAccountsPage, listGroups, listProviders, migrateAccountTraffic, setAccountGroup, updateAccount, updateAuthorizedAccountBindingDispatch, type AccountListOptions, type AccountListSchedulableFilter, type AccountListSortDirection, type AccountListSortField } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/openai-gateway-session-affinity.service.js'
import { diffSafeFields, operationMode, recordOperationLog, resolveOperationOwner, runLoggedOperation, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { testOpenAIAccount } from './account-test.service.js'

export const accountsRouter = Router()

const accountCreateSchema = z.object({
  providerCode: z.string().min(1).optional(),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  status: z.enum(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
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

const authorizedAccountDispatchSchema = z.object({
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clearFailureState: z.boolean().optional()
})

const accountListSortFields = new Set<AccountListSortField>([
  'priority',
  'superPriority',
  'fallback',
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

accountsRouter.post('/', mutationGuard({
  operationKey: 'accounts.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')) || 'openai',
    type: normalizedText(bodyField(req, 'type')),
    name: normalizedText(bodyField(req, 'name')),
    credential: accountCredentialFingerprint(bodyField(req, 'credentials'))
  })
}), (req, res) => {
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
    const account = runLoggedOperation(() => {
      const account = createAccount({
        ...parsed.data,
        providerCode
      }, requestAccess)
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        afterCommit: clearGatewayRuntimeCache,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'create',
          operationKey: 'accounts.create',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `创建 AI 账户：${account.name}`,
          changes: [
            safeChange('name', '名称', undefined, account.name),
            safeChange('providerCode', '供应商', undefined, account.providerCode),
            safeChange('type', '账户类型', undefined, account.type),
            safeChange('status', '状态', undefined, account.status),
            safeChange('credentials', '凭据', undefined, parsed.data.credentials),
            safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
            safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
            safeChange('errorPolicyId', '错误策略', undefined, account.errorPolicyId),
            safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
            safeChange('notes', '备注', undefined, account.notes)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
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
    const message = error instanceof Error ? error.message : '账户参数无效'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
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

  const before = findAccountForTest(req.params.id, requestAccess)
  try {
    const account = runLoggedOperation(() => {
      const account = setAccountGroup(req.params.id, parsed.data.groupId, requestAccess)
      if (!account) {
        throw new Error('账户不存在、授权已失效或分组不可用')
      }
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        afterCommit: clearGatewayRuntimeCache,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'bind_group',
          operationKey: 'accounts.bind_group',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `绑定账户分组：${account.name}`,
          changes: [safeChange('groupId', '绑定分组', before?.boundGroupId, account.boundGroupId)],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(account))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '绑定账户分组失败'))
  }
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
    let affinityResult = { migratedSessionCount: 0 }
    const migration = runLoggedOperation(() => {
      const migration = migrateAccountTraffic({
        sourceAccountId: req.params.id,
        targetAccountId: parsed.data.targetAccountId,
        sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
      }, requestAccess)
      if (!migration) {
        throw new Error('账户不存在或无权迁移')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(migration.sourceAccount, requestAccess)
        ?? resolveOperationOwner(migration.sourceAccount as unknown as Record<string, unknown>, requestAccess)
      return {
        result: migration,
        afterCommit: () => {
          const affinityScope = authorizedMigrationAffinityScope(migration.sourceAccount, requestAccess)
          affinityResult = migrateOpenAIAccountSessionAffinity(req.params.id, parsed.data.targetAccountId, affinityScope)
          clearGatewayRuntimeCache()
        },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'traffic_migration',
          operationKey: 'accounts.traffic_migration',
          resourceType: 'account',
          resourceId: migration.sourceAccount.id,
          resourceName: migration.sourceAccount.name,
          summary: `迁移账户流量：${migration.sourceAccount.name} -> ${migration.targetAccount.name}`,
          changes: [
            safeChange('targetAccountId', '目标账户', undefined, migration.targetAccount.name),
            safeChange('sourceStatus', '源账户状态', undefined, parsed.data.sourceStatus ?? 'temporary_unavailable')
          ],
          targets: [
            {
              targetType: 'account',
              targetId: migration.targetAccount.id,
              targetName: migration.targetAccount.name,
              targetOwnerSystemAccountId: resolveOperationOwner(migration.targetAccount as unknown as Record<string, unknown>, requestAccess),
              relation: 'affected'
            }
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok({
      ...migration,
      ...affinityResult,
      sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
    }))
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在或无权迁移') {
      res.status(404).json({ message: '账户不存在或无权迁移' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '迁移流量失败'))
  }
})

function authorizedLocalOperationOwner(account: AccountSummary, access?: RequestAccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function authorizedMigrationAffinityScope(account: AccountSummary, access?: RequestAccessScope): { systemAccountId: string; groupId: string } | undefined {
  const systemAccountId = effectiveRequestSystemAccountId(access)
  return account.accessType === 'authorized' && account.boundGroupId && systemAccountId
    ? { systemAccountId, groupId: account.boundGroupId }
    : undefined
}

function effectiveRequestSystemAccountId(access?: RequestAccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

accountsRouter.patch('/:id/authorized-dispatch', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = authorizedAccountDispatchSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('授权账户调度参数无效'))
    return
  }
  try {
    const account = runLoggedOperation(() => {
      const account = updateAuthorizedAccountBindingDispatch(req.params.id, parsed.data, requestAccess)
      if (!account) {
        throw new Error('授权账户不存在或尚未绑定分组')
      }
      const ownerSystemAccountId = effectiveRequestSystemAccountId(requestAccess)
      return {
        result: account,
        afterCommit: clearGatewayRuntimeCache,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'authorized_dispatch',
          operationKey: 'accounts.authorized_dispatch',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `调整授权账户使用设置：${account.name}`,
          changes: [
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'superPriorityEnabled') ? [safeChange('localSuperPriorityEnabled', '本地超级优先', undefined, parsed.data.superPriorityEnabled)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'fallbackEnabled') ? [safeChange('localFallbackEnabled', '本地降级备用', undefined, parsed.data.fallbackEnabled)] : []),
            ...(parsed.data.clearFailureState === true ? [safeChange('clearLocalFailureState', '恢复本地异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (!account) {
      res.status(404).json({ message: '授权账户不存在或尚未绑定分组' })
      return
    }
    res.json(ok(account))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新授权账户调度设置失败'))
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
  try {
    const account = runLoggedOperation(() => {
      if (body.clearFailureState === true) {
        const restoredAccount = clearAccountFailureState(req.params.id, requestAccess)
        if (!restoredAccount) {
          throw new Error('账户不存在')
        }
      }
      let account = updateAccount(req.params.id, body, requestAccess)
      if (!account) {
        throw new Error('账户不存在')
      }
      if (hasGroupId) {
        const nextAccount = setAccountGroup(account.id, body.groupId as string, requestAccess)
        if (!nextAccount) {
          throw new Error('账户分组无效')
        }
        account = nextAccount
      }
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        afterCommit: clearGatewayRuntimeCache,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: body.clearFailureState === true ? 'restore' : 'update',
          operationKey: body.clearFailureState === true ? 'accounts.restore' : 'accounts.update',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: body.clearFailureState === true ? `恢复 AI 账户：${account.name}` : `更新 AI 账户：${account.name}`,
          changes: [
            ...diffSafeFields(existingAccount as unknown as Record<string, unknown>, account as unknown as Record<string, unknown>, {
              name: '名称',
              notes: '备注',
              credentials: '凭据',
              status: '状态',
              concurrencyLimit: '并发限制',
              priority: '优先级',
              superPriorityEnabled: '超级优先',
              fallbackEnabled: '降级备用',
              proxyProfileId: '代理',
              errorPolicyId: '错误策略',
              schedulable: '参与调度',
              accountExpiresAt: '过期时间',
              boundGroupId: '绑定分组',
              cooldownUntil: '冷却结束时间',
              lastErrorCode: '异常类型',
              lastErrorMessage: '错误信息'
            }),
            ...(body.clearFailureState === true ? [safeChange('clearFailureState', '恢复异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(account))
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (error instanceof Error && error.message === '账户分组无效') {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新账户失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
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
    res.status(400).json({ message: '当前仅支持测试 OpenAI 账户' })
    return
  }
  if (account.status === 'disabled') {
    res.status(400).json({ message: '账户已停用，不能执行测试；请先手动启用账户' })
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
    if (result.accountStatusChanged) {
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      recordOperationLog({
        operationScopeSystemAccountId: ownerSystemAccountId,
        mode: operationMode(requestAccess),
        module: 'accounts',
        action: 'test_status_changed',
        operationKey: 'accounts.test_status_changed',
        resourceType: 'account',
        resourceId: account.id,
        resourceName: account.name,
        summary: `账户测试更新状态：${account.name}`,
        changes: [safeChange('status', '状态', account.status, result.accountStatus)],
        viewers: viewer(ownerSystemAccountId, 'resource_owner')
      }, req)
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
  const before = findAccountForTest(req.params.id, requestAccess) ?? listAccountsPage(requestAccess, { limit: 200 }).items.find((item) => item.id === req.params.id)
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    runLoggedOperation(() => {
      if (!deleteAccount(req.params.id, requestAccess)) {
        throw new Error('账户不存在')
      }
      return {
        result: true,
        afterCommit: clearGatewayRuntimeCache,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'delete',
          operationKey: 'accounts.delete',
          resourceType: 'account',
          resourceId: req.params.id,
          resourceName: before?.name ?? req.params.id,
          summary: `删除 AI 账户：${before?.name ?? req.params.id}`,
          changes: [safeChange('deleted', '删除状态', false, true)],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    throw error
  }
  res.status(204).send()
})

function accountCredentialFingerprint(credentials: unknown): string {
  if (typeof credentials !== 'object' || credentials === null || Array.isArray(credentials)) {
    return ''
  }
  const record = credentials as Record<string, unknown>
  return sensitiveFingerprint(
    record.apiKey
      ?? record.api_key
      ?? record.refreshToken
      ?? record.refresh_token
      ?? record.email
      ?? record.accountId
      ?? record.account_id
      ?? ''
  )
}
