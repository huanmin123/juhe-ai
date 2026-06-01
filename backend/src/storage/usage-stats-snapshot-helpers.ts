import type { DatabaseSync } from 'node:sqlite'

import { listRequestQuotaHourlyWindowHours } from './request-quota-hourly-windows.repository.js'
import { dateKey, hourKey, monthKey } from './usage-stats-helpers.js'
import { DAY_MS, HOUR_MS } from './usage-stats-window-helpers.js'

export function refreshUsageQuotaHourlyWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const windows = usageQuotaHourlyWindows()
  database.prepare('DELETE FROM usage_quota_hourly_windows').run()
  const insert = database.prepare(`
    INSERT INTO usage_quota_hourly_windows (
      system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, ?, COALESCE(SUM(total_cost_usd), 0), ?
    FROM usage_stats_hourly
    WHERE stat_hour >= ?
    GROUP BY system_account_id, scope_type, scope_id
    HAVING COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (const hours of windows) {
    insert.run(hours, updatedAt, hourKey(new Date(Date.now() - hours * HOUR_MS), timezone))
  }
}

export function refreshAccountLast7dRequestRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'account',
    windowKey: 'last7d',
    metric: 'request_count',
    metricColumn: 'request_count',
    sourceTable: 'usage_stats_daily',
    timeWhere: 'stat_date >= ?',
    timeParams: [dateKey(new Date(Date.now() - 6 * DAY_MS), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

export function refreshCallerAccountLast7dRequestRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'caller_account',
    windowKey: 'last7d',
    metric: 'request_count',
    metricColumn: 'request_count',
    sourceTable: 'usage_stats_daily',
    timeWhere: 'stat_date >= ?',
    timeParams: [dateKey(new Date(Date.now() - 6 * DAY_MS), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

export function refreshApiKeyCurrentMonthCostRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'api_key',
    windowKey: 'current_month',
    metric: 'total_cost_usd',
    metricColumn: 'total_cost_usd',
    sourceTable: 'usage_stats_monthly',
    timeWhere: 'stat_month = ?',
    timeParams: [monthKey(new Date(), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

export function refreshAuthorizationCurrentMonthCostRankSnapshot(
  database: DatabaseSync,
  scopeType: 'account_authorization' | 'group_authorization',
  snapshotAt: string,
  updatedAt: string,
  timezone: string
): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType,
    windowKey: 'current_month',
    metric: 'total_cost_usd',
    metricColumn: 'total_cost_usd',
    sourceTable: 'usage_stats_monthly',
    timeWhere: 'stat_month = ?',
    timeParams: [monthKey(new Date(), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

function usageQuotaHourlyWindows(): number[] {
  return listRequestQuotaHourlyWindowHours()
}

function refreshUsageRankSnapshotFromStats(database: DatabaseSync, input: {
  scopeType: string
  windowKey: string
  metric: string
  metricColumn: 'request_count' | 'total_cost_usd'
  sourceTable: 'usage_stats_daily' | 'usage_stats_monthly'
  timeWhere: string
  timeParams: string[]
  snapshotAt: string
  updatedAt: string
  limit: number
}): void {
  database.prepare(`
    DELETE FROM usage_rank_snapshots
    WHERE scope_type = ?
      AND window_key = ?
      AND metric = ?
  `).run(input.scopeType, input.windowKey, input.metric)
  database.prepare(`
    INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at)
    SELECT system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    FROM (
      SELECT
        system_account_id,
        ? AS scope_type,
        ? AS window_key,
        ? AS metric,
        ? AS snapshot_at,
        ROW_NUMBER() OVER (
          PARTITION BY system_account_id
          ORDER BY metric_value DESC, last_used_at DESC, scope_id ASC
        ) AS rank,
        scope_id,
        metric_value,
        ? AS updated_at
      FROM (
        SELECT
          system_account_id,
          scope_id,
          SUM(${input.metricColumn}) AS metric_value,
          MAX(last_used_at) AS last_used_at
        FROM ${input.sourceTable}
        WHERE scope_type = ?
          AND ${input.timeWhere}
        GROUP BY system_account_id, scope_id
        HAVING SUM(${input.metricColumn}) > 0
      )
    )
    WHERE rank <= ?
  `).run(input.scopeType, input.windowKey, input.metric, input.snapshotAt, input.updatedAt, input.scopeType, ...input.timeParams, input.limit)
}
