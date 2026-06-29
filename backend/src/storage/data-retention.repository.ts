import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, getUsageCatalogDatabase, nowIso, rollbackDatabaseTransaction, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { cleanupAuditPayloadBlobsBeforeAsync } from './audit-log-payload-blobs.js'
import { deletePostgresUsageRecordCatalogRowsByUsageIds } from './usage-record-catalog-cleanup.js'
import {
  cleanupDiscoveredHardCleanupTablesBefore,
  deleteRowsBeforeByRowid,
  hardCleanupCutoffs,
  type HardCleanupCutoffKey,
  type HardCleanupDatabaseRole
} from './data-retention-hard-cleanup.js'
import {
  cleanupEmptyUsageRecordShardFilesBefore,
  deleteUsageRecordShardEntries,
  getUsageRecordShardDatabase,
  type UsageRecordShardLocation
} from './usage-record-shards.js'

type CleanupRow = Record<string, unknown>
type StatsDatabase = ReturnType<typeof getStatsDatabase>
const usageRecordCleanupRequiredCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
const postgresHardCleanupTables: Record<HardCleanupDatabaseRole, Array<{ tableName: string; timeColumnName: string; cutoffKey: HardCleanupCutoffKey }>> = {
  dataset: [
    { tableName: 'audit_payload_refs', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'audit_log_attempts', timeColumnName: 'started_at', cutoffKey: 'iso' },
    { tableName: 'audit_logs', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'audit_error_groups', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'model_check_items', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'model_check_runs', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'operation_log_targets', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'operation_log_viewers', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'operation_log_summary_search_terms', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'operation_logs', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'public_api_logs', timeColumnName: 'created_at', cutoffKey: 'iso' },
    { tableName: 'runtime_logs', timeColumnName: 'time', cutoffKey: 'iso' },
    { tableName: 'runtime_log_file_cursors', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'runtime_log_facet_summary', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'runtime_log_level_facets', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'runtime_log_event_facets', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'api_key_record_cleanup_targets', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'account_record_cleanup_targets', timeColumnName: 'updated_at', cutoffKey: 'iso' }
  ],
  'usage-catalog': [
    { tableName: 'usage_record_account_shards', timeColumnName: 'last_seen_at', cutoffKey: 'iso' },
    { tableName: 'usage_record_api_key_shards', timeColumnName: 'last_seen_at', cutoffKey: 'iso' }
  ],
  stats: [
    { tableName: 'account_quality_minute_stats', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
    { tableName: 'group_account_stats', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'account_quality_scores', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'account_quality_dirty_accounts', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'account_usage_snapshots', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'usage_stats_totals', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'usage_stats_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
    { tableName: 'usage_stats_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'usage_stats_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'usage_stats_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
    { tableName: 'usage_stats_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
    { tableName: 'authorization_team_usage_summary_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'authorization_team_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'authorization_user_usage_summary_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'authorization_user_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'usage_model_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
    { tableName: 'usage_model_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'usage_model_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'usage_model_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
    { tableName: 'usage_model_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
    { tableName: 'usage_error_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
    { tableName: 'usage_error_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'usage_error_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'usage_error_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
    { tableName: 'usage_error_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
    { tableName: 'usage_latency_minute', timeColumnName: 'stat_minute', cutoffKey: 'minute' },
    { tableName: 'usage_latency_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'usage_latency_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'usage_latency_weekly', timeColumnName: 'stat_week', cutoffKey: 'week' },
    { tableName: 'usage_latency_monthly', timeColumnName: 'stat_month', cutoffKey: 'month' },
    { tableName: 'usage_rank_snapshots', timeColumnName: 'snapshot_at', cutoffKey: 'iso' },
    { tableName: 'usage_overview_summary_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'usage_overview_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'usage_model_rank_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'usage_error_rank_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'ai_performance_summary_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'usage_quota_hourly_windows', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'usage_scope_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'client_ip_registry', timeColumnName: 'last_seen_at', cutoffKey: 'iso' },
    { tableName: 'client_ip_stats_daily', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'client_ip_usage_range_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'client_ip_range_window_dirty_ips', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'client_ip_policies', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'client_ip_policy_hits', timeColumnName: 'stat_date', cutoffKey: 'date' },
    { tableName: 'stats_job_state', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'usage_record_cleanup_deductions', timeColumnName: 'updated_at', cutoffKey: 'iso' },
    { tableName: 'system_metrics_samples', timeColumnName: 'sampled_at', cutoffKey: 'iso' },
    { tableName: 'system_metrics_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'system_metrics_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'process_event_loop_samples', timeColumnName: 'sampled_at', cutoffKey: 'iso' },
    { tableName: 'process_event_loop_hourly', timeColumnName: 'stat_hour', cutoffKey: 'hour' },
    { tableName: 'process_event_loop_trend_windows', timeColumnName: 'end_date', cutoffKey: 'date' },
    { tableName: 'database_storage_snapshots', timeColumnName: 'sampled_at', cutoffKey: 'iso' },
    { tableName: 'table_storage_snapshots', timeColumnName: 'sampled_at', cutoffKey: 'iso' }
  ]
}

interface UsageRecordShardCleanupRow {
  id: string
  created_at: string
  location: UsageRecordShardLocation
}

export interface UsageRecordsCleanupCursor {
  cursorCreatedAt?: string
  cursorId?: string
  blockedReason?: string
}

export interface ProcessedUsageRecordsCleanupBatchResult {
  cutoffCreatedAt: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
  deletedRows: number
  hasMore: boolean
  blockedReason?: string
}

export interface ProcessedUsageRecordsCleanupPreviewResult {
  cutoffCreatedAt: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
  eligibleRows: number
  hasMore: boolean
  blockedReason?: string
}

export interface UsageRecordsCleanupBatchResult {
  cutoffCreatedAt: string
  deletedRows: number
  hasMore: boolean
}

export interface UsageRecordsCleanupPreviewResult {
  cutoffCreatedAt: string
  eligibleRows: number
  hasMore: boolean
}

export interface UsageStatsRetentionCleanupResult {
  accountQualityMinuteStats: number
  usageStatsMinute: number
  usageModelMinute: number
  usageErrorMinute: number
  usageLatencyMinute: number
  usageStatsDaily: number
  usageModelDaily: number
  usageErrorDaily: number
  usageLatencyDaily: number
  usageStatsHourly: number
  usageModelHourly: number
  usageErrorHourly: number
  usageLatencyHourly: number
  usageStatsWeekly: number
  usageModelWeekly: number
  usageErrorWeekly: number
  usageLatencyWeekly: number
  usageStatsMonthly: number
  usageModelMonthly: number
  usageErrorMonthly: number
  usageLatencyMonthly: number
  authorizationTeamUsageSummaryDaily: number
  authorizationTeamUsageRangeWindows: number
  authorizationUserUsageSummaryDaily: number
  authorizationUserUsageRangeWindows: number
  usageRankSnapshots: number
  usageOverviewSummaryWindows: number
  usageOverviewTrendWindows: number
  usageModelRankWindows: number
  usageErrorRankWindows: number
  aiPerformanceSummaryWindows: number
  usageQuotaHourlyWindows: number
  usageScopeRangeWindows: number
  clientIpUsageRangeWindows: number
  accountUsageSnapshots: number
}

export interface SystemMetricsRetentionCleanupResult {
  systemMetricsSamples: number
  systemMetricsHourly: number
  systemMetricsTrendWindows: number
  processEventLoopSamples: number
  processEventLoopHourly: number
  processEventLoopTrendWindows: number
}

export interface ModelCheckRetentionCleanupResult {
  modelCheckRuns: number
  modelCheckItems: number
}

export interface NonBusinessDataHardCleanupResult {
  cutoffAt: string
  deletedRows: number
  deletedFiles: number
  hasMore: boolean
  tableRows: Record<string, number>
  fileDeletes: Record<string, number>
}

export function cleanupProcessedUsageRecordsBefore(cutoffCreatedAt: string, limit = 10000): number {
  return cleanupProcessedUsageRecordsBeforeWithResult(cutoffCreatedAt, limit).deletedRows
}

export function inspectUsageRecordsCleanupBefore(cutoffCreatedAt: string, limit = 10000): UsageRecordsCleanupPreviewResult {
  const batchLimit = positiveLimit(limit)
  const rows = selectUsageRecordCleanupRows(cutoffCreatedAt, batchLimit + 1)
  return {
    cutoffCreatedAt,
    eligibleRows: Math.min(rows.length, batchLimit),
    hasMore: rows.length > batchLimit
  }
}

export function cleanupUsageRecordsBeforeWithResult(cutoffCreatedAt: string, limit = 10000): UsageRecordsCleanupBatchResult {
  const batchLimit = positiveLimit(limit)
  const rows = selectUsageRecordCleanupRows(cutoffCreatedAt, batchLimit + 1)
  return {
    cutoffCreatedAt,
    deletedRows: deleteUsageRecordShardRows(rows.slice(0, batchLimit)),
    hasMore: rows.length > batchLimit
  }
}

export function inspectProcessedUsageRecordsCleanupBefore(cutoffCreatedAt: string, limit = 10000): ProcessedUsageRecordsCleanupPreviewResult {
  const statsDatabase = getStatsDatabase()
  const batchLimit = positiveLimit(limit)
  const safetyCursor = usageRecordShardCleanupFloorCursor(statsDatabase)
  if (!safetyCursor) {
    if (!hasUsageRecordsBefore(cutoffCreatedAt)) {
      return {
        cutoffCreatedAt,
        eligibleRows: 0,
        hasMore: false
      }
    }
    return {
      cutoffCreatedAt,
      eligibleRows: 0,
      hasMore: false,
      blockedReason: usageRecordCleanupMissingCursorBlockedReason()
    }
  }

  const rows = selectProcessedUsageRecordCleanupRowsWithCursor(cutoffCreatedAt, safetyCursor, batchLimit + 1)
  const blockedReason = usageRecordsCleanupBlockedReasonForRows(statsDatabase, rows.slice(0, batchLimit))
  if (blockedReason) {
    return {
      cutoffCreatedAt,
      safetyCursorCreatedAt: safetyCursor.cursorCreatedAt,
      safetyCursorId: safetyCursor.cursorId,
      eligibleRows: 0,
      hasMore: false,
      blockedReason
    }
  }
  return {
    cutoffCreatedAt,
    safetyCursorCreatedAt: safetyCursor.cursorCreatedAt,
    safetyCursorId: safetyCursor.cursorId,
    eligibleRows: Math.min(rows.length, batchLimit),
    hasMore: rows.length > batchLimit
  }
}

export function cleanupProcessedUsageRecordsBeforeWithResult(cutoffCreatedAt: string, limit = 10000): ProcessedUsageRecordsCleanupBatchResult {
  assertSqliteDataRetention('cleanupProcessedUsageRecordsBeforeWithResult')
  const statsDatabase = getStatsDatabase()
  const batchLimit = positiveLimit(limit)
  const safetyCursor = usageRecordShardCleanupFloorCursor(statsDatabase)
  if (!safetyCursor) {
    if (!hasUsageRecordsBefore(cutoffCreatedAt)) {
      return {
        cutoffCreatedAt,
        deletedRows: 0,
        hasMore: false
      }
    }
    return {
      cutoffCreatedAt,
      deletedRows: 0,
      hasMore: false,
      blockedReason: usageRecordCleanupMissingCursorBlockedReason()
    }
  }

  const rows = selectProcessedUsageRecordCleanupRowsWithCursor(cutoffCreatedAt, safetyCursor, batchLimit + 1)
  const rowsToDelete = rows.slice(0, batchLimit)
  const blockedReason = usageRecordsCleanupBlockedReasonForRows(statsDatabase, rowsToDelete)
  if (blockedReason) {
    return {
      cutoffCreatedAt,
      safetyCursorCreatedAt: safetyCursor.cursorCreatedAt,
      safetyCursorId: safetyCursor.cursorId,
      deletedRows: 0,
      hasMore: false,
      blockedReason
    }
  }
  return {
    cutoffCreatedAt,
    safetyCursorCreatedAt: safetyCursor.cursorCreatedAt,
    safetyCursorId: safetyCursor.cursorId,
    deletedRows: deleteUsageRecordShardRows(rowsToDelete),
    hasMore: rows.length > batchLimit
  }
}

export async function cleanupProcessedUsageRecordsBeforeWithResultAsync(cutoffCreatedAt: string, limit = 10000): Promise<ProcessedUsageRecordsCleanupBatchResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupProcessedUsageRecordsBeforeWithResult(cutoffCreatedAt, limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const batchLimit = positiveLimit(limit)
  const safetyCursor = await postgresUsageRecordsCleanupFloorCursor(client)
  if (!safetyCursor) {
    if (!await hasPostgresUsageRecordsBefore(client, cutoffCreatedAt)) {
      return {
        cutoffCreatedAt,
        deletedRows: 0,
        hasMore: false
      }
    }
    return {
      cutoffCreatedAt,
      deletedRows: 0,
      hasMore: false,
      blockedReason: usageRecordCleanupMissingCursorBlockedReason()
    }
  }

  const rows = await selectPostgresProcessedUsageRecordCleanupRowsWithCursor(client, cutoffCreatedAt, safetyCursor, batchLimit + 1)
  const rowsToDelete = rows.slice(0, batchLimit)
  return {
    cutoffCreatedAt,
    safetyCursorCreatedAt: safetyCursor.cursorCreatedAt,
    safetyCursorId: safetyCursor.cursorId,
    deletedRows: await deletePostgresUsageRecordRows(client, rowsToDelete.map((row) => row.id)),
    hasMore: rows.length > batchLimit
  }
}

export async function cleanupNonBusinessDataBeforeWithResult(input: {
  cutoffAt: string
  limit?: number
  scope?: 'all' | 'dataset' | 'stats'
}): Promise<NonBusinessDataHardCleanupResult> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupNonBusinessDataBeforeWithResultPostgres(input)
  }
  const batchLimit = positiveLimit(input.limit)
  const cutoffs = hardCleanupCutoffs(input.cutoffAt)
  const scope = input.scope ?? 'all'
  const tableRows: Record<string, number> = {}
  const fileDeletes: Record<string, number> = {}
  let deletedRows = 0
  let deletedFiles = 0
  let hasMore = false

  const addRows = (key: string, count: number): void => {
    if (count <= 0) return
    tableRows[key] = (tableRows[key] ?? 0) + count
    deletedRows += count
    if (count >= batchLimit) {
      hasMore = true
    }
  }
  const addFiles = (key: string, count: number): void => {
    if (count <= 0) return
    fileDeletes[key] = (fileDeletes[key] ?? 0) + count
    deletedFiles += count
  }

  if (scope === 'all' || scope === 'dataset') {
    const oldAuditBlobs = await cleanupAuditPayloadBlobsBeforeAsync(cutoffs.iso, batchLimit)
    addRows('dataset.audit_payload_blobs', oldAuditBlobs.deletedRows)
    addFiles('audit_payload_blobs', oldAuditBlobs.deletedFiles)

    const usageRecords = cleanupUsageRecordsBeforeWithResult(cutoffs.iso, batchLimit)
    addRows('usage_shards.usage_records', usageRecords.deletedRows)
    hasMore = hasMore || usageRecords.hasMore

    cleanupDiscoveredHardCleanupTablesBefore('dataset', cutoffs, batchLimit, addRows)
    cleanupDiscoveredHardCleanupTablesBefore('usage-catalog', cutoffs, batchLimit, addRows)

    const emptyUsageShards = await cleanupEmptyUsageRecordShardFilesBefore(cutoffs.iso, batchLimit)
    addRows('usage_catalog.usage_record_shards', emptyUsageShards.usageRecordShards)
    addFiles('usage_shard_files', emptyUsageShards.usageShardFiles)
    hasMore = hasMore || emptyUsageShards.hasMore
  }

  if (scope === 'all' || scope === 'stats') {
    cleanupDiscoveredHardCleanupTablesBefore('stats', cutoffs, batchLimit, addRows)
  }

  return {
    cutoffAt: cutoffs.iso,
    deletedRows,
    deletedFiles,
    hasMore,
    tableRows,
    fileDeletes
  }
}

async function cleanupNonBusinessDataBeforeWithResultPostgres(input: {
  cutoffAt: string
  limit?: number
  scope?: 'all' | 'dataset' | 'stats'
}): Promise<NonBusinessDataHardCleanupResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const batchLimit = positiveLimit(input.limit)
  const cutoffs = hardCleanupCutoffs(input.cutoffAt)
  const scope = input.scope ?? 'all'
  const tableRows: Record<string, number> = {}
  const fileDeletes: Record<string, number> = {}
  let deletedRows = 0
  let deletedFiles = 0
  let hasMore = false

  const addRows = (key: string, count: number): void => {
    if (count <= 0) return
    tableRows[key] = (tableRows[key] ?? 0) + count
    deletedRows += count
    if (count >= batchLimit) {
      hasMore = true
    }
  }
  const addFiles = (key: string, count: number): void => {
    if (count <= 0) return
    fileDeletes[key] = (fileDeletes[key] ?? 0) + count
    deletedFiles += count
  }

  if (scope === 'all' || scope === 'dataset') {
    const oldAuditBlobs = await cleanupAuditPayloadBlobsBeforeAsync(cutoffs.iso, batchLimit)
    addRows('dataset.audit_payload_blobs', oldAuditBlobs.deletedRows)
    addFiles('audit_payload_blobs', oldAuditBlobs.deletedFiles)

    const usageRecords = await cleanupProcessedUsageRecordsBeforeWithResultAsync(cutoffs.iso, batchLimit)
    addRows('juhe_usage.usage_records', usageRecords.deletedRows)
    hasMore = hasMore || usageRecords.hasMore

    await cleanupPostgresHardCleanupTablesBefore(client, 'dataset', cutoffs, batchLimit, addRows)
    await cleanupPostgresHardCleanupTablesBefore(client, 'usage-catalog', cutoffs, batchLimit, addRows)
  }

  if (scope === 'all' || scope === 'stats') {
    await cleanupPostgresHardCleanupTablesBefore(client, 'stats', cutoffs, batchLimit, addRows)
  }

  return {
    cutoffAt: cutoffs.iso,
    deletedRows,
    deletedFiles,
    hasMore,
    tableRows,
    fileDeletes
  }
}

async function cleanupPostgresHardCleanupTablesBefore(
  client: DatabaseClient,
  databaseRole: HardCleanupDatabaseRole,
  cutoffs: Record<HardCleanupCutoffKey, string>,
  limit: number,
  addRows: (key: string, count: number) => void
): Promise<void> {
  const schemaName = postgresSchemaForHardCleanupRole(databaseRole)
  for (const rule of postgresHardCleanupTables[databaseRole]) {
    const deleted = await deletePostgresRowsBeforeByCtid(
      client,
      schemaName,
      rule.tableName,
      rule.timeColumnName,
      cutoffs[rule.cutoffKey],
      limit
    )
    addRows(`${schemaName}.${rule.tableName}`, deleted)
  }
}

async function deletePostgresRowsBeforeByCtid(
  client: DatabaseClient,
  schemaName: string,
  tableName: string,
  timeColumnName: string,
  cutoff: string,
  limit: number
): Promise<number> {
  const table = client.dialect.qualifyTable(schemaName, tableName)
  const timeColumn = client.dialect.quoteIdentifier(timeColumnName)
  const result = await client.execute(`
    DELETE FROM ${table}
    WHERE ctid IN (
      SELECT ctid
      FROM ${table}
      WHERE ${timeColumn} < ?
      ORDER BY ${timeColumn} ASC, ctid ASC
      LIMIT ?
    )
  `, [cutoff, positiveLimit(limit)])
  return changed(result)
}

function postgresSchemaForHardCleanupRole(databaseRole: HardCleanupDatabaseRole): 'juhe_dataset' | 'juhe_usage' | 'juhe_stats' {
  if (databaseRole === 'dataset') return 'juhe_dataset'
  if (databaseRole === 'usage-catalog') return 'juhe_usage'
  return 'juhe_stats'
}

function selectProcessedUsageRecordCleanupRowsWithCursor(
  cutoffCreatedAt: string,
  cursor: { cursorCreatedAt: string; cursorId: string },
  limit: number
): UsageRecordShardCleanupRow[] {
  const batchLimit = positiveLimit(limit)
  return selectUsageRecordCleanupCatalogRows({
    cutoffCreatedAt,
    cursorCreatedAt: cursor.cursorCreatedAt,
    cursorId: cursor.cursorId,
    limit: batchLimit
  })
}

function selectUsageRecordCleanupRows(
  cutoffCreatedAt: string,
  limit: number
): UsageRecordShardCleanupRow[] {
  return selectUsageRecordCleanupCatalogRows({
    cutoffCreatedAt,
    limit: positiveLimit(limit)
  })
}

function hasUsageRecordsBefore(cutoffCreatedAt: string): boolean {
  const row = getUsageCatalogDatabase()
    .prepare(`
      SELECT ue.usage_id
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT 1
    `)
    .get(cutoffCreatedAt) as unknown as { usage_id?: string } | undefined
  return Boolean(row?.usage_id)
}

async function hasPostgresUsageRecordsBefore(client: DatabaseClient, cutoffCreatedAt: string): Promise<boolean> {
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM juhe_usage.usage_records
    WHERE created_at < ?
    ORDER BY created_at ASC, id ASC
    LIMIT 1
  `, [cutoffCreatedAt])
  return Boolean(row?.found)
}

export function usageRecordsCleanupCursor(database: StatsDatabase): UsageRecordsCleanupCursor {
  const aggregationCursor = usageRecordShardCleanupFloorCursor(database)
  if (!aggregationCursor) {
    return {
      blockedReason: '统计聚合游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试'
    }
  }
  return aggregationCursor
}

function usageRecordsCleanupBlockedReasonForRows(database: StatsDatabase, rows: UsageRecordShardCleanupRow[]): string | undefined {
  const shardKeys = [...new Set(rows.map((row) => row.location.shardKey.trim()).filter(Boolean))]
  if (shardKeys.length === 0) {
    return undefined
  }
  const cursorShardKeys = usageRecordShardCleanupCursorShardKeysForShards(database, shardKeys)
  return shardKeys.some((shardKey) => !cursorShardKeys.has(shardKey))
    ? usageRecordCleanupMissingCursorBlockedReason()
    : undefined
}

function usageRecordCleanupMissingCursorBlockedReason(): string {
  return '部分使用记录分片的统计安全游标尚未建立，暂不清理使用记录，避免破坏统计聚合；请确认后台 worker 正常运行后稍后重试'
}

async function postgresUsageRecordsCleanupFloorCursor(client: DatabaseClient): Promise<{ cursorCreatedAt: string; cursorId: string } | undefined> {
  const rows = await client.query<{ cursor_created_at?: string | null; cursor_id?: string | null; job_name?: string | null }>(`
    SELECT job_name, cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY(?)
      AND cursor_created_at IS NOT NULL
      AND cursor_id IS NOT NULL
    ORDER BY cursor_created_at ASC, cursor_id ASC
  `, [[...usageRecordCleanupRequiredCursorJobNames]])
  const jobNames = new Set(rows.map((row) => String(row.job_name ?? '').trim()).filter(Boolean))
  if (usageRecordCleanupRequiredCursorJobNames.some((jobName) => !jobNames.has(jobName))) {
    return undefined
  }
  const row = rows[0]
  const cursorCreatedAt = row?.cursor_created_at?.trim()
  const cursorId = row?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function usageRecordShardCleanupFloorCursor(database: StatsDatabase): { cursorCreatedAt: string; cursorId: string } | undefined {
  const placeholders = sqlPlaceholders(usageRecordCleanupRequiredCursorJobNames.length)
  const row = database
    .prepare(`
      SELECT cursor_created_at, cursor_id
      FROM stats_job_state
      WHERE scope_type = 'usage_shard'
        AND job_name IN (${placeholders})
        AND cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL
      ORDER BY cursor_created_at ASC, cursor_id ASC
      LIMIT 1
    `)
    .get(...usageRecordCleanupRequiredCursorJobNames) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  const cursorCreatedAt = row?.cursor_created_at?.trim()
  const cursorId = row?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

async function selectPostgresProcessedUsageRecordCleanupRowsWithCursor(
  client: DatabaseClient,
  cutoffCreatedAt: string,
  cursor: { cursorCreatedAt: string; cursorId: string },
  limit: number
): Promise<Array<{ id: string; created_at: string }>> {
  const rows = await client.query<{ id?: string | null; created_at?: string | null }>(`
    SELECT id, created_at
    FROM juhe_usage.usage_records
    WHERE created_at < ?
      AND (created_at < ? OR (created_at = ? AND id <= ?))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [cutoffCreatedAt, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, positiveLimit(limit)])
  return rows
    .map((row) => ({
      id: String(row.id ?? ''),
      created_at: String(row.created_at ?? '')
    }))
    .filter((row) => row.id && row.created_at)
}

async function deletePostgresUsageRecordRows(client: DatabaseClient, ids: string[]): Promise<number> {
  const usageIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (!usageIds.length) return 0
  let deletedRows = 0
  await client.transaction(async (tx) => {
    await deletePostgresUsageRecordCatalogRowsByUsageIds(tx, usageIds)
    deletedRows = changed(await tx.execute('DELETE FROM juhe_usage.usage_records WHERE id = ANY(?)', [usageIds]))
  })
  return deletedRows
}

function usageRecordShardCleanupCursorShardKeysForShards(database: StatsDatabase, shardKeys: string[]): Set<string> {
  const normalizedShardKeys = [...new Set(shardKeys.map((shardKey) => shardKey.trim()).filter(Boolean))]
  const cursorShardKeys = new Set<string>()
  for (const chunk of chunkValues(normalizedShardKeys, 900)) {
    if (chunk.length === 0) continue
    const jobPlaceholders = sqlPlaceholders(usageRecordCleanupRequiredCursorJobNames.length)
    const rows = database
      .prepare(`
        SELECT scope_id
        FROM stats_job_state
        WHERE scope_type = 'usage_shard'
          AND scope_id IN (${sqlPlaceholders(chunk.length)})
          AND job_name IN (${jobPlaceholders})
          AND cursor_created_at IS NOT NULL
          AND cursor_id IS NOT NULL
        GROUP BY scope_id
        HAVING COUNT(DISTINCT job_name) = ?
      `)
      .all(...chunk, ...usageRecordCleanupRequiredCursorJobNames, usageRecordCleanupRequiredCursorJobNames.length) as Array<{ scope_id?: string | null }>
    for (const row of rows) {
      const scopeId = row.scope_id?.trim()
      if (scopeId) {
        cursorShardKeys.add(scopeId)
      }
    }
  }
  return cursorShardKeys
}

function selectUsageRecordCleanupCatalogRows(input: {
  cutoffCreatedAt: string
  cursorCreatedAt?: string
  cursorId?: string
  limit: number
}): UsageRecordShardCleanupRow[] {
  const params: Array<string | number> = [input.cutoffCreatedAt]
  const cursorClause = input.cursorCreatedAt && input.cursorId
    ? 'AND (ue.created_at < ? OR (ue.created_at = ? AND ue.usage_id <= ?))'
    : ''
  if (cursorClause) {
    params.push(input.cursorCreatedAt as string, input.cursorCreatedAt as string, input.cursorId as string)
  }
  params.push(positiveLimit(input.limit))
  const rows = getUsageCatalogDatabase()
    .prepare(`
      SELECT ue.usage_id, ue.created_at, s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
        ${cursorClause}
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT ?
    `)
    .all(...params) as Array<{
      usage_id?: string | null
      created_at?: string | null
      shard_key?: string | null
      bucket_date?: string | null
      shard_id?: number | null
      file_path?: string | null
    }>
  return rows
    .map(usageRecordCleanupRowFromCatalog)
    .filter((row): row is UsageRecordShardCleanupRow => Boolean(row))
}

function usageRecordCleanupRowFromCatalog(row: {
  usage_id?: string | null
  created_at?: string | null
  shard_key?: string | null
  bucket_date?: string | null
  shard_id?: number | null
  file_path?: string | null
}): UsageRecordShardCleanupRow | undefined {
  const id = row.usage_id?.trim()
  const createdAt = row.created_at?.trim()
  const shardKey = row.shard_key?.trim()
  const bucketDate = row.bucket_date?.trim()
  const filePath = row.file_path?.trim()
  const shardId = Number(row.shard_id)
  if (!id || !createdAt || !shardKey || !bucketDate || !filePath || !Number.isInteger(shardId)) {
    return undefined
  }
  return {
    id,
    created_at: createdAt,
    location: {
      shardKey,
      bucketDate,
      bucketDateKey: bucketDate.replace(/-/g, ''),
      shardId,
      filePath
    }
  }
}

export function cleanupUsageStatsBucketsBefore(input: {
  accountQualityMinuteCutoffMinute: string
  minuteCutoffMinute: string
  hourlyCutoffHour: string
  dailyCutoffDate: string
  weeklyCutoffWeek: string
  monthlyCutoffMonth: string
  rankSnapshotCutoffIso: string
  windowCutoffDate: string
  windowCutoffIso: string
  limit?: number
}): UsageStatsRetentionCleanupResult {
  const database = getStatsDatabase()
  const limit = positiveLimit(input.limit)
  return {
    accountQualityMinuteStats: deleteRowsBeforeByRowid(database, 'account_quality_minute_stats', 'stat_minute', input.accountQualityMinuteCutoffMinute, limit),
    usageStatsMinute: deleteRowsBeforeByRowid(database, 'usage_stats_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageModelMinute: deleteRowsBeforeByRowid(database, 'usage_model_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageErrorMinute: deleteRowsBeforeByRowid(database, 'usage_error_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageLatencyMinute: deleteRowsBeforeByRowid(database, 'usage_latency_minute', 'stat_minute', input.minuteCutoffMinute, limit),
    usageStatsDaily: deleteRowsBeforeByRowid(database, 'usage_stats_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageModelDaily: deleteRowsBeforeByRowid(database, 'usage_model_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageErrorDaily: deleteRowsBeforeByRowid(database, 'usage_error_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageLatencyDaily: deleteRowsBeforeByRowid(database, 'usage_latency_daily', 'stat_date', input.dailyCutoffDate, limit),
    usageStatsHourly: deleteRowsBeforeByRowid(database, 'usage_stats_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageModelHourly: deleteRowsBeforeByRowid(database, 'usage_model_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageErrorHourly: deleteRowsBeforeByRowid(database, 'usage_error_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageLatencyHourly: deleteRowsBeforeByRowid(database, 'usage_latency_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    usageStatsWeekly: deleteRowsBeforeByRowid(database, 'usage_stats_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageModelWeekly: deleteRowsBeforeByRowid(database, 'usage_model_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageErrorWeekly: deleteRowsBeforeByRowid(database, 'usage_error_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageLatencyWeekly: deleteRowsBeforeByRowid(database, 'usage_latency_weekly', 'stat_week', input.weeklyCutoffWeek, limit),
    usageStatsMonthly: deleteRowsBeforeByRowid(database, 'usage_stats_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageModelMonthly: deleteRowsBeforeByRowid(database, 'usage_model_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageErrorMonthly: deleteRowsBeforeByRowid(database, 'usage_error_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    usageLatencyMonthly: deleteRowsBeforeByRowid(database, 'usage_latency_monthly', 'stat_month', input.monthlyCutoffMonth, limit),
    authorizationTeamUsageSummaryDaily: deleteRowsBeforeByRowid(database, 'authorization_team_usage_summary_daily', 'stat_date', input.dailyCutoffDate, limit),
    authorizationTeamUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_team_usage_range_windows', 'end_date', input.windowCutoffDate, limit),
    authorizationUserUsageSummaryDaily: deleteRowsBeforeByRowid(database, 'authorization_user_usage_summary_daily', 'stat_date', input.dailyCutoffDate, limit),
    authorizationUserUsageRangeWindows: deleteRowsBeforeByRowid(database, 'authorization_user_usage_range_windows', 'end_date', input.windowCutoffDate, limit),
    usageRankSnapshots: deleteRowsBeforeByRowid(database, 'usage_rank_snapshots', 'snapshot_at', input.rankSnapshotCutoffIso, limit),
    usageOverviewSummaryWindows: deleteRowsBeforeByRowid(database, 'usage_overview_summary_windows', 'end_date', input.windowCutoffDate, limit),
    usageOverviewTrendWindows: deleteRowsBeforeByRowid(database, 'usage_overview_trend_windows', 'end_date', input.windowCutoffDate, limit),
    usageModelRankWindows: deleteRowsBeforeByRowid(database, 'usage_model_rank_windows', 'end_date', input.windowCutoffDate, limit),
    usageErrorRankWindows: deleteRowsBeforeByRowid(database, 'usage_error_rank_windows', 'end_date', input.windowCutoffDate, limit),
    aiPerformanceSummaryWindows: deleteRowsBeforeByRowid(database, 'ai_performance_summary_windows', 'end_date', input.windowCutoffDate, limit),
    usageQuotaHourlyWindows: deleteRowsBeforeByRowid(database, 'usage_quota_hourly_windows', 'updated_at', input.windowCutoffIso, limit),
    usageScopeRangeWindows: deleteRowsBeforeByRowid(database, 'usage_scope_range_windows', 'end_date', input.windowCutoffDate, limit),
    clientIpUsageRangeWindows: deleteRowsBeforeByRowid(database, 'client_ip_usage_range_windows', 'end_date', input.windowCutoffDate, limit),
    accountUsageSnapshots: deleteRowsBeforeByRowid(database, 'account_usage_snapshots', 'updated_at', input.windowCutoffIso, limit)
  }
}

export function cleanupSystemMetricsBefore(input: { samplesCutoffIso: string; hourlyCutoffHour: string; trendWindowCutoffDate: string; limit?: number }): SystemMetricsRetentionCleanupResult {
  const database = getStatsDatabase()
  const limit = positiveLimit(input.limit)
  return {
    systemMetricsSamples: deleteRowsBeforeByRowid(database, 'system_metrics_samples', 'sampled_at', input.samplesCutoffIso, limit),
    systemMetricsHourly: deleteRowsBeforeByRowid(database, 'system_metrics_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    systemMetricsTrendWindows: deleteRowsBeforeByRowid(database, 'system_metrics_trend_windows', 'end_date', input.trendWindowCutoffDate, limit),
    processEventLoopSamples: deleteRowsBeforeByRowid(database, 'process_event_loop_samples', 'sampled_at', input.samplesCutoffIso, limit),
    processEventLoopHourly: deleteRowsBeforeByRowid(database, 'process_event_loop_hourly', 'stat_hour', input.hourlyCutoffHour, limit),
    processEventLoopTrendWindows: deleteRowsBeforeByRowid(database, 'process_event_loop_trend_windows', 'end_date', input.trendWindowCutoffDate, limit)
  }
}

export function cleanupModelCheckRunsBefore(cutoffCreatedAt: string, limit = 10000): ModelCheckRetentionCleanupResult {
  const database = getDatasetDatabase()
  const batchLimit = positiveLimit(limit)
  const rows = database
    .prepare(`
      SELECT id
      FROM model_check_runs
      WHERE created_at < ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, batchLimit) as CleanupRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) {
    return {
      modelCheckRuns: 0,
      modelCheckItems: 0
    }
  }

  return runInDatabaseTransaction(() => {
    let modelCheckItems = 0
    let modelCheckRuns = 0
    for (const chunk of chunkValues(ids, 900)) {
      const placeholders = sqlPlaceholders(chunk.length)
      modelCheckItems += changed(database.prepare(`DELETE FROM model_check_items WHERE run_id IN (${placeholders})`).run(...chunk))
      modelCheckRuns += changed(database.prepare(`DELETE FROM model_check_runs WHERE id IN (${placeholders})`).run(...chunk))
    }
    return {
      modelCheckRuns,
      modelCheckItems
    }
  }, database)
}

export function cleanupExpiredSystemSessions(expiredBefore = nowIso(), limit = 1000): number {
  const batchLimit = Math.max(1, Math.trunc(limit))
  return changed(getBusinessDatabase().prepare(`
    DELETE FROM system_sessions
    WHERE rowid IN (
      SELECT rowid
      FROM system_sessions
      WHERE expires_at < ?
      ORDER BY expires_at ASC, rowid ASC
      LIMIT ?
    )
  `).run(expiredBefore, batchLimit))
}

export async function cleanupExpiredSystemSessionsAsync(expiredBefore = nowIso(), limit = 1000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cleanupExpiredSystemSessions(expiredBefore, limit)
  }
  const batchLimit = Math.max(1, Math.trunc(limit))
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const result = await client.execute(`
    DELETE FROM ${client.dialect.qualifyTable('juhe_business', 'system_sessions')}
    WHERE ctid IN (
      SELECT ctid
      FROM ${client.dialect.qualifyTable('juhe_business', 'system_sessions')}
      WHERE expires_at < ?
      ORDER BY expires_at ASC, ctid ASC
      LIMIT ?
    )
  `, [expiredBefore, batchLimit])
  return changed(result)
}

function deleteUsageRecordShardRows(rows: UsageRecordShardCleanupRow[]): number {
  let deletedRows = 0
  const processedCatalogIds: string[] = []
  const rowsByShard = new Map<string, UsageRecordShardCleanupRow[]>()
  for (const row of rows) {
    rowsByShard.set(row.location.shardKey, [...(rowsByShard.get(row.location.shardKey) ?? []), row])
  }
  for (const shardRows of rowsByShard.values()) {
    const ids = shardRows.map((row) => row.id).filter(Boolean)
    if (ids.length === 0) continue
    const database = getUsageRecordShardDatabase(shardRows[0].location)
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      for (const chunk of chunkValues(ids, 900)) {
        const placeholders = sqlPlaceholders(chunk.length)
        const existingIds = database
          .prepare(`SELECT id FROM usage_records WHERE id IN (${placeholders})`)
          .all(...chunk)
          .map((row) => String((row as { id?: unknown }).id ?? ''))
          .filter(Boolean)
        if (existingIds.length === 0) continue
        const existingPlaceholders = sqlPlaceholders(existingIds.length)
        deletedRows += changed(database.prepare(`DELETE FROM usage_records WHERE id IN (${existingPlaceholders})`).run(...existingIds))
      }
      commitDatabaseTransaction(database, transactionStarted)
      processedCatalogIds.push(...ids)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
  deleteUsageRecordShardEntries(processedCatalogIds)
  return deletedRows
}

function positiveLimit(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 10000
}

function assertSqliteDataRetention(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error(`高性能模式禁止调用 SQLite 数据保留清理入口：${operation}`)
  }
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}
