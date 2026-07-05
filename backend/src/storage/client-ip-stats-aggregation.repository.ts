import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginImmediateDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocationsPage, type UsageRecordShardLocation } from './usage-record-shards.js'
import { usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import {
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type StatsJobStateRow,
  type UsageStatsRecordRow
} from './usage-stats-types.js'
import { writeClientIpStatsAggregatesFromUsageRows, writeClientIpStatsAggregatesFromUsageRowsAsync } from './client-ip-stats-writer.js'

const clientIpStatsJobName = 'client_ip_stats_aggregation'
const cursorSafetyDelaySeconds = 15
const clientIpStatsMaxShardsPerBatch = 16
let clientIpStatsShardScanOffset = 0

export function aggregateClientIpStatsBatch(limit = 2000): number {
  const database = getStatsDatabase()
  const batchLimit = Math.max(1, Math.trunc(limit))
  const shardLocationsWindow = clientIpStatsShardLocationsForBatch(batchLimit)
  const shardLocations = shardLocationsWindow.locations
  const scannedAllShardLocations = !shardLocationsWindow.hasMore
  const safeCreatedBefore = clientIpStatsSafeCreatedBefore()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  let processedRows = 0
  try {
    const updatedAt = nowIso()
    if (shardLocations.length === 0) {
      updateClientIpStatsJobState(database, {
        lastSuccessAt: updatedAt,
        lagSeconds: 0
      })
      commitDatabaseTransaction(database, transactionStarted)
      return 0
    }

    const perShardLimit = Math.max(1, Math.ceil(batchLimit / shardLocations.length))
    let globalCursor: { created_at: string; id: string } | undefined
    let maxLagSeconds = 0
    const shardsWithMoreRows: UsageRecordShardLocation[] = []
    const processShard = (location: UsageRecordShardLocation, limitForShard: number, updateIgnoredCursor: boolean): boolean => {
      if (processedRows >= batchLimit) return false
      const state = clientIpStatsShardJobState(database, location.shardKey)
      const shardDatabase = getUsageRecordShardDatabase(location)
      const rowLimit = Math.max(1, Math.min(limitForShard, batchLimit - processedRows))
      const rows = shardDatabase
        .prepare(`
          SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
          FROM usage_records
          WHERE created_at <= ?
            AND traffic_source NOT IN ('runtime_recovery_probe', 'cooldown_retest')
            AND (created_at > ? OR (created_at = ? AND id > ?))
          ORDER BY created_at ASC, id ASC
          LIMIT ?
        `)
        .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, rowLimit) as unknown as UsageStatsRecordRow[]

      if (rows.length > 0) {
        writeClientIpStatsAggregatesFromUsageRows(database, rows, updatedAt)
        processedRows += rows.length
        const last = rows[rows.length - 1]
        updateClientIpStatsShardJobState(database, location, {
          cursorCreatedAt: last.created_at,
          cursorId: last.id,
          lastSuccessAt: updatedAt,
          lagSeconds: cursorLagSecondsFromCreatedAt(last.created_at)
        })
        globalCursor = latestCursor(globalCursor, { created_at: last.created_at, id: last.id })
        maxLagSeconds = Math.max(maxLagSeconds, cursorLagSecondsFromCreatedAt(last.created_at))
        return rows.length >= rowLimit
      }

      if (!updateIgnoredCursor) return false
      const ignoredCursor = latestIgnoredUsageRecordCursor(shardDatabase, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
      const cursorCreatedAt = ignoredCursor?.created_at ?? state.cursorCreatedAt
      const cursorId = ignoredCursor?.id ?? state.cursorId
      const lagSeconds = latestUsageRecordLagSeconds(shardDatabase, safeCreatedBefore, cursorCreatedAt, cursorId)
      updateClientIpStatsShardJobState(database, location, {
        cursorCreatedAt: ignoredCursor?.created_at,
        cursorId: ignoredCursor?.id,
        lastSuccessAt: updatedAt,
        lagSeconds
      })
      if (ignoredCursor) {
        globalCursor = latestCursor(globalCursor, ignoredCursor)
      }
      maxLagSeconds = Math.max(maxLagSeconds, lagSeconds)
      return false
    }

    for (const location of shardLocations) {
      if (processedRows >= batchLimit) break
      if (processShard(location, perShardLimit, true)) {
        shardsWithMoreRows.push(location)
      }
    }
    while (processedRows < batchLimit && shardsWithMoreRows.length > 0) {
      const candidates = shardsWithMoreRows.splice(0, shardsWithMoreRows.length)
      for (const location of candidates) {
        if (processedRows >= batchLimit) break
        if (processShard(location, batchLimit - processedRows, false)) {
          shardsWithMoreRows.push(location)
        }
      }
    }
    updateClientIpStatsJobState(database, {
      cursorCreatedAt: globalCursor?.created_at,
      cursorId: globalCursor?.id,
      lastSuccessAt: updatedAt,
      lagSeconds: scannedAllShardLocations
        ? maxLagSeconds
        : Math.max(maxLagSeconds, latestClientIpStatsLagSeconds() ?? maxLagSeconds)
    })
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    updateClientIpStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : 'IP 统计聚合失败',
      lagSeconds: latestClientIpStatsLagSeconds()
    })
    throw error
  }
  return processedRows
}

