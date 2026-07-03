import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { ClientIpPolicyHitInput } from '../../storage/client-ip-stats.repository.js'
import {
  createClientIpPolicyAsync,
  aggregateClientIpStatsBatchAsync,
  disableClientIpPoliciesAsync,
  listActiveClientIpPoliciesAsync,
  recordClientIpPolicyHitsAsync,
  refreshClientIpUsageRangeWindowsAsync
} from '../../storage/client-ip-stats.repository.js'
import type {
  CollectTableStorageSnapshotOptions,
  CollectTableStorageSnapshotResult
} from '../../storage/table-monitor.repository.js'
import { cleanupTableStorageSnapshotsBefore, cleanupTableStorageSnapshotsBeforeAsync, collectTableStorageSnapshot, collectTableStorageSnapshotAsync } from '../../storage/table-monitor.repository.js'
import {
  aggregateUsageStatsBatchAsync,
  checkUsageStatsConsistency,
  checkUsageStatsConsistencyAsync,
  insertProcessEventLoopSample,
  insertProcessEventLoopSampleAsync,
  insertSystemMetricsSample,
  insertSystemMetricsSampleAsync,
  refreshDirtyGroupAccountStatsCacheAsync,
  refreshDirtyGroupAccountStatsCacheWithWriter,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageQuotaHourlyWindowsCacheAsync,
  refreshUsageRankSnapshotsInStages,
  type ProcessEventLoopSampleInput,
  type SystemMetricsSampleInput,
  type UsageRankSnapshotRefreshResult,
  type UsageRankSnapshotStageName
} from '../../storage/usage-stats.repository.js'
import {
  cleanupSystemMetricsBefore,
  cleanupSystemMetricsBeforeAsync,
  cleanupNonBusinessDataBeforeWithResult,
  type NonBusinessDataHardCleanupResult,
  cleanupUsageStatsBucketsBefore,
  cleanupUsageStatsBucketsBeforeAsync
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
  listAccountQualityFailurePrecheckCandidatesAsync,
  refreshAccountQualityFromUsage,
  refreshAccountQualityFromUsageAsync,
  upsertAccountUsageSnapshotsAsync,
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
import { buildGatewayQuotaSnapshot, buildGatewayQuotaSnapshotAsync } from '../../storage/gateway-quota-snapshot.repository.js'
import { checkpointSqliteWal } from '../../storage/sqlite-maintenance.js'
import { getStatsDatabase } from '../../storage/database.js'

const statsAggregationBatchPauseMs = 25
const usageStatsAggregationOnlineBatchSizeCap = 1000
const usageStatsAggregationMaxRunMsCap = 60_000

export type BackgroundStatsWriteOperation =
  | {
    type: 'aggregate_usage_stats'
    batchSize: number
    maxBatches: number
    maxRunMs: number
    safeCreatedBefore?: string
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
    type: 'create_client_ip_policy'
    input: import('../../storage/client-ip-stats.repository.js').ClientIpPolicyMutationInput
  }
  | {
    type: 'disable_client_ip_policies'
    input: import('../../storage/client-ip-stats.repository.js').ClientIpPolicyDisableInput
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
  T extends { type: 'aggregate_usage_stats' } ? { processed: number; quotaSnapshotSent: boolean; stoppedByTimeBudget: boolean; effectiveBatchSize: number } :
  T extends { type: 'aggregate_client_ip_stats' } ? { processed: number; policies: ActiveClientIpPolicy[] } :
  T extends { type: 'refresh_group_account_stats' } ? { refreshed: true } :
  T extends { type: 'refresh_account_quality' } ? AccountQualityRealtimeRefreshResult & { failureCandidates: AccountQualityFailurePrecheckCandidate[] } :
  T extends { type: 'record_system_metrics_sample' } ? { recorded: true } :
  T extends { type: 'refresh_usage_rank_snapshots' } ? UsageRankSnapshotRefreshResult :
  T extends { type: 'check_usage_stats_consistency' } ? ReturnType<typeof checkUsageStatsConsistency> :
  T extends { type: 'collect_table_storage_snapshot' } ? CollectTableStorageSnapshotResult :
  T extends { type: 'record_client_ip_policy_hits' } ? { recorded: number } :
  T extends { type: 'create_client_ip_policy' } ? import('../../storage/client-ip-stats.repository.js').ClientIpPolicySummary :
  T extends { type: 'disable_client_ip_policies' } ? { disabledCount: number } :
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
      return await aggregateUsageStats(operation.batchSize, operation.maxBatches, operation.maxRunMs, operation.safeCreatedBefore)
    case 'aggregate_client_ip_stats':
      return await aggregateClientIpStats(operation.batchSize, operation.maxBatches, operation.maxRunMs)
    case 'refresh_group_account_stats':
      return { refreshed: await refreshGroupAccountStats() }
    case 'refresh_account_quality':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await refreshAccountQualityAsync(operation.windowMinutes, operation.failureCandidateLimit)
      }
      return refreshAccountQuality(operation.windowMinutes, operation.failureCandidateLimit)
    case 'record_system_metrics_sample':
      if (runtimeConfig.databaseDriver === 'postgres') {
        await insertSystemMetricsSampleAsync(operation.sample)
        for (const sample of operation.processEventLoopSamples) {
          await insertProcessEventLoopSampleAsync(processEventLoopSampleInput(sample))
        }
      } else {
        insertSystemMetricsSample(operation.sample)
        for (const sample of operation.processEventLoopSamples) {
          insertProcessEventLoopSample(processEventLoopSampleInput(sample))
        }
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
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await checkUsageStatsConsistencyAsync(operation.limit)
      }
      return checkUsageStatsConsistency(operation.limit)
    case 'collect_table_storage_snapshot':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await collectTableStorageSnapshotAsync(operation.sampledAt, operation.options)
      }
      return collectTableStorageSnapshot(operation.sampledAt, operation.options)
    case 'record_client_ip_policy_hits':
      return await recordClientIpPolicyHitsAsync(operation.hits)
    case 'create_client_ip_policy':
      return await createClientIpPolicyAsync(operation.input)
    case 'disable_client_ip_policies':
      return await disableClientIpPoliciesAsync(operation.input)
    case 'list_active_client_ip_policies':
      return await listActiveClientIpPoliciesAsync()
    case 'upsert_account_usage_snapshots':
      if (runtimeConfig.databaseDriver === 'postgres') {
        await upsertAccountUsageSnapshotsAsync(operation.inputs)
        return { upsertedCount: operation.inputs.length }
      }
      upsertAccountUsageSnapshots(operation.inputs)
      return { upsertedCount: operation.inputs.length }
    case 'cleanup_usage_stats_retention':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await cleanupUsageStatsBucketsBeforeAsync(operation.input)
      }
      return cleanupStatsDatabaseAfterDelete(cleanupUsageStatsBucketsBefore(operation.input))
    case 'cleanup_system_metrics_retention':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await cleanupSystemMetricsBeforeAsync(operation.input)
      }
      return cleanupStatsDatabaseAfterDelete(cleanupSystemMetricsBefore(operation.input))
    case 'cleanup_table_storage_snapshots_retention':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return { deleted: await cleanupTableStorageSnapshotsBeforeAsync(operation.cutoffIso, operation.limit) }
      }
      return cleanupStatsDatabaseAfterDelete({ deleted: cleanupTableStorageSnapshotsBefore(operation.cutoffIso, operation.limit) })
    case 'cleanup_non_business_stats_data':
      return await cleanupNonBusinessDataBeforeWithResult({
        cutoffAt: operation.cutoffAt,
        limit: operation.limit,
        scope: 'stats'
      })
    case 'cleanup_deleted_api_key_record_stats':
      if (runtimeConfig.databaseDriver === 'postgres') {
        throw postgresStatsWriterOperationNotImplemented(operation.type)
      }
      cleanupDeletedApiKeyRecordStatsData(operation.input)
      return { cleaned: true }
    case 'cleanup_deleted_account_record_stats':
      if (runtimeConfig.databaseDriver === 'postgres') {
        throw postgresStatsWriterOperationNotImplemented(operation.type)
      }
      cleanupDeletedAccountRecordStatsData(operation.input)
      return { cleaned: true }
    default:
      return assertNever(operation)
  }
}

