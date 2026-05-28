import type { DatabaseSync } from 'node:sqlite'

import { cleanupUnreferencedAuditPayloadBlobs } from './audit-log-payload-blobs.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocations, type UsageRecordShardLocation } from './usage-record-shards.js'
import { refreshUsageQuotaHourlyWindowsCache, refreshUsageRankSnapshots } from './usage-stats.repository.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'

export interface DeletedAccountRecordCleanupTarget {
  accountId: string
  systemAccountId: string
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

export function registerDeletedAccountRecordCleanupTarget(input: DeletedAccountRecordCleanupTarget): void {
  upsertDeletedAccountRecordCleanupTarget(getDatasetDatabase(), input, nowIso())
}

export function cleanupPendingDeletedAccountRecordTargets(limit = 50): PendingDeletedAccountRecordCleanupSummary {
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

export function listDeletedAccountRecordCleanupTargets(limit = 50): DeletedAccountRecordCleanupTarget[] {
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT account_id, system_account_id
        , authorization_ids_json, team_scope_ids_json
      FROM account_record_cleanup_targets
      ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, account_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as PendingDeletedAccountRecordCleanupTargetRow[]
  return rows
    .map((row) => ({
      accountId: String(row.account_id ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      authorizationIds: parseStringArrayJson(row.authorization_ids_json),
      teamScopeIds: parseStringArrayJson(row.team_scope_ids_json)
    }))
    .filter((row) => row.accountId && row.systemAccountId)
}

export function cleanupDeletedAccountDetachedStats(input: DeletedAccountDetachedStatsCleanupTarget): void {
  const database = getStatsDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    deleteAccountScopeStatsRows(database, input, input.authorizationIds ?? [], input.teamScopeIds ?? [])
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshDeletedAccountDerivedWindowsIfNeeded(input, true)
}

export function cleanupDeletedAccountRelatedRecordData(input: DeletedAccountRecordCleanupTarget): DeletedAccountRecordCleanupResult {
  const database = getDatasetDatabase()
  const statsDatabase = getStatsDatabase()
  const updatedAt = nowIso()
  upsertDeletedAccountRecordCleanupTarget(database, input, updatedAt)
  let shouldRefreshDerivedWindows = false
  const batchLimit = deletedAccountRecordCleanupBatchLimit
  let deletedRows = 0
  let transactionStarted = false
  let statsTransactionStarted = false
  try {
    transactionStarted = beginDatabaseTransaction(database)
    const usageBatch = selectAccountUsageRowsCoveredByShardCursors(statsDatabase, input, batchLimit)
    const rowsToDelete = usageBatch.rows.slice(0, batchLimit)
    const hasMoreCoveredRows = usageBatch.hasMoreCoveredRows
    if (rowsToDelete.length > 0) {
      subtractAccountUsageRowsOnce(statsDatabase, rowsToDelete, input, updatedAt)
    }
    deletedRows += deleteAccountUsageRows(rowsToDelete, input)
    markAccountUsageCleanupRowsDeleted(statsDatabase, rowsToDelete, updatedAt)
    deletedRows += deleteAccountAuditDataBatch(database, input, batchLimit)
    deletedRows += deleteAccountModelCheckRunsBatch(database, input, batchLimit)

    const hasUsageMore = hasAccountUsageRecords(input)
    const hasAuditMore = hasAccountAuditData(database, input)
    const hasModelCheckMore = hasAccountModelCheckRuns(database, input)
    const hasMore = hasUsageMore || hasAuditMore || hasModelCheckMore
    if (!hasMore) {
      if (!statsTransactionStarted) {
        statsTransactionStarted = beginDatabaseTransaction(statsDatabase)
      }
      deleteAccountScopeStatsRows(statsDatabase, input, input.authorizationIds ?? [], input.teamScopeIds ?? [])
      deleteAccountUsageCleanupDeductions(statsDatabase, input)
    }
    const result: DeletedAccountRecordCleanupResult = {
      ...input,
      deletedRows,
      hasMore,
      blockedReason: hasMore
        ? (hasAuditMore
          ? '仍有已删除 AI 账户的原始审计记录待后续批次清理，已保留待后台重试'
          : hasModelCheckMore
          ? '仍有已删除 AI 账户的模型检测记录待后续批次清理，已保留待后台重试'
          : hasMoreCoveredRows
          ? '仍有已被统计安全游标覆盖的使用记录待后续批次清理，已保留待后台重试'
          : usageBatch.hasUncoveredRows
          ? '仍有使用记录尚未被对应分片统计安全游标覆盖，已保留待后台重试清理'
          : '仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理')
        : undefined
    }
    if (hasMore) {
      markDeletedAccountRecordCleanupTargetDeferred(database, input, result.blockedReason ?? '等待统计安全游标追平', updatedAt)
    } else {
      clearDeletedAccountRecordCleanupTarget(database, input)
      shouldRefreshDerivedWindows = true
    }
    commitDatabaseTransaction(statsDatabase, statsTransactionStarted)
    statsTransactionStarted = false
    commitDatabaseTransaction(database, transactionStarted)
    transactionStarted = false
    refreshDeletedAccountDerivedWindowsIfNeeded(input, shouldRefreshDerivedWindows)
    cleanupAuditPayloadBlobsBestEffort(batchLimit)
    return result
  } catch (error) {
    rollbackDatabaseTransaction(statsDatabase, statsTransactionStarted)
    rollbackDatabaseTransaction(database, transactionStarted)
    markDeletedAccountRecordCleanupTargetError(database, input, errorMessage(error), nowIso())
    throw error
  }
}

function upsertDeletedAccountRecordCleanupTarget(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, updatedAt: string): void {
  database.prepare(`
    INSERT INTO account_record_cleanup_targets (
      account_id, system_account_id, authorization_ids_json, team_scope_ids_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
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

function hasAccountUsageRecords(input: DeletedAccountRecordCleanupTarget): boolean {
  for (const location of listUsageRecordShardLocations()) {
    const row = getUsageRecordShardDatabase(location)
      .prepare('SELECT id FROM usage_records WHERE account_id = ? LIMIT 1')
      .get(input.accountId) as unknown as { id?: string } | undefined
    if (row?.id) return true
  }
  return false
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
  for (const location of listUsageRecordShardLocations()) {
    const shardDatabase = getUsageRecordShardDatabase(location)
    const anyUsage = shardDatabase
      .prepare('SELECT id FROM usage_records WHERE account_id = ? LIMIT 1')
      .get(input.accountId) as unknown as { id?: string } | undefined
    if (!anyUsage?.id) continue
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
      .get(input.accountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId) as unknown as { id?: string } | undefined
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
      `).all(input.accountId, cursor.cursorCreatedAt, cursor.cursorCreatedAt, cursor.cursorId, queryLimit) as unknown as UsageStatsRecordRow[])
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
    hasMoreCoveredRows: sortedRows.length > batchLimit,
    hasUncoveredRows
  }
}

function usageStatsShardCursor(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } | undefined {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = ? AND job_name = 'usage_stats_aggregation'")
    .get(shardKey) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  const cursorCreatedAt = row?.cursor_created_at?.trim()
  const cursorId = row?.cursor_id?.trim()
  return cursorCreatedAt && cursorId ? { cursorCreatedAt, cursorId } : undefined
}

function usageStatsRecordForCleanup(row: AccountUsageShardRow): UsageStatsRecordRow {
  const { location: _location, ...record } = row
  return record
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
        input.accountId,
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
        deletedRows += changed(deleteStatement.run(row.id, input.accountId))
      }
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
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

function hasAccountAuditData(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): boolean {
  const auditLog = database
    .prepare('SELECT id FROM audit_logs WHERE account_id = ? LIMIT 1')
    .get(input.accountId) as unknown as { id?: string } | undefined
  if (auditLog?.id) return true
  const auditErrorGroup = database
    .prepare('SELECT id FROM audit_error_groups WHERE account_id = ? LIMIT 1')
    .get(input.accountId) as unknown as { id?: string } | undefined
  return Boolean(auditErrorGroup?.id)
}

function deleteAccountAuditDataBatch(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, limit: number): number {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const rows = database
    .prepare(`
      SELECT id
      FROM audit_logs
      WHERE account_id = ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.accountId, batchLimit) as unknown as Array<{ id?: string }>
  const auditLogIds = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  let deletedRows = 0
  if (auditLogIds.length > 0) {
    const placeholders = sqlPlaceholders(auditLogIds.length)
    deletedRows += changed(database.prepare(`DELETE FROM audit_payload_refs WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_log_attempts WHERE audit_log_id IN (${placeholders})`).run(...auditLogIds))
    deletedRows += changed(database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders}) AND account_id = ?`).run(...auditLogIds, input.accountId))
  }
  const groupRows = database
    .prepare(`
      SELECT id
      FROM audit_error_groups
      WHERE account_id = ?
      ORDER BY updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.accountId, batchLimit) as unknown as Array<{ id?: string }>
  const groupIds = groupRows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (groupIds.length > 0) {
    const placeholders = sqlPlaceholders(groupIds.length)
    deletedRows += changed(database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders}) AND account_id = ?`).run(...groupIds, input.accountId))
  }
  return deletedRows
}

