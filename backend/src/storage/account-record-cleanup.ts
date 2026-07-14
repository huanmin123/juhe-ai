import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { cleanupUnreferencedAuditPayloadBlobs, cleanupUnreferencedAuditPayloadBlobsAsync } from './audit-log-payload-blobs.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, getStatsDatabase, isSqliteDatabaseLocked, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { deletePostgresUsageRecordCatalogRowsByUsageIds } from './usage-record-catalog-cleanup.js'
import { deleteUsageRecordShardEntries, getUsageRecordShardDatabase, listUsageRecordShardLocationsForAccount, type UsageRecordShardLocation } from './usage-record-shards.js'
import { refreshUsageQuotaHourlyWindowsCache, refreshUsageRankSnapshots, subtractPostgresUsageStatsRows } from './usage-stats.repository.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'

export interface DeletedAccountRecordCleanupTarget {
  accountId: string
  systemAccountId: string
  relatedAccountIds?: string[]
  authorizationIds?: string[]
  teamScopeIds?: string[]
}

export type DeletedAccountDetachedStatsCleanupTarget = DeletedAccountRecordCleanupTarget

export interface DeletedAccountRecordCleanupResult extends DeletedAccountRecordCleanupTarget {
  deletedRows: number
  hasMore: boolean
  blockedReason?: string
}

export interface PendingDeletedAccountRecordCleanupSummary {
  attempted: number
  completed: number
  deferred: number
  failed: number
  deletedRows: number
}

type PendingDeletedAccountRecordCleanupTargetRow = {
  account_id?: string | null
  system_account_id?: string | null
  related_account_ids_json?: string | null
  authorization_ids_json?: string | null
  team_scope_ids_json?: string | null
}

type AccountUsageShardRow = UsageStatsRecordRow & {
  location: UsageRecordShardLocation
  source_shard_key: string
}

type UsageRecordCleanupDeductionRow = {
  stats_subtracted_at?: string | null
}

interface AccountUsageShardBatch {
  rows: AccountUsageShardRow[]
  hasMoreCoveredRows: boolean
  hasUncoveredRows: boolean
}

interface AccountUsageCleanupStepResult {
  deletedRows: number
  usageBatch: AccountUsageShardBatch
  blockedReason?: string
}

interface AccountDatasetCleanupStepResult {
  deletedRows: number
  hasAuditMore: boolean
  hasModelCheckMore: boolean
}

export interface DeletedAccountRecordStatsCleanupInput {
  target: DeletedAccountRecordCleanupTarget
  rows: Array<UsageStatsRecordRow & { source_shard_key: string }>
  updatedAt: string
  shardDeleted?: boolean
}

export type DeletedAccountRecordStatsCleanupWriter = (input: DeletedAccountRecordStatsCleanupInput) => Promise<void>

const accountScopeStatsTables = [
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

const deletedAccountRecordCleanupBatchLimit = 100
const deletedAccountRecordCleanupShardLimit = 16
const usageRecordCleanupRequiredCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
const postgresUsageRecordCleanupDeductionShardKey = 'postgres'

export function registerDeletedAccountRecordCleanupTarget(input: DeletedAccountRecordCleanupTarget): void {
  assertSqliteAccountRecordCleanup('registerDeletedAccountRecordCleanupTarget')
  upsertDeletedAccountRecordCleanupTarget(getDatasetDatabase(), input, nowIso())
}

export async function registerDeletedAccountRecordCleanupTargetAsync(input: DeletedAccountRecordCleanupTarget): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    registerDeletedAccountRecordCleanupTarget(input)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await upsertDeletedAccountRecordCleanupTargetAsync(client, input, nowIso())
}

