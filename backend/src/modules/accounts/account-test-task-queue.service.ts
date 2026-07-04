import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountSupportedEndpointMode, AccountTestApiKeyPoolItemResult, AccountTestResult } from '../../domain/types.js'
import { logger, errorLogFields } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  accountTestUnavailableMessage,
  getAccountPrecheckMutationStateAsync,
  resolveProxyUrlForProfileAsync,
  runtimeOpenAIAccountCredentials,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { accountApiKeyEntries, selectAccountRuntimeApiKey, type AccountApiKeyEntry } from '../../storage/account-api-key-rotation.js'
import { getSettings } from '../../storage/settings.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  type AccountTestDraftSnapshot,
} from '../../storage/account-test-tasks.repository.js'
import { requestBackgroundWorkerDbService, sendAccountRuntimeClearToServer, sendAccountTestCancelToWorker, sendAccountTestTasksToWorker } from '../background/background-ipc.js'
import { operationMode, recordOperationLogAsync, resolveOperationOwner, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { buildOpenAIOAuthCredentials, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import { isGatewaySupportedProtocolProfile } from '../../domain/provider-protocol.js'
import { preferredSystemAccountTestModelAsync, testOpenAIAccount, testOpenAIAccountWithDiagnosticRetries } from './account-test.service.js'
import {
  type AccountDiagnosticAttemptProgress,
  accountDiagnosticAttemptProgress,
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from './account-diagnostic-retry-policy.js'
import {
  accountApiKeyPoolEntriesForCandidate,
  fixedAccountApiKeyPoolCandidate,
  isCandidateAccountApiKeyPoolTestable
} from './account-api-key-pool-runtime.js'

interface AccountTestQueueItem {
  taskId: string
}

interface ManualAccountTestFailurePrecheckQueueItem {
  accountId: string
  accountName: string
  access: AccessScope
  model?: string
  testEndpointMode?: AccountSupportedEndpointMode
  originalResult: Pick<AccountTestResult, 'traceId' | 'statusCode' | 'errorCode' | 'message' | 'model'>
  precheckStartedAt: string
}

const unsupportedGatewayProtocolTestMessage = '当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户'
const defaultManualAccountTestConcurrency = 100
const manualAccountTestRefillMinBatchSize = 100
const manualAccountTestRefillMaxBatchSize = 1000
const manualAccountTestQueuedMaxWaitMs = 10 * 60_000
const manualAccountTestQueuedSweepBatchSize = 500
const accountApiKeyPoolTestConcurrency = 5
const manualAccountTestRetryPolicy = sequenceRetryPolicy('manual_account_test', [], 0)
const manualAccountTestFailurePrecheckRetryPolicy = sequenceRetryPolicy('manual_account_test_failure_precheck', [], 0)
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
    void failAccountTestTaskViaDbService(event.item.taskId, event.error instanceof Error ? event.error.message : '账号测试任务执行失败')
    refillManualAccountTestQueue()
  }
})

const manualAccountTestFailurePrecheckQueue = createRetryQueue<ManualAccountTestFailurePrecheckQueueItem>({
  name: 'manual-account-test-failure-precheck',
  policy: manualAccountTestFailurePrecheckRetryPolicy,
  concurrency: 1,
  run: runManualAccountTestFailurePrecheckQueueItem,
  onExhausted: (event) => {
    logger.warn(errorLogFields(event.error, {
      event: 'manual_account_test_failure_precheck_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      traceId: event.item.originalResult.traceId,
      attemptCount: event.attemptIndex + 1
    }), '账号测试失败事前确认任务已用尽，本轮跳过状态写入')
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
  void markAccountTestTaskCanceledViaDbService(normalizedId, '已停止测试')
}

export function startAccountTestTaskQueue(): void {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  void runAccountTestTaskMaintenance('start').then((taskIds) => {
    for (const taskId of taskIds) {
      enqueueAccountTestTaskLocal(taskId)
    }
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'manual_account_test_start_maintenance_failed'
    }), '账号测试队列启动维护失败')
  })
  startAccountTestSessionStaleSweep()
  refillManualAccountTestQueue()
}

function refillManualAccountTestQueue(): void {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  manualAccountTestQueue.setConcurrency(accountTestTaskConcurrency())
  void runAccountTestTaskMaintenance('sweep').then((taskIds) => {
    for (const taskId of taskIds) {
      enqueueAccountTestTaskLocal(taskId)
    }
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'manual_account_test_refill_failed'
    }), '账号测试队列补充任务失败')
  })
}

function accountTestTaskConcurrency(): number {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return defaultManualAccountTestConcurrency
  }
  const value = getSettings().accountTestTaskConcurrency
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return defaultManualAccountTestConcurrency
  }
  return Math.min(1000, Math.max(1, Math.trunc(value)))
}

function manualAccountTestRefillBatchSize(): number {
  return Math.min(manualAccountTestRefillMaxBatchSize, Math.max(manualAccountTestRefillMinBatchSize, accountTestTaskConcurrency() * 2))
}

