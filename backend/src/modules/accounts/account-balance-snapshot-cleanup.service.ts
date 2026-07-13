import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue, type RetryQueueSnapshot } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy, type RetryPolicy } from '../../shared/retry-policy.js'
import { deleteAccountBalanceSnapshotAsync } from '../../storage/account-balance.repository.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'

export type AccountBalanceSnapshotCleanupReason = 'balance_configuration_changed' | 'multiple_api_keys' | 'batch_multiple_api_keys'

export interface AccountBalanceSnapshotCleanupRequest {
  accountId: string
  configRevision: number
  reason: AccountBalanceSnapshotCleanupReason
  batchId?: string
}

interface AccountBalanceSnapshotCleanupQueueItem extends AccountBalanceSnapshotCleanupRequest {
  requestId: string
  updatedBefore: string
}

export interface AccountBalanceSnapshotCleanupRuntime extends RetryQueueSnapshot {
  suppressedAccountCount: number
  exhaustedAccountCount: number
  completedCount: number
  failedAttemptCount: number
  exhaustedCount: number
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
}

interface AccountBalanceSnapshotCleanupCoordinatorOptions {
  deleteSnapshot: (item: AccountBalanceSnapshotCleanupQueueItem) => Promise<void>
  retryPolicy?: RetryPolicy
  onLog?: (level: 'info' | 'warn', event: Record<string, unknown>, message: string, error?: unknown) => void
  now?: () => string
}

export interface AccountBalanceSnapshotCleanupCoordinator {
  cleanupAfterSave(request: AccountBalanceSnapshotCleanupRequest): Promise<void>
  isSuppressed(accountId: string): boolean
  snapshot(): AccountBalanceSnapshotCleanupRuntime
  clearForTest(): void
}

const cleanupRetryPolicy = sequenceRetryPolicy('account_balance_snapshot_cleanup', [250, 1000], 2)
let cleanupRequestSequence = 0

