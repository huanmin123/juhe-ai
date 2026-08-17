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
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../shared/rfc3339.js'

export type AccountBalanceSnapshotCleanupReason = 'balance_configuration_changed' | 'multiple_api_keys' | 'batch_multiple_api_keys' | 'batch_balance_identity_changed'

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
  const readNow = options.now ?? (() => new Date().toISOString())
  const now = () => requiredRfc3339Instant(readNow(), '余额快照清理 now')
  const log = options.onLog ?? (() => undefined)

  const retryQueue = createRetryQueue<AccountBalanceSnapshotCleanupQueueItem>({
    name: 'account-balance-snapshot-cleanup',
    policy: options.retryPolicy ?? cleanupRetryPolicy,
    concurrency: runtimeConfig.concurrency.globalMax,
    run: async (item) => {
      await runWithGlobalBackgroundConcurrencySlot(async () => await options.deleteSnapshot(item))
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
      const normalizedCurrent = current === undefined ? undefined : normalizeSuppressionRead(current)
      if (!normalizedCurrent || !snapshotSupersedesCleanup(item, normalizedCurrent)) return true
      if (suppressedItems.get(accountId)?.requestId === item.requestId) {
        retryQueue.delete(accountId)
        suppressedItems.delete(accountId)
        exhaustedAccountIds.delete(accountId)
        completedCount += 1
        lastSuccessAt = now()
        lastError = undefined
        log('info', cleanupLogFields(item, {
          event: 'account_balance_snapshot_cleanup_superseded_by_current_snapshot',
          snapshotUpdatedAt: normalizedCurrent.snapshotRecord?.updatedAt,
          snapshotNextRefreshAfter: normalizedCurrent.snapshotRecord?.nextRefreshAfter,
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
  const snapshotUpdatedAtMs = rfc3339InstantMilliseconds(snapshotRecord.updatedAt)
  const cleanupCutoffMs = rfc3339InstantMilliseconds(item.updatedBefore)
  if (snapshotUpdatedAtMs === undefined) {
    throw new Error('余额快照 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  if (cleanupCutoffMs === undefined) {
    throw new Error('余额快照清理 updatedBefore 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return snapshotUpdatedAtMs > cleanupCutoffMs
}

function normalizeSuppressionRead(current: AccountBalanceSnapshotSuppressionRead): AccountBalanceSnapshotSuppressionRead {
  const nextRefreshAt = optionalBalanceTimestamp(current.configuration.nextRefreshAt, '余额快照配置 nextRefreshAt')
  const snapshotRecord = current.snapshotRecord
  const snapshotNextRefreshAfter = snapshotRecord === undefined
    ? undefined
    : optionalBalanceTimestamp(snapshotRecord.nextRefreshAfter, '余额快照 nextRefreshAfter')
  return {
    configuration: {
      ...current.configuration,
      ...(nextRefreshAt === undefined ? {} : { nextRefreshAt })
    },
    ...(snapshotRecord === undefined
      ? {}
      : {
          snapshotRecord: {
            ...snapshotRecord,
            updatedAt: requiredRfc3339Instant(snapshotRecord.updatedAt, '余额快照 updatedAt'),
            ...(snapshotNextRefreshAfter === undefined ? {} : { nextRefreshAfter: snapshotNextRefreshAfter })
          }
        })
  }
}

function optionalBalanceTimestamp(value: unknown, label: string): string | undefined {
  return value === undefined ? undefined : requiredRfc3339Instant(value, label)
}
