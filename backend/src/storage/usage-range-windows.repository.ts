import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { dateKey } from './usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from './usage-stats-window-helpers.js'

const USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY = 1

export function refreshUsageScopeRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  database.prepare('DELETE FROM usage_scope_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  const insert = database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, last_error_at, updated_at
    )
    SELECT
      system_account_id,
      scope_type,
      scope_id,
      ?,
      ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(success_count), 0),
      COALESCE(SUM(error_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      COALESCE(SUM(duration_ms_sum), 0),
      COALESCE(SUM(duration_ms_count), 0),
      COALESCE(MAX(duration_ms_max), 0),
      COALESCE(SUM(first_token_ms_sum), 0),
      COALESCE(SUM(first_token_ms_count), 0),
      COALESCE(MAX(first_token_ms_max), 0),
      COUNT(CASE
        WHEN request_count > 0
          OR input_tokens > 0
          OR output_tokens > 0
          OR cache_read_tokens > 0
          OR total_cost_usd > 0
        THEN 1
      END),
      MAX(last_used_at),
      MAX(last_error_at),
      ?
    FROM usage_stats_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, scope_type, scope_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insert.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

export async function refreshUsageScopeRangeWindowSnapshotsAsync(client: DatabaseClient, updatedAt: string, timezone: string, yieldToEventLoop?: () => Promise<void>): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      await client.transaction(async (tx) => {
        await tx.execute(`DELETE FROM ${statsTable(tx, 'usage_scope_range_windows')} WHERE end_date = ? AND start_date = ?`, [rangeEndDate, startDate])
        await tx.execute(`
          INSERT INTO ${statsTable(tx, 'usage_scope_range_windows')} (
            system_account_id, scope_type, scope_id, start_date, end_date,
            request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
            cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
            first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
            last_used_at, last_error_at, updated_at
          )
          SELECT
            system_account_id,
            scope_type,
            scope_id,
            ?,
            ?,
            COALESCE(SUM(request_count), 0),
            COALESCE(SUM(success_count), 0),
            COALESCE(SUM(error_count), 0),
            COALESCE(SUM(input_tokens), 0),
            COALESCE(SUM(output_tokens), 0),
            COALESCE(SUM(cache_read_tokens), 0),
            COALESCE(SUM(cache_read_cost_usd), 0),
            COALESCE(SUM(total_cost_usd), 0),
            COALESCE(SUM(duration_ms_sum), 0),
            COALESCE(SUM(duration_ms_count), 0),
            COALESCE(MAX(duration_ms_max), 0),
            COALESCE(SUM(first_token_ms_sum), 0),
            COALESCE(SUM(first_token_ms_count), 0),
            COALESCE(MAX(first_token_ms_max), 0),
            COUNT(CASE
              WHEN request_count > 0
                OR input_tokens > 0
                OR output_tokens > 0
                OR cache_read_tokens > 0
                OR total_cost_usd > 0
              THEN 1
            END),
            MAX(last_used_at),
            MAX(last_error_at),
            ?
          FROM ${statsTable(tx, 'usage_stats_daily')}
          WHERE stat_date >= ?
            AND stat_date <= ?
          GROUP BY system_account_id, scope_type, scope_id
          HAVING COALESCE(SUM(request_count), 0) > 0
            OR COALESCE(SUM(input_tokens), 0) > 0
            OR COALESCE(SUM(output_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
            OR COALESCE(SUM(total_cost_usd), 0) > 0
        `, [startDate, rangeEndDate, updatedAt, startDate, rangeEndDate])
      })
      await yieldToEventLoop?.()
    }
  }
}

export function refreshAuthorizationUsageRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  database.prepare('DELETE FROM authorization_team_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  database.prepare('DELETE FROM authorization_user_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)

  const insertTeamRange = database.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_team_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  const insertUserRange = database.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      grantee_filter_system_account_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_user_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insertTeamRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
      insertUserRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

