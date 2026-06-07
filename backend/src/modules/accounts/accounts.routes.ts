import { Router } from 'express'
import { z } from 'zod'

import { isAdminRole, type AccountSummary } from '../../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { accountAvailabilityScheduleFromRequest, accountAvailabilityScheduleJson } from '../../storage/account-availability-schedule.js'
import { newId } from '../../storage/database.js'
import { ProxyProfileUnavailableError, accountTestUnavailableMessage, clearAccountFailureState, createAccount, deleteAccountWithRelatedCleanup, findAccountForTest, findAccountSummary, findGroupSummary, listAccountOptions, listAccountsPage, listProviders, migrateAccountTraffic, normalizeAccountCredentialsForWrite, normalizeAccountModelMappingsForProvider, returnAccountAuthorizationInstanceForGrantee, setAccountGroup, updateAccount, updateAuthorizedAccountBindingDispatch, type AccountListOptions, type AccountOptionListOptions, type AccountListSchedulableFilter, type AccountListSortDirection, type AccountListSortField } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountConcurrencyToAccountList, applyServerAccountRuntimeToAccount } from '../gateway/gateway-runtime-snapshot.service.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/openai-gateway-session-affinity.service.js'
import { diffSafeFields, operationMode, ownerTarget, recordOperationLog, resolveOperationOwner, runLoggedOperation, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import { cancelAccountTestTask, createAccountTestTask, failAccountTestTask, getAccountTestTask, listAccountTestTasks, type AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { exportAccountsAsImportDocument } from './account-export.service.js'
import { accountImportMaxAccounts, executeAccountImport, previewAccountImport, type AccountImportOptions } from './account-import.service.js'
import { accountErrorPolicyValidationMessage, validateAccountCredentialsErrorHandlingRules } from './account-error-policy-validation.js'
import { sanitizeAccountListResponse, sanitizeAccountResponse, sanitizeAccountTrafficMigrationResponse } from './account-response-sanitizer.js'
import { accountStreamInterceptValidationMessage, validateAccountStreamInterceptRules } from './account-stream-intercept-policy-validation.js'
import { dispatchAccountTestCancel, dispatchAccountTestTasks } from './account-test-task-queue.service.js'

export const accountsRouter = Router()

const accountModelMappingSchema = z.object({
  sourceModel: z.string().trim().min(1),
  upstreamModel: z.string().trim().min(1),
  enabled: z.boolean().optional()
}).strict()

const accountCreateSchema = z.object({
  providerCode: z.string().trim().min(1),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  status: z.enum(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional()
}).strict()

const accountUpdateSchema = z.object({
  name: z.string().trim().min(1).optional(),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  status: z.enum(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  proxyProfileId: z.string().nullable().optional(),
  errorPolicyId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().trim().min(1, '账户分组不能为空').optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

const accountTestSchema = z.object({
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional()
}).strict().optional()

const accountDraftTestAccountSchema = z.object({
  providerCode: z.string().trim().min(1),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  proxyProfileId: z.string().nullable().optional(),
  groupId: z.string().trim().min(1),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional()
}).strict()

const accountDraftTestSchema = z.object({
  account: accountDraftTestAccountSchema,
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional()
}).strict()

type AccountDraftTestAccountRequest = z.infer<typeof accountDraftTestAccountSchema>

const accountGroupSchema = z.object({
  groupId: z.string().trim().min(1, '分组不能为空')
}).strict()

const accountTrafficMigrationSchema = z.object({
  targetAccountId: z.string().trim().min(1, '目标账户不能为空'),
  sourceStatus: z.enum(['temporary_unavailable', 'disabled']).optional()
}).strict()

const authorizedAccountDispatchSchema = z.object({
  status: z.enum(['active', 'disabled']).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

const accountImportRequestSchema = z.object({
  data: z.unknown(),
  options: z.object({
    createMissingGroups: z.boolean().optional(),
    createMissingProxies: z.boolean().optional(),
    skipDuplicates: z.boolean().optional()
  }).strict().optional()
}).strict()

const accountListSortFieldValues = [
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
  'lastUsedAt'
] as const
const accountListSortFields = new Set<AccountListSortField>(accountListSortFieldValues)

const accountExportFilterSchema = z.object({
  sorts: z.array(z.object({
    field: z.enum(accountListSortFieldValues),
    order: z.enum(['asc', 'desc'])
  }).strict()).max(accountListSortFieldValues.length).optional(),
  keyword: z.string().trim().max(200).optional(),
  providerCode: z.string().trim().max(80).optional(),
  groupId: z.string().trim().max(120).optional(),
  type: z.string().trim().max(80).optional(),
  status: z.union([
    z.string().trim(),
    z.array(z.string().trim()).max(20)
  ]).optional(),
  schedulable: z.enum(['all', 'enabled', 'disabled', 'cooling']).optional()
}).strict()

const accountExportByIdsRequestSchema = z.object({
  accountIds: z.array(z.string().trim().min(1)).min(1).max(accountImportMaxAccounts)
}).strict()

const accountExportByFiltersRequestSchema = z.object({
  filters: accountExportFilterSchema
}).strict()

const accountExportRequestSchema = z.union([
  accountExportByIdsRequestSchema,
  accountExportByFiltersRequestSchema
])

type AccountExportRequest = z.infer<typeof accountExportRequestSchema>

accountsRouter.get('/', async (req, res, next) => {
  try {
    const listStartedAt = performance.now()
    const result = listAccountsPage(getRequestAccessScope(req.query.systemAccountId), parseAccountListOptions(req.query))
    const listDurationMs = performance.now() - listStartedAt
    const concurrencyStartedAt = performance.now()
    const hydratedResult = await applyServerAccountConcurrencyToAccountList(result)
    const concurrencyDurationMs = performance.now() - concurrencyStartedAt
    res.setHeader('Server-Timing', [
      serverTimingMetric('account-list', listDurationMs),
      serverTimingMetric('account-concurrency', concurrencyDurationMs)
    ].join(', '))
    res.json(ok(sanitizeAccountListResponse(hydratedResult)))
  } catch (error) {
    next(error)
  }
})

accountsRouter.get('/options', (req, res, next) => {
  try {
    const options = listAccountOptions(getRequestAccessScope(req.query.systemAccountId), parseAccountOptionsQuery(req.query))
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

accountsRouter.post('/export', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = accountExportRequestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(`账户导出参数无效，单次最多导出 ${accountImportMaxAccounts} 个账户`))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  try {
    const result = exportAccountsForRequest(parsed.data, requestAccess)
    const ownerSystemAccountId = resolveOperationOwner(undefined, requestAccess)
    const matchedText = typeof result.summary.matchedAccounts === 'number' ? `，匹配 ${result.summary.matchedAccounts} 条` : ''
    const truncatedText = result.summary.truncated ? `，仅处理前 ${accountImportMaxAccounts} 条` : ''
    recordOperationLog({
      operationScopeSystemAccountId: ownerSystemAccountId,
      mode: operationMode(requestAccess),
      module: 'accounts',
      action: 'export',
      operationKey: 'accounts.export',
      resourceType: 'account',
      resourceName: 'AI 账户导出',
      summary: `导出 AI 账户：${result.summary.accounts} 个账户，${result.summary.proxies} 个代理${matchedText}${truncatedText}`,
      visibilityScope: isAdminRole(requestAccess.role) ? 'admin_only' : 'targeted',
      changes: [
        safeChange('accountExported', '导出账户数', undefined, result.summary.accounts),
        safeChange('proxyExported', '导出代理数', undefined, result.summary.proxies),
        safeChange('accountSkipped', '跳过账户数', undefined, result.summary.skippedAccounts),
        ...(typeof result.summary.matchedAccounts === 'number'
          ? [safeChange('accountMatched', '匹配账户数', undefined, result.summary.matchedAccounts)]
          : []),
        ...(result.summary.truncated
          ? [safeChange('accountExportTruncated', '导出结果截断', false, true)]
          : [])
      ],
      ...(!isAdminRole(requestAccess.role) ? { viewers: viewer(ownerSystemAccountId, 'resource_owner') } : {})
    }, req)
    res.json(ok(result))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '导出账户失败'))
  }
})

accountsRouter.get('/test-tasks', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const taskIds = queryTextList(req.query.ids, 200)
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  res.json(ok(listAccountTestTasks(taskIds, requestAccess)))
})

accountsRouter.get('/test-tasks/:taskId', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const task = getAccountTestTask(req.params.taskId, requestAccess)
  if (!task) {
    res.status(404).json({ message: '账户测试任务不存在' })
    return
  }
  res.json(ok(task))
})

accountsRouter.post('/test-tasks/:taskId/cancel', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const task = cancelAccountTestTask(req.params.taskId, requestAccess)
  if (!task) {
    res.status(404).json({ message: '账户测试任务不存在' })
    return
  }
  dispatchAccountTestCancel(task.id)
  res.json(ok(task))
})

accountsRouter.post('/test-draft', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(403).json({ message: '缺少系统账户上下文' })
    return
  }
  const parsed = accountDraftTestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户草稿测试参数无效'))
    return
  }
  const accountInput = parsed.data.account
  if (accountInput.providerCode !== 'openai') {
    res.status(400).json({ message: '当前仅支持测试 OpenAI 账户' })
    return
  }
  const group = findGroupSummary(accountInput.groupId, requestAccess)
  if (!group || group.providerCode !== accountInput.providerCode || group.permissions?.canManageAccounts === false) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const provider = listProviders().find((item) => item.code === accountInput.providerCode)
  if (!provider || !provider.accountTypes.includes(accountInput.type)) {
    res.status(400).json(badRequest(`供应商 ${accountInput.providerCode} 不支持账户类型 ${accountInput.type}`))
    return
  }
  const ownerSystemAccountId = group.ownerSystemAccountId ?? group.systemAccountId ?? requestAccess.systemAccountFilterId ?? requestAccess.systemAccountId
  if (!ownerSystemAccountId) {
    res.status(400).json(badRequest('账户分组缺少归属用户，无法测试'))
    return
  }

  try {
    const credentials = normalizeAccountCredentialsForWrite(accountInput.type, draftAccountCredentials(accountInput, provider.baseUrl))
    const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: accountInput.availabilitySchedule })
    const availabilityScheduleJson = accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined
    const clientCompatibility = normalizeOpenAIAccountClientCompatibility(accountInput.providerCode, accountInput.type, accountInput.clientCompatibility)
    const account = draftTestAccountSummary({
      account: accountInput,
      availabilitySchedule,
      clientCompatibility,
      credentials,
      groupName: group.name,
      ownerSystemAccountId
    })
    const { prompt: _ignoredPrompt, ...testOptions } = parsed.data
    const draftAccount: AccountTestDraftSnapshot = {
      id: account.id,
      ownerSystemAccountId,
      groupId: accountInput.groupId,
      groupName: group.name,
      providerCode: accountInput.providerCode,
      name: account.name,
      type: accountInput.type,
      credentials,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      superPriorityEnabled: account.superPriorityEnabled,
      fallbackEnabled: account.fallbackEnabled,
      clientCompatibility,
      supportedModels: account.supportedModels,
      modelMappings: account.modelMappings,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: account.accountExpiresAt,
      availabilitySchedule,
      availabilityScheduleJson,
      notes: account.notes
    }
    const task = createAccountTestTask({
      account,
      access: requestAccess,
      diagnostics: 'full',
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility,
      draftAccount
    })
    if (!dispatchAccountTestTasks([task.id])) {
      failAccountTestTask(task.id, '后台 worker 暂不可用，账号草稿测试任务未能投递')
      res.status(503).json({ message: '后台 worker 暂不可用，账号草稿测试任务未能投递' })
      return
    }
    res.status(202).json(ok(task))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户草稿测试任务失败'))
  }
})

