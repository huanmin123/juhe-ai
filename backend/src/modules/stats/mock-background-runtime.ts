import { runtimeConfig } from '../../config/runtime.js'
import { getBusinessDatabase, getDatasetDatabase, getStatsDatabase, nowIso } from '../../storage/database.js'
import type { DbServiceRuntimeQueueSnapshot, DbServiceServerRuntimeSnapshot } from '../db-service/db-service-types.js'

const mockdataIdPrefix = 'mockdata_%'
const mockdataNamePrefix = '造数-%'

export function loadMockBackgroundRuntimeSnapshot(): DbServiceServerRuntimeSnapshot | undefined {
  if (runtimeConfig.databaseDriver !== 'sqlite' || runtimeConfig.runtimeMode === 'performance') return undefined
  if (!mockdataBackgroundRunsAvailable()) return undefined
  const now = new Date(nowIso())
  const backgroundRunCounts = backgroundTaskRunCounts()
  const accountTestCounts = accountTestTaskCounts()
  const accountStatusCounts = mockAccountStatusCounts()
  const datasetCounts = datasetMaintenanceCounts()
  const statsQueueCounts = statsMaintenanceCounts()
  const completedRuns = countValue(backgroundRunCounts, 'completed')
  const runningRuns = countValue(backgroundRunCounts, 'running')
  const failedRuns = countValue(backgroundRunCounts, 'failed')
  const accountProblemCount = accountStatusCounts.error + accountStatusCounts.rateLimited + accountStatusCounts.temporaryUnavailable
  return {
    ingestWorker: {
      pid: 9101,
      ready: true,
      pendingMessageCount: datasetCounts.accountCleanupTargets + datasetCounts.apiKeyCleanupTargets,
      pendingMessageBytes: 88_000,
      pendingQueues: {
        usageRecords: queue({
          queueLength: 18,
          queueBytes: 176_000,
          completedCount: 3729,
          oldestQueuedMs: 42_000,
          flushLastSuccessAt: minutesAgo(now, 2)
        }),
        auditLogs: queue({
          queueLength: 6,
          queueBytes: 72_000,
          completedCount: 933,
          oldestQueuedMs: 18_000,
          flushLastSuccessAt: minutesAgo(now, 3)
        }),
        operationLogs: queue({
          queueLength: 3,
          queueBytes: 24_000,
          completedCount: 180,
          oldestQueuedMs: 12_000,
          flushLastSuccessAt: minutesAgo(now, 3)
        }),
        publicApiLogs: queue({
          queueLength: 4,
          queueBytes: 36_000,
          completedCount: 372,
          oldestQueuedMs: 15_000,
          flushLastSuccessAt: minutesAgo(now, 4)
        }),
        recordMaintenance: queue({
          queueLength: datasetCounts.accountCleanupTargets + datasetCounts.apiKeyCleanupTargets,
          queueBytes: (datasetCounts.accountCleanupTargets + datasetCounts.apiKeyCleanupTargets) * 2400,
          completedCount: statsQueueCounts.cleanupDeductions,
          oldestQueuedMs: 56_000,
          flushLastSuccessAt: minutesAgo(now, 5)
        }),
        runtimeLogLines: queue({
          queueLength: datasetCounts.runtimeLogCursors,
          queueBytes: datasetCounts.runtimeLogCursors * 1600,
          completedCount: 240,
          oldestQueuedMs: 9000,
          flushLastSuccessAt: minutesAgo(now, 1)
        })
      },
      pendingWriteRequestCount: 2,
      oldestPendingWriteMs: 24_000,
      snapshot: {
        pid: 9101,
        ready: true,
        workerRole: 'ingest-worker',
        jobs: [
          job('runtime-log-index-refresh', 'ingest-worker', 60_000, {
            running: runningRuns > 0,
            successCount: completedRuns + datasetCounts.runtimeLogCursors,
            failureCount: failedRuns,
            skippedCount: 1,
            lastDurationMs: 820,
            maxDurationMs: 1840,
            lastSuccessAt: minutesAgo(now, 1)
          }),
          job('api-key-record-cleanup-retry', 'ingest-worker', 10 * 60_000, {
            successCount: datasetCounts.apiKeyCleanupTargets,
            failureCount: 0,
            lastDurationMs: 1240,
            maxDurationMs: 2320,
            lastSuccessAt: minutesAgo(now, 8)
          })
        ],
        usageRecordQueue: queue({
          queueLength: 18,
          queueBytes: 176_000,
          completedCount: 3729,
          oldestQueuedMs: 42_000,
          flushLastSuccessAt: minutesAgo(now, 2)
        }),
        operationLogQueue: queue({
          queueLength: 3,
          queueBytes: 24_000,
          completedCount: 180,
          oldestQueuedMs: 12_000,
          flushLastSuccessAt: minutesAgo(now, 3)
        }),
        publicApiLogQueue: queue({
          queueLength: 4,
          queueBytes: 36_000,
          completedCount: 372,
          oldestQueuedMs: 15_000,
          flushLastSuccessAt: minutesAgo(now, 4)
        }),
        auditLogQueue: queue({
          queueLength: 6,
          queueBytes: 72_000,
          completedCount: 933,
          droppedCount: 1,
          droppedSuccessCount: 1,
          droppedFailureCount: 0,
          oldestQueuedMs: 18_000,
          flushLastSuccessAt: minutesAgo(now, 3)
        }),
        recordMaintenanceQueue: queue({
          queueLength: datasetCounts.accountCleanupTargets + datasetCounts.apiKeyCleanupTargets,
          queueBytes: (datasetCounts.accountCleanupTargets + datasetCounts.apiKeyCleanupTargets) * 2400,
          completedCount: statsQueueCounts.cleanupDeductions,
          oldestQueuedMs: 56_000,
          flushLastSuccessAt: minutesAgo(now, 5)
        }),
        runtimeLogIndexQueue: queue({
          queueLength: datasetCounts.runtimeLogCursors,
          queueBytes: datasetCounts.runtimeLogCursors * 1600,
          completedCount: 240,
          oldestQueuedMs: 9000,
          flushLastSuccessAt: minutesAgo(now, 1),
          retentionDays: 31
        })
      }
    },
    statsWorker: {
      pid: 9102,
      ready: true,
      pendingWriteRequestCount: Math.max(1, statsQueueCounts.cleanupDeductions),
      oldestPendingWriteMs: 38_000,
      snapshot: {
        pid: 9102,
        ready: true,
        workerRole: 'stats-worker',
        jobs: [
          job('usage-stats-aggregation', 'stats-worker', 30_000, {
            running: runningRuns > 0,
            successCount: completedRuns + 31,
            failureCount: failedRuns,
            skippedCount: 2,
            lastDurationMs: 1380,
            maxDurationMs: 4260,
            lastSuccessAt: minutesAgo(now, 2)
          }),
          job('table-storage-monitor', 'stats-worker', 10 * 60_000, {
            successCount: 5,
            failureCount: 0,
            lastDurationMs: 960,
            maxDurationMs: 1510,
            lastSuccessAt: minutesAgo(now, 4)
          })
        ],
        recordMaintenanceQueue: queue({
          queueLength: statsQueueCounts.accountQualityDirty + statsQueueCounts.clientIpDirty + statsQueueCounts.clientIpAccountDirty + statsQueueCounts.cleanupDeductions,
          queueBytes: 44_000,
          completedCount: completedRuns + statsQueueCounts.cleanupDeductions,
          oldestQueuedMs: 38_000,
          flushLastSuccessAt: minutesAgo(now, 2),
          writerPoolEnabled: true,
          writerPoolWorkerCount: 2,
          writerPoolQueueLength: statsQueueCounts.cleanupDeductions,
          writerPoolActiveJobs: runningRuns,
          writerPoolHandledJobs: completedRuns + 12,
          writerPoolFailedJobs: failedRuns,
          writerPoolRejectedJobs: 0,
          writerPoolOldestQueuedMs: 21_000
        }),
        accountQualityFailurePrecheckQueue: {
          name: 'account-quality-failure-precheck-queue',
          pendingCount: accountProblemCount,
          runningCount: accountStatusCounts.temporaryUnavailable > 0 ? 1 : 0,
          nextRunAt: minutesFrom(now, 4)
        }
      }
    },
    opsWorker: {
      pid: 9103,
      ready: true,
      pendingMessageCount: accountTestCounts.failed + accountTestCounts.canceled,
      pendingMessageBytes: 32_000,
      pendingQueues: {
        accountTests: queue({
          queueLength: accountTestCounts.failed + accountTestCounts.canceled,
          queueBytes: 32_000,
          completedCount: accountTestCounts.success,
          droppedCount: accountTestCounts.canceled,
          oldestQueuedMs: 64_000,
          flushLastSuccessAt: minutesAgo(now, 6)
        })
      },
      snapshot: {
        pid: 9103,
        ready: true,
        workerRole: 'ops-worker',
        jobs: [
          job('account-health-check', 'ops-worker', 5 * 60_000, {
            running: accountStatusCounts.error > 0,
            successCount: accountStatusCounts.active,
            failureCount: accountStatusCounts.error,
            lastDurationMs: 1640,
            maxDurationMs: 4920,
            lastSuccessAt: minutesAgo(now, 6)
          }),
          job('cooldown-account-retest', 'ops-worker', 30_000, {
            running: accountStatusCounts.temporaryUnavailable > 0,
            successCount: accountStatusCounts.rateLimited,
            failureCount: accountStatusCounts.temporaryUnavailable,
            lastDurationMs: 1180,
            maxDurationMs: 3480,
            lastSuccessAt: minutesAgo(now, 3)
          }),
          job('account-api-key-cooldown-retest', 'ops-worker', 30_000, {
            successCount: Math.max(1, accountStatusCounts.rateLimited),
            failureCount: 0,
            lastDurationMs: 740,
            maxDurationMs: 1220,
            lastSuccessAt: minutesAgo(now, 5)
          })
        ],
        accountHealthCheckQueue: {
          name: 'account-health-check-queue',
          pendingCount: accountStatusCounts.error,
          runningCount: accountStatusCounts.error > 0 ? 1 : 0,
          nextRunAt: minutesFrom(now, 3)
        },
        cooldownAccountRetestQueue: {
          name: 'cooldown-account-retest-queue',
          pendingCount: accountStatusCounts.rateLimited + accountStatusCounts.temporaryUnavailable,
          runningCount: accountStatusCounts.temporaryUnavailable > 0 ? 1 : 0,
          nextRunAt: minutesFrom(now, 2)
        },
        accountApiKeyCooldownRetestQueue: {
          name: 'account-api-key-cooldown-retest-queue',
          pendingCount: Math.max(1, accountStatusCounts.rateLimited),
          runningCount: 0,
          nextRunAt: minutesFrom(now, 6)
        },
        manualAccountTestQueue: {
          name: 'manual-account-test-queue',
          pendingCount: accountTestCounts.failed + accountTestCounts.canceled,
          runningCount: accountTestCounts.running,
          nextRunAt: minutesFrom(now, 1)
        },
        accountQualityFailurePrecheckQueue: {
          name: 'account-quality-failure-precheck-queue',
          pendingCount: accountProblemCount,
          runningCount: accountStatusCounts.error > 0 ? 1 : 0,
          nextRunAt: minutesFrom(now, 5)
        }
      }
    },
    dbService: {
      pid: 9100,
      ready: true,
      pendingRequestCount: 4,
      pendingDatasetWriteRequestCount: datasetCounts.runtimeLogCursors,
      oldestDatasetWriteRequestMs: 27_000,
      timedOutDatasetWriteRequestCount: 0,
      rejectedDatasetWriteRequestCount: 0,
      timedOutRequestCount: 0,
      rejectedRequestCount: 1,
      failedRequestCount: failedRuns,
      queuedRequestCount: 4,
      queuedRequestBytes: 96_000,
      queuedHighRequestCount: 1,
      queuedNormalRequestCount: 2,
      queuedLowRequestCount: 1,
      oldestQueuedMs: 31_000,
      activeConcurrentRequestCount: Math.max(1, runningRuns),
      maxActiveConcurrentRequestCount: 4,
      lastExecMs: 42,
      maxExecMs: 420,
      slowOpCount: 1,
      lastSlowOpType: 'mockdata.table_monitor.overview',
      lastSlowOpMs: 420,
      lastSlowOpAt: minutesAgo(now, 3),
      pendingProcessEventLoopRequestCount: 1,
      timedOutProcessEventLoopRequestCount: 0,
      failedProcessEventLoopRequestCount: 0,
      pendingServerRuntimeRequestCount: 1,
      timedOutServerRuntimeRequestCount: 0,
      failedServerRuntimeRequestCount: 0
    },
    highConcurrencyQueues: [
      {
        groupKey: 'mockdata-high-concurrency',
        lane: 'default',
        queueSize: 7,
        perApiKeyQueueSize: {
          'mockdata-admin-main': 4,
          'mockdata-admin-burst': 3
        }
      }
    ],
    gatewayAccountSideEffects: {
      queueLength: accountProblemCount,
      completedCount: completedRuns + accountTestCounts.success,
      droppedCount: 0,
      expiredCount: accountTestCounts.canceled,
      failedAttemptCount: accountTestCounts.failed + failedRuns,
      processing: accountProblemCount > 0,
      nextAttemptAt: minutesFrom(now, 2),
      precheckPendingAccountCount: accountStatusCounts.temporaryUnavailable,
      degradedAccountCount: accountStatusCounts.error,
      localSuppressedAccountCount: accountStatusCounts.rateLimited
    }
  }
}

