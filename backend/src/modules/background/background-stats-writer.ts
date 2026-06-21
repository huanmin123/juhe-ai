import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { ClientIpPolicyHitInput } from '../../storage/client-ip-stats.repository.js'
import {
  aggregateClientIpStatsBatch,
  listActiveClientIpPolicies,
  recordClientIpPolicyHits,
  refreshClientIpUsageRangeWindows
} from '../../storage/client-ip-stats.repository.js'
import type {
  CollectTableStorageSnapshotOptions,
  CollectTableStorageSnapshotResult
} from '../../storage/table-monitor.repository.js'
import { cleanupTableStorageSnapshotsBefore, collectTableStorageSnapshot } from '../../storage/table-monitor.repository.js'
import {
  aggregateUsageStatsBatch,
  checkUsageStatsConsistency,
  insertProcessEventLoopSample,
  insertSystemMetricsSample,
  refreshDirtyGroupAccountStatsCacheWithWriter,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageRankSnapshotsInStages,
  type ProcessEventLoopSampleInput,
  type SystemMetricsSampleInput,
  type UsageRankSnapshotRefreshResult,
  type UsageRankSnapshotStageName
} from '../../storage/usage-stats.repository.js'
import {
  cleanupSystemMetricsBefore,
  cleanupNonBusinessDataBeforeWithResult,
  type NonBusinessDataHardCleanupResult,
  cleanupUsageStatsBucketsBefore
} from '../../storage/data-retention.repository.js'
import {
  cleanupDeletedAccountRecordStatsData,
  type DeletedAccountRecordStatsCleanupInput
} from '../../storage/account-record-cleanup.js'
import {
  cleanupDeletedApiKeyRecordStatsData,
  type DeletedApiKeyRecordStatsCleanupInput
} from '../../storage/api-key-record-cleanup.js'
import {
  listAccountQualityFailurePrecheckCandidates,
  refreshAccountQualityFromUsage,
  upsertAccountUsageSnapshots,
  type AccountUsageSnapshotUpsertInput,
  type AccountQualityFailurePrecheckCandidate,
  type AccountQualityRealtimeRefreshResult
} from '../../storage/repositories.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import {
  requestBackgroundWorkerDbService,
  requestBackgroundWorkerStatsWrite,
  sendClientIpPolicySnapshotToServer,
  sendGatewayQuotaSnapshotToServer
} from './background-ipc.js'
import { buildGatewayQuotaSnapshot } from '../../storage/gateway-quota-snapshot.repository.js'
import { checkpointSqliteWal } from '../../storage/sqlite-maintenance.js'
import { getStatsDatabase } from '../../storage/database.js'

export type BackgroundStatsWriteOperation =
  | {
    type: 'aggregate_usage_stats'
    batchSize: number
    maxBatches: number
  }
  | {
    type: 'aggregate_client_ip_stats'
    batchSize: number
    maxBatches: number
    maxRunMs: number
  }
  | {
    type: 'refresh_group_account_stats'
  }
  | {
    type: 'refresh_account_quality'
    windowMinutes: number
    failureCandidateLimit: number
  }
  | {
    type: 'record_system_metrics_sample'
    sample: SystemMetricsSampleInput
    processEventLoopSamples: ProcessEventLoopSample[]
  }
  | {
    type: 'refresh_usage_rank_snapshots'
    jobName: string
    stageNames: UsageRankSnapshotStageName[]
  }
  | {
    type: 'check_usage_stats_consistency'
    limit: number
  }
  | {
    type: 'collect_table_storage_snapshot'
    sampledAt: string
    options: CollectTableStorageSnapshotOptions
  }
  | {
    type: 'record_client_ip_policy_hits'
    hits: ClientIpPolicyHitInput[]
  }
  | {
    type: 'list_active_client_ip_policies'
  }
  | {
    type: 'upsert_account_usage_snapshots'
    inputs: AccountUsageSnapshotUpsertInput[]
  }
  | {
    type: 'cleanup_usage_stats_retention'
    input: Parameters<typeof cleanupUsageStatsBucketsBefore>[0]
  }
  | {
    type: 'cleanup_system_metrics_retention'
    input: Parameters<typeof cleanupSystemMetricsBefore>[0]
  }
  | {
    type: 'cleanup_table_storage_snapshots_retention'
    cutoffIso: string
    limit: number
  }
  | {
    type: 'cleanup_non_business_stats_data'
    cutoffAt: string
    limit: number
  }
  | {
    type: 'cleanup_deleted_api_key_record_stats'
    input: DeletedApiKeyRecordStatsCleanupInput
  }
  | {
    type: 'cleanup_deleted_account_record_stats'
    input: DeletedAccountRecordStatsCleanupInput
  }

