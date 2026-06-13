import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger, errorLogFields } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  accountTestUnavailableMessage,
  clearAuthorizedAccountBindingFailureState,
  clearAccountFailureStateResult,
  findAccountForTest,
  findRecentOpenAIRequestShapeForAccount,
  markAccountTestTemporaryUnavailable,
  recordAccountSuccessfulTestModel,
  resolveProxyUrlForProfile,
  runtimeOpenAIAccountCredentials,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { getSettings } from '../../storage/settings.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  accountTestTaskCancelMessage,
  type AccountTestDraftSnapshot,
  cancelExpiredAccountTestSessions,
  cleanupExpiredAccountTestTasks,
  completeAccountTestTask,
  failAccountTestTask,
  getAccountTestTaskRecord,
  isAccountTestTaskCancelRequested,
  listRunnableAccountTestTaskIds,
  markAccountTestTaskCanceled,
  markAccountTestTaskRunning,
  requeueInterruptedAccountTestTasks,
  updateAccountTestTaskMessage
} from '../../storage/account-test-tasks.repository.js'
import { sendAccountRuntimeClearToServer, sendAccountTestCancelToWorker, sendAccountTestTasksToWorker } from '../background/background-ipc.js'
import { operationMode, recordOperationLog, resolveOperationOwner, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { buildOpenAIOAuthCredentials, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import { isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { testOpenAIAccount, testOpenAIAccountWithDiagnosticRetries } from './account-test.service.js'
import {
  type AccountDiagnosticAttemptProgress,
  accountDiagnosticAttemptProgress,
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from './account-diagnostic-retry-policy.js'

interface AccountTestQueueItem {
  taskId: string
}

const defaultManualAccountTestConcurrency = 100
const manualAccountTestRefillBatchSize = Number.MAX_SAFE_INTEGER
const manualAccountTestRetryPolicy = sequenceRetryPolicy('manual_account_test', [], 0)
const runningAccountTestControllers = new Map<string, AbortController>()
let accountTestSessionStaleSweepTimer: NodeJS.Timeout | undefined

const manualAccountTestQueue = createRetryQueue<AccountTestQueueItem>({
  name: 'manual-account-test',
  policy: manualAccountTestRetryPolicy,
  concurrency: defaultManualAccountTestConcurrency,
  run: runAccountTestQueueItem,
  onSuccess: () => {
    refillManualAccountTestQueue()
  },
  onExhausted: (event) => {
    failAccountTestTask(event.item.taskId, event.error instanceof Error ? event.error.message : '账号测试任务执行失败')
    refillManualAccountTestQueue()
  }
})

export function dispatchAccountTestTasks(taskIds: string[]): boolean {
  const normalizedIds = taskIds.map(normalizedString).filter((taskId): taskId is string => Boolean(taskId))
  if (normalizedIds.length === 0) {
    return true
  }
  if (runtimeConfig.processRole === 'worker') {
    for (const taskId of normalizedIds) {
      enqueueAccountTestTaskLocal(taskId)
    }
    return true
  }
  if (runtimeConfig.processRole === 'db-service') {
    return sendAccountTestTasksFromDbService(normalizedIds)
  }
  return sendAccountTestTasksToWorker(normalizedIds)
}

export function dispatchAccountTestCancel(taskId: string): boolean {
  const normalizedId = normalizedString(taskId)
  if (!normalizedId) {
    return false
  }
  if (runtimeConfig.processRole === 'worker') {
    cancelAccountTestTaskLocal(normalizedId)
    return true
  }
  if (runtimeConfig.processRole === 'db-service') {
    return sendAccountTestCancelFromDbService(normalizedId)
  }
  return sendAccountTestCancelToWorker(normalizedId)
}

export function enqueueAccountTestTaskLocal(taskId: string): boolean {
  const normalizedId = normalizedString(taskId)
  return normalizedId ? manualAccountTestQueue.enqueue(normalizedId, { taskId: normalizedId }) : false
}

export function cancelAccountTestTaskLocal(taskId: string): void {
  const normalizedId = normalizedString(taskId)
  if (!normalizedId) return
  manualAccountTestQueue.delete(normalizedId)
  const controller = runningAccountTestControllers.get(normalizedId)
  if (controller) {
    controller.abort()
    return
  }
  markAccountTestTaskCanceled(normalizedId, '已停止测试')
}

export function startAccountTestTaskQueue(): void {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  cleanupExpiredAccountTestTasks()
  requeueInterruptedAccountTestTasks()
  startAccountTestSessionStaleSweep()
  refillManualAccountTestQueue()
}

function refillManualAccountTestQueue(): void {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  abortExpiredAccountTestSessions()
  manualAccountTestQueue.setConcurrency(accountTestTaskConcurrency())
  const taskIds = listRunnableAccountTestTaskIds(manualAccountTestRefillBatchSize)
  for (const taskId of taskIds) {
    enqueueAccountTestTaskLocal(taskId)
  }
}

function accountTestTaskConcurrency(): number {
  const value = getSettings().accountTestTaskConcurrency
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return defaultManualAccountTestConcurrency
  }
  return Math.min(1000, Math.max(1, Math.trunc(value)))
}

function startAccountTestSessionStaleSweep(): void {
  if (accountTestSessionStaleSweepTimer) {
    return
  }
  accountTestSessionStaleSweepTimer = setInterval(() => {
    abortExpiredAccountTestSessions()
  }, 2_000)
  accountTestSessionStaleSweepTimer.unref()
}

function abortExpiredAccountTestSessions(): void {
  const taskIds = cancelExpiredAccountTestSessions()
  for (const taskId of taskIds) {
    manualAccountTestQueue.delete(taskId)
    runningAccountTestControllers.get(taskId)?.abort()
  }
}

export function getManualAccountTestQueueSnapshot() {
  return manualAccountTestQueue.snapshot()
}

async function runAccountTestQueueItem(item: AccountTestQueueItem): Promise<boolean> {
  const task = markAccountTestTaskRunning(item.taskId)
  if (!task) {
    return true
  }

  const access: AccessScope = {
    systemAccountId: task.requestSystemAccountId,
    role: task.requestRole,
    systemAccountFilterId: task.requestSystemAccountFilterId
  }
  const controller = new AbortController()
  runningAccountTestControllers.set(task.id, controller)
  const onDiagnosticAttemptProgress = accountTestTaskProgressReporter(task.id)

  try {
    if (isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, accountTestTaskCancelMessage(task.id))
      return true
    }

    if (task.draftAccount) {
      const draft = task.draftAccount
      const draftAccount = accountSummaryFromDraftSnapshot(draft)
      if (!isOpenAIProtocolProfile(draftAccount)) {
        failAccountTestTask(task.id, '当前仅支持测试 OpenAI 协议账户', failedAccountTestResult(draftAccount, task.message ?? '当前仅支持测试 OpenAI 协议账户', task.model))
        return true
      }

      const stateTargetAccountId = normalizedString(draft.stateTargetAccountId)
      if (stateTargetAccountId) {
        const account = findAccountForTest(stateTargetAccountId, access)
        if (!account) {
          failAccountTestTask(task.id, '账户不存在')
          return true
        }
        if (account.accessType === 'authorized') {
          failAccountTestTask(task.id, '授权账户测试不支持使用未保存表单配置', failedAccountTestResult(account, task.message ?? '授权账户测试不支持使用未保存表单配置', task.model))
          return true
        }
        if (!isOpenAIProtocolProfile(account)) {
          failAccountTestTask(task.id, '当前仅支持测试 OpenAI 协议账户', failedAccountTestResult(account, task.message ?? '当前仅支持测试 OpenAI 协议账户', task.model))
          return true
        }
        const unavailableMessage = accountTestUnavailableMessage(account)
        if (unavailableMessage) {
          failAccountTestTask(task.id, unavailableMessage, failedAccountTestResult(account, unavailableMessage, task.model))
          return true
        }
        const result = await runOpenAIAccountTestWithSideEffects(account, access, {
          model: task.model,
          clientCompatibility: task.clientCompatibility ?? draft.clientCompatibility,
          diagnostics: task.diagnostics,
          signal: controller.signal,
          draftAccount: draft,
          onDiagnosticAttemptProgress
        })
        if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
          markAccountTestTaskCanceled(task.id, accountTestTaskCancelMessage(task.id))
          return true
        }
        completeAccountTestTask(task.id, result)
        return true
      }

      const result = await runOpenAIDraftAccountTest(draftAccount, draft, {
        model: task.model,
        clientCompatibility: task.clientCompatibility ?? draft.clientCompatibility,
        diagnostics: task.diagnostics,
        signal: controller.signal,
        onDiagnosticAttemptProgress
      })
      if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
        markAccountTestTaskCanceled(task.id, accountTestTaskCancelMessage(task.id))
        return true
      }
      completeAccountTestTask(task.id, result)
      return true
    }

    const account = findAccountForTest(task.accountId, access)
    if (!account) {
      failAccountTestTask(task.id, '账户不存在')
      return true
    }
    if (!isOpenAIProtocolProfile(account)) {
      failAccountTestTask(task.id, '当前仅支持测试 OpenAI 协议账户', failedAccountTestResult(account, task.message ?? '当前仅支持测试 OpenAI 协议账户', task.model))
      return true
    }
    const unavailableMessage = accountTestUnavailableMessage(account)
    if (unavailableMessage) {
      failAccountTestTask(task.id, unavailableMessage, failedAccountTestResult(account, unavailableMessage, task.model))
      return true
    }

    const result = await runOpenAIAccountTestWithSideEffects(account, access, {
      model: task.model,
      clientCompatibility: task.clientCompatibility,
      diagnostics: task.diagnostics,
      signal: controller.signal,
      onDiagnosticAttemptProgress
    })

    if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, accountTestTaskCancelMessage(task.id))
      return true
    }

    completeAccountTestTask(task.id, result)
    return true
  } catch (error) {
    if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, accountTestTaskCancelMessage(task.id))
      return true
    }
    logger.warn(errorLogFields(error, {
      event: 'manual_account_test_task_failed',
      taskId: task.id,
      accountId: task.accountId
    }), '账号测试后台任务执行失败')
    failAccountTestTask(task.id, error instanceof Error ? error.message : '账号测试任务执行失败')
    return true
  } finally {
    runningAccountTestControllers.delete(task.id)
  }
}