function mockdataBackgroundRunsAvailable(): boolean {
  return scalar(getStatsDatabase(), 'SELECT COUNT(*) FROM background_task_runs WHERE run_id LIKE ?', mockdataIdPrefix) > 0
}

function backgroundTaskRunCounts(): Record<string, number> {
  const rows = getStatsDatabase().prepare(`
    SELECT status, COUNT(*) AS total
    FROM background_task_runs
    WHERE run_id LIKE ?
    GROUP BY status
  `).all(mockdataIdPrefix) as Array<{ status?: string; total?: number }>
  return Object.fromEntries(rows.map((row) => [row.status ?? '', Number(row.total ?? 0)]))
}

function accountTestTaskCounts(): { success: number; failed: number; canceled: number; running: number } {
  const database = getBusinessDatabase()
  return {
    success: scalar(database, "SELECT COUNT(*) FROM account_test_tasks WHERE account_name LIKE ? AND status = 'success'", mockdataNamePrefix),
    failed: scalar(database, "SELECT COUNT(*) FROM account_test_tasks WHERE account_name LIKE ? AND status = 'failed'", mockdataNamePrefix),
    canceled: scalar(database, "SELECT COUNT(*) FROM account_test_tasks WHERE account_name LIKE ? AND status = 'canceled'", mockdataNamePrefix),
    running: scalar(database, "SELECT COUNT(*) FROM account_test_tasks WHERE account_name LIKE ? AND status = 'running'", mockdataNamePrefix)
  }
}

