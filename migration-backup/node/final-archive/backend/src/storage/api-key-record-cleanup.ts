import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, getStatsDatabase, isSqliteDatabaseLocked, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import { deletePostgresUsageRecordCatalogRowsByUsageIds } from './usage-record-catalog-cleanup.js'
import { deleteUsageRecordShardEntries, getUsageRecordShardDatabase, listUsageRecordShardLocationsForApiKey, type UsageRecordShardLocation } from './usage-record-shards.js'
import { refreshUsageQuotaHourlyWindowsCache, refreshUsageRankSnapshots, subtractPostgresUsageStatsRows } from './usage-stats.repository.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'

export interface DeletedApiKeyRecordCleanupTarget {
  apiKeyId: string
  systemAccountId: string
}

export interface DeletedApiKeyRecordCleanupResult extends DeletedApiKeyRecordCleanupTarget {
  deletedRows: number
  hasMore: boolean
  blockedReason?: string
  safetyCursorCreatedAt?: string
  safetyCursorId?: string
}

export interface PendingDeletedApiKeyRecordCleanupSummary {
  attempted: number
  completed: number
  deferred: number
  failed: number
  deletedRows: number
}

export interface DeletedApiKeyRecordCleanupQueueSummary {
  pendingTargets: number
  blockedTargets: number
  failedTargets: number
  oldestCreatedAt?: string
  lastAttemptAt?: string
}

export interface DeletedApiKeyRecordCleanupQueueTarget extends DeletedApiKeyRecordCleanupTarget {
  createdAt: string
  updatedAt: string
  attemptCount: number
  lastAttemptAt?: string
  lastBlockedReason?: string
  lastErrorMessage?: string
}

type PendingDeletedApiKeyRecordCleanupTargetRow = {
  api_key_id?: string | null
  system_account_id?: string | null
}

type DeletedApiKeyRecordCleanupQueueTargetRow = PendingDeletedApiKeyRecordCleanupTargetRow & {
  created_at?: string | null
  updated_at?: string | null
  attempt_count?: number | null
  last_attempt_at?: string | null
  last_blocked_reason?: string | null
  last_error_message?: string | null
}

type DeletedApiKeyRecordCleanupQueueSummaryRow = {
  pending_targets?: number | null
  blocked_targets?: number | null
  failed_targets?: number | null
  oldest_created_at?: string | null
  last_attempt_at?: string | null
}

type ApiKeyUsageShardRow = UsageStatsRecordRow & {
  location: UsageRecordShardLocation
  source_shard_key: string
}

type UsageRecordCleanupDeductionRow = {
  stats_subtracted_at?: string | null
}

interface ApiKeyUsageShardBatch {
  rows: ApiKeyUsageShardRow[]
  hasMoreCoveredRows: boolean
  hasUncoveredRows: boolean
}

interface ApiKeyUsageCleanupStepResult {
  deletedRows: number
  usageBatch: ApiKeyUsageShardBatch
  blockedReason?: string
}

interface ApiKeyDatasetCleanupStepResult {
  deletedRows: number
}

export interface DeletedApiKeyRecordStatsCleanupInput {
  target: DeletedApiKeyRecordCleanupTarget
  rows: Array<UsageStatsRecordRow & { source_shard_key: string }>
  updatedAt: string
  shardDeleted?: boolean
}

export type DeletedApiKeyRecordStatsCleanupWriter = (input: DeletedApiKeyRecordStatsCleanupInput) => Promise<void>

const apiKeyScopeStatsTables = [
  'usage_stats_totals',
  'usage_stats_minute',
  'usage_stats_hourly',
  'usage_stats_daily',
  'usage_stats_weekly',
  'usage_stats_monthly',
  'usage_latency_minute',
  'usage_latency_hourly',
  'usage_latency_daily',
  'usage_latency_weekly',
  'usage_latency_monthly',
  'usage_rank_snapshots',
  'usage_quota_hourly_windows',
  'usage_scope_range_windows'
] as const
const deletedApiKeyRecordCleanupBatchLimit = 100
const deletedApiKeyRecordCleanupShardLimit = 16
const usageRecordCleanupRequiredCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
const postgresUsageRecordCleanupDeductionShardKey = 'postgres'

export function registerDeletedApiKeyRecordCleanupTarget(input: DeletedApiKeyRecordCleanupTarget): void {
  assertSqliteApiKeyRecordCleanup('registerDeletedApiKeyRecordCleanupTarget')
  upsertDeletedApiKeyRecordCleanupTarget(getDatasetDatabase(), input, nowIso())
}

export async function registerDeletedApiKeyRecordCleanupTargetAsync(input: DeletedApiKeyRecordCleanupTarget): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    registerDeletedApiKeyRecordCleanupTarget(input)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await registerDeletedApiKeyRecordCleanupTargetInClientAsync(client, input)
}

export async function registerDeletedApiKeyRecordCleanupTargetInClientAsync(
  client: DatabaseClient,
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt = nowIso()
): Promise<void> {
  await upsertDeletedApiKeyRecordCleanupTargetAsync(client, input, updatedAt)
}