function startAccountTestSessionStaleSweep(): void {
  if (accountTestSessionStaleSweepTimer) {
    return
  }
  accountTestSessionStaleSweepTimer = setInterval(() => {
    sweepManualAccountTestQueue()
  }, 2_000)
  accountTestSessionStaleSweepTimer.unref()
}

function sweepManualAccountTestQueue(): void {
  void runAccountTestTaskMaintenance('sweep')
}

export function getManualAccountTestQueueSnapshot() {
  return manualAccountTestQueue.snapshot()
}

async function runAccountTestQueueItem(item: AccountTestQueueItem): Promise<boolean> {
  const task = await markAccountTestTaskRunningViaDbService(item.taskId)
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
    if (await isAccountTestTaskCancelRequestedViaDbService(task.id)) {
      await markAccountTestTaskCanceledViaDbService(task.id, await accountTestTaskCancelMessageViaDbService(task.id))
      return true
    }

    if (task.draftAccount) {
      const draft = task.draftAccount
      const draftAccount = accountSummaryFromDraftSnapshot(draft)
      if (!isGatewaySupportedProtocolProfile(draftAccount)) {
          await failAccountTestTaskViaDbService(task.id, unsupportedGatewayProtocolTestMessage, failedAccountTestResult(draftAccount, task.message ?? unsupportedGatewayProtocolTestMessage, task.model))
        return true
      }

      const stateTargetAccountId = normalizedString(draft.stateTargetAccountId)
      if (stateTargetAccountId) {
        const account = await loadAccountForTestViaDbService(stateTargetAccountId, access)
        if (!account) {
          await failAccountTestTaskViaDbService(task.id, '账户不存在')
          return true
        }
        if (account.accessType === 'authorized') {
          await failAccountTestTaskViaDbService(task.id, '授权账户测试不支持使用未保存表单配置', failedAccountTestResult(account, task.message ?? '授权账户测试不支持使用未保存表单配置', task.model))
          return true
        }
        if (!isGatewaySupportedProtocolProfile(account)) {
          await failAccountTestTaskViaDbService(task.id, unsupportedGatewayProtocolTestMessage, failedAccountTestResult(account, task.message ?? unsupportedGatewayProtocolTestMessage, task.model))
          return true
        }
        const unavailableMessage = accountTestUnavailableMessage(account)
        if (unavailableMessage) {
          await failAccountTestTaskViaDbService(task.id, unavailableMessage, failedAccountTestResult(account, unavailableMessage, task.model))
          return true
        }
        const result = await runOpenAIAccountTestWithSideEffects(account, access, {
          model: task.model,
          testEndpointMode: task.testEndpointMode,
          diagnostics: task.diagnostics,
          signal: controller.signal,
          draftAccount: draft,
          onDiagnosticAttemptProgress,
          onStatusMessage: (message) => {
            void updateAccountTestTaskMessageViaDbService(task.id, message)
          }
        })
        if (controller.signal.aborted || await isAccountTestTaskCancelRequestedViaDbService(task.id)) {
          await markAccountTestTaskCanceledViaDbService(task.id, await accountTestTaskCancelMessageViaDbService(task.id))
          return true
        }
        await completeAccountTestTaskViaDbService(task.id, result)
        return true
      }

      const result = await runOpenAIDraftAccountTestWithApiKeyPool(draftAccount, access, draft, {
        model: task.model,
        testEndpointMode: task.testEndpointMode,
        diagnostics: task.diagnostics,
        signal: controller.signal,
        onDiagnosticAttemptProgress,
        onStatusMessage: (message) => {
          void updateAccountTestTaskMessageViaDbService(task.id, message)
        }
      })
      if (controller.signal.aborted || await isAccountTestTaskCancelRequestedViaDbService(task.id)) {
        await markAccountTestTaskCanceledViaDbService(task.id, await accountTestTaskCancelMessageViaDbService(task.id))
        return true
      }
      await completeAccountTestTaskViaDbService(task.id, result)
      return true
    }

    const account = await loadAccountForTestViaDbService(task.accountId, access)
    if (!account) {
      await failAccountTestTaskViaDbService(task.id, '账户不存在')
      return true
    }
    if (!isGatewaySupportedProtocolProfile(account)) {
      await failAccountTestTaskViaDbService(task.id, unsupportedGatewayProtocolTestMessage, failedAccountTestResult(account, task.message ?? unsupportedGatewayProtocolTestMessage, task.model))
      return true
    }
    const unavailableMessage = accountTestUnavailableMessage(account)
    if (unavailableMessage) {
      await failAccountTestTaskViaDbService(task.id, unavailableMessage, failedAccountTestResult(account, unavailableMessage, task.model))
      return true
    }

    const result = await runOpenAIAccountTestWithSideEffects(account, access, {
      model: task.model,
      testEndpointMode: task.testEndpointMode,
      diagnostics: task.diagnostics,
      signal: controller.signal,
      onDiagnosticAttemptProgress,
      onStatusMessage: (message) => {
        void updateAccountTestTaskMessageViaDbService(task.id, message)
      }
    })

    if (controller.signal.aborted || await isAccountTestTaskCancelRequestedViaDbService(task.id)) {
      await markAccountTestTaskCanceledViaDbService(task.id, await accountTestTaskCancelMessageViaDbService(task.id))
      return true
    }

    await completeAccountTestTaskViaDbService(task.id, result)
    return true
  } catch (error) {
    if (controller.signal.aborted || await isAccountTestTaskCancelRequestedViaDbService(task.id)) {
      await markAccountTestTaskCanceledViaDbService(task.id, await accountTestTaskCancelMessageViaDbService(task.id))
      return true
    }
    logger.warn(errorLogFields(error, {
      event: 'manual_account_test_task_failed',
      taskId: task.id,
      accountId: task.accountId
    }), '账号测试后台任务执行失败')
    await failAccountTestTaskViaDbService(task.id, error instanceof Error ? error.message : '账号测试任务执行失败')
    return true
  } finally {
    runningAccountTestControllers.delete(task.id)
  }
}