function accountTestTaskProgressReporter(taskId: string): (progress: AccountDiagnosticAttemptProgress) => void {
  return (progress) => {
    updateAccountTestTaskMessage(taskId, accountDiagnosticAttemptMessage(progress))
  }
}

function accountDiagnosticAttemptMessage(progress: AccountDiagnosticAttemptProgress): string {
  return `真实请求测试中：第 ${progress.attemptNumber}/${progress.totalAttempts} 次，本次最多等待 ${formatDiagnosticTimeout(progress.timeoutMs)}，总上限 ${formatDiagnosticTimeout(progress.maxTotalTimeoutMs)}`
}

function formatDiagnosticTimeout(timeoutMs: number): string {
  return `${Math.max(1, Math.ceil(timeoutMs / 1000))}s`
}

async function runOpenAIDraftAccountTest(
  account: AccountSummary,
  draft: AccountTestDraftSnapshot,
  input: {
    model?: string
    clientCompatibility?: AccountSummary['clientCompatibility']
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
  }
): Promise<AccountTestResult> {
  return testOpenAIDraftAccountWithDiagnosticRetries(account, draft, input)
}

async function testOpenAIDraftAccountWithDiagnosticRetries(
  account: AccountSummary,
  draft: AccountTestDraftSnapshot,
  input: {
    model?: string
    clientCompatibility?: AccountSummary['clientCompatibility']
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
  }
): Promise<AccountTestResult> {
  const startedAt = Date.now()
  let candidateAccount: OpenAIAccountSecret | undefined
  let lastResult: AccountTestResult | undefined
  for (let attemptIndex = 0; attemptIndex < accountDiagnosticRetryTimeoutMs.length; attemptIndex += 1) {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attemptIndex] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    input.onDiagnosticAttemptProgress?.(accountDiagnosticAttemptProgress(attemptIndex, timeoutMs, startedAt))
    const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
    const attemptStartedAt = Date.now()
    let result: AccountTestResult
    try {
      candidateAccount = candidateAccount ?? await openAIDraftAccountSecret(draft, attemptSignal)
      result = await testOpenAIAccount(account, {
        model: input.model,
        groupId: draft.groupId,
        systemAccountId: draft.ownerSystemAccountId,
        clientCompatibility: input.clientCompatibility,
        diagnostics: input.diagnostics,
        signal: attemptSignal,
        candidateAccount,
        gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs)
      })
    } catch (error) {
      result = failedAccountTestResult(account, draftAccountTestErrorMessage(error, attemptSignal), input.model, {
        accountFailureEligible: draftAccountTestFailureEligible(error, attemptSignal),
        durationMs: Date.now() - attemptStartedAt
      })
    }
    lastResult = result
    if (result.success || result.accountFailureEligible === false || input.signal.aborted) {
      return accountTestResultWithTotalDuration(result, startedAt)
    }
    if (attemptIndex + 1 < accountDiagnosticRetryTimeoutMs.length) {
      logger.info({
        event: 'account_draft_diagnostic_test_retry_scheduled',
        accountId: account.id,
        accountName: account.name,
        attemptNumber: attemptIndex + 1,
        nextAttemptNumber: attemptIndex + 2,
        attemptTimeoutMs: timeoutMs,
        nextAttemptTimeoutMs: accountDiagnosticRetryTimeoutMs[attemptIndex + 1],
        durationMs: result.durationMs,
        totalElapsedMs: Date.now() - startedAt,
        traceId: result.traceId
      }, '账户草稿诊断请求未通过，将继续使用真实网关链路重试')
    }
  }
  return accountTestResultWithTotalDuration(lastResult ?? failedAccountTestResult(account, '账户测试失败', input.model, {
    accountFailureEligible: true
  }), startedAt)
}

