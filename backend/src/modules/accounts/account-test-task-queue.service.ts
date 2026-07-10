import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountSupportedEndpointMode, AccountTestApiKeyPoolItemResult, AccountTestResult } from '../../domain/types.js'
import { logger, errorLogFields } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  accountTestUnavailableMessage,
  resolveProxyUrlForProfileAsync,
  runtimeOpenAIAccountCredentials,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { accountApiKeyEntries, type AccountApiKeyEntry } from '../../storage/account-api-key-rotation.js'
import { getSettings } from '../../storage/settings.repository.js'
import { DEFAULT_SYSTEM_SETTINGS } from '../../storage/schema-defaults.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  type AccountTestDraftSnapshot,
} from '../../storage/account-test-tasks.repository.js'
import { requestBackgroundWorkerDbService, sendAccountTestCancelToWorker, sendAccountTestTasksToWorker } from '../background/background-ipc.js'
import { buildOpenAIOAuthCredentials, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import { isGatewaySupportedProtocolProfile } from '../../domain/provider-protocol.js'
import { resolveAccountTestModelAsync, testOpenAIAccount, testOpenAIAccountWithDiagnosticRetries } from './account-test.service.js'
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

const unsupportedGatewayProtocolTestMessage = '当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户'
const defaultManualAccountTestConcurrency = 100
const defaultSystemSettingsByKey = new Map<string, unknown>(DEFAULT_SYSTEM_SETTINGS.map(([key, value]) => [key, value]))
const manualAccountTestRefillMinBatchSize = 100
const manualAccountTestRefillMaxBatchSize = 1000
const manualAccountTestQueuedMaxWaitMs = 10 * 60_000
const manualAccountTestQueuedSweepBatchSize = 500
const accountApiKeyPoolTestConcurrency = 5
const manualAccountTestRetryPolicy = sequenceRetryPolicy('manual_account_test', [], 0)
const runningAccountTestControllers = new Map<string, AbortController>()
let accountTestSessionStaleSweepTimer: NodeJS.Timeout | undefined
let sqliteSettingsTableMissingWarningLogged = false

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
  const value = accountTestTaskConcurrencySettingValue()
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return defaultManualAccountTestConcurrency
  }
  return Math.min(1000, Math.max(1, Math.trunc(value)))
}

function accountTestTaskConcurrencySettingValue(): unknown {
  try {
    return getSettings().accountTestTaskConcurrency
  } catch (error) {
    if (!isMissingSystemSettingsTableError(error)) {
      throw error
    }
    if (!sqliteSettingsTableMissingWarningLogged) {
      sqliteSettingsTableMissingWarningLogged = true
      logger.warn(errorLogFields(error, {
        event: 'manual_account_test_settings_table_missing_default'
      }), '账号测试队列启动时系统设置表尚未初始化，将临时使用默认并发')
    }
    return defaultSystemSettingsByKey.get('accountTestTaskConcurrency')
  }
}

function manualAccountTestRefillBatchSize(): number {
  return Math.min(manualAccountTestRefillMaxBatchSize, Math.max(manualAccountTestRefillMinBatchSize, accountTestTaskConcurrency() * 2))
}

function isMissingSystemSettingsTableError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('no such table: system_settings')
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
        const result = await runOpenAIAccountTestWithoutStateMutation(account, access, {
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

    const result = await runOpenAIAccountTestWithoutStateMutation(account, access, {
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
  const model = await resolveAccountTestModelAsync(account, {
    explicitModel: input.model,
    systemAccountId: draft.ownerSystemAccountId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    supportedModels: draft.supportedModels,
    testEndpointMode: input.testEndpointMode
  })
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
        model,
        groupId: draft.groupId,
        systemAccountId: draft.ownerSystemAccountId,
        testEndpointMode: input.testEndpointMode,
        diagnostics: input.diagnostics,
        signal: attemptSignal,
        candidateAccount,
        disableAccountStateMutation: true,
        gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs)
      })
    } catch (error) {
      result = failedAccountTestResult(account, draftAccountTestErrorMessage(error, attemptSignal), model, {
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
  return accountTestResultWithTotalDuration(lastResult ?? failedAccountTestResult(account, '账户测试失败', model, {
    accountFailureEligible: true
  }), startedAt)
}

async function runOpenAIAccountTestWithoutStateMutation(
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
  const result = await runAccountApiKeyPoolTestIfNeeded(account, access, input)
  if (!result) {
    return input.draftAccount
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
        systemAccountId: access.systemAccountFilterId ?? access.systemAccountId,
        disableAccountStateMutation: true,
        onDiagnosticAttemptProgress: input.onDiagnosticAttemptProgress,
        findAccountForTest: loadAccountForTestViaDbService,
        findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService
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
  const model = await resolveAccountTestModelAsync(account, {
    explicitModel: input.model,
    systemAccountId,
    providerCode: input.draftAccount?.providerCode,
    providerProtocolProfileId: input.draftAccount?.providerProtocolProfileId,
    supportedModels: input.draftAccount?.supportedModels,
    testEndpointMode: input.testEndpointMode
  })

  input.onStatusMessage?.(accountApiKeyPoolProgressMessage(0, entries.length, 0, 0))
  const itemResults = await runAccountApiKeyPoolEntryTests(account, baseCandidate, entries, {
    model,
    testEndpointMode: input.testEndpointMode,
    diagnostics: input.diagnostics,
    signal: input.signal,
    groupId,
    systemAccountId,
    onProgress: input.onStatusMessage
  })
  const result = accountApiKeyPoolSummaryResult(account, model, itemResults, {
    total: entries.length
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
    message: accountApiKeyPoolTestMessage(pool),
    accountFailureEligible: false,
    apiKeyPool: pool
  }
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
  }
): string {
  if (pool.successCount >= 1) {
    if (pool.failedCount > 0) {
      return `API Key 池测试通过：${pool.successCount}/${pool.total} 个 Key 可用，${pool.failedCount} 个 Key 未通过`
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
  const selectedApiKeyEntry = draft.type === 'api_key'
    ? accountApiKeyEntries(credentials)[0]
    : undefined
  const apiKey = draft.type === 'oauth'
    ? stringCredential(credentials.access_token)
    : selectedApiKeyEntry?.key
  if (!apiKey) {
    throw new DraftAccountConfigurationError(draft.type === 'oauth' ? 'OAuth 草稿缺少 Access Token' : '账户草稿缺少 API Key')
  }
  const runtimeCredentials = runtimeOpenAIAccountCredentials({
    ...credentials,
    ...(draft.type === 'api_key' ? { api_key: apiKey } : {})
  })
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
    selectedApiKeyFingerprint: selectedApiKeyEntry?.fingerprint,
    selectedApiKeyIndex: selectedApiKeyEntry?.index,
    refreshToken: stringCredential(credentials.refresh_token) || undefined,
    clientId: stringCredential(credentials.client_id) || undefined,
    proxyProfileId: draft.proxyProfileId,
    proxyUrl: proxy.proxyUrl,
    proxyProfileUnavailable: proxy.unavailable,
    proxyProfileErrorMessage: proxy.errorMessage,
    streamFailureCount: 0,
    accountExpiresAt: draft.accountExpiresAt,
    expiresAt: stringCredential(credentials.expires_at) || undefined,
    credentials: runtimeCredentials
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

function accountTestPrecheckSystemAccountId(account: AccountSummary): string | undefined {
  return account.accessType === 'authorized'
    ? account.bindingSystemAccountId
    : account.ownerSystemAccountId ?? account.systemAccountId
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