function accountTestTaskProgressReporter(taskId: string): (progress: AccountDiagnosticAttemptProgress) => void {
  return (progress) => {
    void updateAccountTestTaskMessageViaDbService(taskId, accountDiagnosticAttemptMessage(progress))
  }
}

async function runAccountTestTaskMaintenance(action: 'start' | 'sweep'): Promise<string[]> {
  const result = await requestBackgroundWorkerDbService({
    type: 'account_test_task_maintenance',
    action,
    maxQueuedMs: manualAccountTestQueuedMaxWaitMs,
    sweepLimit: manualAccountTestQueuedSweepBatchSize,
    refillLimit: manualAccountTestRefillBatchSize()
  })
  if (!result) {
    return []
  }
  const expiredTaskIds = [...result.canceledTaskIds, ...result.expiredQueuedTaskIds]
  for (const taskId of expiredTaskIds) {
    manualAccountTestQueue.delete(taskId)
    runningAccountTestControllers.get(taskId)?.abort()
  }
  if (result.expiredQueuedTaskIds.length > 0) {
    logger.warn({
      event: 'manual_account_test_queued_wait_expired',
      taskCount: result.expiredQueuedTaskIds.length,
      maxQueuedMs: manualAccountTestQueuedMaxWaitMs
    }, '账号测试 queued 等待超过后台上限，已自动失败收口')
  }
  return result.taskIds
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_test',
    accountId,
    access
  }, 10_000)
}

async function markAccountTestTaskRunningViaDbService(taskId: string) {
  return await requestBackgroundWorkerDbService({
    type: 'mark_account_test_task_running',
    taskId
  })
}

async function markAccountTestTaskCanceledViaDbService(taskId: string, message: string) {
  return await requestBackgroundWorkerDbService({
    type: 'mark_account_test_task_canceled',
    taskId,
    message
  })
}

async function completeAccountTestTaskViaDbService(taskId: string, result: AccountTestResult) {
  return await requestBackgroundWorkerDbService({
    type: 'complete_account_test_task',
    taskId,
    result
  })
}

async function failAccountTestTaskViaDbService(taskId: string, message: string, result?: AccountTestResult) {
  return await requestBackgroundWorkerDbService({
    type: 'fail_account_test_task',
    taskId,
    message,
    result
  })
}

async function updateAccountTestTaskMessageViaDbService(taskId: string, message: string) {
  return await requestBackgroundWorkerDbService({
    type: 'update_account_test_task_message',
    taskId,
    message
  })
}

async function isAccountTestTaskCancelRequestedViaDbService(taskId: string): Promise<boolean> {
  const result = await requestBackgroundWorkerDbService({
    type: 'is_account_test_task_cancel_requested',
    taskId
  })
  return result?.canceled ?? false
}

async function accountTestTaskCancelMessageViaDbService(taskId: string): Promise<string> {
  const result = await requestBackgroundWorkerDbService({
    type: 'read_account_test_task_cancel_message',
    taskId
  })
  return result?.message ?? '已停止测试'
}

async function recordAccountSuccessfulTestModelViaDbService(accountId: string, model: string, access: AccessScope): Promise<void> {
  await requestBackgroundWorkerDbService({
    type: 'record_account_successful_test_model',
    accountId,
    model,
    access
  })
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
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
  }
): Promise<AccountTestResult> {
  return testOpenAIDraftAccountWithDiagnosticRetries(account, draft, input)
}

async function runOpenAIDraftAccountTestWithApiKeyPool(
  account: AccountSummary,
  access: AccessScope,
  draft: AccountTestDraftSnapshot,
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
    onStatusMessage?: (message: string) => void
  }
): Promise<AccountTestResult> {
  return await runAccountApiKeyPoolTestIfNeeded(account, access, {
    ...input,
    draftAccount: draft
  }) ?? await runOpenAIDraftAccountTest(account, draft, input)
}