function mockAccountStatusCounts(): { active: number; error: number; rateLimited: number; temporaryUnavailable: number } {
  const database = getBusinessDatabase()
  return {
    active: scalar(database, "SELECT COUNT(*) FROM accounts WHERE name LIKE ? AND status = 'active'", mockdataNamePrefix),
    error: scalar(database, "SELECT COUNT(*) FROM accounts WHERE name LIKE ? AND status = 'error'", mockdataNamePrefix),
    rateLimited: scalar(database, "SELECT COUNT(*) FROM accounts WHERE name LIKE ? AND status = 'rate_limited'", mockdataNamePrefix),
    temporaryUnavailable: scalar(database, "SELECT COUNT(*) FROM accounts WHERE name LIKE ? AND status = 'temporary_unavailable'", mockdataNamePrefix)
  }
}

function datasetMaintenanceCounts(): { accountCleanupTargets: number; apiKeyCleanupTargets: number; runtimeLogCursors: number } {
  const database = getDatasetDatabase()
  return {
    accountCleanupTargets: scalar(database, 'SELECT COUNT(*) FROM account_record_cleanup_targets'),
    apiKeyCleanupTargets: scalar(database, 'SELECT COUNT(*) FROM api_key_record_cleanup_targets'),
    runtimeLogCursors: scalar(database, 'SELECT COUNT(*) FROM runtime_log_file_cursors WHERE log_file LIKE ?', mockdataIdPrefix)
  }
}