async function runOpenAIAccountTestWithSideEffects(
  account: AccountSummary,
  access: AccessScope,
  input: {
    model?: string
    clientCompatibility?: AccountSummary['clientCompatibility']
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    draftAccount?: AccountTestDraftSnapshot
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
  }
): Promise<AccountTestResult> {
  let accountTestStatusChanges: ReturnType<typeof safeChange>[] | undefined
  let result = input.draftAccount
    ? await runOpenAIDraftAccountTest(account, input.draftAccount, {
      model: input.model,
      clientCompatibility: input.clientCompatibility ?? input.draftAccount.clientCompatibility,
      diagnostics: input.diagnostics,
      signal: input.signal,
      onDiagnosticAttemptProgress: input.onDiagnosticAttemptProgress
    })
    : await testOpenAIAccountWithDiagnosticRetries(account, {
      model: input.model,
      clientCompatibility: input.clientCompatibility,
      signal: input.signal,
      diagnostics: input.diagnostics,
      requestShape: findRecentOpenAIRequestShapeForAccount(account.id, account.boundGroupId),
      onDiagnosticAttemptProgress: input.onDiagnosticAttemptProgress
    })

  if (input.signal.aborted) {
    return result
  }

  if (result.success && shouldClearAccountAfterSuccessfulTest(account)) {
    const restored = clearAccountAfterSuccessfulTest(account, access)
    if (restored.changed && restored.account) {
      accountTestStatusChanges = accountTestStatusLogChanges(account, restored.account)
      result = {
        ...result,
        accountStatusChanged: restored.changed,
        accountStatus: restored.account.status
      }
    }
  }

  if (result.success) {
    recordAccountSuccessfulTestModel(account.id, result.model ?? '', access)
    clearAccountGatewayRuntimeAfterRestore(account, access)
  }

  if (shouldMarkAccountTestFailureAsTemporaryUnavailable(account, result)) {
    const updatedAccount = markAccountTestTemporaryUnavailable(account, accountTestFailureCooldownReason(result), access)
    if (updatedAccount) {
      accountTestStatusChanges = accountTestStatusLogChanges(account, updatedAccount)
      if (accountTestStatusChanges.length > 0 || updatedAccount.status !== result.accountStatus) {
        result = {
          ...result,
          accountStatusChanged: accountTestStatusChanges.length > 0,
          accountStatus: updatedAccount.status
        }
      }
    }
  }

  if (result.accountStatusChanged) {
    const ownerSystemAccountId = authorizedLocalOperationOwner(account, access)
      ?? resolveOperationOwner(account as unknown as Record<string, unknown>, access)
    recordOperationLog({
      actorSystemAccountId: access.systemAccountId,
      actorRole: access.role,
      operationScopeSystemAccountId: ownerSystemAccountId,
      mode: operationMode(access),
      module: 'accounts',
      action: 'test_status_changed',
      operationKey: 'accounts.test_status_changed',
      resourceType: 'account',
      resourceId: account.id,
      resourceName: account.name,
      summary: `账户测试更新状态：${account.name}`,
      changes: accountTestStatusChanges ?? [safeChange('status', '状态', account.status, result.accountStatus)],
      viewers: viewer(ownerSystemAccountId, 'resource_owner')
    })
  }

  return result
}