function postgresStatsWriterOperationNotImplemented(operationType: string): Error {
  return new Error(`高性能模式统计写操作 ${operationType} 尚未实现 PostgreSQL 当前 schema 写入逻辑，禁止静默跳过或回落 SQLite`)
}

function currentProcessOwnsStatsWriter(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'stats-worker'
}

async function aggregateUsageStats(batchSize: number, maxBatches: number, maxRunMs: number, safeCreatedBefore?: string): Promise<{ processed: number; quotaSnapshotSent: boolean; stoppedByTimeBudget: boolean; effectiveBatchSize: number }> {
  const startedAtMs = Date.now()
  let processed = 0
  let stoppedByTimeBudget = false
  const normalizedBatchSize = Math.min(
    boundedPositiveInteger(batchSize, 1, 10000),
    usageStatsAggregationOnlineBatchSizeCap
  )
  const normalizedMaxBatches = boundedPositiveInteger(maxBatches, 1, 100)
  const normalizedMaxRunMs = boundedPositiveInteger(maxRunMs, 1, usageStatsAggregationMaxRunMsCap)
  for (let index = 0; index < normalizedMaxBatches; index += 1) {
    if (Date.now() - startedAtMs >= normalizedMaxRunMs) {
      stoppedByTimeBudget = true
      break
    }
    const batchProcessed = await aggregateUsageStatsBatchAsync(normalizedBatchSize, safeCreatedBefore)
    processed += batchProcessed
    if (batchProcessed < normalizedBatchSize) break
    if (Date.now() - startedAtMs >= normalizedMaxRunMs) {
      stoppedByTimeBudget = true
      break
    }
    await yieldToEventLoop()
    await pauseBetweenStatsAggregationBatches()
  }
  if (processed > 0) {
    if (runtimeConfig.databaseDriver === 'postgres') {
      await refreshUsageQuotaHourlyWindowsCacheAsync()
      sendGatewayQuotaSnapshotToServer(await buildGatewayQuotaSnapshotAsync())
      return { processed, quotaSnapshotSent: true, stoppedByTimeBudget, effectiveBatchSize: normalizedBatchSize }
    }
    refreshUsageQuotaHourlyWindowsCache()
    sendGatewayQuotaSnapshotToServer(buildGatewayQuotaSnapshot())
    return { processed, quotaSnapshotSent: true, stoppedByTimeBudget, effectiveBatchSize: normalizedBatchSize }
  }
  return { processed, quotaSnapshotSent: false, stoppedByTimeBudget, effectiveBatchSize: normalizedBatchSize }
}

