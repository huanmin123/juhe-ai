import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso } from './database.js'
import {
  defaultRequestQuotaHourlyWindowHours,
  maxRequestQuotaHourlyWindowHours,
  parseRequestQuotaLimitsJson
} from './request-quota-limits.js'

export function rememberRequestQuotaHourlyWindowsFromJson(
  limitsJson: string | null | undefined,
  database: DatabaseSync = getBusinessDatabase(),
  timestamp: string = nowIso()
): void {
  const limits = parseRequestQuotaLimitsJson(limitsJson)
  const hours = limits.hourly?.enabled ? limits.hourly.hours : undefined
  if (!isValidRequestQuotaHourlyWindowHours(hours)) {
    return
  }
  database
    .prepare(`
      INSERT INTO request_quota_hourly_window_configs (window_hours, created_at, updated_at)
      VALUES (?, ?, ?)
      ON CONFLICT(window_hours) DO UPDATE SET updated_at = excluded.updated_at
    `)
    .run(hours, timestamp, timestamp)
}

export function listRequestQuotaHourlyWindowHours(database: DatabaseSync = getBusinessDatabase()): number[] {
  const rows = database
    .prepare(`
      SELECT window_hours
      FROM request_quota_hourly_window_configs
      WHERE window_hours BETWEEN 1 AND ?
      ORDER BY window_hours ASC
      LIMIT ?
    `)
    .all(maxRequestQuotaHourlyWindowHours, maxRequestQuotaHourlyWindowHours) as unknown as Array<{ window_hours?: number | null }>
  const windows = new Set<number>(defaultRequestQuotaHourlyWindowHours)
  for (const row of rows) {
    if (isValidRequestQuotaHourlyWindowHours(row.window_hours)) {
      windows.add(row.window_hours)
    }
  }
  return [...windows].sort((left, right) => left - right)
}

function isValidRequestQuotaHourlyWindowHours(value: unknown): value is number {
  return Number.isInteger(value) && typeof value === 'number' && value >= 1 && value <= maxRequestQuotaHourlyWindowHours
}
