import { Router } from 'express'
import { z } from 'zod'

import { isAdminRole, type AccountStatus, type AccountSummary } from '../../domain/types.js'
import { isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText, queryTextList } from '../../shared/query-values.js'
import { accountAvailabilityScheduleFromRequest, accountAvailabilityScheduleJson } from '../../storage/account-availability-schedule.js'
import { newId } from '../../storage/database.js'
import { AccountTagInUseError, ProxyProfileUnavailableError, accountTestUnavailableMessage, clearAccountFailureState, createAccount, deleteAccountTag, deleteAccountWithRelatedCleanup, findAccountForTest, findAccountSummary, findGroupSummary, listAccountOptions, listAccountTags, listAccountsPage, listProviders, migrateAccountTraffic, normalizeAccountCredentialsForWrite, normalizeAccountModelMappingsForProvider, returnAccountAuthorizationInstanceForGrantee, setAccountGroup, updateAccount, updateAccountTags, updateAuthorizedAccountBindingDispatch, type AccountListOptions, type AccountOptionListOptions, type AccountListSchedulableFilter, type AccountListSortDirection, type AccountListSortField } from '../../storage/repositories.js'
import { getRequestAccessScope, type RequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { hashStableValue } from '../deduplication/deduplication.service.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { applyServerAccountConcurrencyToAccountList, applyServerAccountRuntimeToAccount } from '../gateway/gateway-runtime-snapshot.service.js'
import { migrateOpenAIAccountSessionAffinity } from '../gateway/openai-gateway-session-affinity.service.js'
import { diffSafeFields, operationMode, ownerTarget, recordOperationLog, resolveOperationOwner, runLoggedOperation, safeChange, viewer, viewers } from '../operation-logs/operation-log.service.js'
import { cancelAccountTestTask, createAccountTestTask, failAccountTestTask, getAccountTestTask, getAccountTestTaskRecord, listAccountTestTasks, type AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
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
  providerProtocolProfileId: z.string().trim().min(1).optional(),
  name: z.string().trim().min(1),
  type: z.string().trim().min(1),
  credentials: z.record(z.unknown()).optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  activationTestTaskId: z.string().trim().min(1).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  openAIResponsesUpstreamMode: z.enum(['passthrough', 'chat_completions_bridge']).optional(),
  proxyProfileId: z.string().optional(),
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
  tags: z.array(z.string().trim()).max(24).optional(),
  status: z.enum(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().min(0).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  openAIResponsesUpstreamMode: z.enum(['passthrough', 'chat_completions_bridge']).optional(),
  proxyProfileId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().trim().min(1, '账户分组不能为空').optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional(),
  clearFailureState: z.boolean().optional()
}).strict()

const accountDraftTestAccountSchema = z.object({
  providerCode: z.string().trim().min(1),
  providerProtocolProfileId: z.string().trim().min(1).optional(),
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
  openAIResponsesUpstreamMode: z.enum(['passthrough', 'chat_completions_bridge']).optional(),
  proxyProfileId: z.string().nullable().optional(),
  groupId: z.string().trim().min(1),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().optional()
}).strict()

const accountTestSchema = z.object({
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional(),
  account: accountDraftTestAccountSchema.optional()
}).strict().optional()

const accountDraftTestSchema = z.object({
  account: accountDraftTestAccountSchema,
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional(),
  clientCompatibility: z.enum(['openai_standard', 'codex_responses']).optional()
}).strict()

type AccountDraftTestAccountRequest = z.infer<typeof accountDraftTestAccountSchema>
type AccountCreateRequest = z.infer<typeof accountCreateSchema>

const accountGroupSchema = z.object({
  groupId: z.string().trim().min(1, '分组不能为空')
}).strict()

const accountTagsUpdateSchema = z.object({
  tags: z.array(z.string().trim()).max(24)
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

accountsRouter.get('/tags', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    res.json(ok(listAccountTags(getRequestAccessScope(scopeQuery.data.systemAccountId))))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '加载账户标签失败'))
  }
})