async function testOpenAIDraftAccountWithDiagnosticRetries(
  account: AccountSummary,
  draft: AccountTestDraftSnapshot,
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
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
        testEndpointMode: input.testEndpointMode,
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
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    draftAccount?: AccountTestDraftSnapshot
    onDiagnosticAttemptProgress?: (progress: AccountDiagnosticAttemptProgress) => void
    onStatusMessage?: (message: string) => void
  }
): Promise<AccountTestResult> {
  let accountTestStatusChanges: ReturnType<typeof safeChange>[] | undefined
  let result = await runAccountApiKeyPoolTestIfNeeded(account, access, input)
  if (!result) {
    result = input.draftAccount
      ? await runOpenAIDraftAccountTest(account, input.draftAccount, {
        model: input.model,
        testEndpointMode: input.testEndpointMode,
        diagnostics: input.diagnostics,
        signal: input.signal,
        onDiagnosticAttemptProgress: input.onDiagnosticAttemptProgress
      })
      : await testOpenAIAccountWithDiagnosticRetries(account, {
        model: input.model,
        testEndpointMode: input.testEndpointMode,
        signal: input.signal,
        diagnostics: input.diagnostics,
        systemAccountId: access.systemAccountId,
        onDiagnosticAttemptProgress: input.onDiagnosticAttemptProgress,
        findAccountForTest: loadAccountForTestViaDbService
      })
  }

  if (input.signal.aborted) {
    return result
  }

  if (result.success && shouldClearAccountAfterSuccessfulTest(account)) {
    const restored = await clearAccountAfterSuccessfulTest(account, access)
    if (restored.changed && restored.accountStatus) {
      const restoredStatus = restored.accountStatus as AccountSummary['status']
      accountTestStatusChanges = accountTestStatusLogChanges(account, {
        ...account,
        status: restoredStatus
      })
      result = {
        ...result,
        accountStatusChanged: restored.changed,
        accountStatus: restoredStatus
      }
    }
  }

  if (result.success) {
    await recordAccountSuccessfulTestModelViaDbService(account.id, result.model ?? '', access)
    clearAccountGatewayRuntimeAfterRestore(account, access)
  }

  if (shouldRunAccountTestFailurePrecheck(account, result)) {
    enqueueManualAccountTestFailurePrecheck(account, access, result, {
      model: input.model ?? result.model,
      testEndpointMode: input.testEndpointMode ?? result.testEndpointMode
    })
  }

  if (result.accountStatusChanged) {
    const ownerSystemAccountId = authorizedLocalOperationOwner(account, access)
      ?? resolveOperationOwner(account as unknown as Record<string, unknown>, access)
    await recordOperationLogAsync({
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

interface AccountApiKeyPoolEntryTestResult extends AccountTestApiKeyPoolItemResult {
  entry: AccountApiKeyEntry
  result: AccountTestResult
}

async function runAccountApiKeyPoolTestIfNeeded(
  account: AccountSummary,
  access: AccessScope,
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    draftAccount?: AccountTestDraftSnapshot
    onStatusMessage?: (message: string) => void
  }
): Promise<AccountTestResult | undefined> {
  if (account.type !== 'api_key') {
    return undefined
  }
  const baseCandidate = input.draftAccount
    ? await openAIDraftAccountSecret(input.draftAccount, input.signal)
    : await loadSavedAccountApiKeyPoolCandidate(account, access)
  if (!baseCandidate) {
    return undefined
  }
  const entries = accountApiKeyPoolEntriesForCandidate(baseCandidate)
  if (!isCandidateAccountApiKeyPoolTestable(baseCandidate, entries)) {
    return undefined
  }

  const startedAt = Date.now()
  const groupId = baseCandidate.boundGroupId ?? account.boundGroupId
  const systemAccountId = baseCandidate.systemAccountId || accountTestPrecheckSystemAccountId(account) || access.systemAccountId
  if (!groupId || !systemAccountId) {
    return undefined
  }

  input.onStatusMessage?.(accountApiKeyPoolProgressMessage(0, entries.length, 0, 0))
  const itemResults = await runAccountApiKeyPoolEntryTests(account, baseCandidate, entries, {
    model: input.model,
    testEndpointMode: input.testEndpointMode,
    diagnostics: input.diagnostics,
    signal: input.signal,
    groupId,
    systemAccountId,
    onProgress: input.onStatusMessage
  })
  let persistedRuntime = false
  if (!input.draftAccount && !input.signal.aborted) {
    try {
      await persistSavedAccountApiKeyPoolRuntime(baseCandidate, itemResults)
      persistedRuntime = true
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'account_api_key_pool_test_runtime_persist_failed',
        accountId: account.id,
        accountName: account.name
      }), 'API Key 池测试结果写入运行态失败，本次仅返回测试结论')
    }
  }
  const result = accountApiKeyPoolSummaryResult(account, input.model, itemResults, {
    total: entries.length,
    persistedRuntime
  })
  return accountTestResultWithTotalDuration(result, startedAt)
}