export async function refreshAuthorizationUsageRangeWindowSnapshotsAsync(client: DatabaseClient, updatedAt: string, timezone: string, yieldToEventLoop?: () => Promise<void>): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      await client.transaction(async (tx) => {
        await tx.execute(`DELETE FROM ${statsTable(tx, 'authorization_team_usage_range_windows')} WHERE end_date = ? AND start_date = ?`, [rangeEndDate, startDate])
        await tx.execute(`DELETE FROM ${statsTable(tx, 'authorization_user_usage_range_windows')} WHERE end_date = ? AND start_date = ?`, [rangeEndDate, startDate])
        await tx.execute(`
          INSERT INTO ${statsTable(tx, 'authorization_team_usage_range_windows')} (
            system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
            request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
          )
          SELECT
            system_account_id,
            ?,
            ?,
            team_filter_id,
            resource_filter_type,
            resource_filter_id,
            COALESCE(SUM(request_count), 0),
            COALESCE(SUM(input_tokens), 0),
            COALESCE(SUM(output_tokens), 0),
            COALESCE(SUM(cache_read_tokens), 0),
            COALESCE(SUM(cache_read_cost_usd), 0),
            COALESCE(SUM(total_cost_usd), 0),
            MAX(last_used_at),
            ?
          FROM ${statsTable(tx, 'authorization_team_usage_summary_daily')}
          WHERE stat_date >= ?
            AND stat_date <= ?
          GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
          HAVING COALESCE(SUM(request_count), 0) > 0
            OR COALESCE(SUM(input_tokens), 0) > 0
            OR COALESCE(SUM(output_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
            OR COALESCE(SUM(total_cost_usd), 0) > 0
        `, [startDate, rangeEndDate, updatedAt, startDate, rangeEndDate])
        await tx.execute(`
          INSERT INTO ${statsTable(tx, 'authorization_user_usage_range_windows')} (
            system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
            request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
          )
          SELECT
            system_account_id,
            ?,
            ?,
            team_filter_id,
            grantee_filter_system_account_id,
            resource_filter_type,
            resource_filter_id,
            COALESCE(SUM(request_count), 0),
            COALESCE(SUM(input_tokens), 0),
            COALESCE(SUM(output_tokens), 0),
            COALESCE(SUM(cache_read_tokens), 0),
            COALESCE(SUM(cache_read_cost_usd), 0),
            COALESCE(SUM(total_cost_usd), 0),
            MAX(last_used_at),
            ?
          FROM ${statsTable(tx, 'authorization_user_usage_summary_daily')}
          WHERE stat_date >= ?
            AND stat_date <= ?
          GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
          HAVING COALESCE(SUM(request_count), 0) > 0
            OR COALESCE(SUM(input_tokens), 0) > 0
            OR COALESCE(SUM(output_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_tokens), 0) > 0
            OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
            OR COALESCE(SUM(total_cost_usd), 0) > 0
        `, [startDate, rangeEndDate, updatedAt, startDate, rangeEndDate])
      })
      await yieldToEventLoop?.()
    }
  }
}