export function cleanupPendingDeletedAccountRecordTargets(limit = 50): PendingDeletedAccountRecordCleanupSummary {
  assertSqliteAccountRecordCleanup('cleanupPendingDeletedAccountRecordTargets')
  const targets = listDeletedAccountRecordCleanupTargets(Math.max(1, Math.trunc(limit)))
  const summary: PendingDeletedAccountRecordCleanupSummary = {
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0
  }
  for (const target of targets) {
    summary.attempted += 1
    try {
      const result = cleanupDeletedAccountRelatedRecordData(target)
      summary.deletedRows += result.deletedRows
      if (result.hasMore || result.blockedReason) {
        summary.deferred += 1
      } else {
        summary.completed += 1
      }
    } catch (error) {
      summary.failed += 1
      markDeletedAccountRecordCleanupTargetError(getDatasetDatabase(), target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export async function cleanupPendingDeletedAccountRecordTargetsAsync(
  limit = 50,
  statsWriter?: DeletedAccountRecordStatsCleanupWriter
): Promise<PendingDeletedAccountRecordCleanupSummary> {
  const targets = runtimeConfig.databaseDriver === 'postgres'
    ? await listDeletedAccountRecordCleanupTargetsAsync(Math.max(1, Math.trunc(limit)))
    : listDeletedAccountRecordCleanupTargets(Math.max(1, Math.trunc(limit)))
  const summary: PendingDeletedAccountRecordCleanupSummary = {
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0
  }
  for (const target of targets) {
    summary.attempted += 1
    try {
      const result = await cleanupDeletedAccountRelatedRecordDataAsync(target, statsWriter)
      summary.deletedRows += result.deletedRows
      if (result.hasMore || result.blockedReason) {
        summary.deferred += 1
      } else {
        summary.completed += 1
      }
    } catch (error) {
      summary.failed += 1
      await markDeletedAccountRecordCleanupTargetErrorAsync(target, errorMessage(error), nowIso())
    }
  }
  return summary
}

export function listDeletedAccountRecordCleanupTargets(limit = 50): DeletedAccountRecordCleanupTarget[] {
  assertSqliteAccountRecordCleanup('listDeletedAccountRecordCleanupTargets')
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT account_id, system_account_id,
        related_account_ids_json, authorization_ids_json, team_scope_ids_json
      FROM account_record_cleanup_targets
      ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, account_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as PendingDeletedAccountRecordCleanupTargetRow[]
  return rows
    .map((row) => ({
      accountId: String(row.account_id ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      relatedAccountIds: parseStringArrayJson(row.related_account_ids_json),
      authorizationIds: parseStringArrayJson(row.authorization_ids_json),
      teamScopeIds: parseStringArrayJson(row.team_scope_ids_json)
    }))
    .filter((row) => row.accountId && row.systemAccountId)
}

export async function listDeletedAccountRecordCleanupTargetsAsync(limit = 50): Promise<DeletedAccountRecordCleanupTarget[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listDeletedAccountRecordCleanupTargets(limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<PendingDeletedAccountRecordCleanupTargetRow>(`
    SELECT account_id, system_account_id,
      related_account_ids_json, authorization_ids_json, team_scope_ids_json
    FROM juhe_dataset.account_record_cleanup_targets
    ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, account_id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  return rows
    .map((row) => ({
      accountId: String(row.account_id ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      relatedAccountIds: parseStringArrayJson(row.related_account_ids_json),
      authorizationIds: parseStringArrayJson(row.authorization_ids_json),
      teamScopeIds: parseStringArrayJson(row.team_scope_ids_json)
    }))
    .filter((row) => row.accountId && row.systemAccountId)
}

export function hasDeletedAccountRelatedRecordData(input: DeletedAccountRecordCleanupTarget): boolean {
  assertSqliteAccountRecordCleanup('hasDeletedAccountRelatedRecordData')
  if (hasDeletedAccountRecordCleanupTarget(input)) {
    return true
  }
  if (hasAccountUsageRecords(input)) {
    return true
  }
  const datasetDatabase = getDatasetDatabase()
  if (hasAccountAuditData(datasetDatabase, input) || hasAccountModelCheckRuns(datasetDatabase, input)) {
    return true
  }
  return hasDeletedAccountStatsRows(getStatsDatabase(), input)
}

export function cleanupDeletedAccountDetachedStats(input: DeletedAccountDetachedStatsCleanupTarget): void {
  assertSqliteAccountRecordCleanup('cleanupDeletedAccountDetachedStats')
  const database = getStatsDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    deleteAccountScopeStatsRows(database, input, input.authorizationIds ?? [], input.teamScopeIds ?? [])
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  const refreshBlockedReason = refreshDeletedAccountDerivedWindowsIfNeeded(input, true)
  if (refreshBlockedReason) {
    throw new Error(refreshBlockedReason)
  }
}

export function cleanupDeletedAccountRelatedRecordData(input: DeletedAccountRecordCleanupTarget): DeletedAccountRecordCleanupResult {
  assertSqliteAccountRecordCleanup('cleanupDeletedAccountRelatedRecordData')
  const cleanup = cleanupDeletedAccountRelatedRecordDataCore(input)
  cleanupAuditPayloadBlobsBestEffort(cleanup.batchLimit)
  return cleanup.result
}

export async function cleanupDeletedAccountRelatedRecordDataAsync(
  input: DeletedAccountRecordCleanupTarget,
  statsWriter?: DeletedAccountRecordStatsCleanupWriter
): Promise<DeletedAccountRecordCleanupResult> {
  const cleanup = await cleanupDeletedAccountRelatedRecordDataCoreAsync(input, statsWriter)
  await cleanupAuditPayloadBlobsBestEffortAsync(cleanup.batchLimit)
  return cleanup.result
}

function cleanupDeletedAccountRelatedRecordDataCore(input: DeletedAccountRecordCleanupTarget): {
  result: DeletedAccountRecordCleanupResult
  batchLimit: number
} {
  const database = getDatasetDatabase()
  const statsDatabase = getStatsDatabase()
  const updatedAt = nowIso()
  upsertDeletedAccountRecordCleanupTarget(database, input, updatedAt)
  const batchLimit = deletedAccountRecordCleanupBatchLimit
  try {
    const usageCleanup = cleanupDeletedAccountUsageData(statsDatabase, input, updatedAt, batchLimit)
    const datasetCleanup = cleanupDeletedAccountDatasetRecordData(database, input, batchLimit)
    let blockedReason = usageCleanup.blockedReason
    let hasUsageMore = true
    if (blockedReason) {
      hasUsageMore = true
    } else {
      try {
        hasUsageMore = hasAccountUsageRecords(input)
      } catch (error) {
        if (!isSqliteDatabaseLocked(error)) {
          throw error
        }
        blockedReason = accountCleanupSqliteBusyBlockedReason()
        hasUsageMore = true
      }
    }
    const hasAuditMore = datasetCleanup.hasAuditMore
    const hasModelCheckMore = datasetCleanup.hasModelCheckMore
    let hasMore = hasUsageMore || hasAuditMore || hasModelCheckMore
    if (!blockedReason && !hasMore) {
      blockedReason = cleanupDeletedAccountFinalStats(statsDatabase, input)
      hasMore = Boolean(blockedReason)
    }
    if (!blockedReason && !hasMore) {
      blockedReason = refreshDeletedAccountDerivedWindowsIfNeeded(input, true)
      hasMore = Boolean(blockedReason)
    }
    const result: DeletedAccountRecordCleanupResult = {
      ...input,
      deletedRows: usageCleanup.deletedRows + datasetCleanup.deletedRows,
      hasMore,
      blockedReason: blockedReason ?? (hasMore
        ? accountCleanupPendingReason({
          hasAuditMore,
          hasModelCheckMore,
          hasMoreCoveredRows: usageCleanup.usageBatch.hasMoreCoveredRows,
          hasUncoveredRows: usageCleanup.usageBatch.hasUncoveredRows
        })
        : undefined)
    }
    if (result.hasMore || result.blockedReason) {
      markDeletedAccountRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedAccountRecordCleanupTarget(database, input)
    }
    return { result, batchLimit }
  } catch (error) {
    markDeletedAccountRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

async function cleanupDeletedAccountRelatedRecordDataCoreAsync(
  input: DeletedAccountRecordCleanupTarget,
  statsWriter?: DeletedAccountRecordStatsCleanupWriter
): Promise<{
  result: DeletedAccountRecordCleanupResult
  batchLimit: number
}> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupDeletedAccountRelatedRecordDataCorePostgresAsync(input)
  }
  if (!statsWriter) {
    return cleanupDeletedAccountRelatedRecordDataCore(input)
  }
  const database = getDatasetDatabase()
  const updatedAt = nowIso()
  upsertDeletedAccountRecordCleanupTarget(database, input, updatedAt)
  const batchLimit = deletedAccountRecordCleanupBatchLimit
  try {
    const usageCleanup = selectDeletedAccountUsageData(input, batchLimit)
    const rowsToDelete = usageCleanup.usageBatch.rows.slice(0, batchLimit)
    let blockedReason = usageCleanup.blockedReason
    if (!blockedReason && rowsToDelete.length > 0) {
      await statsWriter({
        target: input,
        rows: rowsToDelete.map(accountUsageShardStatsCleanupRow),
        updatedAt,
        shardDeleted: false
      })
    }
    const deletedUsageRows = blockedReason ? 0 : deleteAccountUsageRows(rowsToDelete, input)
    const datasetCleanup = cleanupDeletedAccountDatasetRecordData(database, input, batchLimit)
    if (!blockedReason && rowsToDelete.length > 0) {
      await statsWriter({
        target: input,
        rows: rowsToDelete.map(accountUsageShardStatsCleanupRow),
        updatedAt,
        shardDeleted: true
      })
    }
    let hasUsageMore = true
    if (blockedReason) {
      hasUsageMore = true
    } else {
      try {
        hasUsageMore = hasAccountUsageRecords(input)
      } catch (error) {
        if (!isSqliteDatabaseLocked(error)) {
          throw error
        }
        blockedReason = accountCleanupSqliteBusyBlockedReason()
        hasUsageMore = true
      }
    }
    const hasAuditMore = datasetCleanup.hasAuditMore
    const hasModelCheckMore = datasetCleanup.hasModelCheckMore
    let hasMore = hasUsageMore || hasAuditMore || hasModelCheckMore || Boolean(blockedReason)
    if (!blockedReason && !hasMore) {
      await statsWriter({
        target: input,
        rows: [],
        updatedAt,
        shardDeleted: true
      })
      hasMore = false
    }
    const result: DeletedAccountRecordCleanupResult = {
      ...input,
      deletedRows: deletedUsageRows + datasetCleanup.deletedRows,
      hasMore,
      blockedReason: blockedReason ?? (hasMore
        ? accountCleanupPendingReason({
          hasAuditMore,
          hasModelCheckMore,
          hasMoreCoveredRows: usageCleanup.usageBatch.hasMoreCoveredRows,
          hasUncoveredRows: usageCleanup.usageBatch.hasUncoveredRows
        })
        : undefined)
    }
    if (result.hasMore || result.blockedReason) {
      markDeletedAccountRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedAccountRecordCleanupTarget(database, input)
    }
    return { result, batchLimit }
  } catch (error) {
    markDeletedAccountRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

async function cleanupDeletedAccountRelatedRecordDataCorePostgresAsync(
  input: DeletedAccountRecordCleanupTarget
): Promise<{
  result: DeletedAccountRecordCleanupResult
  batchLimit: number
}> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const updatedAt = nowIso()
  const batchLimit = deletedAccountRecordCleanupBatchLimit
  await upsertDeletedAccountRecordCleanupTargetAsync(client, input, updatedAt)
  try {
    let deletedRows = 0
    await client.transaction(async (tx) => {
      deletedRows += await deletePostgresAccountUsageDataBatch(tx, input, batchLimit, updatedAt)
      deletedRows += await deletePostgresAccountAuditDataBatch(tx, input, batchLimit)
      deletedRows += await deletePostgresAccountModelCheckRunsBatch(tx, input, batchLimit)
    })

    const [hasUsageMore, hasAuditMore, hasModelCheckMore] = await Promise.all([
      hasPostgresAccountUsageRecords(client, input),
      hasPostgresAccountAuditData(client, input),
      hasPostgresAccountModelCheckRuns(client, input)
    ])
    let hasMore = hasUsageMore || hasAuditMore || hasModelCheckMore
    if (!hasMore) {
      await cleanupDeletedAccountFinalStatsAsync(client, input)
    }
    hasMore = hasMore || await hasPostgresDeletedAccountStatsRows(client, input)

    const result: DeletedAccountRecordCleanupResult = {
      ...input,
      deletedRows,
      hasMore,
      blockedReason: hasMore
        ? accountCleanupPendingReason({
          hasAuditMore,
          hasModelCheckMore,
          hasMoreCoveredRows: hasUsageMore,
          hasUncoveredRows: false
        })
        : undefined
    }
    if (result.hasMore || result.blockedReason) {
      await markDeletedAccountRecordCleanupTargetDeferredAsync(client, input, result.blockedReason ?? '等待高性能模式后续批次清理', updatedAt)
    } else {
      await clearDeletedAccountRecordCleanupTargetAsync(client, input)
    }
    return { result, batchLimit }
  } catch (error) {
    await markDeletedAccountRecordCleanupTargetErrorAsync(input, errorMessage(error), nowIso())
    throw error
  }
}

function cleanupDeletedAccountUsageData(
  statsDatabase: DatabaseSync,
  input: DeletedAccountRecordCleanupTarget,
  updatedAt: string,
  batchLimit: number
): AccountUsageCleanupStepResult {
  let usageBatch = emptyAccountUsageShardBatch()
  let deletedRows = 0
  try {
    usageBatch = selectAccountUsageRowsCoveredByShardCursors(statsDatabase, input, batchLimit)
    const rowsToDelete = usageBatch.rows.slice(0, batchLimit)
    if (rowsToDelete.length > 0) {
      subtractAccountUsageRowsOnce(statsDatabase, rowsToDelete, input, updatedAt)
    }
    deletedRows += deleteAccountUsageRows(rowsToDelete, input)
    markAccountUsageCleanupRowsDeleted(statsDatabase, rowsToDelete, updatedAt)
    return { deletedRows, usageBatch }
  } catch (error) {
    if (!isSqliteDatabaseLocked(error)) {
      throw error
    }
    return {
      deletedRows,
      usageBatch,
      blockedReason: accountCleanupSqliteBusyBlockedReason()
    }
  }
}

function selectDeletedAccountUsageData(
  input: DeletedAccountRecordCleanupTarget,
  batchLimit: number
): AccountUsageCleanupStepResult {
  let usageBatch = emptyAccountUsageShardBatch()
  try {
    usageBatch = selectAccountUsageRowsCoveredByShardCursors(getStatsDatabase(), input, batchLimit)
    return { deletedRows: 0, usageBatch }
  } catch (error) {
    if (!isSqliteDatabaseLocked(error)) {
      throw error
    }
    return {
      deletedRows: 0,
      usageBatch,
      blockedReason: accountCleanupSqliteBusyBlockedReason()
    }
  }
}

function cleanupDeletedAccountDatasetRecordData(
  database: DatabaseSync,
  input: DeletedAccountRecordCleanupTarget,
  batchLimit: number
): AccountDatasetCleanupStepResult {
  let deletedRows = 0
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    deletedRows += deleteAccountAuditDataBatch(database, input, batchLimit)
    deletedRows += deleteAccountModelCheckRunsBatch(database, input, batchLimit)
    const hasAuditMore = hasAccountAuditData(database, input)
    const hasModelCheckMore = hasAccountModelCheckRuns(database, input)
    commitDatabaseTransaction(database, transactionStarted)
    return {
      deletedRows,
      hasAuditMore,
      hasModelCheckMore
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function cleanupDeletedAccountFinalStats(statsDatabase: DatabaseSync, input: DeletedAccountRecordCleanupTarget): string | undefined {
  let transactionStarted = false
  try {
    transactionStarted = beginDatabaseTransaction(statsDatabase)
    deleteAccountScopeStatsRows(statsDatabase, input, input.authorizationIds ?? [], input.teamScopeIds ?? [])
    deleteAccountUsageCleanupDeductions(statsDatabase, input)
    commitDatabaseTransaction(statsDatabase, transactionStarted)
    return undefined
  } catch (error) {
    rollbackDatabaseTransaction(statsDatabase, transactionStarted)
    if (isSqliteDatabaseLocked(error)) {
      return accountCleanupSqliteBusyBlockedReason()
    }
    throw error
  }
}

function emptyAccountUsageShardBatch(): AccountUsageShardBatch {
  return {
    rows: [],
    hasMoreCoveredRows: false,
    hasUncoveredRows: false
  }
}

function accountCleanupPendingReason(input: {
  hasAuditMore: boolean
  hasModelCheckMore: boolean
  hasMoreCoveredRows: boolean
  hasUncoveredRows: boolean
}): string {
  return input.hasAuditMore
    ? '仍有已删除 AI 账户的原始审计记录待后续批次清理，已保留待后台重试'
    : input.hasModelCheckMore
    ? '仍有已删除 AI 账户的模型检测记录待后续批次清理，已保留待后台重试'
    : input.hasMoreCoveredRows
    ? '仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试'
    : input.hasUncoveredRows
    ? '仍有使用记录尚未被对应分片统计安全游标覆盖，已保留待后台重试清理'
    : '仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理'
}

function accountCleanupSqliteBusyBlockedReason(): string {
  return 'SQLite 正在执行其他写入，已保留已删除 AI 账户关联数据清理目标，等待后台重试'
}

function upsertDeletedAccountRecordCleanupTarget(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, updatedAt: string): void {
  database.prepare(`
    INSERT INTO account_record_cleanup_targets (
      account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      related_account_ids_json = CASE
        WHEN excluded.related_account_ids_json <> '[]' THEN excluded.related_account_ids_json
        ELSE account_record_cleanup_targets.related_account_ids_json
      END,
      authorization_ids_json = CASE
        WHEN excluded.authorization_ids_json <> '[]' THEN excluded.authorization_ids_json
        ELSE account_record_cleanup_targets.authorization_ids_json
      END,
      team_scope_ids_json = CASE
        WHEN excluded.team_scope_ids_json <> '[]' THEN excluded.team_scope_ids_json
        ELSE account_record_cleanup_targets.team_scope_ids_json
      END,
      updated_at = excluded.updated_at
  `).run(
    input.accountId,
    input.systemAccountId,
    stringArrayJson(input.relatedAccountIds),
    stringArrayJson(input.authorizationIds),
    stringArrayJson(input.teamScopeIds),
    updatedAt,
    updatedAt
  )
}

function markDeletedAccountRecordCleanupTargetDeferred(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, blockedReason: string, updatedAt: string): void {
  database.prepare(`
    UPDATE account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `).run(updatedAt, blockedReason, updatedAt, input.accountId, input.systemAccountId)
}

function markDeletedAccountRecordCleanupTargetError(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, message: string, updatedAt: string): void {
  database.prepare(`
    UPDATE account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `).run(updatedAt, message, updatedAt, input.accountId, input.systemAccountId)
}

function clearDeletedAccountRecordCleanupTarget(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): void {
  database.prepare('DELETE FROM account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ?')
    .run(input.accountId, input.systemAccountId)
}

async function upsertDeletedAccountRecordCleanupTargetAsync(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  updatedAt: string
): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_dataset.account_record_cleanup_targets (
      account_id, system_account_id, related_account_ids_json, authorization_ids_json, team_scope_ids_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      related_account_ids_json = CASE
        WHEN excluded.related_account_ids_json <> '[]' THEN excluded.related_account_ids_json
        ELSE account_record_cleanup_targets.related_account_ids_json
      END,
      authorization_ids_json = CASE
        WHEN excluded.authorization_ids_json <> '[]' THEN excluded.authorization_ids_json
        ELSE account_record_cleanup_targets.authorization_ids_json
      END,
      team_scope_ids_json = CASE
        WHEN excluded.team_scope_ids_json <> '[]' THEN excluded.team_scope_ids_json
        ELSE account_record_cleanup_targets.team_scope_ids_json
      END,
      updated_at = excluded.updated_at
  `, [
    input.accountId,
    input.systemAccountId,
    stringArrayJson(input.relatedAccountIds),
    stringArrayJson(input.authorizationIds),
    stringArrayJson(input.teamScopeIds),
    updatedAt,
    updatedAt
  ])
}

async function markDeletedAccountRecordCleanupTargetDeferredAsync(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  blockedReason: string,
  updatedAt: string
): Promise<void> {
  await client.execute(`
    UPDATE juhe_dataset.account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = ?,
        last_error_message = NULL,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `, [updatedAt, blockedReason, updatedAt, input.accountId, input.systemAccountId])
}

async function markDeletedAccountRecordCleanupTargetErrorAsync(
  input: DeletedAccountRecordCleanupTarget,
  message: string,
  updatedAt: string
): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    markDeletedAccountRecordCleanupTargetError(getDatasetDatabase(), input, message, updatedAt)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.execute(`
    UPDATE juhe_dataset.account_record_cleanup_targets
    SET attempt_count = attempt_count + 1,
        last_attempt_at = ?,
        last_blocked_reason = NULL,
        last_error_message = ?,
        updated_at = ?
    WHERE account_id = ? AND system_account_id = ?
  `, [updatedAt, message, updatedAt, input.accountId, input.systemAccountId])
}

async function clearDeletedAccountRecordCleanupTargetAsync(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<void> {
  await client.execute('DELETE FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ?', [
    input.accountId,
    input.systemAccountId
  ])
}

function hasDeletedAccountRecordCleanupTarget(input: DeletedAccountRecordCleanupTarget): boolean {
  const row = getDatasetDatabase()
    .prepare('SELECT account_id FROM account_record_cleanup_targets WHERE account_id = ? AND system_account_id = ? LIMIT 1')
    .get(input.accountId, input.systemAccountId) as unknown as { account_id?: string } | undefined
  return Boolean(row?.account_id)
}

function deletedAccountCleanupAccountIds(input: DeletedAccountRecordCleanupTarget): string[] {
  return uniqueNonEmpty([input.accountId, ...(input.relatedAccountIds ?? [])])
}

function hasAccountUsageRecords(input: DeletedAccountRecordCleanupTarget): boolean {
  return deletedAccountCleanupAccountIds(input)
    .some((accountId) => listUsageRecordShardLocationsForAccount(accountId, 1).locations.length > 0)
}

function selectAccountUsageRowsCoveredByShardCursors(
  statsDatabase: DatabaseSync,
  input: DeletedAccountRecordCleanupTarget,
  limit: number
): AccountUsageShardBatch {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const queryLimit = batchLimit + 1
  const rows: AccountUsageShardRow[] = []
  let hasUncoveredRows = false
  let hasMoreCoveredRows = false
  for (const accountId of deletedAccountCleanupAccountIds(input)) {
    const shardWindow = listUsageRecordShardLocationsForAccount(accountId, deletedAccountRecordCleanupShardLimit)
    hasMoreCoveredRows = hasMoreCoveredRows || shardWindow.hasMore
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
          WHERE account_id = ?
            AND (created_at > ? OR (created_at = ? AND id > ?))
          LIMIT 1
        `)
        .get(accountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId) as unknown as { id?: string } | undefined
      if (uncovered?.id) {
        hasUncoveredRows = true
      }
      rows.push(...(shardDatabase.prepare(`
          SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
          FROM usage_records
          WHERE account_id = ?
            AND (created_at < ? OR (created_at = ? AND id <= ?))
          ORDER BY created_at ASC, id ASC
          LIMIT ?
        `).all(accountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, queryLimit) as unknown as UsageStatsRecordRow[])
        .map((row) => ({
          ...row,
          location,
          source_shard_key: location.shardKey
        })))
    }
  }
  const sortedRows = rows
    .sort((left, right) => left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id))
  return {
    rows: sortedRows.slice(0, queryLimit),
    hasMoreCoveredRows: sortedRows.length > batchLimit || hasMoreCoveredRows,
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

function usageStatsRecordForCleanup(row: AccountUsageShardRow): UsageStatsRecordRow {
  const { location: _location, ...record } = row
  return record
}

function accountUsageShardStatsCleanupRow(row: AccountUsageShardRow): UsageStatsRecordRow & { source_shard_key: string } {
  return {
    ...usageStatsRecordForCleanup(row),
    source_shard_key: row.source_shard_key
  }
}

export function cleanupDeletedAccountRecordStatsData(input: DeletedAccountRecordStatsCleanupInput): void {
  assertSqliteAccountRecordCleanup('cleanupDeletedAccountRecordStatsData')
  const database = getStatsDatabase()
  const rows = input.rows.map((row) => ({
    ...row,
    location: {
      shardKey: row.source_shard_key,
      databasePath: '',
      filePath: ''
    }
  } as unknown as AccountUsageShardRow))
  subtractAccountUsageRowsOnce(database, rows, input.target, input.updatedAt)
  if (input.shardDeleted) {
    markAccountUsageCleanupRowsDeleted(database, rows, input.updatedAt)
  }
  if (rows.length === 0) {
    const blockedReason = cleanupDeletedAccountFinalStats(database, input.target)
    if (blockedReason) {
      throw new Error(blockedReason)
    }
    const refreshBlockedReason = refreshDeletedAccountDerivedWindowsIfNeeded(input.target, true)
    if (refreshBlockedReason) {
      throw new Error(refreshBlockedReason)
    }
  }
}

function subtractAccountUsageRowsOnce(
  database: DatabaseSync,
  rows: AccountUsageShardRow[],
  input: DeletedAccountRecordCleanupTarget,
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
        row.api_key_id ?? '',
        row.account_id ?? input.accountId,
        row.system_account_id ?? input.systemAccountId,
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

function deleteAccountUsageRows(rows: AccountUsageShardRow[], input: DeletedAccountRecordCleanupTarget): number {
  let deletedRows = 0
  const rowsByShard = new Map<string, AccountUsageShardRow[]>()
  for (const row of rows) {
    rowsByShard.set(row.location.shardKey, [...(rowsByShard.get(row.location.shardKey) ?? []), row])
  }
  for (const shardRows of rowsByShard.values()) {
    const database = getUsageRecordShardDatabase(shardRows[0].location)
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      const deleteStatement = database.prepare('DELETE FROM usage_records WHERE id = ? AND account_id = ?')
      for (const row of shardRows) {
        deletedRows += changed(deleteStatement.run(row.id, row.account_id ?? input.accountId))
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

function markAccountUsageCleanupRowsDeleted(
  database: DatabaseSync,
  rows: AccountUsageShardRow[],
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

async function deletePostgresAccountUsageDataBatch(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  limit: number,
  updatedAt: string
): Promise<number> {
  const cursor = await postgresUsageRecordCleanupFloorCursor(client)
  if (!cursor) return 0
  const accountIds = deletedAccountCleanupAccountIds(input)
  const authorizationIds = uniqueNonEmpty(input.authorizationIds ?? [])
  if (accountIds.length === 0 && authorizationIds.length === 0) return 0
  const rows = await client.query<UsageStatsRecordRow>(`
    SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
    FROM juhe_usage.usage_records
    WHERE (account_id = ANY(?::text[]) OR account_authorization_id = ANY(?::text[]))
      AND (created_at < ? OR (created_at = ? AND id <= ?))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [accountIds, authorizationIds, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, Math.max(1, Math.trunc(limit))])
  const usageIds = uniqueNonEmpty(rows.map((row) => row.id))
  if (!usageIds.length) return 0
  let deletedRows = 0
  await client.transaction(async (tx) => {
    await subtractPostgresAccountUsageRowsOnce(tx, rows, input, updatedAt)
    await deletePostgresUsageRecordCatalogRowsByUsageIds(tx, usageIds)
    const result = await deletePostgresUsageRecordsByPartitionKeys(tx, rows)
    deletedRows = changed(result)
    await markPostgresUsageCleanupRowsDeleted(tx, usageIds, updatedAt)
  })
  return deletedRows
}

async function subtractPostgresAccountUsageRowsOnce(
  client: DatabaseClient,
  rows: UsageStatsRecordRow[],
  input: DeletedAccountRecordCleanupTarget,
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
        api_key_id = COALESCE(usage_record_cleanup_deductions.api_key_id, EXCLUDED.api_key_id),
        account_id = EXCLUDED.account_id,
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `, [
      row.id,
      row.api_key_id ?? '',
      row.account_id ?? input.accountId,
      row.system_account_id ?? input.systemAccountId,
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

async function hasPostgresAccountUsageRecords(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<boolean> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  const authorizationIds = uniqueNonEmpty(input.authorizationIds ?? [])
  if (accountIds.length === 0 && authorizationIds.length === 0) return false
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM juhe_usage.usage_records
    WHERE account_id = ANY(?::text[])
      OR account_authorization_id = ANY(?::text[])
    LIMIT 1
  `, [accountIds, authorizationIds])
  return Boolean(row?.found)
}

async function hasPostgresAccountAuditData(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<boolean> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return false
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM juhe_dataset.audit_logs
    WHERE account_id = ANY(?::text[])
    UNION ALL
    SELECT 1 AS found
    FROM juhe_dataset.audit_error_groups
    WHERE account_id = ANY(?::text[])
    LIMIT 1
  `, [accountIds, accountIds])
  return Boolean(row?.found)
}

async function deletePostgresAccountAuditDataBatch(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  limit: number
): Promise<number> {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return 0
  const rows = await client.query<{ id?: string | null }>(`
    SELECT id
    FROM juhe_dataset.audit_logs
    WHERE account_id = ANY(?::text[])
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [accountIds, batchLimit])
  const auditLogIds = uniqueNonEmpty(rows.map((row) => row.id))
  let deletedRows = 0
  if (auditLogIds.length > 0) {
    deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY(?::text[])', [auditLogIds]))
    deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY(?::text[])', [auditLogIds]))
    deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY(?::text[]) AND account_id = ANY(?::text[])', [auditLogIds, accountIds]))
  }

  const groupRows = await client.query<{ id?: string | null }>(`
    SELECT id
    FROM juhe_dataset.audit_error_groups
    WHERE account_id = ANY(?::text[])
    ORDER BY updated_at ASC, id ASC
    LIMIT ?
  `, [accountIds, batchLimit])
  const groupIds = uniqueNonEmpty(groupRows.map((row) => row.id))
  if (groupIds.length > 0) {
    deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.audit_error_groups WHERE id = ANY(?::text[]) AND account_id = ANY(?::text[])', [groupIds, accountIds]))
  }
  return deletedRows
}

async function hasPostgresAccountModelCheckRuns(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<boolean> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return false
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM juhe_dataset.model_check_runs
    WHERE account_id = ANY(?::text[])
      OR (target_type = 'account' AND target_id = ANY(?::text[]))
    LIMIT 1
  `, [accountIds, accountIds])
  return Boolean(row?.found)
}

async function deletePostgresAccountModelCheckRunsBatch(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  limit: number
): Promise<number> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return 0
  const rows = await client.query<{ id?: string | null }>(`
    SELECT id
    FROM juhe_dataset.model_check_runs
    WHERE account_id = ANY(?::text[])
      OR (target_type = 'account' AND target_id = ANY(?::text[]))
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [accountIds, accountIds, Math.max(1, Math.trunc(limit))])
  const runIds = uniqueNonEmpty(rows.map((row) => row.id))
  if (!runIds.length) return 0
  let deletedRows = 0
  deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.model_check_items WHERE run_id = ANY(?::text[])', [runIds]))
  deletedRows += changed(await client.execute('DELETE FROM juhe_dataset.model_check_runs WHERE id = ANY(?::text[])', [runIds]))
  return deletedRows
}

function hasAccountAuditData(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): boolean {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return false
  const placeholders = sqlPlaceholders(accountIds.length)
  const auditLog = database
    .prepare(`SELECT id FROM audit_logs WHERE account_id IN (${placeholders}) LIMIT 1`)
    .get(...accountIds) as unknown as { id?: string } | undefined
  if (auditLog?.id) return true
  const auditErrorGroup = database
    .prepare(`SELECT id FROM audit_error_groups WHERE account_id IN (${placeholders}) LIMIT 1`)
    .get(...accountIds) as unknown as { id?: string } | undefined
  return Boolean(auditErrorGroup?.id)
}

function deleteAccountAuditDataBatch(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, limit: number): number {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return 0
  const accountPlaceholders = sqlPlaceholders(accountIds.length)
  const rows = database
    .prepare(`
      SELECT id
      FROM audit_logs
      WHERE account_id IN (${accountPlaceholders})
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(...accountIds, batchLimit) as unknown as Array<{ id?: string }>
  const auditLogIds = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  let deletedRows = 0
  if (auditLogIds.length > 0) {
    const placeholders = sqlPlaceholders(auditLogIds.length)
    deletedRows += changed(database.prepare(`DELETE FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_log_attempts WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders}) AND account_id IN (${accountPlaceholders})`).run(...auditLogIds, ...accountIds))
  }
  const groupRows = database
    .prepare(`
      SELECT id
      FROM audit_error_groups
      WHERE account_id IN (${accountPlaceholders})
      ORDER BY updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(...accountIds, batchLimit) as unknown as Array<{ id?: string }>
  const groupIds = groupRows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (groupIds.length > 0) {
    const placeholders = sqlPlaceholders(groupIds.length)
    deletedRows += changed(database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders}) AND account_id IN (${accountPlaceholders})`).run(...groupIds, ...accountIds))
  }
  return deletedRows
}

function hasAccountModelCheckRuns(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): boolean {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return false
  const placeholders = sqlPlaceholders(accountIds.length)
  const row = database
    .prepare(`SELECT id FROM model_check_runs WHERE account_id IN (${placeholders}) OR (target_type = 'account' AND target_id IN (${placeholders})) LIMIT 1`)
    .get(...accountIds, ...accountIds) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function deleteAccountModelCheckRunsBatch(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, limit: number): number {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return 0
  const placeholders = sqlPlaceholders(accountIds.length)
  const rows = database
    .prepare(`
      SELECT id
      FROM model_check_runs
      WHERE account_id IN (${placeholders})
        OR (target_type = 'account' AND target_id IN (${placeholders}))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(...accountIds, ...accountIds, batchLimit) as unknown as Array<{ id?: string }>
  const runIds = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (!runIds.length) return 0
  let deletedRows = 0
  for (const chunk of chunkValues(runIds, 100)) {
    const childPlaceholders = sqlPlaceholders(chunk.length)
    deletedRows += changed(database.prepare(`DELETE FROM model_check_items WHERE run_id IN (${childPlaceholders})`).run(...chunk))
    deletedRows += changed(database.prepare(`DELETE FROM model_check_runs WHERE id IN (${childPlaceholders})`).run(...chunk))
  }
  return deletedRows
}

function deleteAccountScopeStatsRows(
  database: DatabaseSync,
  input: DeletedAccountRecordCleanupTarget,
  authorizationIds: string[] = [],
  teamScopeIds: string[] = []
): void {
  const normalizedAuthorizationIds = uniqueNonEmpty(authorizationIds)
  const normalizedTeamScopeIds = uniqueNonEmpty(teamScopeIds)
  const accountIds = deletedAccountCleanupAccountIds(input)
  for (const tableName of accountScopeStatsTables) {
    for (const accountId of accountIds) {
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type IN ('account', 'caller_account') AND scope_id = ?`)
        .run(accountId)
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`)
        .run(`${escapeLikePrefix(accountId)}:%`)
    }
    for (const chunk of chunkValues(normalizedAuthorizationIds, 400)) {
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
        .run(...chunk)
    }
    for (const chunk of chunkValues(normalizedTeamScopeIds, 400)) {
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
        .run(...chunk)
    }
  }
  for (const accountId of accountIds) {
    database.prepare("DELETE FROM stats_job_state WHERE scope_type IN ('account', 'caller_account') AND scope_id = ?")
      .run(accountId)
    database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'")
      .run(`${escapeLikePrefix(accountId)}:%`)
  }
  for (const chunk of chunkValues(normalizedAuthorizationIds, 400)) {
    database.prepare(`DELETE FROM stats_job_state WHERE scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
  for (const chunk of chunkValues(normalizedTeamScopeIds, 400)) {
    database.prepare(`DELETE FROM stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
  for (const accountId of accountIds) {
    database.prepare('DELETE FROM account_quality_scores WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM account_quality_dirty_accounts WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM account_quality_minute_stats WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM account_usage_snapshots WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_token_integrity_windows WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_token_integrity_rounds WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_trust_window_sources WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_identity_source_features WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_paired_similarity_windows WHERE account_id = ?').run(accountId)
    database.prepare('DELETE FROM model_account_trust_results WHERE account_id = ?').run(accountId)
    deleteAccountAuthorizationReportRows(database, accountId)
  }
}

async function cleanupDeletedAccountFinalStatsAsync(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<void> {
  await client.transaction(async (tx) => {
    await deletePostgresAccountScopeStatsRows(tx, input, input.authorizationIds ?? [], input.teamScopeIds ?? [])
    await deletePostgresAccountUsageCleanupDeductions(tx, input)
  })
}

async function deletePostgresAccountScopeStatsRows(
  client: DatabaseClient,
  input: DeletedAccountRecordCleanupTarget,
  authorizationIds: string[] = [],
  teamScopeIds: string[] = []
): Promise<void> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  const normalizedAuthorizationIds = uniqueNonEmpty(authorizationIds)
  const normalizedTeamScopeIds = uniqueNonEmpty(teamScopeIds)
  for (const tableName of accountScopeStatsTables) {
    if (accountIds.length > 0) {
      await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_type IN ('account', 'caller_account') AND scope_id = ANY(?::text[])`, [accountIds])
      for (const accountId of accountIds) {
        await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`, [`${escapeLikePrefix(accountId)}:%`])
      }
    }
    for (const chunk of chunkValues(normalizedAuthorizationIds, 900)) {
      await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_type = 'account_authorization' AND scope_id = ANY(?::text[])`, [chunk])
    }
    for (const chunk of chunkValues(normalizedTeamScopeIds, 900)) {
      await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id = ANY(?::text[])`, [chunk])
    }
  }
  if (accountIds.length > 0) {
    await client.execute("DELETE FROM juhe_stats.stats_job_state WHERE scope_type IN ('account', 'caller_account') AND scope_id = ANY(?::text[])", [accountIds])
    for (const accountId of accountIds) {
      await client.execute("DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'", [`${escapeLikePrefix(accountId)}:%`])
    }
    await client.execute('DELETE FROM juhe_stats.account_quality_scores WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_token_integrity_windows WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_token_integrity_rounds WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_trust_window_sources WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_identity_source_features WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_paired_similarity_windows WHERE account_id = ANY(?::text[])', [accountIds])
    await client.execute('DELETE FROM juhe_stats.model_account_trust_results WHERE account_id = ANY(?::text[])', [accountIds])
    await deletePostgresAccountAuthorizationReportRows(client, accountIds)
  }
  for (const chunk of chunkValues(normalizedAuthorizationIds, 900)) {
    await client.execute("DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization' AND scope_id = ANY(?::text[])", [chunk])
  }
  for (const chunk of chunkValues(normalizedTeamScopeIds, 900)) {
    await client.execute("DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id = ANY(?::text[])", [chunk])
  }
}

async function hasPostgresDeletedAccountStatsRows(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<boolean> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  const authorizationIds = uniqueNonEmpty(input.authorizationIds ?? [])
  const teamScopeIds = uniqueNonEmpty(input.teamScopeIds ?? [])
  for (const tableName of accountScopeStatsTables) {
    if (accountIds.length > 0 && await postgresRowsExist(client, `
      SELECT 1
      FROM juhe_stats.${tableName}
      WHERE scope_type IN ('account', 'caller_account')
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, [accountIds])) {
      return true
    }
    if (authorizationIds.length > 0 && await postgresRowsExist(client, `
      SELECT 1
      FROM juhe_stats.${tableName}
      WHERE scope_type = 'account_authorization'
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, [authorizationIds])) {
      return true
    }
    if (teamScopeIds.length > 0 && await postgresRowsExist(client, `
      SELECT 1
      FROM juhe_stats.${tableName}
      WHERE scope_type = 'account_authorization_team'
        AND scope_id = ANY(?::text[])
      LIMIT 1
    `, [teamScopeIds])) {
      return true
    }
  }
  if (accountIds.length > 0 && await postgresRowsExist(client, `
    SELECT 1
    FROM juhe_stats.usage_record_cleanup_deductions
    WHERE account_id = ANY(?::text[])
    LIMIT 1
  `, [accountIds])) {
    return true
  }
  for (const tableName of ['model_token_integrity_windows', 'model_token_integrity_rounds', 'model_trust_window_sources', 'model_identity_source_features', 'model_paired_similarity_windows', 'model_account_trust_results']) {
    if (accountIds.length > 0 && await postgresRowsExist(client, `SELECT 1 FROM juhe_stats.${tableName} WHERE account_id = ANY(?::text[]) LIMIT 1`, [accountIds])) {
      return true
    }
  }
  return false
}

function hasDeletedAccountStatsRows(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): boolean {
  const accountIds = deletedAccountCleanupAccountIds(input)
  for (const accountId of accountIds) {
    if (hasStatsRow(database, accountScopeStatsTables, "scope_type IN ('account', 'caller_account') AND scope_id = ?", [accountId])) {
      return true
    }
    if (hasStatsRow(database, accountScopeStatsTables, "scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'", [`${escapeLikePrefix(accountId)}:%`])) {
      return true
    }
    if (singleStatsRowExists(database, 'stats_job_state', "scope_type IN ('account', 'caller_account') AND scope_id = ?", [accountId])) {
      return true
    }
    if (singleStatsRowExists(database, 'stats_job_state', "scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'", [`${escapeLikePrefix(accountId)}:%`])) {
      return true
    }
    if (singleStatsRowExists(database, 'account_quality_scores', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'account_quality_dirty_accounts', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'account_quality_minute_stats', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'account_usage_snapshots', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_token_integrity_windows', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_token_integrity_rounds', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_trust_window_sources', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_identity_source_features', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_paired_similarity_windows', 'account_id = ?', [accountId])
      || singleStatsRowExists(database, 'model_account_trust_results', 'account_id = ?', [accountId])
      || hasAccountAuthorizationReportRows(database, accountId)) {
      return true
    }
  }
  for (const chunk of chunkValues(uniqueNonEmpty(input.authorizationIds ?? []), 400)) {
    if (hasStatsRow(database, accountScopeStatsTables, `scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`, chunk)
      || singleStatsRowExists(database, 'stats_job_state', `scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`, chunk)) {
      return true
    }
  }
  for (const chunk of chunkValues(uniqueNonEmpty(input.teamScopeIds ?? []), 400)) {
    if (hasStatsRow(database, accountScopeStatsTables, `scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`, chunk)
      || singleStatsRowExists(database, 'stats_job_state', `scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`, chunk)) {
      return true
    }
  }
  for (const accountId of accountIds) {
    if (singleStatsRowExists(database, 'usage_record_cleanup_deductions', 'account_id = ?', [accountId])) {
      return true
    }
  }
  return false
}

function hasStatsRow(database: DatabaseSync, tables: readonly string[], condition: string, params: SQLInputValue[]): boolean {
  return tables.some((tableName) => singleStatsRowExists(database, tableName, condition, params))
}

function singleStatsRowExists(database: DatabaseSync, tableName: string, condition: string, params: SQLInputValue[]): boolean {
  const row = database.prepare(`SELECT 1 AS found FROM ${tableName} WHERE ${condition} LIMIT 1`)
    .get(...params) as unknown as { found?: number } | undefined
  return Boolean(row?.found)
}

function hasAccountAuthorizationReportRows(database: DatabaseSync, accountId: string): boolean {
  const reportTables = [
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows'
  ] as const
  return reportTables.some((tableName) => singleStatsRowExists(
    database,
    tableName,
    "resource_filter_type = 'account' AND resource_filter_id = ?",
    [accountId]
  ))
}

function deleteAccountAuthorizationReportRows(database: DatabaseSync, accountId: string): void {
  const reportTables = [
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows'
  ] as const
  for (const tableName of reportTables) {
    database.prepare(`DELETE FROM ${tableName} WHERE resource_filter_type = 'account' AND resource_filter_id = ?`)
      .run(accountId)
  }
}

async function deletePostgresAccountAuthorizationReportRows(client: DatabaseClient, accountIds: string[]): Promise<void> {
  const reportTables = [
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows'
  ] as const
  for (const tableName of reportTables) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE resource_filter_type = 'account' AND resource_filter_id = ANY(?::text[])`, [accountIds])
  }
}

function deleteAccountUsageCleanupDeductions(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): void {
  for (const accountId of deletedAccountCleanupAccountIds(input)) {
    database.prepare('DELETE FROM usage_record_cleanup_deductions WHERE account_id = ?')
      .run(accountId)
  }
}

async function deletePostgresAccountUsageCleanupDeductions(client: DatabaseClient, input: DeletedAccountRecordCleanupTarget): Promise<void> {
  const accountIds = deletedAccountCleanupAccountIds(input)
  if (!accountIds.length) return
  await client.execute('DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE account_id = ANY(?::text[])', [accountIds])
}

function refreshDeletedAccountDerivedWindowsIfNeeded(input: DeletedAccountRecordCleanupTarget, shouldRefresh: boolean): string | undefined {
  if (!shouldRefresh) return undefined
  try {
    refreshUsageQuotaHourlyWindowsCache()
    refreshUsageRankSnapshots()
    return undefined
  } catch (error) {
    if (isSqliteDatabaseLocked(error)) {
      return accountCleanupSqliteBusyBlockedReason()
    }
    const database = getDatasetDatabase()
    const updatedAt = nowIso()
    upsertDeletedAccountRecordCleanupTarget(database, input, updatedAt)
    markDeletedAccountRecordCleanupTargetError(database, input, `已删除 AI 账户衍生统计窗口刷新失败：${errorMessage(error)}`, updatedAt)
    throw error
  }
}

function cleanupAuditPayloadBlobsBestEffort(limit: number): void {
  try {
    cleanupUnreferencedAuditPayloadBlobs(limit)
  } catch {
  }
}

async function cleanupAuditPayloadBlobsBestEffortAsync(limit: number): Promise<void> {
  try {
    await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  } catch {
  }
}

async function postgresRowsExist(client: DatabaseClient, sql: string, params: unknown[]): Promise<boolean> {
  const rows = await client.query<{ found?: number }>(sql, params)
  return rows.length > 0
}

function assertSqliteAccountRecordCleanup(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error(`高性能模式禁止调用 SQLite AI 账户记录清理入口：${operation}`)
  }
}

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
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

function parseStringArrayJson(value: unknown): string[] {
  if (typeof value !== 'string' || !value.trim()) return []
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? uniqueNonEmpty(parsed.filter((item): item is string => typeof item === 'string')) : []
  } catch {
    return []
  }
}

function stringArrayJson(values: string[] | undefined): string {
  return JSON.stringify(uniqueNonEmpty(values ?? []))
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '已删除 AI 账户关联数据清理失败'
}
