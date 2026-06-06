import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger, errorLogFields } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  accountTestUnavailableMessage,
  clearAuthorizedAccountBindingFailureState,
  findAccountForTest,
  findRecentOpenAIRequestShapeForAccount,
  markAccountTestTemporaryUnavailable,
  recordAccountSuccessfulTestModel
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  cleanupExpiredAccountTestTasks,
  completeAccountTestTask,
  failAccountTestTask,
  getAccountTestTaskRecord,
  isAccountTestTaskCancelRequested,
  markAccountTestTaskCanceled,
  markAccountTestTaskRunning,
  requeueInterruptedAccountTestTasks
} from '../../storage/account-test-tasks.repository.js'
import { sendAccountRuntimeClearToServer, sendAccountTestCancelToWorker, sendAccountTestTasksToWorker } from '../background/background-ipc.js'
import { operationMode, recordOperationLog, resolveOperationOwner, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import { testOpenAIAccount } from './account-test.service.js'

interface AccountTestQueueItem {
  taskId: string
}

const manualAccountTestConcurrency = 3
const manualAccountTestRetryPolicy = sequenceRetryPolicy('manual_account_test', [], 0)
const runningAccountTestControllers = new Map<string, AbortController>()

const manualAccountTestQueue = createRetryQueue<AccountTestQueueItem>({
  name: 'manual-account-test',
  policy: manualAccountTestRetryPolicy,
  concurrency: manualAccountTestConcurrency,
  run: runAccountTestQueueItem,
  onExhausted: (event) => {
    failAccountTestTask(event.item.taskId, event.error instanceof Error ? event.error.message : '账号测试任务执行失败')
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
  const taskIds = requeueInterruptedAccountTestTasks()
  for (const taskId of taskIds) {
    enqueueAccountTestTaskLocal(taskId)
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

  try {
    if (isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, '已停止测试')
      return true
    }

    const account = findAccountForTest(task.accountId, access)
    if (!account) {
      failAccountTestTask(task.id, '账户不存在')
      return true
    }
    if (account.providerCode !== 'openai') {
      failAccountTestTask(task.id, '当前仅支持测试 OpenAI 账户', failedAccountTestResult(account, task.message ?? '当前仅支持测试 OpenAI 账户', task.model))
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
      signal: controller.signal
    })

    if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, '已停止测试')
      return true
    }

    completeAccountTestTask(task.id, result)
    return true
  } catch (error) {
    if (controller.signal.aborted || isAccountTestTaskCancelRequested(task.id)) {
      markAccountTestTaskCanceled(task.id, '已停止测试')
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

async function runOpenAIAccountTestWithSideEffects(
  account: AccountSummary,
  access: AccessScope,
  input: {
    model?: string
    clientCompatibility?: AccountSummary['clientCompatibility']
    diagnostics: 'full' | 'limited'
    signal: AbortSignal
  }
): Promise<AccountTestResult> {
  let accountTestStatusChanges: ReturnType<typeof safeChange>[] | undefined
  let result = await testOpenAIAccount(account, {
    model: input.model,
    clientCompatibility: input.clientCompatibility,
    signal: input.signal,
    diagnostics: input.diagnostics,
    requestShape: findRecentOpenAIRequestShapeForAccount(account.id, account.boundGroupId)
  })

  if (input.signal.aborted) {
    return result
  }

  if (result.success && shouldClearAuthorizedAccountTestInstanceFailure(account)) {
    const restored = clearAuthorizedAccountBindingFailureState(account.id, access)
    if (restored.changed && restored.account) {
      accountTestStatusChanges = accountTestStatusLogChanges(account, restored.account)
      result = {
        ...result,
        accountStatusChanged: accountTestStatusChanges.length > 0,
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

function shouldClearAuthorizedAccountTestInstanceFailure(account: AccountSummary): boolean {
  if (account.accessType !== 'authorized') return false
  if (account.status === 'disabled') return false
  return Boolean(
    account.status !== 'active'
    || account.cooldownUntil
    || account.lastErrorMessage
  )
}

function accountTestStatusLogChanges(before: AccountSummary, after: AccountSummary): ReturnType<typeof safeChange>[] {
  const changes: ReturnType<typeof safeChange>[] = []
  if (before.status !== after.status) {
    changes.push(safeChange('status', '状态', before.status, after.status))
  }
  if ((before.cooldownUntil ?? null) !== (after.cooldownUntil ?? null)) {
    changes.push(safeChange('cooldownUntil', before.accessType === 'authorized' || after.accessType === 'authorized' ? '实例冷却结束时间' : '冷却结束时间', before.cooldownUntil, after.cooldownUntil))
  }
  if ((before.lastErrorMessage ?? null) !== (after.lastErrorMessage ?? null)) {
    changes.push(safeChange('lastErrorMessage', before.accessType === 'authorized' || after.accessType === 'authorized' ? '实例错误信息' : '错误信息', before.lastErrorMessage, after.lastErrorMessage))
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

function accountTestFailureCooldownReason(result: { statusCode?: number; errorCode?: string; message?: string }): string {
  const parts = ['账户测试失败，已自动标记为临时不可调用']
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

function authorizedLocalOperationOwner(account: AccountSummary, access?: AccessScope): string | undefined {
  return account.accessType === 'authorized' ? effectiveRequestSystemAccountId(access) : undefined
}

function effectiveRequestSystemAccountId(access?: AccessScope): string | undefined {
  return access?.systemAccountFilterId?.trim() || access?.systemAccountId
}

function failedAccountTestResult(account: AccountSummary, message: string, model?: string): AccountTestResult {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    type: account.type,
    success: false,
    message,
    model,
    accountStatus: account.status,
    accountFailureEligible: false
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

function normalizedString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}