async function loadSavedAccountApiKeyPoolCandidate(
  account: AccountSummary,
  access: AccessScope
): Promise<OpenAIAccountSecret | undefined> {
  if (!account.boundGroupId || account.accessType === 'authorized') {
    return undefined
  }
  const systemAccountId = accountTestPrecheckSystemAccountId(account) ?? access.systemAccountId
  if (!systemAccountId) {
    return undefined
  }
  return await loadOpenAIAccountForGroupViaDbService(account.boundGroupId, account.id, systemAccountId, { ignoreAvailability: true })
}

async function runAccountApiKeyPoolEntryTests(
  account: AccountSummary,
  baseCandidate: OpenAIAccountSecret,
  entries: AccountApiKeyEntry[],
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    groupId: string
    systemAccountId: string
    onProgress?: (message: string) => void
  }
): Promise<AccountApiKeyPoolEntryTestResult[]> {
  const results: Array<AccountApiKeyPoolEntryTestResult | undefined> = new Array(entries.length)
  let nextIndex = 0
  let completed = 0
  let successCount = 0
  let failedCount = 0
  const workerCount = Math.min(accountApiKeyPoolTestConcurrency, entries.length)

  async function runWorker(): Promise<void> {
    while (!input.signal.aborted && nextIndex < entries.length) {
      const index = nextIndex
      nextIndex += 1
      const result = await runAccountApiKeyPoolEntryTest(account, baseCandidate, entries[index], input)
      results[index] = result
      completed += 1
      if (result.success) {
        successCount += 1
      } else {
        failedCount += 1
      }
      input.onProgress?.(accountApiKeyPoolProgressMessage(completed, entries.length, successCount, failedCount))
    }
  }

  await Promise.all(Array.from({ length: workerCount }, runWorker))
  return results.filter((result): result is AccountApiKeyPoolEntryTestResult => Boolean(result))
}

async function runAccountApiKeyPoolEntryTest(
  account: AccountSummary,
  baseCandidate: OpenAIAccountSecret,
  entry: AccountApiKeyEntry,
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
    groupId: string
    systemAccountId: string
  }
): Promise<AccountApiKeyPoolEntryTestResult> {
  const timeoutMs = accountDiagnosticRetryTimeoutMs[0] ?? 10_000
  const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
  const startedAt = Date.now()
  const fixedCandidate = fixedAccountApiKeyPoolCandidate(baseCandidate, entry, { apiKeyRuntimeStateDisabled: true })
  try {
    const result = await testOpenAIAccount(account, {
      model: input.model,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      testEndpointMode: input.testEndpointMode,
      diagnostics: input.diagnostics,
      signal: attemptSignal,
      candidateAccount: fixedCandidate,
      disableAccountStateMutation: true,
      findAccountForTest: loadAccountForTestViaDbService,
      findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
      gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs)
    })
    return accountApiKeyPoolEntryTestResult(entry, result)
  } catch (error) {
    return {
      entry,
      result: failedAccountTestResult(account, error instanceof Error ? error.message : 'API Key 测试失败', input.model, {
        accountFailureEligible: false,
        durationMs: Date.now() - startedAt
      }),
      keyIndex: entry.index,
      keyPrefix: keyPrefixForDisplay(entry.key),
      keySuffix: keySuffixForDisplay(entry.key),
      success: false,
      message: error instanceof Error ? error.message : 'API Key 测试失败',
      durationMs: Date.now() - startedAt
    }
  }
}

function accountApiKeyPoolEntryTestResult(
  entry: AccountApiKeyEntry,
  result: AccountTestResult
): AccountApiKeyPoolEntryTestResult {
  return {
    entry,
    result,
    keyIndex: entry.index,
    keyPrefix: keyPrefixForDisplay(entry.key),
    keySuffix: keySuffixForDisplay(entry.key),
    success: result.success,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    message: result.message,
    durationMs: result.durationMs
  }
}

function accountApiKeyPoolSummaryResult(
  account: AccountSummary,
  model: string | undefined,
  itemResults: AccountApiKeyPoolEntryTestResult[],
  input: {
    total: number
    persistedRuntime: boolean
  }
): AccountTestResult {
  const successCount = itemResults.filter((item) => item.success).length
  const failedCount = itemResults.filter((item) => !item.success).length
  const success = successCount >= 1
  const representative = (success
    ? itemResults.find((item) => item.success)?.result
    : itemResults.find((item) => !item.success)?.result)
    ?? failedAccountTestResult(account, 'API Key 池测试未完成', model, { accountFailureEligible: false })
  const pool = {
    total: input.total,
    tested: itemResults.length,
    successCount,
    failedCount,
    requiredSuccessCount: 1,
    results: itemResults.map(({ entry: _entry, result: _result, ...item }) => item)
  }
  return {
    ...representative,
    success,
    errorCode: success ? undefined : representative.errorCode,
    message: accountApiKeyPoolTestMessage(pool, input.persistedRuntime),
    accountFailureEligible: false,
    apiKeyPool: pool
  }
}