async function aggregateClientIpStats(batchSize: number, maxBatches: number, maxRunMs: number): Promise<{ processed: number; policies: ActiveClientIpPolicy[] }> {
  const startedAtMs = Date.now()
  let processed = 0
  const normalizedBatchSize = boundedPositiveInteger(batchSize, 1, 10000)
  const normalizedMaxBatches = boundedPositiveInteger(maxBatches, 1, 100)
  for (let index = 0; index < normalizedMaxBatches; index += 1) {
    const batchProcessed = await aggregateClientIpStatsBatchAsync(normalizedBatchSize)
    processed += batchProcessed
    if (batchProcessed < normalizedBatchSize) break
    if (Date.now() - startedAtMs >= boundedPositiveInteger(maxRunMs, 1, 60_000)) break
    await yieldToEventLoop()
    await pauseBetweenStatsAggregationBatches()
  }
  await refreshClientIpUsageRangeWindowsAsync()
  const policies = await listActiveClientIpPoliciesAsync()
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
  if (runtimeConfig.databaseDriver === 'postgres') {
    return await refreshDirtyGroupAccountStatsCacheAsync()
  }
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

async function refreshAccountQualityAsync(windowMinutes: number, failureCandidateLimit: number): Promise<AccountQualityRealtimeRefreshResult & { failureCandidates: AccountQualityFailurePrecheckCandidate[] }> {
  const result = await refreshAccountQualityFromUsageAsync(boundedPositiveInteger(windowMinutes, 1, 24 * 60))
  return {
    ...result,
    failureCandidates: await listAccountQualityFailurePrecheckCandidatesAsync(boundedPositiveInteger(failureCandidateLimit, 1, 100))
  }
}

function processEventLoopSampleInput(sample: ProcessEventLoopSample): ProcessEventLoopSampleInput {
  return {
    processRole: sample.processRole,
    processPid: sample.processPid,
    sampledAt: sample.sampledAt,
    eventLoopLagMs: sample.eventLoopLagMs,
    processRssBytes: sample.processRssBytes,
    processHeapUsedBytes: sample.processHeapUsedBytes,
    processHeapTotalBytes: sample.processHeapTotalBytes,
    processExternalBytes: sample.processExternalBytes,
    processArrayBuffersBytes: sample.processArrayBuffersBytes
  }
}

function boundedPositiveInteger(value: number, min: number, max: number): number {
  const parsed = Math.trunc(Number(value))
  return Number.isFinite(parsed) ? Math.max(min, Math.min(max, parsed)) : min
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

function pauseBetweenStatsAggregationBatches(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, statsAggregationBatchPauseMs))
}

function assertNever(value: never): never {
  throw new Error(`未知统计写操作：${JSON.stringify(value)}`)
}