function statsMaintenanceCounts(): { accountQualityDirty: number; clientIpDirty: number; clientIpAccountDirty: number; cleanupDeductions: number } {
  const database = getStatsDatabase()
  return {
    accountQualityDirty: scalar(database, 'SELECT COUNT(*) FROM account_quality_dirty_accounts'),
    clientIpDirty: scalar(database, 'SELECT COUNT(*) FROM client_ip_range_window_dirty_ips'),
    clientIpAccountDirty: scalar(database, 'SELECT COUNT(*) FROM client_ip_account_range_window_dirty_ips'),
    cleanupDeductions: scalar(database, 'SELECT COUNT(*) FROM usage_record_cleanup_deductions WHERE usage_id LIKE ?', mockdataIdPrefix)
  }
}

function queue(input: DbServiceRuntimeQueueSnapshot): DbServiceRuntimeQueueSnapshot {
  return input
}

function job(
  name: string,
  workerRole: string,
  intervalMs: number,
  input: {
    running?: boolean
    successCount?: number
    failureCount?: number
    skippedCount?: number
    lastDurationMs?: number
    maxDurationMs?: number
    lastSuccessAt?: string
  }
) {
  const lastStartedAt = input.lastSuccessAt ? new Date(Date.parse(input.lastSuccessAt) - (input.lastDurationMs ?? 0)).toISOString() : undefined
  return {
    name,
    workerRole,
    intervalMs,
    running: input.running ?? false,
    lastStartedAt,
    lastFinishedAt: input.lastSuccessAt,
    lastSuccessAt: input.lastSuccessAt,
    lastDurationMs: input.lastDurationMs,
    maxDurationMs: input.maxDurationMs,
    runCount: (input.successCount ?? 0) + (input.failureCount ?? 0) + (input.skippedCount ?? 0),
    successCount: input.successCount ?? 0,
    failureCount: input.failureCount ?? 0,
    skippedCount: input.skippedCount ?? 0
  }
}

function countValue(values: Record<string, number>, key: string): number {
  return values[key] ?? 0
}

function scalar(database: ReturnType<typeof getStatsDatabase>, sql: string, ...params: Array<string | number>): number {
  const row = database.prepare(sql).get(...params) as { [key: string]: unknown } | undefined
  const firstValue = row ? Object.values(row)[0] : 0
  const number = typeof firstValue === 'number' ? firstValue : typeof firstValue === 'string' ? Number(firstValue) : 0
  return Number.isFinite(number) ? number : 0
}

function minutesAgo(base: Date, minutes: number): string {
  return new Date(base.getTime() - minutes * 60_000).toISOString()
}

function minutesFrom(base: Date, minutes: number): string {
  return new Date(base.getTime() + minutes * 60_000).toISOString()
}
