import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { pinScheduledJobLeaseInTransaction, type ScheduledJobLeaseFence } from './scheduled-job-lease.repository.js'
import { dateKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from './usage-stats-window-helpers.js'

export const clientIpRangeWindowJobName = 'client_ip_range_window_refresh'
export const clientIpRangeWindowScopeType = 'client_ip_range_window'

const clientIpRangeWindowDirtyLimit = 1000
const clientIpRangeWindowChunkSize = 200
const clientIpRangeWindowDirtyIpHashes = new Set<string>()
const clientIpAccountRangeWindowDirtyIpHashes = new Set<string>()
const statsSchemaName = 'juhe_stats'

interface ClientIpRangeWindowDirtyRow {
  ipHash: string
  generation: number | string
}

interface ClientIpRangeWindowDirtyClaim {
  ipHashes: string[]
  clientIpRows: ClientIpRangeWindowDirtyRow[]
  accountRows: ClientIpRangeWindowDirtyRow[]
}

export function refreshClientIpUsageRangeWindows(options: { full?: boolean; dirtyLimit?: number } = {}): void {
  const database = getStatsDatabase()
  const windows = currentClientIpRangeWindows()
  if (!windows.length) return
  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const dirtyClaim = options.full
      ? takeAllClientIpRangeWindowDirtyIpHashes(database)
      : takeClientIpRangeWindowDirtyIpHashes(database, options.dirtyLimit ?? clientIpRangeWindowDirtyLimit)
    if (options.full) {
      for (const window of windows) {
        refreshClientIpAccountUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
        refreshClientIpUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
      }
      clearClientIpRangeWindowDirtyIpHashes(database, dirtyClaim)
      commitDatabaseTransaction(database, transactionStarted)
      return
    }
    const dirtyIpHashes = dirtyClaim.ipHashes
    if (!dirtyIpHashes.length) {
      if (hasStaleClientIpUsageRangeWindows(database, windows)) {
        for (const window of windows) {
          refreshClientIpAccountUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
          refreshClientIpUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
        }
      }
      commitDatabaseTransaction(database, transactionStarted)
      return
    }
    for (const window of windows) {
      refreshClientIpUsageRangeWindowForIps(database, window.startDate, window.endDate, dirtyIpHashes, updatedAt, false)
      refreshClientIpAccountUsageRangeWindowForIps(database, window.startDate, window.endDate, dirtyIpHashes, updatedAt, false)
    }
    clearClientIpRangeWindowDirtyIpHashes(database, dirtyClaim)
    if (!hasPendingClientIpRangeWindowDirtyIpHashes(database)) {
      markClientIpUsageRangeWindowsReady(database, windows, updatedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export async function refreshClientIpUsageRangeWindowsAsync(options: { full?: boolean; dirtyLimit?: number; scheduledLease?: ScheduledJobLeaseFence } = {}): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshClientIpUsageRangeWindows(options)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const windows = await currentClientIpRangeWindowsAsync()
  if (!windows.length) return
  const updatedAt = nowIso()
  await client.transaction(async (tx) => {
    if (options.scheduledLease) await pinScheduledJobLeaseInTransaction(tx, options.scheduledLease)
    const dirtyClaim = options.full
      ? await takeAllClientIpRangeWindowDirtyIpHashesAsync(tx)
      : await takeClientIpRangeWindowDirtyIpHashesAsync(tx, options.dirtyLimit ?? clientIpRangeWindowDirtyLimit)
    if (options.full) {
      for (const window of windows) {
        await refreshClientIpAccountUsageRangeWindowAsync(tx, window.startDate, window.endDate, updatedAt)
        await refreshClientIpUsageRangeWindowAsync(tx, window.startDate, window.endDate, updatedAt)
      }
      await clearClientIpRangeWindowDirtyIpHashesAsync(tx, dirtyClaim)
      return
    }

    const dirtyIpHashes = dirtyClaim.ipHashes
    if (!dirtyIpHashes.length) {
      if (await hasStaleClientIpUsageRangeWindowsAsync(tx, windows)) {
        for (const window of windows) {
          await refreshClientIpAccountUsageRangeWindowAsync(tx, window.startDate, window.endDate, updatedAt)
          await refreshClientIpUsageRangeWindowAsync(tx, window.startDate, window.endDate, updatedAt)
        }
      }
      return
    }

    for (const window of windows) {
      await refreshClientIpUsageRangeWindowForIpsAsync(tx, window.startDate, window.endDate, dirtyIpHashes, updatedAt)
      await refreshClientIpAccountUsageRangeWindowForIpsAsync(tx, window.startDate, window.endDate, dirtyIpHashes, updatedAt)
    }
    await clearClientIpRangeWindowDirtyIpHashesAsync(tx, dirtyClaim)
    if (!await hasPendingClientIpRangeWindowDirtyIpHashesAsync(tx)) {
      await markClientIpUsageRangeWindowsReadyAsync(tx, windows, updatedAt)
    }
  })
}

export function rebuildClientIpUsageRangeWindows(): void {
  refreshClientIpUsageRangeWindows({ full: true })
}

export function pendingClientIpRangeWindowDirtyCountForTest(): number {
  const row = getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM client_ip_range_window_dirty_ips')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

export function clearClientIpRangeWindowDirtyMemoryForTest(): void {
  clientIpRangeWindowDirtyIpHashes.clear()
  clientIpAccountRangeWindowDirtyIpHashes.clear()
}

export function clientIpUsageRangeWindowReady(database: DatabaseSync, startDate: string, endDate: string): boolean {
  const windowState = database.prepare(`
    SELECT last_success_at
    FROM stats_job_state
    WHERE scope_type = ?
      AND scope_id = ?
      AND job_name = ?
    LIMIT 1
  `).get(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName) as unknown as { last_success_at?: string | null } | undefined
  if (hasPendingClientIpRangeWindowDirtyIpHashes(database)) return false
  if (windowState) return Boolean(windowState.last_success_at)
  const row = database.prepare('SELECT 1 FROM client_ip_usage_range_windows WHERE start_date = ? AND end_date = ? LIMIT 1')
    .get(startDate, endDate) as unknown as { 1?: number } | undefined
  return Boolean(row)
}

export async function clientIpUsageRangeWindowReadyAsync(client: DatabaseClient, startDate: string, endDate: string): Promise<boolean> {
  const windowState = await client.one<{ last_success_at?: string | null }>(`
    SELECT last_success_at
    FROM ${statsTable(client, 'stats_job_state')}
    WHERE scope_type = ?
      AND scope_id = ?
      AND job_name = ?
    LIMIT 1
  `, [clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName])
  if (await hasPendingClientIpRangeWindowDirtyIpHashesAsync(client)) return false
  if (windowState) return Boolean(windowState.last_success_at)
  const row = await client.one<{ ready?: number }>(`
    SELECT 1 AS ready
    FROM ${statsTable(client, 'client_ip_usage_range_windows')}
    WHERE start_date = ?
      AND end_date = ?
    LIMIT 1
  `, [startDate, endDate])
  return Boolean(row)
}

export function markCurrentClientIpUsageRangeWindowsStale(database: DatabaseSync): void {
  const windows = currentClientIpRangeWindows()
  if (!windows.length) return
  const updatedAt = nowIso()
  const staleStatement = database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES (?, ?, ?, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = NULL,
      updated_at = excluded.updated_at
  `)
  for (const window of windows) {
    staleStatement.run(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName, updatedAt)
  }
}

export async function markCurrentClientIpUsageRangeWindowsStaleAsync(client: DatabaseClient, updatedAt = nowIso()): Promise<void> {
  const windows = await currentClientIpRangeWindowsAsync()
  if (!windows.length) return
  for (const window of windows) {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'stats_job_state')} (scope_type, scope_id, job_name, last_success_at, updated_at)
      VALUES (?, ?, ?, NULL, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        last_success_at = NULL,
        updated_at = excluded.updated_at
    `, [clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName, updatedAt])
  }
}

export function markClientIpRangeWindowsDirty(database: DatabaseSync, ipHashes: Iterable<string>, updatedAt = nowIso()): void {
  const dirtyStatement = database.prepare(`
    INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at)
    VALUES (?, 1, ?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET
      generation = client_ip_range_window_dirty_ips.generation + 1,
      updated_at = excluded.updated_at
  `)
  const accountDirtyStatement = database.prepare(`
    INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at)
    VALUES (?, 1, ?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET
      generation = client_ip_account_range_window_dirty_ips.generation + 1,
      updated_at = excluded.updated_at
  `)
  for (const ipHash of ipHashes) {
    clientIpRangeWindowDirtyIpHashes.add(ipHash)
    clientIpAccountRangeWindowDirtyIpHashes.add(ipHash)
    dirtyStatement.run(ipHash, updatedAt, updatedAt)
    accountDirtyStatement.run(ipHash, updatedAt, updatedAt)
  }
}

export async function markClientIpRangeWindowsDirtyAsync(client: DatabaseClient, ipHashes: Iterable<string>, updatedAt = nowIso()): Promise<void> {
  const normalized = [...new Set([...ipHashes].map((ipHash) => ipHash.trim()).filter(Boolean))]
  if (!normalized.length) return
  for (const chunk of chunkValues(normalized, clientIpRangeWindowChunkSize)) {
    for (const ipHash of chunk) {
      clientIpRangeWindowDirtyIpHashes.add(ipHash)
      clientIpAccountRangeWindowDirtyIpHashes.add(ipHash)
      await client.execute(`
        INSERT INTO ${statsTable(client, 'client_ip_range_window_dirty_ips')} AS dirty (ip_hash, generation, first_dirty_at, updated_at)
        VALUES (?, 1, ?, ?)
        ON CONFLICT(ip_hash) DO UPDATE SET
          generation = dirty.generation + 1,
          updated_at = excluded.updated_at
      `, [ipHash, updatedAt, updatedAt])
      await client.execute(`
        INSERT INTO ${statsTable(client, 'client_ip_account_range_window_dirty_ips')} AS dirty (ip_hash, generation, first_dirty_at, updated_at)
        VALUES (?, 1, ?, ?)
        ON CONFLICT(ip_hash) DO UPDATE SET
          generation = dirty.generation + 1,
          updated_at = excluded.updated_at
      `, [ipHash, updatedAt, updatedAt])
    }
  }
}

export function clientIpRangeWindowScopeId(startDate: string, endDate: string): string {
  return `${startDate}:${endDate}`
}

function currentClientIpRangeWindows(): Array<{ startDate: string; endDate: string }> {
  const timezone = usageStatsTimezone()
  return clientIpRangeWindowsForTimezone(timezone)
}

async function currentClientIpRangeWindowsAsync(): Promise<Array<{ startDate: string; endDate: string }>> {
  const timezone = await usageStatsTimezoneAsync()
  return clientIpRangeWindowsForTimezone(timezone)
}

function clientIpRangeWindowsForTimezone(timezone: string): Array<{ startDate: string; endDate: string }> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return []
  const windows = [
    { startDate: todayKey, endDate: todayKey },
    { startDate: dates[Math.max(0, dates.length - 7)], endDate: todayKey },
    { startDate: dates[0], endDate: todayKey }
  ]
  const seen = new Set<string>()
  const result: Array<{ startDate: string; endDate: string }> = []
  for (const window of windows) {
    const key = `${window.startDate}:${window.endDate}`
    if (seen.has(key)) continue
    seen.add(key)
    result.push(window)
  }
  return result
}

function refreshClientIpUsageRangeWindow(database: DatabaseSync, startDate: string, endDate: string, updatedAt: string): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM client_ip_usage_range_windows WHERE start_date = ? AND end_date = ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO client_ip_usage_range_windows (
        ip_hash, start_date, end_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
        first_token_ms_sum, first_token_ms_count, average_first_token_ms,
        active_days, last_used_at, last_error_at, updated_at
      )
      SELECT
        ip_hash,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM client_ip_stats_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY ip_hash
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `).run(startDate, endDate, updatedAt, startDate, endDate)
    markClientIpUsageRangeWindowReady(database, startDate, endDate, updatedAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function refreshClientIpUsageRangeWindowForIps(database: DatabaseSync, startDate: string, endDate: string, ipHashes: string[], updatedAt: string, useTransaction = true): void {
  if (!ipHashes.length) return
  const transactionStarted = useTransaction ? beginDatabaseTransaction(database) : false
  try {
    for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
      const placeholders = sqlPlaceholders(chunk.length)
      database.prepare(`
        DELETE FROM client_ip_usage_range_windows
        WHERE start_date = ?
          AND end_date = ?
          AND ip_hash IN (${placeholders})
      `).run(startDate, endDate, ...chunk)
      database.prepare(`
        INSERT INTO client_ip_usage_range_windows (
          ip_hash, start_date, end_date, request_count, success_count, error_count,
          input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
          duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
          first_token_ms_sum, first_token_ms_count, average_first_token_ms,
          active_days, last_used_at, last_error_at, updated_at
        )
        SELECT
          ip_hash,
          ?,
          ?,
          COALESCE(SUM(request_count), 0),
          COALESCE(SUM(success_count), 0),
          COALESCE(SUM(error_count), 0),
          COALESCE(SUM(input_tokens), 0),
          COALESCE(SUM(output_tokens), 0),
          COALESCE(SUM(cache_read_tokens), 0),
          COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
          COALESCE(SUM(duration_ms_sum), 0),
          COALESCE(SUM(duration_ms_count), 0),
          COALESCE(MAX(duration_ms_max), 0),
          CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(first_token_ms_sum), 0),
          COALESCE(SUM(first_token_ms_count), 0),
          CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
          MAX(last_used_at),
          MAX(last_error_at),
          ?
        FROM client_ip_stats_daily
        WHERE stat_date >= ?
          AND stat_date <= ?
          AND ip_hash IN (${placeholders})
        GROUP BY ip_hash
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
      `).run(startDate, endDate, updatedAt, startDate, endDate, ...chunk)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

async function refreshClientIpUsageRangeWindowAsync(client: DatabaseClient, startDate: string, endDate: string, updatedAt: string): Promise<void> {
  await client.execute(`DELETE FROM ${statsTable(client, 'client_ip_usage_range_windows')} WHERE start_date = ? AND end_date = ?`, [startDate, endDate])
  await client.execute(`
    INSERT INTO ${statsTable(client, 'client_ip_usage_range_windows')} (
      ip_hash, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
      first_token_ms_sum, first_token_ms_count, average_first_token_ms,
      active_days, last_used_at, last_error_at, updated_at
    )
    SELECT
      ip_hash,
      ?,
      ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(success_count), 0),
      COALESCE(SUM(error_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
      COALESCE(SUM(duration_ms_sum), 0),
      COALESCE(SUM(duration_ms_count), 0),
      COALESCE(MAX(duration_ms_max), 0),
      CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
      COALESCE(SUM(first_token_ms_sum), 0),
      COALESCE(SUM(first_token_ms_count), 0),
      CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
      COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
      MAX(last_used_at),
      MAX(last_error_at),
      ?
    FROM ${statsTable(client, 'client_ip_stats_daily')}
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY ip_hash
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
  `, [startDate, endDate, updatedAt, startDate, endDate])
  await markClientIpUsageRangeWindowReadyAsync(client, startDate, endDate, updatedAt)
}

async function refreshClientIpUsageRangeWindowForIpsAsync(
  client: DatabaseClient,
  startDate: string,
  endDate: string,
  ipHashes: string[],
  updatedAt: string
): Promise<void> {
  if (!ipHashes.length) return
  for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
    const placeholders = sqlPlaceholders(chunk.length)
    await client.execute(`
      DELETE FROM ${statsTable(client, 'client_ip_usage_range_windows')}
      WHERE start_date = ?
        AND end_date = ?
        AND ip_hash IN (${placeholders})
    `, [startDate, endDate, ...chunk])
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_usage_range_windows')} (
        ip_hash, start_date, end_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
        first_token_ms_sum, first_token_ms_count, average_first_token_ms,
        active_days, last_used_at, last_error_at, updated_at
      )
      SELECT
        ip_hash,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM ${statsTable(client, 'client_ip_stats_daily')}
      WHERE stat_date >= ?
        AND stat_date <= ?
        AND ip_hash IN (${placeholders})
      GROUP BY ip_hash
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `, [startDate, endDate, updatedAt, startDate, endDate, ...chunk])
  }
}

