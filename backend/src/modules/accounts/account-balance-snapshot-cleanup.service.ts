import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue, type RetryQueueSnapshot } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy, type RetryPolicy } from '../../shared/retry-policy.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  deleteAccountBalanceSnapshotAsync,
  type AccountBalanceSnapshotRecord
} from '../../storage/account-balance.repository.js'
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

export interface AccountBalanceSnapshotSuppressionRead {
  configuration: { nextRefreshAt?: string }
  snapshotRecord?: AccountBalanceSnapshotRecord
}

export interface AccountBalanceSnapshotCleanupCoordinator {
  cleanupAfterSave(request: AccountBalanceSnapshotCleanupRequest): void
  isSuppressed(accountId: string, current?: AccountBalanceSnapshotSuppressionRead): boolean
  snapshot(): AccountBalanceSnapshotCleanupRuntime
  clearForTest(): void
}

const cleanupRetryPolicy = sequenceRetryPolicy('account_balance_snapshot_cleanup', [250, 1000, 1000], 3)
let cleanupRequestSequence = 0

export function createAccountBalanceSnapshotCleanupCoordinator(
  options: AccountBalanceSnapshotCleanupCoordinatorOptions
): AccountBalanceSnapshotCleanupCoordinator {
  const suppressedItems = new Map<string, AccountBalanceSnapshotCleanupQueueItem>()
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
      if (suppressedItems.get(event.item.accountId)?.requestId !== event.item.requestId) return
      suppressedItems.delete(event.item.accountId)
      exhaustedAccountIds.delete(event.item.accountId)
      completedCount += 1
      lastSuccessAt = now()
      lastError = undefined
      log('info', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_succeeded',
        attemptCount: event.attemptIndex + 1,
        ...runtimeFields(retryQueue.snapshot(), suppressedItems.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照重试清理成功')
    },
    onFailure: (event) => {
      failedAttemptCount += 1
      lastErrorAt = now()
      lastError = errorText(event.error)
      log('warn', cleanupLogFields(event.item, {
        event: event.attemptIndex === 0
          ? 'account_balance_snapshot_cleanup_initial_failed'
          : 'account_balance_snapshot_cleanup_retry_failed',
        attemptCount: event.attemptIndex + 1,
        ...runtimeFields(retryQueue.snapshot(), suppressedItems.size, exhaustedAccountIds.size)
      }), event.attemptIndex === 0
        ? 'AI 账户保存已提交，余额旧快照首次清理失败并已安排有限重试'
        : 'AI 账户余额旧快照重试清理失败', event.error)
    },
    onRetryScheduled: (event) => {
      log('warn', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_scheduled',
        attemptCount: event.attemptIndex + 1,
        delayMs: event.delayMs,
        nextAttemptAt: new Date(event.nextAttemptAtMs).toISOString(),
        ...runtimeFields(retryQueue.snapshot(), suppressedItems.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照已安排有限重试', event.error)
    },
    onExhausted: (event) => {
      if (suppressedItems.get(event.item.accountId)?.requestId === event.item.requestId) {
        exhaustedAccountIds.add(event.item.accountId)
      }
      exhaustedCount += 1
      lastErrorAt = now()
      lastError = errorText(event.error)
      log('warn', cleanupLogFields(event.item, {
        event: 'account_balance_snapshot_cleanup_retry_exhausted',
        attemptCount: event.attemptIndex + 1,
        staleSnapshotSuppressed: true,
        ...runtimeFields(retryQueue.snapshot(), suppressedItems.size, exhaustedAccountIds.size)
      }), 'AI 账户余额旧快照清理已用尽重试，继续屏蔽旧快照', event.error)
    }
  })

  return {
    cleanupAfterSave: (request) => {
      const item: AccountBalanceSnapshotCleanupQueueItem = {
        ...request,
        requestId: `${request.accountId}:${request.configRevision}:${Date.now()}:${cleanupRequestSequence += 1}`,
        updatedBefore: now()
      }
      suppressedItems.set(request.accountId, item)
      exhaustedAccountIds.delete(request.accountId)
      retryQueue.enqueue(request.accountId, item, { replaceExisting: true })
    },
    isSuppressed: (accountId, current) => {
      const item = suppressedItems.get(accountId)
      if (!item) return false
      if (!current || !snapshotSupersedesCleanup(item, current)) return true
      if (suppressedItems.get(accountId)?.requestId === item.requestId) {
        retryQueue.delete(accountId)
        suppressedItems.delete(accountId)
        exhaustedAccountIds.delete(accountId)
        completedCount += 1
        lastSuccessAt = now()
        lastError = undefined
        log('info', cleanupLogFields(item, {
          event: 'account_balance_snapshot_cleanup_superseded_by_current_snapshot',
          snapshotUpdatedAt: current.snapshotRecord?.updatedAt,
          snapshotNextRefreshAfter: current.snapshotRecord?.nextRefreshAfter,
          ...runtimeFields(retryQueue.snapshot(), suppressedItems.size, exhaustedAccountIds.size)
        }), 'AI 账户已生成当前刷新代次的新余额快照，旧快照清理抑制已解除')
      }
      return false
    },
    snapshot: () => ({
      ...retryQueue.snapshot(),
      suppressedAccountCount: suppressedItems.size,
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
      suppressedItems.clear()
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

export function cleanupAccountBalanceSnapshotAfterSave(
  request: AccountBalanceSnapshotCleanupRequest
): void {
  accountBalanceSnapshotCleanupCoordinator.cleanupAfterSave(request)
}

export function isAccountBalanceSnapshotSuppressed(
  accountId: string,
  current?: AccountBalanceSnapshotSuppressionRead
): boolean {
  return accountBalanceSnapshotCleanupCoordinator.isSuppressed(accountId, current)
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

function snapshotSupersedesCleanup(
  item: AccountBalanceSnapshotCleanupQueueItem,
  current: AccountBalanceSnapshotSuppressionRead
): boolean {
  const snapshotRecord = current.snapshotRecord
  if (!snapshotRecord || !accountBalanceSnapshotMatchesConfiguration(current.configuration, snapshotRecord)) {
    return false
  }
  const snapshotUpdatedAtMs = Date.parse(snapshotRecord.updatedAt)
  const cleanupCutoffMs = Date.parse(item.updatedBefore)
  return Number.isFinite(snapshotUpdatedAtMs)
    && Number.isFinite(cleanupCutoffMs)
    && snapshotUpdatedAtMs >= cleanupCutoffMs
}