export async function aggregateClientIpStatsBatchAsync(limit = 2000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return aggregateClientIpStatsBatch(limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const batchLimit = Math.max(1, Math.trunc(limit))
  const safeCreatedBefore = clientIpStatsSafeCreatedBefore()
  const updatedAt = nowIso()
  const timezone = await usageStatsTimezoneAsync()
  try {
    return await client.transaction(async (tx) => {
      const state = await postgresClientIpStatsJobState(tx)
      const rows = (await tx.query<UsageStatsRecordRow>(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM juhe_usage.usage_records
        WHERE created_at <= ?
          AND traffic_source NOT IN ('runtime_recovery_probe', 'cooldown_retest')
          AND (created_at > ? OR (created_at = ? AND id > ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `, [safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, batchLimit]))
        .map(normalizePostgresUsageStatsRecordRow)

      if (rows.length === 0) {
        const ignoredCursor = await latestPostgresIgnoredUsageRecordCursor(tx, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
        const cursorCreatedAt = ignoredCursor?.created_at ?? state.cursorCreatedAt
        const cursorId = ignoredCursor?.id ?? state.cursorId
        const lagSeconds = await latestPostgresUsageRecordLagSeconds(tx, safeCreatedBefore, cursorCreatedAt, cursorId)
        await updatePostgresClientIpStatsJobState(tx, {
          cursorCreatedAt: ignoredCursor?.created_at,
          cursorId: ignoredCursor?.id,
          lastSuccessAt: updatedAt,
          lagSeconds
        })
        return 0
      }

      await writeClientIpStatsAggregatesFromUsageRowsAsync(tx, rows, updatedAt, timezone)
      const last = rows[rows.length - 1]
      await updatePostgresClientIpStatsJobState(tx, {
        cursorCreatedAt: last.created_at,
        cursorId: last.id,
        lastSuccessAt: updatedAt,
        lagSeconds: cursorLagSecondsFromCreatedAt(last.created_at)
      })
      return rows.length
    })
  } catch (error) {
    await updatePostgresClientIpStatsJobState(client, {
      lastErrorMessage: error instanceof Error ? error.message : 'IP 统计 PG 聚合失败',
      lagSeconds: await latestClientIpStatsLagSecondsAsync()
    }).catch(() => undefined)
    throw error
  }
}

export function latestClientIpStatsLagSeconds(): number | undefined {
  const row = getStatsDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(clientIpStatsJobName) as unknown as { lag_seconds?: number | null } | undefined
  const value = row?.lag_seconds
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

export async function latestClientIpStatsLagSecondsAsync(): Promise<number | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return latestClientIpStatsLagSeconds()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ lag_seconds?: number | string | null }>(`
    SELECT lag_seconds
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ?
    LIMIT 1
  `, [clientIpStatsJobName])
  const value = Number(row?.lag_seconds)
  return Number.isFinite(value) ? value : undefined
}

function clientIpStatsShardLocationsForBatch(batchLimit: number): ReturnType<typeof listUsageRecordShardLocationsPage> {
  const maxShardCount = Math.max(1, Math.min(clientIpStatsMaxShardsPerBatch, Math.trunc(batchLimit)))
  const window = listUsageRecordShardLocationsPage({
    offset: clientIpStatsShardScanOffset,
    limit: maxShardCount
  })
  clientIpStatsShardScanOffset = window.total > 0
    ? (clientIpStatsShardScanOffset + window.locations.length) % window.total
    : 0
  return window
}

function clientIpStatsShardJobState(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = ? AND job_name = ?")
    .get(shardKey, clientIpStatsJobName) as unknown as StatsJobStateRow | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

async function postgresClientIpStatsJobState(client: DatabaseClient): Promise<{ cursorCreatedAt: string; cursorId: string }> {
  const row = await client.one<StatsJobStateRow>(`
    SELECT cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ?
    LIMIT 1
  `, [clientIpStatsJobName])
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function updateClientIpStatsJobState(database: DatabaseSync, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(clientIpStatsJobName, input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

function updateClientIpStatsShardJobState(database: DatabaseSync, location: UsageRecordShardLocation, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('usage_shard', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(location.shardKey, clientIpStatsJobName, input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

async function updatePostgresClientIpStatsJobState(
  client: DatabaseClient,
  input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }
): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(EXCLUDED.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(EXCLUDED.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(EXCLUDED.last_success_at, stats_job_state.last_success_at),
      last_error_message = EXCLUDED.last_error_message,
      lag_seconds = EXCLUDED.lag_seconds,
      updated_at = EXCLUDED.updated_at
  `, [
    clientIpStatsJobName,
    input.cursorCreatedAt ?? null,
    input.cursorId ?? null,
    input.lastSuccessAt ?? null,
    input.lastErrorMessage ?? null,
    input.lagSeconds ?? null,
    nowIso()
  ])
}

function clientIpStatsSafeCreatedBefore(): string {
  return new Date(Date.now() - cursorSafetyDelaySeconds * 1000).toISOString()
}

function latestIgnoredUsageRecordCursor(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): { created_at: string; id: string } | undefined {
  const latest = database
    .prepare(`
      SELECT created_at, id
      FROM usage_records
      WHERE created_at <= ?
        AND traffic_source IN ('runtime_recovery_probe', 'cooldown_retest')
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string; id?: string } | undefined
  return latest?.created_at && latest.id ? { created_at: latest.created_at, id: latest.id } : undefined
}

function latestUsageRecordLagSeconds(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): number {
  const latest = database
    .prepare(`
      SELECT created_at
      FROM usage_records
      WHERE created_at <= ?
        AND traffic_source NOT IN ('runtime_recovery_probe', 'cooldown_retest')
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string } | undefined
  return latest?.created_at ? cursorLagSecondsFromCreatedAt(latest.created_at) : 0
}

async function latestPostgresIgnoredUsageRecordCursor(
  client: DatabaseClient,
  safeCreatedBefore: string,
  cursorCreatedAt: string,
  cursorId: string
): Promise<{ created_at: string; id: string } | undefined> {
  const latest = await client.one<{ created_at?: string | null; id?: string | null }>(`
    SELECT created_at, id
    FROM juhe_usage.usage_records
    WHERE created_at <= ?
      AND traffic_source IN ('runtime_recovery_probe', 'cooldown_retest')
      AND (created_at > ? OR (created_at = ? AND id > ?))
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId])
  return latest?.created_at && latest.id ? { created_at: latest.created_at, id: latest.id } : undefined
}

async function latestPostgresUsageRecordLagSeconds(
  client: DatabaseClient,
  safeCreatedBefore: string,
  cursorCreatedAt: string,
  cursorId: string
): Promise<number> {
  const latest = await client.one<{ created_at?: string | null }>(`
    SELECT created_at
    FROM juhe_usage.usage_records
    WHERE created_at <= ?
      AND traffic_source NOT IN ('runtime_recovery_probe', 'cooldown_retest')
      AND (created_at > ? OR (created_at = ? AND id > ?))
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId])
  return latest?.created_at ? cursorLagSecondsFromCreatedAt(latest.created_at) : 0
}

function latestCursor(
  current: { created_at: string; id: string } | undefined,
  next: { created_at: string; id: string }
): { created_at: string; id: string } {
  if (!current) return next
  if (next.created_at > current.created_at) return next
  if (next.created_at === current.created_at && next.id > current.id) return next
  return current
}

function cursorLagSecondsFromCreatedAt(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}

function normalizePostgresUsageStatsRecordRow(row: UsageStatsRecordRow): UsageStatsRecordRow {
  return {
    ...row,
    status_code: nullableNumber(row.status_code),
    success: Number(row.success ?? 0),
    first_token_ms: nullableNumber(row.first_token_ms),
    duration_ms: nullableNumber(row.duration_ms),
    input_tokens: nullableNumber(row.input_tokens),
    output_tokens: nullableNumber(row.output_tokens),
    cache_read_tokens: nullableNumber(row.cache_read_tokens),
    cache_read_cost_usd: nullableNumber(row.cache_read_cost_usd),
    cost_usd: nullableNumber(row.cost_usd)
  }
}

function nullableNumber(value: unknown): number | null {
  if (value === null || value === undefined) return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}