function refreshClientIpAccountUsageRangeWindow(database: DatabaseSync, startDate: string, endDate: string, updatedAt: string): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM client_ip_account_usage_range_windows WHERE start_date = ? AND end_date = ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO client_ip_account_usage_range_windows (
        ip_hash, account_id, start_date, end_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
        first_token_ms_sum, first_token_ms_count, average_first_token_ms,
        active_days, last_used_at, last_error_at, updated_at
      )
      SELECT
        ip_hash,
        account_id,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM client_ip_account_stats_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY ip_hash, account_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `).run(startDate, endDate, updatedAt, startDate, endDate)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function refreshClientIpAccountUsageRangeWindowForIps(database: DatabaseSync, startDate: string, endDate: string, ipHashes: string[], updatedAt: string, useTransaction = true): void {
  if (!ipHashes.length) return
  const transactionStarted = useTransaction ? beginDatabaseTransaction(database) : false
  try {
    for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
      const placeholders = sqlPlaceholders(chunk.length)
      database.prepare(`
        DELETE FROM client_ip_account_usage_range_windows
        WHERE start_date = ?
          AND end_date = ?
          AND ip_hash IN (${placeholders})
      `).run(startDate, endDate, ...chunk)
      database.prepare(`
        INSERT INTO client_ip_account_usage_range_windows (
          ip_hash, account_id, start_date, end_date, request_count, success_count, error_count,
          input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
          duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
          first_token_ms_sum, first_token_ms_count, average_first_token_ms,
          active_days, last_used_at, last_error_at, updated_at
        )
        SELECT
          ip_hash,
          account_id,
          ?,
          ?,
          COALESCE(SUM(request_count), 0),
          COALESCE(SUM(success_count), 0),
          COALESCE(SUM(error_count), 0),
          COALESCE(SUM(input_tokens), 0),
          COALESCE(SUM(output_tokens), 0),
          COALESCE(SUM(cache_read_tokens), 0),
          COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
          COALESCE(SUM(duration_ms_sum), 0),
          COALESCE(SUM(duration_ms_count), 0),
          COALESCE(MAX(duration_ms_max), 0),
          CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(first_token_ms_sum), 0),
          COALESCE(SUM(first_token_ms_count), 0),
          CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
          MAX(last_used_at),
          MAX(last_error_at),
          ?
        FROM client_ip_account_stats_daily
        WHERE stat_date >= ?
          AND stat_date <= ?
          AND ip_hash IN (${placeholders})
        GROUP BY ip_hash, account_id
        HAVING COALESCE(SUM(request_count), 0) > 0
          OR COALESCE(SUM(input_tokens), 0) > 0
          OR COALESCE(SUM(output_tokens), 0) > 0
          OR COALESCE(SUM(cache_read_tokens), 0) > 0
          OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
      `).run(startDate, endDate, updatedAt, startDate, endDate, ...chunk)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

async function refreshClientIpAccountUsageRangeWindowAsync(client: DatabaseClient, startDate: string, endDate: string, updatedAt: string): Promise<void> {
  await client.execute(`DELETE FROM ${statsTable(client, 'client_ip_account_usage_range_windows')} WHERE start_date = ? AND end_date = ?`, [startDate, endDate])
  await client.execute(`
    INSERT INTO ${statsTable(client, 'client_ip_account_usage_range_windows')} (
      ip_hash, account_id, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
      first_token_ms_sum, first_token_ms_count, average_first_token_ms,
      active_days, last_used_at, last_error_at, updated_at
    )
    SELECT
      ip_hash,
      account_id,
      ?,
      ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(success_count), 0),
      COALESCE(SUM(error_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
      COALESCE(SUM(duration_ms_sum), 0),
      COALESCE(SUM(duration_ms_count), 0),
      COALESCE(MAX(duration_ms_max), 0),
      CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
      COALESCE(SUM(first_token_ms_sum), 0),
      COALESCE(SUM(first_token_ms_count), 0),
      CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
      COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
      MAX(last_used_at),
      MAX(last_error_at),
      ?
    FROM ${statsTable(client, 'client_ip_account_stats_daily')}
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY ip_hash, account_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
  `, [startDate, endDate, updatedAt, startDate, endDate])
}

async function refreshClientIpAccountUsageRangeWindowForIpsAsync(
  client: DatabaseClient,
  startDate: string,
  endDate: string,
  ipHashes: string[],
  updatedAt: string
): Promise<void> {
  if (!ipHashes.length) return
  for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
    const placeholders = sqlPlaceholders(chunk.length)
    await client.execute(`
      DELETE FROM ${statsTable(client, 'client_ip_account_usage_range_windows')}
      WHERE start_date = ?
        AND end_date = ?
        AND ip_hash IN (${placeholders})
    `, [startDate, endDate, ...chunk])
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_account_usage_range_windows')} (
        ip_hash, account_id, start_date, end_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
        first_token_ms_sum, first_token_ms_count, average_first_token_ms,
        active_days, last_used_at, last_error_at, updated_at
      )
      SELECT
        ip_hash,
        account_id,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(cache_write_tokens), 0),
        COALESCE(SUM(cache_write_1h_tokens), 0),
        COALESCE(SUM(cache_write_cost_usd), 0),
        COALESCE(SUM(thinking_tokens), 0),
        COALESCE(SUM(input_image_tokens), 0),
        COALESCE(SUM(output_image_tokens), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM ${statsTable(client, 'client_ip_account_stats_daily')}
      WHERE stat_date >= ?
        AND stat_date <= ?
        AND ip_hash IN (${placeholders})
      GROUP BY ip_hash, account_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(cache_write_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
        OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
        OR COALESCE(SUM(thinking_tokens), 0) > 0
        OR COALESCE(SUM(input_image_tokens), 0) > 0
        OR COALESCE(SUM(output_image_tokens), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `, [startDate, endDate, updatedAt, startDate, endDate, ...chunk])
  }
}