accountsRouter.get('/:id', async (req, res, next) => {
  try {
    const scopeQuery = parseRequestScopeQuery(req.query)
    if (!scopeQuery.success) {
      res.status(400).json(badRequest(scopeQuery.message))
      return
    }
    const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
    const visibleAccount = findAccountSummary(req.params.id, requestAccess)
    if (!visibleAccount) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    if (visibleAccount.accessType === 'authorized') {
      const hydratedAccount = await applyServerAccountRuntimeToAccount(visibleAccount)
      res.json(ok(sanitizeAccountResponse(hydratedAccount)))
      return
    }
    if (visibleAccount.permissions?.canViewCredentials === false || visibleAccount.permissions?.canEdit === false) {
      res.status(403).json({ message: '无权查看账户凭据' })
      return
    }
    const account = findAccountForTest(req.params.id, requestAccess)
    if (!account) {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    const hydratedAccount = await applyServerAccountRuntimeToAccount(account)
    res.json(ok(hydratedAccount))
  } catch (error) {
    next(error)
  }
})

function parseAccountOptionsQuery(query: Record<string, unknown>): AccountOptionListOptions {
  return {
    ids: queryTextList(query.ids, 50),
    page: integerQueryValue(query.page),
    limit: optionLimitValue(integerQueryValue(query.limit)),
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    groupId: optionalQueryText(query.groupId),
    type: optionalQueryText(query.type),
    status: statusQueryValue(query.status),
    schedulable: schedulableQueryValue(query.schedulable)
  }
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}

function parseAccountListOptions(query: Record<string, unknown>): AccountListOptions {
  const sorts = stringValues(query.sorts)
    .flatMap((value) => value.split(','))
    .map((value) => value.trim())
    .filter(Boolean)
    .map(parseAccountListSort)
    .filter((sort): sort is NonNullable<ReturnType<typeof parseAccountListSort>> => Boolean(sort))
  return {
    sorts,
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    providerCode: optionalQueryText(query.providerCode),
    groupId: optionalQueryText(query.groupId),
    type: optionalQueryText(query.type),
    status: statusQueryValue(query.status),
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

function statusQueryValue(value: unknown): string | undefined {
  const statuses = stringValues(value)
    .flatMap((item) => item.split(','))
    .map((item) => item.trim())
    .filter((item) => item && item !== 'all')
  return statuses.length ? [...new Set(statuses)].join(',') : undefined
}

function schedulableQueryValue(value: unknown): AccountListSchedulableFilter | undefined {
  const text = optionalQueryText(value)
  return text === 'all' || text === 'enabled' || text === 'disabled' || text === 'cooling' ? text : undefined
}

function serverTimingMetric(name: string, durationMs: number): string {
  return `${name};dur=${Math.max(0, durationMs).toFixed(1)}`
}

function exportAccountsForRequest(request: AccountExportRequest, access: RequestAccessScope) {
  if ('accountIds' in request) {
    return exportAccountsAsImportDocument({ accountIds: request.accountIds }, access)
  }

  const page = listAccountsPage(access, accountExportListOptions(request.filters))
  const accountIds = page.items.map((account) => account.id)
  if (!accountIds.length) {
    throw new Error('当前筛选条件下没有匹配的 AI 账户')
  }
  return exportAccountsAsImportDocument({
    accountIds,
    matchedAccounts: page.total,
    truncated: page.hasMore
  }, access)
}

function accountExportListOptions(filters: z.infer<typeof accountExportFilterSchema>): AccountListOptions {
  return {
    sorts: filters.sorts,
    page: 1,
    pageSize: accountImportMaxAccounts,
    keyword: accountExportTextFilter(filters.keyword),
    providerCode: accountExportAllFilter(filters.providerCode),
    groupId: accountExportTextFilter(filters.groupId),
    type: accountExportAllFilter(filters.type),
    status: statusQueryValue(filters.status),
    schedulable: schedulableQueryValue(filters.schedulable)
  }
}

function accountExportTextFilter(value: string | undefined): string | undefined {
  const text = value?.trim()
  return text || undefined
}

function accountExportAllFilter(value: string | undefined): string | undefined {
  const text = accountExportTextFilter(value)
  return text && text !== 'all' ? text : undefined
}

accountsRouter.post('/import/preview', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = accountImportRequestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户导入参数无效'))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  res.json(ok(previewAccountImport(parsed.data.data, parsed.data.options, requestAccess)))
})

accountsRouter.post('/import/confirm', mutationGuard({
  operationKey: 'accounts.import',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    data: bodyField(req, 'data'),
    options: bodyField(req, 'options')
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const parsed = accountImportRequestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('账户导入参数无效'))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  if (!requestAccess) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  const importOptions: AccountImportOptions = parsed.data.options ?? {}
  const result = runLoggedOperation(() => {
    const result = executeAccountImport(parsed.data.data, importOptions, requestAccess)
    const ownerSystemAccountId = resolveOperationOwner(undefined, requestAccess)
    return {
      result,
      log: {
        operationScopeSystemAccountId: ownerSystemAccountId,
        mode: operationMode(requestAccess),
        module: 'accounts',
        action: 'import',
        operationKey: 'accounts.import',
        resourceType: 'account',
        resourceName: 'AI 账户导入',
        summary: `导入 AI 账户：创建 ${result.summary.accounts.create} 个，跳过 ${result.summary.accounts.skip} 个，失败 ${result.summary.accounts.failed} 个`,
        changes: [
          safeChange('accountCreated', '创建账户数', undefined, result.summary.accounts.create),
          safeChange('accountSkipped', '跳过账户数', undefined, result.summary.accounts.skip),
          safeChange('accountFailed', '失败账户数', undefined, result.summary.accounts.failed),
          safeChange('proxyCreated', '创建代理数', undefined, result.summary.proxies.create),
          safeChange('groupCreated', '创建分组数', undefined, result.summary.groups.create)
        ],
        viewers: viewer(ownerSystemAccountId, 'resource_owner')
      }
    }
  }, req)
  res.json(ok(result))
})

accountsRouter.post('/', mutationGuard({
  operationKey: 'accounts.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
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
  const errorPolicyValidationMessage = accountErrorPolicyValidationMessage(validateAccountCredentialsErrorHandlingRules(parsed.data.credentials))
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }
  const streamPolicyValidationMessage = accountStreamInterceptValidationMessage(validateAccountStreamInterceptRules(parsed.data.credentials?.stream_intercept_rules))
  if (streamPolicyValidationMessage) {
    res.status(400).json(badRequest(streamPolicyValidationMessage))
    return
  }

  const providerCode = parsed.data.providerCode
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
    const group = findGroupSummary(groupId, requestAccess)
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
            safeChange('clientCompatibility', '客户端兼容', undefined, account.clientCompatibility),
            safeChange('credentials', '凭据', undefined, parsed.data.credentials),
            safeChange('supportedModels', '支持模型', undefined, account.supportedModels),
            safeChange('modelMappings', '模型映射', undefined, account.modelMappings),
            safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
            safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
            safeChange('errorPolicyId', '错误策略', undefined, account.errorPolicyId),
            safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
            safeChange('availabilitySchedule', '可用时段计划', undefined, account.availabilitySchedule),
            safeChange('notes', '备注', undefined, account.notes)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
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
    res.status(400).json(badRequest('绑定分组参数无效'))
    return
  }

  const before = findAccountForTest(req.params.id, requestAccess)
  try {
    const account = runLoggedOperation(() => {
      const account = setAccountGroup(req.params.id, parsed.data.groupId, requestAccess)
      if (!account) {
        throw new Error('账户不存在、授权已失效或分组不可用')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
        ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
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
          changes: [
            safeChange('groupId', '绑定分组', before?.boundGroupId, account.boundGroupId)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(account)))
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
    res.json(ok(sanitizeAccountTrafficMigrationResponse({
      ...migration,
      ...affinityResult,
      sourceStatus: parsed.data.sourceStatus ?? 'temporary_unavailable'
    })))
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

async function clearAccountGatewayRuntimeAfterRestore(account: AccountSummary, access?: RequestAccessScope): Promise<void> {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
    : undefined
  await clearServerAccountRuntimeAvailability({
    accountId: account.id,
    authorizedBinding: account.accessType === 'authorized' && systemAccountId && account.boundGroupId && account.accountAuthorizationId
      ? {
          systemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  }).catch(() => undefined)
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

accountsRouter.patch('/:id/authorized-dispatch', async (req, res) => {
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
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'status') ? [safeChange('status', '实例状态', undefined, parsed.data.status)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'priority') ? [safeChange('priority', '分组内优先级', undefined, parsed.data.priority)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'superPriorityEnabled') ? [safeChange('superPriorityEnabled', '分组内超级优先', undefined, parsed.data.superPriorityEnabled)] : []),
            ...(Object.prototype.hasOwnProperty.call(parsed.data, 'fallbackEnabled') ? [safeChange('fallbackEnabled', '分组内降级备用', undefined, parsed.data.fallbackEnabled)] : []),
            ...(parsed.data.clearFailureState === true ? [safeChange('clearFailureState', '恢复实例异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (!account) {
      res.status(404).json({ message: '授权账户不存在或尚未绑定分组' })
      return
    }
    if (parsed.data.clearFailureState === true || parsed.data.status === 'active') {
      await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
    }
    res.json(ok(sanitizeAccountResponse(await applyServerAccountRuntimeToAccount(account))))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新授权账户调度设置失败'))
  }
})

accountsRouter.patch('/:id', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户更新参数无效'))
    return
  }
  const body = parsed.data as Record<string, unknown>
  const { groupId: requestedGroupId, clearFailureState: requestedClearFailureState, ...accountUpdateInput } = parsed.data
  const existingAccount = findAccountForTest(req.params.id, requestAccess)
  if (!existingAccount) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  const hasGroupId = Object.prototype.hasOwnProperty.call(body, 'groupId')
  const groupIdToBind = typeof requestedGroupId === 'string' ? requestedGroupId : undefined
  if (hasGroupId && !groupIdToBind) {
    res.status(400).json(badRequest('账户分组不能为空'))
    return
  }
  if (hasGroupId) {
    const group = findGroupSummary(groupIdToBind as string, requestAccess)
    if (!group || group.providerCode !== existingAccount.providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  const errorPolicyValidationMessage = accountErrorPolicyValidationMessage(validateAccountCredentialsErrorHandlingRules(body.credentials))
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }
  const streamPolicyValidationMessage = accountStreamInterceptValidationMessage(validateAccountStreamInterceptRules(credentialsRecordValue(body.credentials)?.stream_intercept_rules))
  if (streamPolicyValidationMessage) {
    res.status(400).json(badRequest(streamPolicyValidationMessage))
    return
  }
  const requestedCredentials = credentialsRecordValue(body.credentials)
  if (Object.prototype.hasOwnProperty.call(body, 'credentials') && requestedCredentials) {
    accountUpdateInput.credentials = mergeAccountCredentialsForUpdate(existingAccount, requestedCredentials)
  }
  try {
    const account = runLoggedOperation(() => {
      if (requestedClearFailureState === true) {
        const restoredAccount = clearAccountFailureState(req.params.id, requestAccess)
        if (!restoredAccount) {
          throw new Error('账户不存在')
        }
      }
      let account = updateAccount(req.params.id, accountUpdateInput, requestAccess)
      if (!account) {
        throw new Error('账户不存在')
      }
      if (hasGroupId) {
        const nextAccount = setAccountGroup(account.id, groupIdToBind as string, requestAccess)
        if (!nextAccount) {
          throw new Error('账户分组无效')
        }
        account = nextAccount
      }
      const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: account,
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: requestedClearFailureState === true ? 'restore' : 'update',
          operationKey: requestedClearFailureState === true ? 'accounts.restore' : 'accounts.update',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: requestedClearFailureState === true ? `恢复 AI 账户：${account.name}` : `更新 AI 账户：${account.name}`,
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
              clientCompatibility: '客户端兼容',
              supportedModels: '支持模型',
              modelMappings: '模型映射',
              proxyProfileId: '代理',
              errorPolicyId: '错误策略',
              schedulable: '参与调度',
              accountExpiresAt: '过期时间',
              availabilitySchedule: '可用时段计划',
              boundGroupId: '绑定分组',
              cooldownUntil: '冷却结束时间',
              lastErrorCode: '异常类型',
              lastErrorMessage: '错误信息'
            }),
            ...(requestedClearFailureState === true ? [safeChange('clearFailureState', '恢复异常状态', false, true)] : [])
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    if (requestedClearFailureState === true || body.status === 'active') {
      await clearAccountGatewayRuntimeAfterRestore(account, requestAccess)
    }
    res.json(ok(sanitizeAccountResponse(await applyServerAccountRuntimeToAccount(account))))
  } catch (error) {
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
  if (!requestAccess) {
    res.status(403).json({ message: '缺少系统账户上下文' })
    return
  }
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
  const unavailableMessage = accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    res.status(400).json({ message: unavailableMessage })
    return
  }

  try {
    const diagnostics = isAdminRole(requestAccess?.role) || account.accessType !== 'authorized' ? 'full' : 'limited'
    const { prompt: _ignoredPrompt, ...testOptions } = parsed.data ?? {}
    const task = createAccountTestTask({
      account,
      access: requestAccess,
      diagnostics,
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility
    })
    if (!dispatchAccountTestTasks([task.id])) {
      failAccountTestTask(task.id, '后台 worker 暂不可用，账号测试任务未能投递')
      res.status(503).json({ message: '后台 worker 暂不可用，账号测试任务未能投递' })
      return
    }
    res.status(202).json(ok(task))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '创建账户测试任务失败'))
  }
})

accountsRouter.post('/:id/return-authorization', mutationGuard({
  operationKey: 'accounts.return_authorization',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    accountId: normalizedText(req.params.id),
    grantee: normalizedText(queryField(req, 'systemAccountId'))
  })
}), (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findAccountSummary(req.params.id, requestAccess)
  try {
    runLoggedOperation(() => {
      const authorization = returnAccountAuthorizationInstanceForGrantee(req.params.id, requestAccess)
      if (!authorization) {
        throw new Error('授权账户不存在或不可归还')
      }
      const resourceName = before?.name ?? authorization.resource_id
      return {
        result: true,
        log: {
          operationScopeSystemAccountId: authorization.grantee_system_account_id,
          mode: operationMode(requestAccess),
          module: 'authorizations',
          action: 'return',
          operationKey: 'accounts.return_authorization',
          resourceType: 'authorization',
          resourceId: authorization.id,
          resourceName,
          summary: `归还授权账户：${resourceName}`,
          changes: [safeChange('returned', '归还授权账户', false, true)],
          targets: [
            ownerTarget({
              targetType: authorization.resource_type,
              targetId: authorization.resource_id,
              ownerSystemAccountId: authorization.resource_owner_system_account_id,
              relation: 'owner'
            }),
            ownerTarget({
              targetType: 'system_account',
              targetId: authorization.grantee_system_account_id,
              ownerSystemAccountId: authorization.grantee_system_account_id,
              relation: 'grantee'
            })
          ],
          viewers: viewers(
            viewer(authorization.resource_owner_system_account_id, 'authorization_owner'),
            viewer(authorization.grantee_system_account_id, 'authorization_grantee')
          )
        }
      }
    }, req)
    res.status(204).send()
  } catch (error) {
    if (error instanceof Error && error.message === '授权账户不存在或不可归还') {
      res.status(404).json({ message: '授权账户不存在或不可归还' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '归还授权账户失败'))
  }
})

accountsRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const before = findAccountSummary(req.params.id, requestAccess)
  const ownerSystemAccountId = resolveOperationOwner(before as unknown as Record<string, unknown> | undefined, requestAccess)
  try {
    runLoggedOperation(() => {
      const deleteResult = deleteAccountWithRelatedCleanup(req.params.id, requestAccess)
      if (!deleteResult.deleted) {
        throw new Error('账户不存在')
      }
      return {
        result: true,
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
    if (error instanceof Error && error.message === '授权账户请使用归还操作') {
      res.status(400).json(badRequest('授权账户请使用归还操作'))
      return
    }
    throw error
  }
  res.status(204).send()
})

function draftAccountCredentials(account: AccountDraftTestAccountRequest, providerBaseUrl: string): Record<string, unknown> {
  const credentials = credentialsRecordValue(account.credentials) ?? {}
  if (account.type !== 'oauth' || hasCredentialText(credentials.base_url)) {
    return credentials
  }
  return {
    ...credentials,
    base_url: providerBaseUrl || 'https://api.openai.com/v1'
  }
}

function draftTestAccountSummary(input: {
  account: AccountDraftTestAccountRequest
  availabilitySchedule: ReturnType<typeof accountAvailabilityScheduleFromRequest>
  clientCompatibility: AccountSummary['clientCompatibility']
  credentials: Record<string, unknown>
  groupName?: string
  ownerSystemAccountId: string
}): AccountSummary {
  const usage = emptyAccountUsageSummary()
  return {
    id: newId('acctdraft'),
    systemAccountId: input.ownerSystemAccountId,
    ownerSystemAccountId: input.ownerSystemAccountId,
    providerCode: input.account.providerCode,
    name: input.account.name,
    notes: optionalText(input.account.notes),
    type: input.account.type,
    credentials: input.credentials,
    status: 'active',
    concurrencyLimit: input.account.concurrencyLimit ?? 20,
    currentConcurrency: 0,
    priority: input.account.priority ?? 0,
    superPriorityEnabled: input.account.superPriorityEnabled ?? false,
    fallbackEnabled: input.account.fallbackEnabled ?? false,
    clientCompatibility: input.clientCompatibility,
    supportedModels: normalizedTextList(input.account.supportedModels),
    modelMappings: normalizeDraftAccountModelMappings(input.account.modelMappings, input.account.providerCode, input.ownerSystemAccountId),
    proxyProfileId: optionalText(input.account.proxyProfileId),
    schedulable: true,
    availabilitySchedule: input.availabilitySchedule,
    availabilityScheduleActive: true,
    accountExpiresAt: optionalText(input.account.accountExpiresAt),
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: input.account.groupId,
    boundGroupName: input.groupName,
    groupBindStatus: 'bound',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: false,
      canViewCredentials: true,
      canManageAccounts: true,
      canBindToApiKey: true
    },
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '草稿测试',
      color: 'blue'
    }
  }
}

function emptyAccountUsageSummary(): AccountSummary['usage'] {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function normalizedTextList(value: string[] | undefined): string[] {
  return [...new Set((value ?? []).map((item) => item.trim()).filter(Boolean))]
}

function normalizeDraftAccountModelMappings(
  value: AccountDraftTestAccountRequest['modelMappings'],
  providerCode: string,
  ownerSystemAccountId: string
): AccountSummary['modelMappings'] {
  return normalizeAccountModelMappingsForProvider(value ?? [], providerCode, ownerSystemAccountId) ?? []
}

function optionalText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function accountCredentialFingerprint(credentials: unknown): string {
  if (typeof credentials !== 'object' || credentials === null || Array.isArray(credentials)) {
    return ''
  }
  const record = credentials as Record<string, unknown>
  return sensitiveFingerprint(
    record.api_key
      ?? record.refresh_token
      ?? record.access_token
      ?? record.email
      ?? record.account_id
      ?? ''
  )
}

function credentialsRecordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function mergeAccountCredentialsForUpdate(account: AccountSummary, requested: Record<string, unknown>): Record<string, unknown> {
  const credentials = { ...requested }
  preserveCredentialText(credentials, account.credentials, 'base_url')
  if (account.type === 'api_key') {
    preserveCredentialText(credentials, account.credentials, 'api_key')
  } else if (account.type === 'oauth') {
    for (const key of [
      'access_token',
      'refresh_token',
      'expires_at',
      'client_id',
      'id_token',
      'email',
      'account_id',
      'chatgpt_user_id',
      'plan_type'
    ]) {
      preserveCredentialText(credentials, account.credentials, key)
    }
  }
  return credentials
}

function preserveCredentialText(output: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  if (hasCredentialText(output[key])) return
  const value = source[key]
  if (hasCredentialText(value)) {
    output[key] = value
  }
}

function hasCredentialText(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}