export function cleanupPendingDeletedApiKeyRecordTargets(limit = 50): PendingDeletedApiKeyRecordCleanupSummary {
  assertSqliteApiKeyRecordCleanup('cleanupPendingDeletedApiKeyRecordTargets')
  const targets = listDeletedApiKeyRecordCleanupTargets(Math.max(1, Math.trunc(limit)))
  const summary: PendingDeletedApiKeyRecordCleanupSummary = {
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0
  }
  for (const target of targets) {
    summary.attempted += 1
    try {
      const result = cleanupDeletedApiKeyRelatedRecordData(target)
      summary.deletedRows += result.deletedRows
      if (result.hasMore || result.blockedReason) {
        summary.deferred += 1
      } else {
        summary.completed += 1
      }
    } catch (error) {
      summary.failed += 1
      markDeletedApiKeyRecordCleanupTargetError(getDatasetDatabase(), target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export async function cleanupPendingDeletedApiKeyRecordTargetsAsync(
  limit = 50,
  statsWriter?: DeletedApiKeyRecordStatsCleanupWriter
): Promise<PendingDeletedApiKeyRecordCleanupSummary> {
  const targets = runtimeConfig.databaseDriver === 'postgres'
    ? await listDeletedApiKeyRecordCleanupTargetsAsync(Math.max(1, Math.trunc(limit)))
    : listDeletedApiKeyRecordCleanupTargets(Math.max(1, Math.trunc(limit)))
  const summary: PendingDeletedApiKeyRecordCleanupSummary = {
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0
  }
  for (const target of targets) {
    summary.attempted += 1
    try {
      const result = await cleanupDeletedApiKeyRelatedRecordDataAsync(target, statsWriter)
      summary.deletedRows += result.deletedRows
      if (result.hasMore || result.blockedReason) {
        summary.deferred += 1
      } else {
        summary.completed += 1
      }
    } catch (error) {
      summary.failed += 1
      await markDeletedApiKeyRecordCleanupTargetErrorAsync(target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export function listDeletedApiKeyRecordCleanupTargets(limit = 50): DeletedApiKeyRecordCleanupTarget[] {
  assertSqliteApiKeyRecordCleanup('listDeletedApiKeyRecordCleanupTargets')
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT api_key_id, system_account_id
      FROM api_key_record_cleanup_targets
      ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as PendingDeletedApiKeyRecordCleanupTargetRow[]
  return rows
    .map((row) => ({
      apiKeyId: String(row.api_key_id ?? ''),
      systemAccountId: String(row.system_account_id ?? '')
    }))
    .filter((row) => row.apiKeyId && row.systemAccountId)
}

export async function listDeletedApiKeyRecordCleanupTargetsAsync(limit = 50): Promise<DeletedApiKeyRecordCleanupTarget[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listDeletedApiKeyRecordCleanupTargets(limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<PendingDeletedApiKeyRecordCleanupTargetRow>(`
    SELECT api_key_id, system_account_id
    FROM juhe_dataset.api_key_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  return rows
    .map((row) => ({
      apiKeyId: String(row.api_key_id ?? ''),
      systemAccountId: String(row.system_account_id ?? '')
    }))
    .filter((row) => row.apiKeyId && row.systemAccountId)
}

export function getDeletedApiKeyRecordCleanupQueueSummary(): DeletedApiKeyRecordCleanupQueueSummary {
  assertSqliteApiKeyRecordCleanup('getDeletedApiKeyRecordCleanupQueueSummary')
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        COUNT(*) AS pending_targets,
        SUM(CASE WHEN last_blocked_reason IS NOT NULL THEN 1 ELSE 0 END) AS blocked_targets,
        SUM(CASE WHEN last_error_message IS NOT NULL THEN 1 ELSE 0 END) AS failed_targets,
        MIN(created_at) AS oldest_created_at,
        MAX(last_attempt_at) AS last_attempt_at
      FROM api_key_record_cleanup_targets
    `)
    .get() as DeletedApiKeyRecordCleanupQueueSummaryRow | undefined
  return {
    pendingTargets: Number(row?.pending_targets ?? 0),
    blockedTargets: Number(row?.blocked_targets ?? 0),
    failedTargets: Number(row?.failed_targets ?? 0),
    oldestCreatedAt: optionalText(row?.oldest_created_at),
    lastAttemptAt: optionalText(row?.last_attempt_at)
  }
}

export function listDeletedApiKeyRecordCleanupQueueTargets(limit = 50): DeletedApiKeyRecordCleanupQueueTarget[] {
  assertSqliteApiKeyRecordCleanup('listDeletedApiKeyRecordCleanupQueueTargets')
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT
        api_key_id,
        system_account_id,
        created_at,
        updated_at,
        attempt_count,
        last_attempt_at,
        last_blocked_reason,
        last_error_message
      FROM api_key_record_cleanup_targets
      ORDER BY
        CASE
          WHEN last_error_message IS NOT NULL THEN 0
          WHEN last_blocked_reason IS NOT NULL THEN 1
          ELSE 2
        END ASC,
        COALESCE(last_attempt_at, created_at) ASC,
        created_at ASC,
        api_key_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as DeletedApiKeyRecordCleanupQueueTargetRow[]
  return rows
    .map((row) => ({
      apiKeyId: String(row.api_key_id ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      createdAt: String(row.created_at ?? ''),
      updatedAt: String(row.updated_at ?? ''),
      attemptCount: Number(row.attempt_count ?? 0),
      lastAttemptAt: optionalText(row.last_attempt_at),
      lastBlockedReason: optionalText(row.last_blocked_reason),
      lastErrorMessage: optionalText(row.last_error_message)
    }))
    .filter((row) => row.apiKeyId && row.systemAccountId && row.createdAt && row.updatedAt)
}

export function cleanupDeletedApiKeyRelatedRecordData(input: DeletedApiKeyRecordCleanupTarget): DeletedApiKeyRecordCleanupResult {
  assertSqliteApiKeyRecordCleanup('cleanupDeletedApiKeyRelatedRecordData')
  const cleanup = cleanupDeletedApiKeyRelatedRecordDataCore(input)
  return cleanup.result
}

export async function cleanupDeletedApiKeyRelatedRecordDataAsync(
  input: DeletedApiKeyRecordCleanupTarget,
  statsWriter?: DeletedApiKeyRecordStatsCleanupWriter
): Promise<DeletedApiKeyRecordCleanupResult> {
  const cleanup = await cleanupDeletedApiKeyRelatedRecordDataCoreAsync(input, statsWriter)
  return cleanup.result
}

function cleanupDeletedApiKeyRelatedRecordDataCore(input: DeletedApiKeyRecordCleanupTarget): {
  result: DeletedApiKeyRecordCleanupResult
  batchLimit: number
} {
  const database = getDatasetDatabase()
  const statsDatabase = getStatsDatabase()
  const updatedAt = nowIso()
  upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
  const batchLimit = deletedApiKeyRecordCleanupBatchLimit
  try {
    const usageCleanup = cleanupDeletedApiKeyUsageData(statsDatabase, input, updatedAt, batchLimit)
    const datasetCleanup = cleanupDeletedApiKeyDatasetRecordData(database, input, batchLimit)
    let blockedReason = usageCleanup.blockedReason
    let hasUsageMore = true
    if (blockedReason) {
      hasUsageMore = true
    } else {
      try {
        hasUsageMore = hasApiKeyUsageRecords(input)
      } catch (error) {
        if (!isSqliteDatabaseLocked(error)) {
          throw error
        }
        blockedReason = apiKeyCleanupSqliteBusyBlockedReason()
        hasUsageMore = true
      }
    }
    let hasMore = hasUsageMore
    if (!blockedReason && !hasMore) {
      blockedReason = cleanupDeletedApiKeyFinalStats(statsDatabase, input)
      hasMore = Boolean(blockedReason)
    }
    if (!blockedReason && !hasMore) {
      blockedReason = refreshDeletedApiKeyDerivedWindowsIfNeeded(input, true)
      hasMore = Boolean(blockedReason)
    }
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows: usageCleanup.deletedRows + datasetCleanup.deletedRows,
      hasMore,
      blockedReason: blockedReason ?? (hasMore
        ? apiKeyCleanupPendingReason({
          hasMoreCoveredRows: usageCleanup.usageBatch.hasMoreCoveredRows,
          hasUncoveredRows: usageCleanup.usageBatch.hasUncoveredRows
        })
        : undefined)
    }
    if (result.hasMore || result.blockedReason) {
      markDeletedApiKeyRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedApiKeyRecordCleanupTarget(database, input)
    }
    return { result, batchLimit }
  } catch (error) {
    markDeletedApiKeyRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

async function cleanupDeletedApiKeyRelatedRecordDataCoreAsync(
  input: DeletedApiKeyRecordCleanupTarget,
  statsWriter?: DeletedApiKeyRecordStatsCleanupWriter
): Promise<{
  result: DeletedApiKeyRecordCleanupResult
  batchLimit: number
}> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupDeletedApiKeyRelatedRecordDataCorePostgresAsync(input)
  }
  if (!statsWriter) {
    return cleanupDeletedApiKeyRelatedRecordDataCore(input)
  }
  const database = getDatasetDatabase()
  const updatedAt = nowIso()
  upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
  const batchLimit = deletedApiKeyRecordCleanupBatchLimit
  try {
    const usageCleanup = selectDeletedApiKeyUsageData(input, batchLimit)
    const rowsToDelete = usageCleanup.usageBatch.rows.slice(0, batchLimit)
    let blockedReason = usageCleanup.blockedReason
    if (!blockedReason && rowsToDelete.length > 0) {
      await statsWriter({
        target: input,
        rows: rowsToDelete.map(apiKeyUsageShardStatsCleanupRow),
        updatedAt,
        shardDeleted: false
      })
    }
    const deletedUsageRows = blockedReason ? 0 : deleteApiKeyUsageRows(rowsToDelete, input)
    const datasetCleanup = cleanupDeletedApiKeyDatasetRecordData(database, input, batchLimit)
    if (!blockedReason && rowsToDelete.length > 0) {
      await statsWriter({
        target: input,
        rows: rowsToDelete.map(apiKeyUsageShardStatsCleanupRow),
        updatedAt,
        shardDeleted: true
      })
    }
    let hasUsageMore = true
    if (blockedReason) {
      hasUsageMore = true
    } else {
      try {
        hasUsageMore = hasApiKeyUsageRecords(input)
      } catch (error) {
        if (!isSqliteDatabaseLocked(error)) {
          throw error
        }
        blockedReason = apiKeyCleanupSqliteBusyBlockedReason()
        hasUsageMore = true
      }
    }
    let hasMore = hasUsageMore || Boolean(blockedReason)
    if (!blockedReason && !hasMore) {
      await statsWriter({
        target: input,
        rows: [],
        updatedAt,
        shardDeleted: true
      })
      hasMore = false
    }
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows: deletedUsageRows + datasetCleanup.deletedRows,
      hasMore,
      blockedReason: blockedReason ?? (hasMore
        ? apiKeyCleanupPendingReason({
          hasMoreCoveredRows: usageCleanup.usageBatch.hasMoreCoveredRows,
          hasUncoveredRows: usageCleanup.usageBatch.hasUncoveredRows
        })
        : undefined)
    }
    if (result.hasMore || result.blockedReason) {
      markDeletedApiKeyRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedApiKeyRecordCleanupTarget(database, input)
    }
    return { result, batchLimit }
  } catch (error) {
    markDeletedApiKeyRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

async function cleanupDeletedApiKeyRelatedRecordDataCorePostgresAsync(
  input: DeletedApiKeyRecordCleanupTarget
): Promise<{
  result: DeletedApiKeyRecordCleanupResult
  batchLimit: number
}> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const updatedAt = nowIso()
  const batchLimit = deletedApiKeyRecordCleanupBatchLimit
  await upsertDeletedApiKeyRecordCleanupTargetAsync(client, input, updatedAt)
  try {
    let deletedRows = 0
    await client.transaction(async (tx) => {
      deletedRows += await deletePostgresApiKeyUsageDataBatch(tx, input, batchLimit, updatedAt)
    })
    const hasUsageMore = await hasPostgresApiKeyUsageRecords(client, input)
    let hasMore = hasUsageMore
    if (!hasMore) {
      await cleanupDeletedApiKeyFinalStatsAsync(client, input)
    }
    hasMore = hasMore || await hasPostgresDeletedApiKeyStatsRows(client, input)
    const result: DeletedApiKeyRecordCleanupResult = {
      ...input,
      deletedRows,
      hasMore,
      blockedReason: hasMore
        ? apiKeyCleanupPendingReason({
          hasMoreCoveredRows: hasUsageMore,
          hasUncoveredRows: false
        })
        : undefined
    }
    if (result.hasMore || result.blockedReason) {
      await markDeletedApiKeyRecordCleanupTargetDeferredAsync(client, input, result.blockedReason ?? '等待高性能模式后续批次清理', updatedAt)
    } else {
      await clearDeletedApiKeyRecordCleanupTargetAsync(client, input)
    }
    return { result, batchLimit }
  } catch (error) {
    await markDeletedApiKeyRecordCleanupTargetErrorAsync(input, errorMessage(error), nowIso())
    throw error
  }
}

function cleanupDeletedApiKeyUsageData(
  statsDatabase: DatabaseSync,
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string,
  batchLimit: number
): ApiKeyUsageCleanupStepResult {
  let usageBatch = emptyApiKeyUsageShardBatch()
  let deletedRows = 0
  try {
    usageBatch = selectApiKeyUsageRowsCoveredByShardCursors(statsDatabase, input, batchLimit)
    const rowsToDelete = usageBatch.rows.slice(0, batchLimit)
    if (rowsToDelete.length > 0) {
      subtractApiKeyUsageRowsOnce(statsDatabase, rowsToDelete, input, updatedAt)
    }
    deletedRows += deleteApiKeyUsageRows(rowsToDelete, input)
    markApiKeyUsageCleanupRowsDeleted(statsDatabase, rowsToDelete, input, updatedAt)
    return { deletedRows, usageBatch }
  } catch (error) {
    if (!isSqliteDatabaseLocked(error)) {
      throw error
    }
    return {
      deletedRows,
      usageBatch,
      blockedReason: apiKeyCleanupSqliteBusyBlockedReason()
    }
  }
}

function selectDeletedApiKeyUsageData(
  input: DeletedApiKeyRecordCleanupTarget,
  batchLimit: number
): ApiKeyUsageCleanupStepResult {
  let usageBatch = emptyApiKeyUsageShardBatch()
  try {
    usageBatch = selectApiKeyUsageRowsCoveredByShardCursors(getStatsDatabase(), input, batchLimit)
    return { deletedRows: 0, usageBatch }
  } catch (error) {
    if (!isSqliteDatabaseLocked(error)) {
      throw error
    }
    return {
      deletedRows: 0,
      usageBatch,
      blockedReason: apiKeyCleanupSqliteBusyBlockedReason()
    }
  }
}

function cleanupDeletedApiKeyDatasetRecordData(
  database: DatabaseSync,
  input: DeletedApiKeyRecordCleanupTarget,
  batchLimit: number
): ApiKeyDatasetCleanupStepResult {
  let deletedRows = 0
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    commitDatabaseTransaction(database, transactionStarted)
    return { deletedRows }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function cleanupDeletedApiKeyFinalStats(statsDatabase: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): string | undefined {
  let transactionStarted = false
  try {
    transactionStarted = beginDatabaseTransaction(statsDatabase)
    deleteApiKeyScopeStatsRows(statsDatabase, input)
    deleteApiKeyUsageCleanupDeductions(statsDatabase, input)
    commitDatabaseTransaction(statsDatabase, transactionStarted)
    return undefined
  } catch (error) {
    rollbackDatabaseTransaction(statsDatabase, transactionStarted)
    if (isSqliteDatabaseLocked(error)) {
      return apiKeyCleanupSqliteBusyBlockedReason()
    }
    throw error
  }
}

function emptyApiKeyUsageShardBatch(): ApiKeyUsageShardBatch {
  return {
    rows: [],
    hasMoreCoveredRows: false,
    hasUncoveredRows: false
  }
}

function apiKeyCleanupPendingReason(input: {
  hasMoreCoveredRows: boolean
  hasUncoveredRows: boolean
}): string {
  return input.hasMoreCoveredRows
    ? '仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试'
    : input.hasUncoveredRows
    ? '仍有使用记录尚未被对应分片统计安全游标覆盖，已保留待后台重试清理'
    : '仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理'
}

function apiKeyCleanupSqliteBusyBlockedReason(): string {
  return 'SQLite 正在执行其他写入，已保留已删除 API Key 关联数据清理目标，等待后台重试'
}

function upsertDeletedApiKeyRecordCleanupTarget(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, updatedAt: string): void {
  database.prepare(`
    INSERT INTO api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(api_key_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      updated_at = excluded.updated_at
  `).run(input.apiKeyId, input.systemAccountId, updatedAt, updatedAt)
}

function markDeletedApiKeyRecordCleanupTargetDeferred(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, blockedReason: string, updatedAt: string): void {
  database.prepare(`
    UPDATE api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `).run(updatedAt, blockedReason, updatedAt, input.apiKeyId, input.systemAccountId)
}

function markDeletedApiKeyRecordCleanupTargetError(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget, message: string, updatedAt: string): void {
  database.prepare(`
    UPDATE api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `).run(updatedAt, message, updatedAt, input.apiKeyId, input.systemAccountId)
}

function clearDeletedApiKeyRecordCleanupTarget(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  database.prepare('DELETE FROM api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?')
    .run(input.apiKeyId, input.systemAccountId)
}

async function upsertDeletedApiKeyRecordCleanupTargetAsync(
  client: DatabaseClient,
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_dataset.api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(api_key_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      updated_at = excluded.updated_at
  `, [input.apiKeyId, input.systemAccountId, updatedAt, updatedAt])
}

async function markDeletedApiKeyRecordCleanupTargetDeferredAsync(
  client: DatabaseClient,
  input: DeletedApiKeyRecordCleanupTarget,
  blockedReason: string,
  updatedAt: string
): Promise<void> {
  await client.execute(`
    UPDATE juhe_dataset.api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `, [updatedAt, blockedReason, updatedAt, input.apiKeyId, input.systemAccountId])
}

async function markDeletedApiKeyRecordCleanupTargetErrorAsync(
  input: DeletedApiKeyRecordCleanupTarget,
  message: string,
  updatedAt: string
): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    markDeletedApiKeyRecordCleanupTargetError(getDatasetDatabase(), input, message, updatedAt)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.execute(`
    UPDATE juhe_dataset.api_key_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE api_key_id = ? AND system_account_id = ?
  `, [updatedAt, message, updatedAt, input.apiKeyId, input.systemAccountId])
}

async function clearDeletedApiKeyRecordCleanupTargetAsync(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<void> {
  await client.execute('DELETE FROM juhe_dataset.api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?', [
    input.apiKeyId,
    input.systemAccountId
  ])
}

function hasApiKeyUsageRecords(input: DeletedApiKeyRecordCleanupTarget): boolean {
  return listUsageRecordShardLocationsForApiKey(input.apiKeyId, input.systemAccountId, 1).locations.length > 0
}

function selectApiKeyUsageRowsCoveredByShardCursors(
  statsDatabase: DatabaseSync,
  input: DeletedApiKeyRecordCleanupTarget,
  limit: number
): ApiKeyUsageShardBatch {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const queryLimit = batchLimit + 1
  const rows: ApiKeyUsageShardRow[] = []
  let hasUncoveredRows = false
  const shardWindow = listUsageRecordShardLocationsForApiKey(input.apiKeyId, input.systemAccountId, deletedApiKeyRecordCleanupShardLimit)
  for (const location of shardWindow.locations) {
    const shardDatabase = getUsageRecordShardDatabase(location)
    const cursor = usageStatsShardCursor(statsDatabase, location.shardKey)
    if (!cursor) {
      hasUncoveredRows = true
      continue
    }
    const uncovered = shardDatabase
      .prepare(`
        SELECT id
        FROM usage_records
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND (created_at > ? OR (created_at = ? AND id > ?))
        LIMIT 1
      `)
      .get(input.apiKeyId, input.systemAccountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId) as unknown as { id?: string } | undefined
    if (uncovered?.id) {
      hasUncoveredRows = true
    }
    rows.push(...(shardDatabase.prepare(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM usage_records
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND (created_at < ? OR (created_at = ? AND id <= ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `).all(input.apiKeyId, input.systemAccountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, queryLimit) as unknown as UsageStatsRecordRow[])
      .map((row) => ({
        ...row,
        location,
        source_shard_key: location.shardKey
      })))
  }
  const sortedRows = rows
    .sort((left, right) => left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id))
  return {
    rows: sortedRows.slice(0, queryLimit),
    hasMoreCoveredRows: sortedRows.length > batchLimit || shardWindow.hasMore,
    hasUncoveredRows
  }
}

function usageStatsShardCursor(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const rows = database
    .prepare(`
      SELECT job_name, cursor_created_at, cursor_id
      FROM stats_job_state
      WHERE scope_type = 'usage_shard'
        AND scope_id = ?
        AND job_name IN (${sqlPlaceholders(usageRecordCleanupRequiredCursorJobNames.length)})
        AND cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL
      ORDER BY cursor_created_at ASC, cursor_id ASC
    `)
    .all(shardKey, ...usageRecordCleanupRequiredCursorJobNames) as unknown as Array<{
      job_name?: string | null
      cursor_created_at?: string | null
      cursor_id?: string | null
    }>
  const jobNames = new Set(rows.map((row) => row.job_name?.trim()).filter(Boolean))
  if (usageRecordCleanupRequiredCursorJobNames.some((jobName) => !jobNames.has(jobName))) {
    return undefined
  }
  const row = rows[0]
  const cursorCreatedAt = row?.cursor_created_at?.trim()
  const cursorId = row?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function usageStatsRecordForCleanup(row: ApiKeyUsageShardRow): UsageStatsRecordRow {
  const { location: _location, ...record } = row
  return record
}

function apiKeyUsageShardStatsCleanupRow(row: ApiKeyUsageShardRow): UsageStatsRecordRow & { source_shard_key: string } {
  return {
    ...usageStatsRecordForCleanup(row),
    source_shard_key: row.source_shard_key
  }
}

export function cleanupDeletedApiKeyRecordStatsData(input: DeletedApiKeyRecordStatsCleanupInput): void {
  assertSqliteApiKeyRecordCleanup('cleanupDeletedApiKeyRecordStatsData')
  const database = getStatsDatabase()
  const rows = input.rows.map((row) => ({
    ...row,
    location: {
      shardKey: row.source_shard_key,
      databasePath: '',
      filePath: ''
    }
  } as unknown as ApiKeyUsageShardRow))
  subtractApiKeyUsageRowsOnce(database, rows, input.target, input.updatedAt)
  if (input.shardDeleted) {
    markApiKeyUsageCleanupRowsDeleted(database, rows, input.target, input.updatedAt)
  }
  if (rows.length === 0) {
    cleanupDeletedApiKeyFinalStats(database, input.target)
    const blockedReason = refreshDeletedApiKeyDerivedWindowsIfNeeded(input.target, true)
    if (blockedReason) {
      throw new Error(blockedReason)
    }
  }
}

function subtractApiKeyUsageRowsOnce(
  database: DatabaseSync,
  rows: ApiKeyUsageShardRow[],
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): void {
  if (rows.length === 0) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const insertDeductionStatement = database.prepare(`
      INSERT INTO usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, excluded.account_id),
        updated_at = excluded.updated_at
    `)
    const findDeductionStatement = database.prepare(`
      SELECT stats_subtracted_at
      FROM usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ?
      LIMIT 1
    `)
    const markSubtractedStatement = database.prepare(`
      UPDATE usage_record_cleanup_deductions
      SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?),
          updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
    `)

    for (const row of rows) {
      insertDeductionStatement.run(
        row.id,
        input.apiKeyId,
        row.account_id ?? null,
        input.systemAccountId,
        row.source_shard_key,
        JSON.stringify(usageStatsRecordForCleanup(row)),
        updatedAt,
        updatedAt
      )
      const deduction = findDeductionStatement.get(row.id, row.source_shard_key) as UsageRecordCleanupDeductionRow | undefined
      if (deduction?.stats_subtracted_at) {
        continue
      }
      subtractUsageStatsRecord(database, usageStatsRecordForCleanup(row), updatedAt)
      markSubtractedStatement.run(updatedAt, updatedAt, row.id, row.source_shard_key)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function deleteApiKeyUsageRows(rows: ApiKeyUsageShardRow[], input: DeletedApiKeyRecordCleanupTarget): number {
  let deletedRows = 0
  const rowsByShard = new Map<string, ApiKeyUsageShardRow[]>()
  for (const row of rows) {
    rowsByShard.set(row.location.shardKey, [...(rowsByShard.get(row.location.shardKey) ?? []), row])
  }
  for (const shardRows of rowsByShard.values()) {
    const database = getUsageRecordShardDatabase(shardRows[0].location)
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      const deleteStatement = database.prepare('DELETE FROM usage_records WHERE id = ? AND api_key_id = ? AND system_account_id = ?')
      for (const row of shardRows) {
        deletedRows += changed(deleteStatement.run(row.id, input.apiKeyId, input.systemAccountId))
      }
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
  deleteUsageRecordShardEntries(rows.map((row) => row.id))
  return deletedRows
}

function markApiKeyUsageCleanupRowsDeleted(
  database: DatabaseSync,
  rows: ApiKeyUsageShardRow[],
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): void {
  if (rows.length === 0) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const statement = database.prepare(`
      UPDATE usage_record_cleanup_deductions
      SET shard_deleted_at = COALESCE(shard_deleted_at, ?),
          updated_at = ?
      WHERE usage_id = ? AND source_shard_key = ?
    `)
    for (const row of rows) {
      statement.run(updatedAt, updatedAt, row.id, row.source_shard_key)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

async function deletePostgresApiKeyUsageDataBatch(
  client: DatabaseClient,
  input: DeletedApiKeyRecordCleanupTarget,
  limit: number,
  updatedAt: string
): Promise<number> {
  const cursor = await postgresUsageRecordCleanupFloorCursor(client)
  if (!cursor) return 0
  const rows = await client.query<UsageStatsRecordRow>(`
    SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
    FROM juhe_usage.usage_records
    WHERE api_key_id = ?
      AND system_account_id = ?
      AND (created_at < ? OR (created_at = ? AND id <= ?))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [input.apiKeyId, input.systemAccountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, Math.max(1, Math.trunc(limit))])
  const usageIds = uniqueNonEmpty(rows.map((row) => row.id))
  if (!usageIds.length) return 0
  let deletedRows = 0
  await client.transaction(async (tx) => {
    await subtractPostgresApiKeyUsageRowsOnce(tx, rows, input, updatedAt)
    await deletePostgresUsageRecordCatalogRowsByUsageIds(tx, usageIds)
    const result = await deletePostgresUsageRecordsByPartitionKeys(tx, rows)
    deletedRows = changed(result)
    await markPostgresUsageCleanupRowsDeleted(tx, usageIds, updatedAt)
  })
  return deletedRows
}

async function subtractPostgresApiKeyUsageRowsOnce(
  client: DatabaseClient,
  rows: UsageStatsRecordRow[],
  input: DeletedApiKeyRecordCleanupTarget,
  updatedAt: string
): Promise<void> {
  const rowsToSubtract: UsageStatsRecordRow[] = []
  for (const row of rows) {
    await client.execute(`
      INSERT INTO juhe_stats.usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        api_key_id = EXCLUDED.api_key_id,
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, EXCLUDED.account_id),
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `, [
      row.id,
      input.apiKeyId,
      row.account_id ?? null,
      input.systemAccountId,
      postgresUsageRecordCleanupDeductionShardKey,
      JSON.stringify(row),
      updatedAt,
      updatedAt
    ])
    const deduction = await client.one<UsageRecordCleanupDeductionRow>(`
      SELECT stats_subtracted_at
      FROM juhe_stats.usage_record_cleanup_deductions
      WHERE usage_id = ? AND source_shard_key = ?
      LIMIT 1
      FOR UPDATE
    `, [row.id, postgresUsageRecordCleanupDeductionShardKey])
    if (!deduction?.stats_subtracted_at) {
      rowsToSubtract.push(row)
    }
  }
  if (rowsToSubtract.length === 0) return
  await subtractPostgresUsageStatsRows(client, rowsToSubtract, updatedAt)
  await markPostgresUsageCleanupRowsSubtracted(client, rowsToSubtract.map((row) => row.id), updatedAt)
}

async function markPostgresUsageCleanupRowsSubtracted(client: DatabaseClient, usageIds: string[], updatedAt: string): Promise<void> {
  if (usageIds.length === 0) return
  await client.execute(`
    UPDATE juhe_stats.usage_record_cleanup_deductions
    SET stats_subtracted_at = COALESCE(stats_subtracted_at, ?),
        updated_at = ?
    WHERE usage_id = ANY(?::text[])
      AND source_shard_key = ?
  `, [updatedAt, updatedAt, usageIds, postgresUsageRecordCleanupDeductionShardKey])
}

async function markPostgresUsageCleanupRowsDeleted(client: DatabaseClient, usageIds: string[], updatedAt: string): Promise<void> {
  if (usageIds.length === 0) return
  await client.execute(`
    UPDATE juhe_stats.usage_record_cleanup_deductions
    SET shard_deleted_at = COALESCE(shard_deleted_at, ?),
        updated_at = ?
    WHERE usage_id = ANY(?::text[])
      AND source_shard_key = ?
  `, [updatedAt, updatedAt, usageIds, postgresUsageRecordCleanupDeductionShardKey])
}

async function postgresUsageRecordCleanupFloorCursor(client: DatabaseClient): Promise<{ cursorCreatedAt: string; cursorId: string } | undefined> {
  const rows = await client.query<{ job_name?: string | null; cursor_created_at?: string | null; cursor_id?: string | null }>(`
    SELECT job_name, cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY(?::text[])
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

async function hasPostgresApiKeyUsageRecords(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<boolean> {
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM juhe_usage.usage_records
    WHERE api_key_id = ?
      AND system_account_id = ?
    LIMIT 1
  `, [input.apiKeyId, input.systemAccountId])
  return Boolean(row?.found)
}

function deleteApiKeyScopeStatsRows(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  for (const tableName of apiKeyScopeStatsTables) {
    database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`)
      .run(input.systemAccountId, input.apiKeyId)
  }
  database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'api_key' AND scope_id = ?")
    .run(input.apiKeyId)
}

async function cleanupDeletedApiKeyFinalStatsAsync(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<void> {
  await client.transaction(async (tx) => {
    await deletePostgresApiKeyScopeStatsRows(tx, input)
    await deletePostgresApiKeyUsageCleanupDeductions(tx, input)
  })
}

async function deletePostgresApiKeyScopeStatsRows(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<void> {
  for (const tableName of apiKeyScopeStatsTables) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`, [
      input.systemAccountId,
      input.apiKeyId
    ])
  }
  await client.execute("DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'api_key' AND scope_id = ?", [input.apiKeyId])
}

async function hasPostgresDeletedApiKeyStatsRows(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<boolean> {
  for (const tableName of apiKeyScopeStatsTables) {
    if (await postgresRowsExist(client, `
      SELECT 1
      FROM juhe_stats.${tableName}
      WHERE system_account_id = ?
        AND scope_type = 'api_key'
        AND scope_id = ?
      LIMIT 1
    `, [input.systemAccountId, input.apiKeyId])) {
      return true
    }
  }
  return await postgresRowsExist(client, `
    SELECT 1
    FROM juhe_stats.usage_record_cleanup_deductions
    WHERE api_key_id = ?
      AND system_account_id = ?
    LIMIT 1
  `, [input.apiKeyId, input.systemAccountId])
}

function deleteApiKeyUsageCleanupDeductions(database: DatabaseSync, input: DeletedApiKeyRecordCleanupTarget): void {
  database.prepare('DELETE FROM usage_record_cleanup_deductions WHERE api_key_id = ? AND system_account_id = ?')
    .run(input.apiKeyId, input.systemAccountId)
}

async function deletePostgresApiKeyUsageCleanupDeductions(client: DatabaseClient, input: DeletedApiKeyRecordCleanupTarget): Promise<void> {
  await client.execute('DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE api_key_id = ? AND system_account_id = ?', [
    input.apiKeyId,
    input.systemAccountId
  ])
}

function refreshDeletedApiKeyDerivedWindowsIfNeeded(input: DeletedApiKeyRecordCleanupTarget, shouldRefresh: boolean): string | undefined {
  if (!shouldRefresh) return undefined
  try {
    refreshUsageQuotaHourlyWindowsCache()
    refreshUsageRankSnapshots()
    return undefined
  } catch (error) {
    if (isSqliteDatabaseLocked(error)) {
      return apiKeyCleanupSqliteBusyBlockedReason()
    }
    const database = getDatasetDatabase()
    const updatedAt = nowIso()
    upsertDeletedApiKeyRecordCleanupTarget(database, input, updatedAt)
    markDeletedApiKeyRecordCleanupTargetError(database, input, `已删除 API Key 衍生统计窗口刷新失败：${errorMessage(error)}`, updatedAt)
    throw error
  }
}


async function postgresRowsExist(client: DatabaseClient, sql: string, params: unknown[]): Promise<boolean> {
  const rows = await client.query<{ found?: number }>(sql, params)
  return rows.length > 0
}

function assertSqliteApiKeyRecordCleanup(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error(`高性能模式禁止调用 SQLite API Key 记录清理入口：${operation}`)
  }
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '已删除 API Key 关联数据清理失败'
}

function uniqueNonEmpty(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

async function deletePostgresUsageRecordsByPartitionKeys(client: DatabaseClient, rows: UsageStatsRecordRow[]): Promise<{ changes?: number | bigint }> {
  const keys = rows
    .map((row) => ({ createdAt: row.created_at?.trim(), id: row.id?.trim() }))
    .filter((row): row is { createdAt: string; id: string } => Boolean(row.createdAt && row.id))
  if (!keys.length) return { changes: 0 }
  const placeholders = keys.map(() => '(?, ?)').join(', ')
  return client.execute(`
    DELETE FROM juhe_usage.usage_records
    WHERE (created_at, id) IN (${placeholders})
  `, keys.flatMap((row) => [row.createdAt, row.id]))
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}