export async function refreshUsageScopeRangeWindowSnapshotsInStages(
  database: DatabaseSync,
  updatedAt: string,
  timezone: string,
  yieldToEventLoop: () => Promise<void>,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  const refreshStartIndex = usageScopeRangeWindowRefreshStartIndex(database, dates, todayKey, previousSourceWatermark, sourceWatermark)
  if (refreshStartIndex === undefined) return
  const tempTableName = 'usage_scope_range_windows_refresh_tmp'
  prepareUsageScopeRangeWindowRefreshTempTable(database, tempTableName)
  try {
    database.prepare(`DELETE FROM ${tempTableName}`).run()
    const insert = database.prepare(`
      INSERT INTO ${tempTableName} (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
        last_used_at, last_error_at, updated_at
      )
      SELECT
        system_account_id,
        scope_type,
        scope_id,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        COALESCE(MAX(first_token_ms_max), 0),
        COUNT(CASE
          WHEN request_count > 0
            OR input_tokens > 0
            OR output_tokens > 0
            OR cache_read_tokens > 0
            OR total_cost_usd > 0
          THEN 1
        END),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM usage_stats_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, scope_type, scope_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    let processedRanges = 0
    for (let endIndex = refreshStartIndex; endIndex < dates.length; endIndex += 1) {
      for (let startIndex = 0; startIndex <= endIndex; startIndex += 1) {
        const startDate = dates[startIndex]
        const rangeEndDate = dates[endIndex]
        insert.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        processedRanges += 1
        if (processedRanges % USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY === 0) {
          await yieldToEventLoop()
        }
      }
    }
    await publishUsageScopeRangeWindowSnapshotsInStages(database, dates, refreshStartIndex, tempTableName, yieldToEventLoop)
  } finally {
    clearTemporaryRangeWindowTable(database, tempTableName)
  }
}

function usageScopeRangeWindowRefreshStartIndex(
  database: DatabaseSync,
  dates: string[],
  todayKey: string,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): number | undefined {
  if (!previousSourceWatermark) return 0
  const previousUpdatedAt = rangeWindowSourceWatermarkUpdatedAt(previousSourceWatermark)
  const sourceUpdatedAt = rangeWindowSourceWatermarkUpdatedAt(sourceWatermark)
  if (!previousUpdatedAt) return 0
  if (sourceUpdatedAt && sourceUpdatedAt < previousUpdatedAt) return 0
  const row = database.prepare(`
    SELECT MIN(stat_date) AS stat_date
    FROM usage_stats_daily
    WHERE updated_at > ?
      AND stat_date >= ?
      AND stat_date <= ?
  `).get(previousUpdatedAt, dates[0], todayKey) as { stat_date?: string | null } | undefined
  const changedDate = row?.stat_date
  if (!changedDate && sourceWatermark !== previousSourceWatermark) return 0
  if (!changedDate) return undefined
  const index = dates.findIndex((date) => date >= changedDate)
  return index >= 0 ? index : undefined
}

function rangeWindowSourceWatermarkUpdatedAt(watermark?: string): string | undefined {
  if (!watermark) return undefined
  const [updatedAt] = watermark.split('|', 1)
  return updatedAt || undefined
}

export async function refreshAuthorizationUsageRangeWindowSnapshotsInStages(
  database: DatabaseSync,
  updatedAt: string,
  timezone: string,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  const teamTempTableName = 'authorization_team_usage_range_windows_refresh_tmp'
  const userTempTableName = 'authorization_user_usage_range_windows_refresh_tmp'
  prepareAuthorizationUsageRangeWindowRefreshTempTables(database, teamTempTableName, userTempTableName)
  try {
    database.prepare(`DELETE FROM ${teamTempTableName}`).run()
    database.prepare(`DELETE FROM ${userTempTableName}`).run()

    const insertTeamRange = database.prepare(`
      INSERT INTO ${teamTempTableName} (
        system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        ?,
        ?,
        team_filter_id,
        resource_filter_type,
        resource_filter_id,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        MAX(last_used_at),
        ?
      FROM authorization_team_usage_summary_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    const insertUserRange = database.prepare(`
      INSERT INTO ${userTempTableName} (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        ?,
        ?,
        team_filter_id,
        grantee_filter_system_account_id,
        resource_filter_type,
        resource_filter_id,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        MAX(last_used_at),
        ?
      FROM authorization_user_usage_summary_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    let processedRanges = 0
    for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
      for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
        const startDate = dates[startIndex]
        const rangeEndDate = dates[endIndex]
        insertTeamRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        insertUserRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        processedRanges += 1
        if (processedRanges % USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY === 0) {
          await yieldToEventLoop()
        }
      }
    }
    publishAuthorizationUsageRangeWindowSnapshots(database, dates[0], todayKey, teamTempTableName, userTempTableName)
  } finally {
    clearTemporaryRangeWindowTable(database, teamTempTableName)
    clearTemporaryRangeWindowTable(database, userTempTableName)
  }
}

function prepareUsageScopeRangeWindowRefreshTempTable(database: DatabaseSync, tableName: string): void {
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${tableName} (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      request_count INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      error_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      duration_ms_sum INTEGER NOT NULL DEFAULT 0,
      duration_ms_count INTEGER NOT NULL DEFAULT 0,
      duration_ms_max INTEGER NOT NULL DEFAULT 0,
      first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
      first_token_ms_count INTEGER NOT NULL DEFAULT 0,
      first_token_ms_max INTEGER NOT NULL DEFAULT 0,
      active_days INTEGER NOT NULL DEFAULT 0,
      last_used_at TEXT,
      last_error_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
    )
  `).run()
  database.prepare(`
    CREATE INDEX IF NOT EXISTS ${tableName}_range_lookup
      ON ${tableName}(end_date, start_date)
  `).run()
}

function prepareAuthorizationUsageRangeWindowRefreshTempTables(database: DatabaseSync, teamTableName: string, userTableName: string): void {
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${teamTableName} (
      system_account_id TEXT NOT NULL,
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      team_filter_id TEXT NOT NULL DEFAULT '',
      resource_filter_type TEXT NOT NULL DEFAULT 'all',
      resource_filter_id TEXT NOT NULL DEFAULT '',
      request_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      last_used_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
    )
  `).run()
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${userTableName} (
      system_account_id TEXT NOT NULL,
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      team_filter_id TEXT NOT NULL DEFAULT '',
      grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
      resource_filter_type TEXT NOT NULL DEFAULT 'all',
      resource_filter_id TEXT NOT NULL DEFAULT '',
      request_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      last_used_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
    )
  `).run()
}

async function publishUsageScopeRangeWindowSnapshotsInStages(
  database: DatabaseSync,
  dates: string[],
  refreshStartIndex: number,
  tempTableName: string,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  for (let endIndex = refreshStartIndex; endIndex < dates.length; endIndex += 1) {
    await publishUsageScopeRangeWindowSnapshotEndDate(database, dates, endIndex, tempTableName, yieldToEventLoop)
  }
}

async function publishUsageScopeRangeWindowSnapshotEndDate(
  database: DatabaseSync,
  dates: string[],
  endIndex: number,
  tempTableName: string,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  const endDate = dates[endIndex]
  for (let startIndex = 0; startIndex <= endIndex; startIndex += 1) {
    const startDate = dates[startIndex]
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      database
        .prepare('DELETE FROM usage_scope_range_windows WHERE end_date = ? AND start_date = ?')
        .run(endDate, startDate)
      database.prepare(`
      INSERT INTO usage_scope_range_windows (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
        last_used_at, last_error_at, updated_at
      )
      SELECT
        system_account_id,
        scope_type,
        scope_id,
        start_date,
        end_date,
        request_count,
        success_count,
        error_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        duration_ms_sum,
        duration_ms_count,
        duration_ms_max,
        first_token_ms_sum,
        first_token_ms_count,
        first_token_ms_max,
        active_days,
        last_used_at,
        last_error_at,
        updated_at
      FROM ${tempTableName}
      WHERE end_date = ? AND start_date = ?
    `).run(endDate, startDate)
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
    await yieldToEventLoop()
  }
}

function publishAuthorizationUsageRangeWindowSnapshots(
  database: DatabaseSync,
  startDate: string,
  endDate: string,
  teamTempTableName: string,
  userTempTableName: string
): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM authorization_team_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(startDate, endDate)
    database.prepare('DELETE FROM authorization_user_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO authorization_team_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        start_date,
        end_date,
        team_filter_id,
        resource_filter_type,
        resource_filter_id,
        request_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        last_used_at,
        updated_at
      FROM ${teamTempTableName}
    `).run()
    database.prepare(`
      INSERT INTO authorization_user_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        start_date,
        end_date,
        team_filter_id,
        grantee_filter_system_account_id,
        resource_filter_type,
        resource_filter_id,
        request_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        last_used_at,
        updated_at
      FROM ${userTempTableName}
    `).run()
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function clearTemporaryRangeWindowTable(database: DatabaseSync, tableName: string): void {
  try {
    database.prepare(`DELETE FROM ${tableName}`).run()
  } catch {
  }
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}