async function persistSavedAccountApiKeyPoolRuntime(
  baseCandidate: OpenAIAccountSecret,
  itemResults: AccountApiKeyPoolEntryTestResult[]
): Promise<void> {
  await Promise.all(itemResults.map(async (item) => {
    const candidate = fixedAccountApiKeyPoolCandidate(baseCandidate, item.entry)
    if (item.success) {
      await requestBackgroundWorkerDbService({
        type: 'record_account_api_key_success',
        account: candidate
      })
      return
    }
    await requestBackgroundWorkerDbService({
      type: 'record_account_api_key_failure',
      account: candidate,
      input: {
        status: 'temporary_unavailable',
        statusCode: item.statusCode,
        errorCode: item.errorCode,
        errorMessage: item.message
      }
    })
  }))
}

async function loadOpenAIAccountForGroupViaDbService(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = { ignoreAvailability: true }
): Promise<OpenAIAccountSecret | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_openai_account_for_group',
    groupId,
    accountId,
    systemAccountId,
    includeUnavailable: options.includeUnavailable,
    ignoreAvailability: options.ignoreAvailability
  }, 10_000)
}

function accountApiKeyPoolProgressMessage(completed: number, total: number, successCount: number, failedCount: number): string {
  return `API Key 池测试中：已完成 ${completed}/${total}，可用 ${successCount}，不可用 ${failedCount}`
}

function accountApiKeyPoolTestMessage(
  pool: {
    total: number
    tested: number
    successCount: number
    failedCount: number
  },
  persistedRuntime: boolean
): string {
  if (pool.successCount >= 1) {
    if (pool.failedCount > 0) {
      return persistedRuntime
        ? `API Key 池测试通过：${pool.successCount}/${pool.total} 个 Key 可用，${pool.failedCount} 个 Key 已进入后台恢复`
        : `API Key 池测试通过：${pool.successCount}/${pool.total} 个 Key 可用，${pool.failedCount} 个 Key 未通过`
    }
    return `API Key 池测试通过：${pool.successCount}/${pool.total} 个 Key 可用`
  }
  if (pool.tested < pool.total) {
    return `API Key 池测试未完成：0/${pool.total} 个 Key 可用`
  }
  return `API Key 池测试未通过：0/${pool.total} 个 Key 可用`
}

function keyPrefixForDisplay(key: string): string | undefined {
  const text = key.trim()
  return text ? text.slice(0, 4) : undefined
}

function keySuffixForDisplay(key: string): string | undefined {
  const text = key.trim()
  return text ? text.slice(-4) : undefined
}

function enqueueManualAccountTestFailurePrecheck(
  account: AccountSummary,
  access: AccessScope,
  result: AccountTestResult,
  input: {
    model?: string
    testEndpointMode?: AccountSupportedEndpointMode
  }
): void {
  const precheckStartedAt = new Date().toISOString()
  const enqueued = manualAccountTestFailurePrecheckQueue.enqueue(manualAccountTestFailurePrecheckKey(account, access), {
    accountId: account.id,
    accountName: account.name,
    access: { ...access },
    model: normalizedString(input.model ?? result.model),
    testEndpointMode: input.testEndpointMode,
    originalResult: {
      traceId: result.traceId,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      message: result.message,
      model: result.model
    },
    precheckStartedAt
  })
  logger.info({
    event: enqueued ? 'manual_account_test_failure_precheck_enqueued' : 'manual_account_test_failure_precheck_deduped',
    accountId: account.id,
    accountName: account.name,
    traceId: result.traceId,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    model: input.model ?? result.model,
    testEndpointMode: input.testEndpointMode,
    precheckStartedAt
  }, enqueued ? '账号测试失败，已进入事前确认队列' : '账号测试失败事前确认已在队列中，本次不重复入队')
}