export type BackgroundStatsWriteOperationResult<T extends BackgroundStatsWriteOperation = BackgroundStatsWriteOperation> =
  T extends { type: 'aggregate_usage_stats' } ? { processed: number; quotaSnapshotSent: boolean } :
  T extends { type: 'aggregate_client_ip_stats' } ? { processed: number; policies: ActiveClientIpPolicy[] } :
  T extends { type: 'refresh_group_account_stats' } ? { refreshed: true } :
  T extends { type: 'refresh_account_quality' } ? AccountQualityRealtimeRefreshResult & { failureCandidates: AccountQualityFailurePrecheckCandidate[] } :
  T extends { type: 'record_system_metrics_sample' } ? { recorded: true } :
  T extends { type: 'refresh_usage_rank_snapshots' } ? UsageRankSnapshotRefreshResult :
  T extends { type: 'check_usage_stats_consistency' } ? ReturnType<typeof checkUsageStatsConsistency> :
  T extends { type: 'collect_table_storage_snapshot' } ? CollectTableStorageSnapshotResult :
  T extends { type: 'record_client_ip_policy_hits' } ? { recorded: number } :
  T extends { type: 'list_active_client_ip_policies' } ? ActiveClientIpPolicy[] :
  T extends { type: 'upsert_account_usage_snapshots' } ? { upsertedCount: number } :
  T extends { type: 'cleanup_usage_stats_retention' } ? ReturnType<typeof cleanupUsageStatsBucketsBefore> :
  T extends { type: 'cleanup_system_metrics_retention' } ? ReturnType<typeof cleanupSystemMetricsBefore> :
  T extends { type: 'cleanup_table_storage_snapshots_retention' } ? { deleted: number } :
  T extends { type: 'cleanup_non_business_stats_data' } ? NonBusinessDataHardCleanupResult :
  T extends { type: 'cleanup_deleted_api_key_record_stats' } ? { cleaned: true } :
  T extends { type: 'cleanup_deleted_account_record_stats' } ? { cleaned: true } :
  unknown

export async function requestStatsWriter<T extends BackgroundStatsWriteOperation>(
  operation: T,
  timeoutMs = 10_000
): Promise<BackgroundStatsWriteOperationResult<T>> {
  if (currentProcessOwnsStatsWriter()) {
    return await handleStatsWriteOperation(operation) as BackgroundStatsWriteOperationResult<T>
  }
  const result = await requestBackgroundWorkerStatsWrite(operation, timeoutMs)
  if (result === undefined) {
    throw new Error(`stats-writer 不可用，无法执行统计写操作：${operation.type}`)
  }
  return result as BackgroundStatsWriteOperationResult<T>
}

export async function handleStatsWriteOperation(operation: BackgroundStatsWriteOperation): Promise<unknown> {
  switch (operation.type) {
    case 'aggregate_usage_stats':
      return aggregateUsageStats(operation.batchSize, operation.maxBatches)
    case 'aggregate_client_ip_stats':
      return aggregateClientIpStats(operation.batchSize, operation.maxBatches, operation.maxRunMs)
    case 'refresh_group_account_stats':
      return { refreshed: await refreshGroupAccountStats() }
    case 'refresh_account_quality':
      return refreshAccountQuality(operation.windowMinutes, operation.failureCandidateLimit)
    case 'record_system_metrics_sample':
      insertSystemMetricsSample(operation.sample)
      for (const sample of operation.processEventLoopSamples) {
        insertProcessEventLoopSample(processEventLoopSampleInput(sample))
      }
      return { recorded: true }
    case 'refresh_usage_rank_snapshots':
      return await refreshUsageRankSnapshotsInStages({
        yieldToEventLoop,
        stageNames: operation.stageNames,
        skipIfUnchanged: true,
        jobName: operation.jobName
      })
    case 'check_usage_stats_consistency':
      return checkUsageStatsConsistency(operation.limit)
    case 'collect_table_storage_snapshot':
      return collectTableStorageSnapshot(operation.sampledAt, operation.options)
    case 'record_client_ip_policy_hits':
      return recordClientIpPolicyHits(operation.hits)
    case 'list_active_client_ip_policies':
      return listActiveClientIpPolicies()
    case 'upsert_account_usage_snapshots':
      upsertAccountUsageSnapshots(operation.inputs)
      return { upsertedCount: operation.inputs.length }
    case 'cleanup_usage_stats_retention':
      return cleanupStatsDatabaseAfterDelete(cleanupUsageStatsBucketsBefore(operation.input))
    case 'cleanup_system_metrics_retention':
      return cleanupStatsDatabaseAfterDelete(cleanupSystemMetricsBefore(operation.input))
    case 'cleanup_table_storage_snapshots_retention':
      return cleanupStatsDatabaseAfterDelete({ deleted: cleanupTableStorageSnapshotsBefore(operation.cutoffIso, operation.limit) })
    case 'cleanup_non_business_stats_data':
      return await cleanupNonBusinessDataBeforeWithResult({
        cutoffAt: operation.cutoffAt,
        limit: operation.limit,
        scope: 'stats'
      })
    case 'cleanup_deleted_api_key_record_stats':
      cleanupDeletedApiKeyRecordStatsData(operation.input)
      return { cleaned: true }
    case 'cleanup_deleted_account_record_stats':
      cleanupDeletedAccountRecordStatsData(operation.input)
      return { cleaned: true }
    default:
      return assertNever(operation)
  }
}