async function openAIDraftAccountSecret(draft: AccountTestDraftSnapshot, signal: AbortSignal): Promise<OpenAIAccountSecret> {
  const proxy = draftProxyProfile(draft.proxyProfileId)
  let credentials = { ...draft.credentials }
  if (draft.type === 'oauth' && shouldRefreshOpenAIOAuthCredentials(credentials)) {
    const refreshToken = stringCredential(credentials.refresh_token)
    if (!refreshToken) {
      throw new DraftAccountConfigurationError('OAuth 草稿缺少 Refresh Token，无法刷新 Access Token')
    }
    const refreshedCredentials = buildOpenAIOAuthCredentials(await refreshOpenAIOAuthToken({
      refreshToken,
      clientId: stringCredential(credentials.client_id),
      proxyUrl: proxy.proxyUrl,
      signal
    }), { refreshToken })
    credentials = {
      ...credentials,
      ...refreshedCredentials
    }
  }
  const apiKey = draft.type === 'oauth'
    ? stringCredential(credentials.access_token)
    : stringCredential(credentials.api_key)
  if (!apiKey) {
    throw new DraftAccountConfigurationError(draft.type === 'oauth' ? 'OAuth 草稿缺少 Access Token' : '账户草稿缺少 API Key')
  }
  const baseUrl = stringCredential(credentials.base_url) || 'https://api.openai.com/v1'
  return {
    id: draft.id,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId ?? '',
    protocolCode: draft.protocolCode ?? '',
    protocolVersion: draft.protocolVersion ?? '',
    systemAccountId: draft.ownerSystemAccountId,
    accountOwnerSystemAccountId: draft.ownerSystemAccountId,
    groupOwnerSystemAccountId: draft.ownerSystemAccountId,
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    boundGroupId: draft.groupId,
    name: draft.name,
    type: draft.type,
    status: 'active',
    concurrencyLimit: draft.concurrencyLimit,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    supportedModels: draft.supportedModels ?? [],
    modelMappings: draft.modelMappings ?? [],
    baseUrl,
    apiKey,
    refreshToken: stringCredential(credentials.refresh_token) || undefined,
    clientId: stringCredential(credentials.client_id) || undefined,
    proxyProfileId: draft.proxyProfileId,
    proxyUrl: proxy.proxyUrl,
    proxyProfileUnavailable: proxy.unavailable,
    proxyProfileErrorMessage: proxy.errorMessage,
    streamFailureCount: 0,
    availabilityScheduleJson: draft.availabilityScheduleJson,
    accountExpiresAt: draft.accountExpiresAt,
    expiresAt: stringCredential(credentials.expires_at) || undefined,
    credentials: runtimeOpenAIAccountCredentials(credentials)
  }
}