async function runManualAccountTestFailurePrecheckQueueItem(
  item: ManualAccountTestFailurePrecheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await loadAccountForTestViaDbService(item.accountId, item.access)
  if (!account) {
    logger.info({
      event: 'manual_account_test_failure_precheck_skipped',
      accountId: item.accountId,
      accountName: item.accountName,
      skipReason: 'account_missing',
      traceId: item.originalResult.traceId
    }, '账号测试失败事前确认已失效，跳过状态写入')
    return true
  }
  const skipReason = await manualAccountTestFailurePrecheckSkipReason(item, account)
  if (skipReason) {
    logger.info({
      event: 'manual_account_test_failure_precheck_skipped',
      accountId: item.accountId,
      accountName: item.accountName,
      accountStatus: account.status,
      skipReason,
      traceId: item.originalResult.traceId
    }, '账号测试失败事前确认已失效，跳过状态写入')
    return true
  }

  const result = await testOpenAIAccountWithDiagnosticRetries(account, {
    model: item.model || await preferredSystemAccountTestModelAsync(account),
    testEndpointMode: item.testEndpointMode,
    diagnostics: 'full',
    groupId: account.boundGroupId,
    systemAccountId: accountTestPrecheckSystemAccountId(account),
    trafficSource: 'cooldown_retest',
    disableAccountStateMutation: true,
    findAccountForTest: loadAccountForTestViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })

  if (result.success) {
    logger.info({
      event: 'manual_account_test_failure_precheck_recovered',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      originalTraceId: item.originalResult.traceId,
      precheckTraceId: result.traceId,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber
    }, '账号测试失败后事前确认通过，保留当前账户状态')
    return true
  }

  if (result.accountFailureEligible === false) {
    logger.warn({
      event: 'manual_account_test_failure_precheck_ineligible_failure_discarded',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      message: result.message,
      originalTraceId: item.originalResult.traceId,
      precheckTraceId: result.traceId
    }, '账号测试失败后事前确认未通过，但失败不属于账号失败，已跳过状态写入')
    return true
  }

  const latestAccount = await loadAccountForTestViaDbService(item.accountId, item.access)
  if (!latestAccount) {
    logger.info({
      event: 'manual_account_test_failure_precheck_mark_skipped',
      accountId: item.accountId,
      accountName: item.accountName,
      skipReason: 'account_missing',
      originalTraceId: item.originalResult.traceId,
      precheckTraceId: result.traceId
    }, '账号测试失败事前确认完成前账户已变化，跳过状态写入')
    return true
  }
  const latestSkipReason = await manualAccountTestFailurePrecheckSkipReason(item, latestAccount)
  if (latestSkipReason) {
    logger.info({
      event: 'manual_account_test_failure_precheck_mark_skipped',
      accountId: item.accountId,
      accountName: item.accountName,
      accountStatus: latestAccount.status,
      skipReason: latestSkipReason,
      originalTraceId: item.originalResult.traceId,
      precheckTraceId: result.traceId
    }, '账号测试失败事前确认完成前账户已变化，跳过状态写入')
    return true
  }

  const reason = accountTestFailurePrecheckCooldownReason(item, result)
  const updatedAccount = await requestBackgroundWorkerDbService({
    type: 'mark_account_test_temporary_unavailable',
    accountId: latestAccount.id,
    reason,
    access: item.access
  })
  if (updatedAccount?.updated) {
    const nextAccount = {
      ...latestAccount,
      status: (updatedAccount.accountStatus ?? latestAccount.status) as AccountSummary['status']
    }
    const changes = accountTestStatusLogChanges(latestAccount, nextAccount)
    await recordAccountTestPrecheckStatusChangedOperation(latestAccount, nextAccount, item.access, changes)
  }
  logger.warn({
    event: 'manual_account_test_failure_precheck_marked',
    accountId: latestAccount.id,
    accountName: latestAccount.name,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    durationMs: result.durationMs,
    originalTraceId: item.originalResult.traceId,
    precheckTraceId: result.traceId,
    accountStatus: updatedAccount?.accountStatus,
    updated: updatedAccount?.updated ?? false
  }, '账号测试失败且事前确认未通过，已尝试标记为临时不可调用')
  return true
}