function currentProcessOwnsStatsWriter(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'stats-worker'
}

function aggregateUsageStats(batchSize: number, maxBatches: number): { processed: number; quotaSnapshotSent: boolean } {
  let processed = 0
  for (let index = 0; index < boundedPositiveInteger(maxBatches, 1, 100); index += 1) {
    const batchProcessed = aggregateUsageStatsBatch(boundedPositiveInteger(batchSize, 1, 10000))
    processed += batchProcessed
    if (batchProcessed < batchSize) break
  }
  refreshUsageQuotaHourlyWindowsCache()
  sendGatewayQuotaSnapshotToServer(buildGatewayQuotaSnapshot())
  return { processed, quotaSnapshotSent: true }
}

function aggregateClientIpStats(batchSize: number, maxBatches: number, maxRunMs: number): { processed: number; policies: ActiveClientIpPolicy[] } {
  const startedAtMs = Date.now()
  let processed = 0
  const normalizedBatchSize = boundedPositiveInteger(batchSize, 1, 10000)
  for (let index = 0; index < boundedPositiveInteger(maxBatches, 1, 100); index += 1) {
    const batchProcessed = aggregateClientIpStatsBatch(normalizedBatchSize)
    processed += batchProcessed
    if (batchProcessed < normalizedBatchSize) break
    if (Date.now() - startedAtMs >= boundedPositiveInteger(maxRunMs, 1, 60_000)) break
  }
  refreshClientIpUsageRangeWindows()
  const policies = listActiveClientIpPolicies()
  sendClientIpPolicySnapshotToServer(policies)
  return { processed, policies }
}

function cleanupStatsDatabaseAfterDelete<T extends object>(result: T): T {
  if (Object.values(result).some((value) => typeof value === 'number' && value > 0)) {
    try {
      checkpointSqliteWal(getStatsDatabase(), 'stats')
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'stats_database_checkpoint_failed'
      }), '统计库 WAL checkpoint 失败，等待下一轮清理继续维护')
    }
  }
  return result
}

async function refreshGroupAccountStats(): Promise<number> {
  return await refreshDirtyGroupAccountStatsCacheWithWriter({
    async markAllDirty(reason) {
      await requestBackgroundWorkerDbService({ type: 'mark_all_group_account_stats_dirty', reason })
    },
    async deleteRows(rows) {
      await requestBackgroundWorkerDbService({
        type: 'delete_group_account_stats_dirty_rows',
        rows: rows.map((row) => ({ groupId: row.groupId, updatedAt: row.updatedAt }))
      })
    },
    async updateAllCursor(cursorGroupId) {
      await requestBackgroundWorkerDbService({ type: 'update_group_account_stats_all_cursor', cursorGroupId })
    }
  })
}

function refreshAccountQuality(windowMinutes: number, failureCandidateLimit: number): AccountQualityRealtimeRefreshResult & { failureCandidates: AccountQualityFailurePrecheckCandidate[] } {
  const result = refreshAccountQualityFromUsage(boundedPositiveInteger(windowMinutes, 1, 24 * 60))
  return {
    ...result,
    failureCandidates: listAccountQualityFailurePrecheckCandidates(boundedPositiveInteger(failureCandidateLimit, 1, 100))
  }
}

function processEventLoopSampleInput(sample: ProcessEventLoopSample): ProcessEventLoopSampleInput {
  return {
    processRole: sample.processRole,
    processPid: sample.processPid,
    sampledAt: sample.sampledAt,
    eventLoopLagMs: sample.eventLoopLagMs
  }
}

function boundedPositiveInteger(value: number, min: number, max: number): number {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) ? Math.max(min, Math.min(max, parsed)) : min
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

function assertNever(value: never): never {
  throw new Error(`未知统计写操作：${JSON.stringify(value)}`)
}