export function createAccountBalanceSnapshotCleanupCoordinator(
  options: AccountBalanceSnapshotCleanupCoordinatorOptions
): AccountBalanceSnapshotCleanupCoordinator {
  const suppressedRequestIds = new Map<string, string>()
  const exhaustedAccountIds = new Set<string>()
  let completedCount = 0
  let failedAttemptCount = 0
  let exhaustedCount = 0
  let lastSuccessAt: string | undefined
  let lastErrorAt: string | undefined
  let lastError: string | undefined
  const now = options.now ?? (() => new Date().toISOString())
  const log = options.onLog ?? (() => undefined)

  const retryQueue = createRetryQueue<AccountBalanceSnapshotCleanupQueueItem>({
    name: 'account-balance-snapshot-cleanup',
    policy: options.retryPolicy ?? cleanupRetryPolicy,
    concurrency: 2,
    run: async (item) => {
      await options.deleteSnapshot(item)
      return true
    },
    onSuccess: (event) => {
      if (suppressedRequestIds.get(event.item.accountId) !== event.item.requestId) return
      suppressedRequestIds.delete(event.item.accountId)
      exhaustedAccountIds.delete(event.item.accountId)
      completedCount += 1
      lastSuccessAt = now()
      lastError = undefined
      log('info', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_succeeded',
        attemptCount: event.attemptIndex + 1,
        ...runtimeFields(retryQueue.snapshot(), suppressedRequestIds.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照重试清理成功')
    },
    onFailure: (event) => {
      failedAttemptCount += 1
      lastErrorAt = now()
      lastError = errorText(event.error)
      log('warn', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_failed',
        attemptCount: event.attemptIndex + 1,
        ...runtimeFields(retryQueue.snapshot(), suppressedRequestIds.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照重试清理失败', event.error)
    },
    onRetryScheduled: (event) => {
      log('warn', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_scheduled',
        attemptCount: event.attemptIndex + 1,
        delayMs: event.delayMs,
        nextAttemptAt: new Date(event.nextAttemptAtMs).toISOString(),
        ...runtimeFields(retryQueue.snapshot(), suppressedRequestIds.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照已安排有限重试', event.error)
    },
    onExhausted: (event) => {
      if (suppressedRequestIds.get(event.item.accountId) === event.item.requestId) {
        exhaustedAccountIds.add(event.item.accountId)
      }
      exhaustedCount += 1
      lastErrorAt = now()
      lastError = errorText(event.error)
      log('warn', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_exhausted',
        attemptCount: event.attemptIndex + 1,
        staleSnapshotSuppressed: true,
        ...runtimeFields(retryQueue.snapshot(), suppressedRequestIds.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照清理已用尽重试，继续屏蔽旧快照', event.error)
    }
  })

  return {
    cleanupAfterSave: async (request) => {
      const item: AccountBalanceSnapshotCleanupQueueItem = {
        ...request,
        requestId: `${request.accountId}:${request.configRevision}:${Date.now()}:${cleanupRequestSequence += 1}`,
        updatedBefore: now()
      }
      suppressedRequestIds.set(request.accountId, item.requestId)
      exhaustedAccountIds.delete(request.accountId)
      try {
        await options.deleteSnapshot(item)
        retryQueue.delete(request.accountId)
        if (suppressedRequestIds.get(request.accountId) === item.requestId) {
          suppressedRequestIds.delete(request.accountId)
          completedCount += 1
          lastSuccessAt = now()
          lastError = undefined
        }
      } catch (error) {
        failedAttemptCount += 1
        lastErrorAt = now()
        lastError = errorText(error)
        retryQueue.enqueue(request.accountId, item, { replaceExisting: true })
        log('warn', cleanupLogFields(item, {
          event: 'account_balance_snapshot_cleanup_initial_failed',
          ...runtimeFields(retryQueue.snapshot(), suppressedRequestIds.size, exhaustedAccountIds.size)
        }), 'AI 账户保存已提交，余额旧快照清理失败并已加入有限重试', error)
      }
    },
    isSuppressed: (accountId) => suppressedRequestIds.has(accountId),
    snapshot: () => ({
      ...retryQueue.snapshot(),
      suppressedAccountCount: suppressedRequestIds.size,
      exhaustedAccountCount: exhaustedAccountIds.size,
      completedCount,
      failedAttemptCount,
      exhaustedCount,
      lastSuccessAt,
      lastErrorAt,
      lastError
    }),
    clearForTest: () => {
      retryQueue.clear()
      suppressedRequestIds.clear()
      exhaustedAccountIds.clear()
      completedCount = 0
      failedAttemptCount = 0
      exhaustedCount = 0
      lastSuccessAt = undefined
      lastErrorAt = undefined
      lastError = undefined
    }
  }
}

const accountBalanceSnapshotCleanupCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  deleteSnapshot: async (item) => {
    if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly) {
      await deleteAccountBalanceSnapshotAsync(item.accountId, { updatedBefore: item.updatedBefore })
      return
    }
    await requestStatsWriter({
      type: 'delete_account_balance_snapshot',
      accountId: item.accountId,
      updatedBefore: item.updatedBefore
    })
  },
  onLog: (level, event, message, error) => {
    if (level === 'info') {
      logger.info(event, message)
      return
    }
    logger.warn(errorLogFields(error, event), message)
  }
})

export async function cleanupAccountBalanceSnapshotAfterSave(
  request: AccountBalanceSnapshotCleanupRequest
): Promise<void> {
  await accountBalanceSnapshotCleanupCoordinator.cleanupAfterSave(request)
}

export function isAccountBalanceSnapshotSuppressed(accountId: string): boolean {
  return accountBalanceSnapshotCleanupCoordinator.isSuppressed(accountId)
}

export function getAccountBalanceSnapshotCleanupRuntime(): AccountBalanceSnapshotCleanupRuntime {
  return accountBalanceSnapshotCleanupCoordinator.snapshot()
}

function cleanupLogFields(
  item: AccountBalanceSnapshotCleanupQueueItem,
  fields: Record<string, unknown>
): Record<string, unknown> {
  return {
    ...fields,
    accountId: item.accountId,
    configRevision: item.configRevision,
    cleanupReason: item.reason,
    batchId: item.batchId,
    updatedBefore: item.updatedBefore
  }
}

function runtimeFields(queue: RetryQueueSnapshot, suppressedAccountCount: number, exhaustedAccountCount: number) {
  return {
    retryQueuePendingCount: queue.pendingCount,
    retryQueueRunningCount: queue.runningCount,
    retryQueueNextRunAt: queue.nextRunAt,
    suppressedAccountCount,
    exhaustedAccountCount
  }
}

function errorText(error: unknown): string {
  return (error instanceof Error ? error.message : String(error || '余额快照清理失败')).slice(0, 1024)
}