function accountSummaryFromDraftSnapshot(draft: AccountTestDraftSnapshot): AccountSummary {
  const usage = emptyAccountUsageSummary()
  return {
    id: draft.id,
    systemAccountId: draft.ownerSystemAccountId,
    ownerSystemAccountId: draft.ownerSystemAccountId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    protocolCode: draft.protocolCode,
    protocolVersion: draft.protocolVersion,
    name: draft.name,
    notes: draft.notes,
    type: draft.type,
    credentials: draft.credentials,
    status: 'active',
    concurrencyLimit: draft.concurrencyLimit,
    currentConcurrency: 0,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    supportedModels: draft.supportedModels ?? [],
    modelMappings: draft.modelMappings ?? [],
    proxyProfileId: draft.proxyProfileId,
    schedulable: true,
    availabilitySchedule: draft.availabilitySchedule,
    availabilityScheduleActive: true,
    accountExpiresAt: draft.accountExpiresAt,
    todayUsage: usage,
    usage,
    boundGroupId: draft.groupId,
    boundGroupName: draft.groupName,
    groupBindStatus: 'bound',
    accessType: 'owner',
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
      label: '可用',
      color: 'green'
    }
  }
}

function draftProxyProfile(proxyProfileId: string | undefined): { proxyUrl?: string; unavailable?: boolean; errorMessage?: string } {
  try {
    return { proxyUrl: resolveProxyUrlForProfile(proxyProfileId) }
  } catch (error) {
    return {
      unavailable: true,
      errorMessage: error instanceof Error ? error.message : '代理配置不可用'
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

function clearAccountGatewayRuntimeAfterRestore(account: AccountSummary, access?: AccessScope): void {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
    : undefined
  sendAccountRuntimeClearToServer({
    accountId: account.id,
    authorizedBinding: account.accessType === 'authorized' && systemAccountId && account.boundGroupId && account.accountAuthorizationId
      ? {
          systemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  })
}

function shouldClearAccountAfterSuccessfulTest(account: AccountSummary): boolean {
  if (account.status === 'disabled') return false
  return Boolean(
    account.status !== 'active'
    || !account.schedulable
    || account.cooldownUntil
    || account.lastErrorMessage
    || account.lastErrorCode
    || account.cooldownRetestFailureCount
    || account.cooldownRetestObservationStartedAt
    || account.cooldownRetestLastAt
    || account.cooldownRetestLastStatusCode
    || account.streamFailureCount
    || account.streamFailureWindowStartedAt
  )
}

function clearAccountAfterSuccessfulTest(account: AccountSummary, access: AccessScope) {
  if (account.accessType === 'authorized') {
    return clearAuthorizedAccountBindingFailureState(account.id, access, { allowPendingTestRestore: true })
  }
  return clearAccountFailureStateResult(account.id, access, { allowPendingTestRestore: true })
}

function accountTestStatusLogChanges(before: AccountSummary, after: AccountSummary): ReturnType<typeof safeChange>[] {
  const changes: ReturnType<typeof safeChange>[] = []
  if (before.status !== after.status) {
    changes.push(safeChange('status', '状态', before.status, after.status))
  }
  if (before.schedulable !== after.schedulable) {
    changes.push(safeChange('schedulable', '是否参与调度', before.schedulable, after.schedulable))
  }
  if ((before.cooldownUntil ?? null) !== (after.cooldownUntil ?? null)) {
    changes.push(safeChange('cooldownUntil', before.accessType === 'authorized' || after.accessType === 'authorized' ? '实例冷却结束时间' : '冷却结束时间', before.cooldownUntil, after.cooldownUntil))
  }
  if ((before.lastErrorCode ?? null) !== (after.lastErrorCode ?? null)) {
    changes.push(safeChange('lastErrorCode', before.accessType === 'authorized' || after.accessType === 'authorized' ? '实例错误码' : '错误码', before.lastErrorCode, after.lastErrorCode))
  }
  if ((before.lastErrorMessage ?? null) !== (after.lastErrorMessage ?? null)) {
    changes.push(safeChange('lastErrorMessage', before.accessType === 'authorized' || after.accessType === 'authorized' ? '实例错误信息' : '错误信息', before.lastErrorMessage, after.lastErrorMessage))
  }
  if ((before.cooldownRetestFailureCount ?? 0) !== (after.cooldownRetestFailureCount ?? 0)) {
    changes.push(safeChange('cooldownRetestFailureCount', '后台复测失败次数', before.cooldownRetestFailureCount ?? 0, after.cooldownRetestFailureCount ?? 0))
  }
  if ((before.cooldownRetestObservationStartedAt ?? null) !== (after.cooldownRetestObservationStartedAt ?? null)) {
    changes.push(safeChange('cooldownRetestObservationStartedAt', '自动恢复观察开始时间', before.cooldownRetestObservationStartedAt, after.cooldownRetestObservationStartedAt))
  }
  if ((before.cooldownRetestLastAt ?? null) !== (after.cooldownRetestLastAt ?? null)) {
    changes.push(safeChange('cooldownRetestLastAt', '最近后台复测时间', before.cooldownRetestLastAt, after.cooldownRetestLastAt))
  }
  if ((before.cooldownRetestLastStatusCode ?? null) !== (after.cooldownRetestLastStatusCode ?? null)) {
    changes.push(safeChange('cooldownRetestLastStatusCode', '最近后台复测状态码', before.cooldownRetestLastStatusCode, after.cooldownRetestLastStatusCode))
  }
  if ((before.streamFailureCount ?? 0) !== (after.streamFailureCount ?? 0)) {
    changes.push(safeChange('streamFailureCount', '流式失败次数', before.streamFailureCount ?? 0, after.streamFailureCount ?? 0))
  }
  if ((before.streamFailureWindowStartedAt ?? null) !== (after.streamFailureWindowStartedAt ?? null)) {
    changes.push(safeChange('streamFailureWindowStartedAt', '流式失败窗口开始时间', before.streamFailureWindowStartedAt, after.streamFailureWindowStartedAt))
  }
  return changes
}

function shouldMarkAccountTestFailureAsTemporaryUnavailable(account: AccountSummary, result: { success: boolean; accountFailureEligible?: boolean; accountStatusChanged?: boolean; accountStatus?: string }): boolean {
  if (result.success) return false
  if (result.accountStatusChanged) return false
  if (result.accountFailureEligible === false) return false
  if (account.status !== 'active' && account.status !== 'rate_limited' && account.status !== 'temporary_unavailable') return false
  if (account.status === 'active' && !account.schedulable) return false
  const observedStatus = result.accountStatus ?? account.status
  if (observedStatus !== 'active' && observedStatus !== 'rate_limited' && observedStatus !== 'temporary_unavailable') return false
  return true
}

function accountTestFailureCooldownReason(result: { traceId?: string; statusCode?: number; errorCode?: string; message?: string }): string {
  const parts = ['账户测试失败，已自动标记为临时不可调用']
  const traceId = normalizedString(result.traceId)
  if (traceId) {
    parts.push(`traceId ${traceId}`)
  }
  if (typeof result.statusCode === 'number') {
    parts.push(`HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  }
  return parts.join('；')
}

function accountTestResultWithTotalDuration(result: AccountTestResult, startedAt: number): AccountTestResult {
  return {
    ...result,
    durationMs: Date.now() - startedAt
  }
}

function draftAccountTestErrorMessage(error: unknown, signal: AbortSignal): string {
  if (signal.aborted) {
    return isDiagnosticTimeoutSignal(signal) ? '账户测试超时' : '账户测试已取消'
  }
  return error instanceof Error ? error.message : 'OpenAI Responses 测试失败'
}

function draftAccountTestFailureEligible(error: unknown, signal: AbortSignal): boolean {
  if (signal.aborted) {
    return isDiagnosticTimeoutSignal(signal)
  }
  return !(error instanceof DraftAccountConfigurationError)
}

class DraftAccountConfigurationError extends Error {
}

function authorizedLocalOperationOwner(account: AccountSummary, access?: AccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function effectiveRequestSystemAccountId(access?: AccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

function failedAccountTestResult(
  account: AccountSummary,
  message: string,
  model?: string,
  options: { accountFailureEligible?: boolean; durationMs?: number } = {}
): AccountTestResult {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    success: false,
    message,
    model,
    durationMs: options.durationMs,
    accountStatus: account.status,
    accountFailureEligible: options.accountFailureEligible ?? false
  }
}

function sendAccountTestTasksFromDbService(taskIds: string[]): boolean {
  if (!process.send || process.connected === false) {
    return false
  }
  try {
    process.send({
      type: 'background_worker_account_test_tasks',
      taskIds
    }, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_test_task_db_service_dispatch_failed',
          taskCount: taskIds.length
        }), 'DB service 投递账号测试任务到父进程失败')
      }
    })
    return true
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_test_task_db_service_dispatch_failed',
      taskCount: taskIds.length
    }), 'DB service 投递账号测试任务到父进程失败')
    return false
  }
}

function sendAccountTestCancelFromDbService(taskId: string): boolean {
  if (!process.send || process.connected === false) {
    return false
  }
  try {
    process.send({
      type: 'background_worker_account_test_cancel',
      taskId
    }, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_test_cancel_db_service_dispatch_failed',
          taskId
        }), 'DB service 投递账号测试取消到父进程失败')
      }
    })
    return true
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_test_cancel_db_service_dispatch_failed',
      taskId
    }), 'DB service 投递账号测试取消到父进程失败')
    return false
  }
}

function stringCredential(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizedString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}