async function openAIDraftAccountSecret(draft: AccountTestDraftSnapshot, signal: AbortSignal): Promise<OpenAIAccountSecret> {
  const proxy = await draftProxyProfile(draft.proxyProfileId)
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
    : selectAccountRuntimeApiKey({ accountId: draft.id, credentials })
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
    apiKeys: draft.type === 'api_key' ? accountApiKeyEntries(credentials).map((entry) => entry.key) : undefined,
    refreshToken: stringCredential(credentials.refresh_token) || undefined,
    clientId: stringCredential(credentials.client_id) || undefined,
    proxyProfileId: draft.proxyProfileId,
    proxyUrl: proxy.proxyUrl,
    proxyProfileUnavailable: proxy.unavailable,
    proxyProfileErrorMessage: proxy.errorMessage,
    streamFailureCount: 0,
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

async function draftProxyProfile(proxyProfileId: string | undefined): Promise<{ proxyUrl?: string; unavailable?: boolean; errorMessage?: string }> {
  try {
    return { proxyUrl: await resolveProxyUrlForProfileAsync(proxyProfileId) }
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
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
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

async function clearAccountAfterSuccessfulTest(account: AccountSummary, access: AccessScope): Promise<{ changed: boolean; accountStatus?: string }> {
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
    : undefined
  const result = await requestBackgroundWorkerDbService({
    type: 'clear_account_failure_state',
    accountId: account.id,
    allowPendingTestRestore: true,
    authorizedBinding: account.accessType === 'authorized' && systemAccountId && account.boundGroupId && account.accountAuthorizationId
      ? {
          systemAccountId,
          groupId: account.boundGroupId,
          accountAuthorizationId: account.accountAuthorizationId
        }
      : undefined
  })
  return result ?? { changed: false }
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
    changes.push(safeChange('streamFailureCount', '流失败诊断次数', before.streamFailureCount ?? 0, after.streamFailureCount ?? 0))
  }
  if ((before.streamFailureWindowStartedAt ?? null) !== (after.streamFailureWindowStartedAt ?? null)) {
    changes.push(safeChange('streamFailureWindowStartedAt', '流失败诊断窗口开始时间', before.streamFailureWindowStartedAt, after.streamFailureWindowStartedAt))
  }
  return changes
}

function shouldRunAccountTestFailurePrecheck(account: AccountSummary, result: { success: boolean; accountFailureEligible?: boolean; accountStatusChanged?: boolean; accountStatus?: string }): boolean {
  if (result.success) return false
  if (result.accountStatusChanged) return false
  if (result.accountFailureEligible === false) return false
  if (account.status !== 'active' && account.status !== 'rate_limited' && account.status !== 'temporary_unavailable') return false
  if (account.status === 'active' && !account.schedulable) return false
  const observedStatus = result.accountStatus ?? account.status
  if (observedStatus !== 'active' && observedStatus !== 'rate_limited' && observedStatus !== 'temporary_unavailable') return false
  return true
}

async function manualAccountTestFailurePrecheckSkipReason(
  item: ManualAccountTestFailurePrecheckQueueItem,
  account: AccountSummary | undefined
): Promise<string | undefined> {
  if (!account) return 'account_missing'
  if (!shouldRunAccountTestFailurePrecheck(account, {
    success: false,
    accountFailureEligible: true,
    accountStatus: account.status
  })) {
    return 'account_not_eligible'
  }
  const state = await getAccountPrecheckMutationStateAsync({
    accountId: account.id,
    authorizedBinding: manualAccountTestFailurePrecheckAuthorizedBinding(account, item.access)
  })
  if (!state) {
    return 'account_missing'
  }
  if (state.status === 'disabled' || state.status === 'error') {
    return 'hard_unavailable'
  }
  const startedAtMs = Date.parse(item.precheckStartedAt)
  const updatedAtMs = state.updatedAt ? Date.parse(state.updatedAt) : NaN
  if (Number.isFinite(startedAtMs) && Number.isFinite(updatedAtMs) && updatedAtMs > startedAtMs && state.updatedAt !== state.lastUsedAt) {
    return 'account_updated'
  }
  return undefined
}

function manualAccountTestFailurePrecheckAuthorizedBinding(account: AccountSummary, access?: AccessScope) {
  if (account.accessType !== 'authorized') {
    return undefined
  }
  const systemAccountId = account.bindingSystemAccountId ?? effectiveRequestSystemAccountId(access)
  if (!systemAccountId || !account.boundGroupId || !account.accountAuthorizationId) {
    return undefined
  }
  return {
    accountId: account.id,
    systemAccountId,
    groupId: account.boundGroupId,
    accountAuthorizationId: account.accountAuthorizationId
  }
}

function manualAccountTestFailurePrecheckKey(account: AccountSummary, access: AccessScope): string {
  const binding = manualAccountTestFailurePrecheckAuthorizedBinding(account, access)
  if (binding) {
    return `${binding.systemAccountId}:${binding.groupId}:${binding.accountAuthorizationId}:${binding.accountId}`
  }
  return account.id
}

function accountTestPrecheckSystemAccountId(account: AccountSummary): string | undefined {
  return account.accessType === 'authorized'
    ? account.bindingSystemAccountId
    : account.ownerSystemAccountId ?? account.systemAccountId
}

function accountTestFailurePrecheckCooldownReason(
  item: ManualAccountTestFailurePrecheckQueueItem,
  result: { traceId?: string; statusCode?: number; errorCode?: string; message?: string }
): string {
  const parts = ['账户测试失败，事前确认仍未通过，已标记为临时不可调用']
  const originalTraceId = normalizedString(item.originalResult.traceId)
  if (originalTraceId) {
    parts.push(`原始 traceId ${originalTraceId}`)
  }
  const traceId = normalizedString(result.traceId)
  if (traceId) {
    parts.push(`确认 traceId ${traceId}`)
  }
  if (typeof result.statusCode === 'number') {
    parts.push(`确认 HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  }
  return parts.join('；').slice(0, 1000)
}

function recordAccountTestPrecheckStatusChangedOperation(
  before: AccountSummary,
  after: AccountSummary,
  access: AccessScope,
  changes: ReturnType<typeof safeChange>[]
): Promise<void> {
  const ownerSystemAccountId = authorizedLocalOperationOwner(before, access)
    ?? resolveOperationOwner(before as unknown as Record<string, unknown>, access)
  return recordOperationLogAsync({
    actorSystemAccountId: access.systemAccountId,
    actorRole: access.role,
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'accounts',
    action: 'test_status_changed',
    operationKey: 'accounts.test_status_changed',
    resourceType: 'account',
    resourceId: before.id,
    resourceName: before.name,
    summary: `账户测试确认失败更新状态：${before.name}`,
    changes: changes.length > 0 ? changes : [safeChange('status', '状态', before.status, after.status)],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  })
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