function markClientIpUsageRangeWindowReady(database: DatabaseSync, startDate: string, endDate: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      updated_at = excluded.updated_at
  `).run(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName, updatedAt, updatedAt)
}

function markClientIpUsageRangeWindowsReady(database: DatabaseSync, windows: Array<{ startDate: string; endDate: string }>, updatedAt: string): void {
  for (const window of windows) {
    markClientIpUsageRangeWindowReady(database, window.startDate, window.endDate, updatedAt)
  }
}

async function markClientIpUsageRangeWindowReadyAsync(client: DatabaseClient, startDate: string, endDate: string, updatedAt: string): Promise<void> {
  await client.execute(`
    INSERT INTO ${statsTable(client, 'stats_job_state')} (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      updated_at = excluded.updated_at
  `, [clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName, updatedAt, updatedAt])
}

async function markClientIpUsageRangeWindowsReadyAsync(
  client: DatabaseClient,
  windows: Array<{ startDate: string; endDate: string }>,
  updatedAt: string
): Promise<void> {
  for (const window of windows) {
    await markClientIpUsageRangeWindowReadyAsync(client, window.startDate, window.endDate, updatedAt)
  }
}

function hasStaleClientIpUsageRangeWindows(database: DatabaseSync, windows: Array<{ startDate: string; endDate: string }>): boolean {
  const statement = database.prepare(`
    SELECT last_success_at
    FROM stats_job_state
    WHERE scope_type = ?
      AND scope_id = ?
      AND job_name = ?
    LIMIT 1
  `)
  for (const window of windows) {
    const row = statement.get(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName) as
      | { last_success_at?: string | null }
      | undefined
    if (row && !row.last_success_at) return true
  }
  return false
}

async function hasStaleClientIpUsageRangeWindowsAsync(client: DatabaseClient, windows: Array<{ startDate: string; endDate: string }>): Promise<boolean> {
  for (const window of windows) {
    const row = await client.one<{ last_success_at?: string | null }>(`
      SELECT last_success_at
      FROM ${statsTable(client, 'stats_job_state')}
      WHERE scope_type = ?
        AND scope_id = ?
        AND job_name = ?
      LIMIT 1
    `, [clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName])
    if (row && !row.last_success_at) return true
  }
  return false
}

function takeClientIpRangeWindowDirtyIpHashes(database: DatabaseSync, limit: number): ClientIpRangeWindowDirtyClaim {
  return takeClientIpRangeWindowDirtyIpHashesWithLimit(database, Math.max(1, Math.trunc(limit)))
}

function takeAllClientIpRangeWindowDirtyIpHashes(database: DatabaseSync): ClientIpRangeWindowDirtyClaim {
  return takeClientIpRangeWindowDirtyIpHashesWithLimit(database)
}

function takeClientIpRangeWindowDirtyIpHashesWithLimit(database: DatabaseSync, limit?: number): ClientIpRangeWindowDirtyClaim {
  const limitClause = limit === undefined ? '' : 'LIMIT ?'
  const params = limit === undefined ? [] : [limit]
  const candidates = database.prepare(`
    SELECT ip_hash
    FROM (
      SELECT ip_hash, first_dirty_at FROM client_ip_range_window_dirty_ips
      UNION ALL
      SELECT ip_hash, first_dirty_at FROM client_ip_account_range_window_dirty_ips
    ) dirty
    GROUP BY ip_hash
    ORDER BY MIN(first_dirty_at) ASC, ip_hash ASC
    ${limitClause}
  `).all(...params) as Array<{ ip_hash?: string }>
  return readClientIpRangeWindowDirtyClaim(database, candidates.map((row) => row.ip_hash ?? '').filter(Boolean))
}

function readClientIpRangeWindowDirtyClaim(database: DatabaseSync, ipHashes: string[]): ClientIpRangeWindowDirtyClaim {
  if (!ipHashes.length) return emptyClientIpRangeWindowDirtyClaim()
  const placeholders = sqlPlaceholders(ipHashes.length)
  const clientIpRows = database.prepare(`
    SELECT ip_hash, generation
    FROM client_ip_range_window_dirty_ips
    WHERE ip_hash IN (${placeholders})
  `).all(...ipHashes) as Array<{ ip_hash?: string; generation?: number }>
  const accountRows = database.prepare(`
    SELECT ip_hash, generation
    FROM client_ip_account_range_window_dirty_ips
    WHERE ip_hash IN (${placeholders})
  `).all(...ipHashes) as Array<{ ip_hash?: string; generation?: number }>
  return clientIpRangeWindowDirtyClaim(clientIpRows, accountRows)
}

async function takeClientIpRangeWindowDirtyIpHashesAsync(client: DatabaseClient, limit: number): Promise<ClientIpRangeWindowDirtyClaim> {
  return await takeClientIpRangeWindowDirtyIpHashesWithLimitAsync(client, Math.max(1, Math.trunc(limit)))
}

async function takeAllClientIpRangeWindowDirtyIpHashesAsync(client: DatabaseClient): Promise<ClientIpRangeWindowDirtyClaim> {
  return await takeClientIpRangeWindowDirtyIpHashesWithLimitAsync(client)
}

async function takeClientIpRangeWindowDirtyIpHashesWithLimitAsync(client: DatabaseClient, limit?: number): Promise<ClientIpRangeWindowDirtyClaim> {
  const limitClause = limit === undefined ? '' : 'LIMIT ?'
  const params = limit === undefined ? [] : [limit]
  const candidates = await client.query<{ ip_hash?: string | null }>(`
    SELECT ip_hash
    FROM (
      SELECT ip_hash, first_dirty_at FROM ${statsTable(client, 'client_ip_range_window_dirty_ips')}
      UNION ALL
      SELECT ip_hash, first_dirty_at FROM ${statsTable(client, 'client_ip_account_range_window_dirty_ips')}
    ) dirty
    GROUP BY ip_hash
    ORDER BY MIN(first_dirty_at) ASC, ip_hash ASC
    ${limitClause}
  `, params)
  const ipHashes = candidates.map((row) => row.ip_hash?.trim() ?? '').filter(Boolean)
  if (!ipHashes.length) return emptyClientIpRangeWindowDirtyClaim()
  const placeholders = sqlPlaceholders(ipHashes.length)
  const clientIpRows = await client.query<{ ip_hash?: string | null; generation?: number | string | null }>(`
    SELECT ip_hash, generation
    FROM ${statsTable(client, 'client_ip_range_window_dirty_ips')}
    WHERE ip_hash IN (${placeholders})
    FOR UPDATE SKIP LOCKED
  `, ipHashes)
  const accountRows = await client.query<{ ip_hash?: string | null; generation?: number | string | null }>(`
    SELECT ip_hash, generation
    FROM ${statsTable(client, 'client_ip_account_range_window_dirty_ips')}
    WHERE ip_hash IN (${placeholders})
    FOR UPDATE SKIP LOCKED
  `, ipHashes)
  return clientIpRangeWindowDirtyClaim(clientIpRows, accountRows)
}

function clientIpRangeWindowDirtyClaim(
  clientIpRows: Array<{ ip_hash?: string | null; generation?: number | string | null }>,
  accountRows: Array<{ ip_hash?: string | null; generation?: number | string | null }>
): ClientIpRangeWindowDirtyClaim {
  const normalizeRows = (rows: Array<{ ip_hash?: string | null; generation?: number | string | null }>): ClientIpRangeWindowDirtyRow[] => rows
    .map((row) => ({ ipHash: row.ip_hash?.trim() ?? '', generation: row.generation ?? 0 }))
    .filter((row) => row.ipHash && Number(row.generation) > 0)
  const normalizedClientIpRows = normalizeRows(clientIpRows)
  const normalizedAccountRows = normalizeRows(accountRows)
  const ipHashes = [...new Set([
    ...normalizedClientIpRows.map((row) => row.ipHash),
    ...normalizedAccountRows.map((row) => row.ipHash)
  ])]
  for (const ipHash of ipHashes) {
    clientIpRangeWindowDirtyIpHashes.add(ipHash)
    clientIpAccountRangeWindowDirtyIpHashes.add(ipHash)
  }
  return { ipHashes, clientIpRows: normalizedClientIpRows, accountRows: normalizedAccountRows }
}

function emptyClientIpRangeWindowDirtyClaim(): ClientIpRangeWindowDirtyClaim {
  return { ipHashes: [], clientIpRows: [], accountRows: [] }
}

function clearClientIpRangeWindowDirtyIpHashes(database: DatabaseSync, claim: ClientIpRangeWindowDirtyClaim): void {
  clearClientIpRangeWindowDirtyRows(database, 'client_ip_range_window_dirty_ips', claim.clientIpRows)
  clearClientIpRangeWindowDirtyRows(database, 'client_ip_account_range_window_dirty_ips', claim.accountRows)
  clearClientIpRangeWindowDirtyMemory(claim.ipHashes)
}

function clearClientIpRangeWindowDirtyRows(database: DatabaseSync, tableName: string, rows: ClientIpRangeWindowDirtyRow[]): void {
  for (const chunk of chunkValues(rows, clientIpRangeWindowChunkSize)) {
    const predicate = chunk.map(() => '(ip_hash = ? AND generation = ?)').join(' OR ')
    database.prepare(`DELETE FROM ${tableName} WHERE ${predicate}`).run(...chunk.flatMap((row) => [row.ipHash, row.generation]))
  }
}

async function clearClientIpRangeWindowDirtyIpHashesAsync(client: DatabaseClient, claim: ClientIpRangeWindowDirtyClaim): Promise<void> {
  await clearClientIpRangeWindowDirtyRowsAsync(client, 'client_ip_range_window_dirty_ips', claim.clientIpRows)
  await clearClientIpRangeWindowDirtyRowsAsync(client, 'client_ip_account_range_window_dirty_ips', claim.accountRows)
  clearClientIpRangeWindowDirtyMemory(claim.ipHashes)
}

async function clearClientIpRangeWindowDirtyRowsAsync(client: DatabaseClient, tableName: string, rows: ClientIpRangeWindowDirtyRow[]): Promise<void> {
  for (const chunk of chunkValues(rows, clientIpRangeWindowChunkSize)) {
    const claimedValues = chunk.map(() => '(?, ?::bigint)').join(', ')
    await client.execute(`
      DELETE FROM ${statsTable(client, tableName)} AS dirty
      USING (VALUES ${claimedValues}) AS claimed(ip_hash, generation)
      WHERE dirty.ip_hash = claimed.ip_hash
        AND dirty.generation = claimed.generation
    `, chunk.flatMap((row) => [row.ipHash, row.generation]))
  }
}

function clearClientIpRangeWindowDirtyMemory(ipHashes: string[]): void {
  for (const ipHash of ipHashes) {
    clientIpRangeWindowDirtyIpHashes.delete(ipHash)
    clientIpAccountRangeWindowDirtyIpHashes.delete(ipHash)
  }
}

function hasPendingClientIpRangeWindowDirtyIpHashes(database: DatabaseSync): boolean {
  if (clientIpRangeWindowDirtyIpHashes.size > 0) return true
  if (clientIpAccountRangeWindowDirtyIpHashes.size > 0) return true
  const row = database.prepare('SELECT 1 FROM client_ip_range_window_dirty_ips LIMIT 1').get() as { 1?: number } | undefined
  if (row) return true
  const accountRow = database.prepare('SELECT 1 FROM client_ip_account_range_window_dirty_ips LIMIT 1').get() as { 1?: number } | undefined
  return Boolean(accountRow)
}

async function hasPendingClientIpRangeWindowDirtyIpHashesAsync(client: DatabaseClient): Promise<boolean> {
  if (clientIpRangeWindowDirtyIpHashes.size > 0) return true
  if (clientIpAccountRangeWindowDirtyIpHashes.size > 0) return true
  const row = await client.one<{ pending?: number }>(`SELECT 1 AS pending FROM ${statsTable(client, 'client_ip_range_window_dirty_ips')} LIMIT 1`)
  if (row) return true
  const accountRow = await client.one<{ pending?: number }>(`SELECT 1 AS pending FROM ${statsTable(client, 'client_ip_account_range_window_dirty_ips')} LIMIT 1`)
  return Boolean(accountRow)
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}