accountsRouter.delete('/tags/:tagId', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  try {
    if (!deleteAccountTag(req.params.tagId, getRequestAccessScope(scopeQuery.data.systemAccountId))) {
      res.status(404).json({ message: '标签不存在' })
      return
    }
    res.status(204).send()
  } catch (error) {
    if (error instanceof AccountTagInUseError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除账户标签失败'))
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
  try {
    const preparedDraft = prepareAccountDraftTestSnapshot({
      accountInput: parsed.data.account,
      requestAccess
    })
    const { prompt: _ignoredPrompt, ...testOptions } = parsed.data
    const task = createAccountTestTask({
      account: preparedDraft.account,
      access: requestAccess,
      diagnostics: 'full',
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility,
      draftAccount: preparedDraft.draftAccount
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
    credential: accountCredentialFingerprint(bodyField(req, 'credentials')),
    status: normalizedText(bodyField(req, 'status')),
    activationTestTaskId: normalizedText(bodyField(req, 'activationTestTaskId'))
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
  const groupId = typeof parsed.data.groupId === 'string' && parsed.data.groupId ? parsed.data.groupId : undefined
  let group: ReturnType<typeof findGroupSummary> | undefined
  if (groupId) {
    group = findGroupSummary(groupId, requestAccess)
    if (!group || group.providerCode !== providerCode) {
      res.status(400).json(badRequest('账户分组无效'))
      return
    }
  }
  const providerProtocolProfileId = parsed.data.providerProtocolProfileId ?? group?.providerProtocolProfileId ?? provider.defaultProtocolProfileId
  const providerProfile = provider.protocolProfiles.find((item) => item.id === providerProtocolProfileId)
  if (!providerProfile || !providerProfile.accountTypes.includes(parsed.data.type)) {
    res.status(400).json(badRequest(`供应商协议档案不支持账户类型：${parsed.data.type}`))
    return
  }
  if (group && group.providerProtocolProfileId !== providerProfile.id) {
    res.status(400).json(badRequest('账户分组协议档案无效'))
    return
  }

  let createStatus: AccountStatus
  try {
    createStatus = accountCreateStatusFromActivationTest({
      account: { ...parsed.data, providerProtocolProfileId: providerProfile.id },
      providerBaseUrl: providerProfile.baseUrl,
      providerProtocolProfileId: providerProfile.id,
      protocolCode: providerProfile.protocolCode,
      protocolVersion: providerProfile.protocolVersion,
      group,
      requestAccess
    })
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '账户创建测试状态无效'))
    return
  }

  try {
    const account = runLoggedOperation(() => {
      const { activationTestTaskId: _activationTestTaskId, ...accountCreateInput } = parsed.data
      const account = createAccount({
        ...accountCreateInput,
        providerCode,
        providerProtocolProfileId: providerProfile.id,
        status: createStatus
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
            safeChange('providerProtocolProfileId', '协议档案', undefined, account.providerProtocolProfileId),
            safeChange('type', '账户类型', undefined, account.type),
            safeChange('status', '状态', undefined, account.status),
            safeChange('clientCompatibility', '客户端兼容', undefined, account.clientCompatibility),
            safeChange('credentials', '凭据', undefined, parsed.data.credentials),
            safeChange('supportedModels', '支持模型', undefined, account.supportedModels),
            safeChange('modelMappings', '模型映射', undefined, account.modelMappings),
            safeChange('tags', '标签', undefined, account.tags),
            safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
            safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
            safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
            safeChange('availabilitySchedule', '时间计划', undefined, account.availabilitySchedule),
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

accountsRouter.patch('/:id/tags', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = accountTagsUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? '账户标签参数无效'))
    return
  }
  const before = findAccountSummary(req.params.id, requestAccess)
  if (!before) {
    res.status(404).json({ message: '账户不存在' })
    return
  }
  try {
    const account = runLoggedOperation(() => {
      const tags = updateAccountTags(req.params.id, parsed.data.tags, requestAccess)
      if (!tags) {
        throw new Error('账户不存在')
      }
      const account = findAccountSummary(req.params.id, requestAccess)
      if (!account) {
        throw new Error('账户不存在')
      }
      const ownerSystemAccountId = authorizedLocalOperationOwner(account, requestAccess)
        ?? resolveOperationOwner(account as unknown as Record<string, unknown>, requestAccess)
      return {
        result: { ...account, tags },
        log: {
          operationScopeSystemAccountId: ownerSystemAccountId,
          mode: operationMode(requestAccess),
          module: 'accounts',
          action: 'update_tags',
          operationKey: 'accounts.update_tags',
          resourceType: 'account',
          resourceId: account.id,
          resourceName: account.name,
          summary: `更新账户标签：${account.name}`,
          changes: [
            safeChange('tags', '标签', before?.tags, tags)
          ],
          viewers: viewer(ownerSystemAccountId, 'resource_owner')
        }
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof Error && error.message === '账户不存在') {
      res.status(404).json({ message: '账户不存在' })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '更新账户标签失败'))
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
  if (requestedClearFailureState === true && existingAccount.status === 'pending_test') {
    res.status(400).json(badRequest('待测试账户需手动测试通过后才能参与调度'))
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
              tags: '标签',
              proxyProfileId: '代理',
              schedulable: '参与调度',
              accountExpiresAt: '过期时间',
              availabilitySchedule: '时间计划',
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
  if (!isOpenAIProtocolProfile(account)) {
    res.status(400).json({ message: '当前仅支持测试 OpenAI 协议账户' })
    return
  }
  const unavailableMessage = accountTestUnavailableMessage(account)
  if (unavailableMessage) {
    res.status(400).json({ message: unavailableMessage })
    return
  }

  try {
    const diagnostics = isAdminRole(requestAccess?.role) || account.accessType !== 'authorized' ? 'full' : 'limited'
    const testRequest = parsed.data ?? {}
    const { prompt: _ignoredPrompt, account: accountSnapshot, ...testOptions } = testRequest
    const draftAccount = accountSnapshot
      ? savedAccountDraftTestSnapshot(account, accountSnapshot, requestAccess)
      : undefined
    const task = createAccountTestTask({
      account,
      access: requestAccess,
      diagnostics,
      model: testOptions.model,
      clientCompatibility: testOptions.clientCompatibility ?? draftAccount?.clientCompatibility,
      draftAccount
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

function savedAccountDraftTestSnapshot(
  account: AccountSummary,
  accountInput: AccountDraftTestAccountRequest,
  requestAccess: RequestAccessScope
): AccountTestDraftSnapshot {
  if (account.accessType === 'authorized') {
    throw new Error('授权账户测试不支持使用未保存表单配置')
  }
  if (accountInput.providerCode !== account.providerCode || accountInput.type !== account.type) {
    throw new Error('账户测试草稿与当前账户不一致')
  }
  const preparedDraft = prepareAccountDraftTestSnapshot({
    accountInput,
    requestAccess,
    draftAccountId: account.id
  })
  if (
    account.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId !== account.providerProtocolProfileId
  ) {
    throw new Error('账户测试草稿与当前账户协议档案不一致')
  }
  return {
    ...preparedDraft.draftAccount,
    stateTargetAccountId: account.id
  }
}

function prepareAccountDraftTestSnapshot(input: {
  accountInput: AccountDraftTestAccountRequest
  requestAccess: RequestAccessScope
  draftAccountId?: string
}): { account: AccountSummary; draftAccount: AccountTestDraftSnapshot } {
  const accountInput = input.accountInput
  const group = findGroupSummary(accountInput.groupId, input.requestAccess)
  if (!group || group.providerCode !== accountInput.providerCode || group.permissions?.canManageAccounts === false) {
    throw new Error('账户分组无效')
  }
  const provider = listProviders().find((item) => item.code === accountInput.providerCode)
  const providerProfile = provider?.protocolProfiles.find((item) => item.id === (accountInput.providerProtocolProfileId ?? group.providerProtocolProfileId))
    ?? provider?.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
  if (!provider || !providerProfile || !providerProfile.accountTypes.includes(accountInput.type as AccountSummary['type'])) {
    throw new Error(`供应商 ${accountInput.providerCode} 不支持账户类型 ${accountInput.type}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${accountInput.providerCode}`)
  }
  if (group.providerProtocolProfileId !== providerProfile.id || !isOpenAIProtocolProfile(providerProfile)) {
    throw new Error('当前仅支持测试 OpenAI 协议账户')
  }
  const ownerSystemAccountId = group.ownerSystemAccountId
    ?? group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  if (!ownerSystemAccountId) {
    throw new Error('账户分组缺少归属用户，无法测试')
  }
  const credentials = normalizeAccountCredentialsForWrite(accountInput.type, draftAccountCredentials(accountInput, providerProfile.baseUrl))
  const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: accountInput.availabilitySchedule })
  const availabilityScheduleJson = accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    accountInput.providerCode,
    accountInput.type,
    accountInput.clientCompatibility,
    'openai_standard',
    providerProfile
  )
  const openAIResponsesUpstreamMode = normalizeDraftOpenAIResponsesUpstreamMode(accountInput.openAIResponsesUpstreamMode, accountInput.type)
  const account = draftTestAccountSummary({
    id: input.draftAccountId,
    account: accountInput,
    availabilitySchedule,
    clientCompatibility,
    openAIResponsesUpstreamMode,
    credentials,
    groupName: group.name,
    ownerSystemAccountId,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  return {
    account,
    draftAccount: {
      id: account.id,
      ownerSystemAccountId,
      groupId: accountInput.groupId,
      groupName: group.name,
      providerCode: accountInput.providerCode,
      providerProtocolProfileId: providerProfile.id,
      protocolCode: providerProfile.protocolCode,
      protocolVersion: providerProfile.protocolVersion,
      name: account.name,
      type: accountInput.type,
      credentials,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      superPriorityEnabled: account.superPriorityEnabled,
      fallbackEnabled: account.fallbackEnabled,
      clientCompatibility,
      openAIResponsesUpstreamMode,
      supportedModels: account.supportedModels,
      modelMappings: account.modelMappings,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: account.accountExpiresAt,
      availabilitySchedule,
      availabilityScheduleJson,
      notes: account.notes
    }
  }
}

function accountCreateStatusFromActivationTest(input: {
  account: AccountCreateRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): AccountStatus {
  const requestedStatus = input.account.status
  const activationTestTaskId = optionalText(input.account.activationTestTaskId)
  if (!activationTestTaskId) {
    if (requestedStatus === 'active') {
      throw new Error('创建为正常状态需要先完成本次账户草稿测试')
    }
    return requestedStatus ?? 'pending_test'
  }
  if (requestedStatus && requestedStatus !== 'active') {
    throw new Error('带测试任务创建账户时，状态只能为正常或留空')
  }
  assertActivationTestTaskMatchesCreate({
    ...input,
    activationTestTaskId
  })
  return 'active'
}

function assertActivationTestTaskMatchesCreate(input: {
  account: AccountCreateRequest
  activationTestTaskId: string
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): void {
  if (!input.requestAccess) {
    throw new Error('缺少系统账户上下文，无法确认账户草稿测试结果')
  }
  if (!input.group) {
    throw new Error('账户分组无效，无法确认账户草稿测试结果')
  }
  const task = getAccountTestTaskRecord(input.activationTestTaskId)
  if (!task || !sameAccountTestRequester(task, input.requestAccess)) {
    throw new Error('账户草稿测试任务不存在或不属于当前创建上下文')
  }
  if (task.status !== 'success' || task.result?.success !== true || !task.draftAccount) {
    throw new Error('账户草稿测试尚未成功，不能直接创建为正常状态')
  }
  const ownerSystemAccountId = input.group.ownerSystemAccountId
    ?? input.group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  const expected = accountCreateActivationFingerprintSnapshot({
    account: input.account,
    providerBaseUrl: input.providerBaseUrl,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    ownerSystemAccountId
  })
  const actual = draftActivationFingerprintSnapshot(task.draftAccount)
  if (hashStableValue(expected) !== hashStableValue(actual)) {
    throw new Error('账户草稿测试内容已变化，请重新测试后再创建为正常状态')
  }
}

function sameAccountTestRequester(
  task: NonNullable<ReturnType<typeof getAccountTestTaskRecord>>,
  access: RequestAccessScope
): boolean {
  return task.requestSystemAccountId === access.systemAccountId
    && task.requestRole === access.role
    && (task.requestSystemAccountFilterId ?? undefined) === access.systemAccountFilterId
}

function accountCreateActivationFingerprintSnapshot(input: {
  account: AccountCreateRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  ownerSystemAccountId: string
}): Record<string, unknown> {
  const account = accountDraftRequestFromCreate(input.account)
  const credentials = normalizeAccountCredentialsForWrite(account.type, draftAccountCredentials(account, input.providerBaseUrl))
  const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: account.availabilitySchedule })
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    account.clientCompatibility,
    'openai_standard',
    { protocolCode: input.protocolCode, protocolVersion: input.protocolVersion }
  )
  const openAIResponsesUpstreamMode = normalizeDraftOpenAIResponsesUpstreamMode(account.openAIResponsesUpstreamMode, account.type)
  return {
    ownerSystemAccountId: input.ownerSystemAccountId,
    groupId: account.groupId,
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    name: account.name,
    type: account.type,
    credentials,
    concurrencyLimit: account.concurrencyLimit ?? 20,
    priority: account.priority ?? 0,
    superPriorityEnabled: account.superPriorityEnabled ?? false,
    fallbackEnabled: account.fallbackEnabled ?? false,
    clientCompatibility,
    openAIResponsesUpstreamMode,
    supportedModels: normalizedTextList(account.supportedModels),
    modelMappings: normalizeDraftAccountModelMappings(account.modelMappings, account.providerCode, input.ownerSystemAccountId),
    proxyProfileId: optionalText(account.proxyProfileId),
    accountExpiresAt: optionalText(account.accountExpiresAt),
    availabilityScheduleJson: accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined,
    notes: optionalText(account.notes)
  }
}

function draftActivationFingerprintSnapshot(draft: AccountTestDraftSnapshot): Record<string, unknown> {
  return {
    ownerSystemAccountId: draft.ownerSystemAccountId,
    groupId: draft.groupId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    protocolCode: draft.protocolCode,
    protocolVersion: draft.protocolVersion,
    name: draft.name,
    type: draft.type,
    credentials: draft.credentials,
    concurrencyLimit: draft.concurrencyLimit,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    openAIResponsesUpstreamMode: draft.openAIResponsesUpstreamMode,
    supportedModels: normalizedTextList(draft.supportedModels),
    modelMappings: draft.modelMappings ?? [],
    proxyProfileId: optionalText(draft.proxyProfileId),
    accountExpiresAt: optionalText(draft.accountExpiresAt),
    availabilityScheduleJson: optionalText(draft.availabilityScheduleJson),
    notes: optionalText(draft.notes)
  }
}

function accountDraftRequestFromCreate(account: AccountCreateRequest): AccountDraftTestAccountRequest {
  return {
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    openAIResponsesUpstreamMode: account.openAIResponsesUpstreamMode,
    proxyProfileId: account.proxyProfileId,
    groupId: typeof account.groupId === 'string' ? account.groupId : '',
    accountExpiresAt: account.accountExpiresAt,
    availabilitySchedule: account.availabilitySchedule,
    notes: account.notes
  }
}

function draftTestAccountSummary(input: {
  id?: string
  account: AccountDraftTestAccountRequest
  availabilitySchedule: ReturnType<typeof accountAvailabilityScheduleFromRequest>
  clientCompatibility: AccountSummary['clientCompatibility']
  openAIResponsesUpstreamMode: AccountSummary['openAIResponsesUpstreamMode']
  credentials: Record<string, unknown>
  groupName?: string
  ownerSystemAccountId: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
}): AccountSummary {
  const usage = emptyAccountUsageSummary()
  return {
    id: input.id ?? newId('acctdraft'),
    systemAccountId: input.ownerSystemAccountId,
    ownerSystemAccountId: input.ownerSystemAccountId,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
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
    openAIResponsesUpstreamMode: input.openAIResponsesUpstreamMode,
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

function normalizeDraftOpenAIResponsesUpstreamMode(
  value: unknown,
  accountType: AccountSummary['type']
): AccountSummary['openAIResponsesUpstreamMode'] {
  if (accountType === 'oauth') {
    return 'passthrough'
  }
  if (value === undefined || value === null || value === '' || value === 'passthrough') {
    return 'passthrough'
  }
  if (value === 'chat_completions_bridge') {
    return 'chat_completions_bridge'
  }
  throw new Error('Responses 上游模式无效')
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