function hasAccountModelCheckRuns(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): boolean {
  const row = database
    .prepare("SELECT id FROM model_check_runs WHERE account_id = ? OR (target_type = 'account' AND target_id = ?) LIMIT 1")
    .get(input.accountId, input.accountId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function deleteAccountModelCheckRunsBatch(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget, limit: number): number {
  const batchLimit = Math.max(1, Math.trunc(limit))
  const rows = database
    .prepare(`
      SELECT id
      FROM model_check_runs
      WHERE account_id = ?
        OR (target_type = 'account' AND target_id = ?)
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.accountId, input.accountId, batchLimit) as unknown as Array<{ id?: string }>
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
  const teamScopePrefix = `${escapeLikePrefix(input.accountId)}:%`
  for (const tableName of accountScopeStatsTables) {
    database.prepare(`DELETE FROM ${tableName} WHERE scope_type IN ('account', 'caller_account') AND scope_id = ?`)
      .run(input.accountId)
    database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'`)
      .run(teamScopePrefix)
    for (const chunk of chunkValues(normalizedAuthorizationIds, 400)) {
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
        .run(...chunk)
    }
    for (const chunk of chunkValues(normalizedTeamScopeIds, 400)) {
      database.prepare(`DELETE FROM ${tableName} WHERE scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
        .run(...chunk)
    }
  }
  database.prepare("DELETE FROM stats_job_state WHERE scope_type IN ('account', 'caller_account') AND scope_id = ?")
    .run(input.accountId)
  database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id LIKE ? ESCAPE '\\'")
    .run(teamScopePrefix)
  for (const chunk of chunkValues(normalizedAuthorizationIds, 400)) {
    database.prepare(`DELETE FROM stats_job_state WHERE scope_type = 'account_authorization' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
  for (const chunk of chunkValues(normalizedTeamScopeIds, 400)) {
    database.prepare(`DELETE FROM stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
  database.prepare('DELETE FROM account_quality_scores WHERE account_id = ?').run(input.accountId)
  database.prepare('DELETE FROM account_quality_minute_stats WHERE account_id = ?').run(input.accountId)
  database.prepare('DELETE FROM account_usage_snapshots WHERE account_id = ?').run(input.accountId)
  deleteAccountAuthorizationReportRows(database, input.accountId)
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

function deleteAccountUsageCleanupDeductions(database: DatabaseSync, input: DeletedAccountRecordCleanupTarget): void {
  database.prepare('DELETE FROM usage_record_cleanup_deductions WHERE account_id = ?')
    .run(input.accountId)
}

function refreshDeletedAccountDerivedWindowsIfNeeded(input: DeletedAccountRecordCleanupTarget, shouldRefresh: boolean): void {
  if (!shouldRefresh) return
  try {
    refreshUsageQuotaHourlyWindowsCache()
    refreshUsageRankSnapshots()
  } catch (error) {
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

function changed(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}

function uniqueNonEmpty(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
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
