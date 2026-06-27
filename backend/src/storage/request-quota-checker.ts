import type { DatabaseSync } from 'node:sqlite'

import type { RequestQuotaLimits } from '../domain/types.js'
import type { DatabaseClient } from './database-client.js'
import { dateKey, monthKey, usageStatsTimezone, weekKey } from './usage-stats-helpers.js'

const statsSchemaName = 'juhe_stats'

export interface RequestQuotaCosts {
  hourly: number
  daily: number
  weekly: number
  monthly: number
  total: number
}

export function loadRequestQuotaCosts(database: DatabaseSync, input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  now: Date
  hourlyWindowHours?: number
}): RequestQuotaCosts {
  const timezone = usageStatsTimezone()
  const totalRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId) as unknown as { total_cost?: number } | undefined

  const hourlyRow = input.hourlyWindowHours
    ? database.prepare(`
      SELECT COALESCE(total_cost_usd, 0) AS total_cost
      FROM usage_quota_hourly_windows
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND window_hours = ?
    `).get(input.systemAccountId, input.scopeType, input.scopeId, Math.max(1, Math.trunc(input.hourlyWindowHours ?? 1))) as unknown as { total_cost?: number } | undefined
    : undefined

  const dailyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, dateKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  const weeklyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_weekly
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_week = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, weekKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  const monthlyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_monthly
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_month = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, monthKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  return {
    hourly: Number(hourlyRow?.total_cost ?? 0),
    daily: Number(dailyRow?.total_cost ?? 0),
    weekly: Number(weeklyRow?.total_cost ?? 0),
    monthly: Number(monthlyRow?.total_cost ?? 0),
    total: Number(totalRow?.total_cost ?? 0)
  }
}

export type RequestQuotaCostInput = {
  systemAccountId: string
  scopeType: string
  scopeId: string
  now: Date
  hourlyWindowHours?: number
}

export function loadRequestQuotaCostsBatch(database: DatabaseSync, inputs: RequestQuotaCostInput[]): Map<string, RequestQuotaCosts> {
  const timezone = usageStatsTimezone()
  const requests = uniqueRequestQuotaCostInputs(inputs, timezone)
  const output = new Map<string, RequestQuotaCosts>()
  for (const request of requests) {
    output.set(request.key, { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 })
  }
  if (!requests.length) return output

  const totalKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId])
  for (const row of loadCostRows(database, 'usage_stats_totals', 'total_cost', ['system_account_id', 'scope_type', 'scope_id'], [...totalKeys.keys()].map(splitTupleKey))) {
    for (const key of totalKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.total = Number(row.total_cost ?? 0)
    }
  }
  const dailyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statDate])
  for (const row of loadCostRows(database, 'usage_stats_daily', 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_date'], [...dailyKeys.keys()].map(splitTupleKey))) {
    for (const key of dailyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_date])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.daily = Number(row.total_cost ?? 0)
    }
  }
  const weeklyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statWeek])
  for (const row of loadCostRows(database, 'usage_stats_weekly', 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_week'], [...weeklyKeys.keys()].map(splitTupleKey))) {
    for (const key of weeklyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_week])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.weekly = Number(row.total_cost ?? 0)
    }
  }
  const monthlyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statMonth])
  for (const row of loadCostRows(database, 'usage_stats_monthly', 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_month'], [...monthlyKeys.keys()].map(splitTupleKey))) {
    for (const key of monthlyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_month])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.monthly = Number(row.total_cost ?? 0)
    }
  }
  const hourlyRequests = requests.filter((request) => request.hourlyWindowHours !== undefined)
  const hourlyKeys = requestKeysByTuple(hourlyRequests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.hourlyWindowHours])
  for (const row of loadCostRows(database, 'usage_quota_hourly_windows', 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'window_hours'], [...hourlyKeys.keys()].map(splitTupleKey))) {
    for (const key of hourlyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.window_hours])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.hourly = Number(row.total_cost ?? 0)
    }
  }
  return output
}

export async function loadRequestQuotaCostsBatchAsync(client: DatabaseClient, inputs: RequestQuotaCostInput[]): Promise<Map<string, RequestQuotaCosts>> {
  if (client.driver !== 'postgres') {
    throw new Error('loadRequestQuotaCostsBatchAsync 仅支持 PostgreSQL DatabaseClient')
  }
  const timezone = usageStatsTimezone()
  const requests = uniqueRequestQuotaCostInputs(inputs, timezone)
  const output = new Map<string, RequestQuotaCosts>()
  for (const request of requests) {
    output.set(request.key, { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 })
  }
  if (!requests.length) return output

  const totalKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId])
  for (const row of await loadCostRowsAsync(client, statsTableName(client, 'usage_stats_totals'), 'total_cost', ['system_account_id', 'scope_type', 'scope_id'], [...totalKeys.keys()].map(splitTupleKey))) {
    for (const key of totalKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.total = Number(row.total_cost ?? 0)
    }
  }
  const dailyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statDate])
  for (const row of await loadCostRowsAsync(client, statsTableName(client, 'usage_stats_daily'), 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_date'], [...dailyKeys.keys()].map(splitTupleKey))) {
    for (const key of dailyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_date])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.daily = Number(row.total_cost ?? 0)
    }
  }
  const weeklyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statWeek])
  for (const row of await loadCostRowsAsync(client, statsTableName(client, 'usage_stats_weekly'), 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_week'], [...weeklyKeys.keys()].map(splitTupleKey))) {
    for (const key of weeklyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_week])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.weekly = Number(row.total_cost ?? 0)
    }
  }
  const monthlyKeys = requestKeysByTuple(requests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.statMonth])
  for (const row of await loadCostRowsAsync(client, statsTableName(client, 'usage_stats_monthly'), 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'stat_month'], [...monthlyKeys.keys()].map(splitTupleKey))) {
    for (const key of monthlyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.stat_month])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.monthly = Number(row.total_cost ?? 0)
    }
  }
  const hourlyRequests = requests.filter((request) => request.hourlyWindowHours !== undefined)
  const hourlyKeys = requestKeysByTuple(hourlyRequests, (request) => [request.systemAccountId, request.scopeType, request.scopeId, request.hourlyWindowHours])
  for (const row of await loadCostRowsAsync(client, statsTableName(client, 'usage_quota_hourly_windows'), 'total_cost', ['system_account_id', 'scope_type', 'scope_id', 'window_hours'], [...hourlyKeys.keys()].map(splitTupleKey))) {
    for (const key of hourlyKeys.get(tupleKey([row.system_account_id, row.scope_type, row.scope_id, row.window_hours])) ?? []) {
      const costs = output.get(key)
      if (costs) costs.hourly = Number(row.total_cost ?? 0)
    }
  }
  return output
}

export function requestQuotaCostKey(input: RequestQuotaCostInput): string {
  const timezone = usageStatsTimezone()
  return requestQuotaCostKeyFromParts(
    input.systemAccountId,
    input.scopeType,
    input.scopeId,
    dateKey(input.now, timezone),
    weekKey(input.now, timezone),
    monthKey(input.now, timezone),
    input.hourlyWindowHours === undefined ? undefined : Math.max(1, Math.trunc(input.hourlyWindowHours))
  )
}

export function isRequestQuotaExceeded(limits: RequestQuotaLimits, costs: RequestQuotaCosts): boolean {
  return Boolean(
    (limits.hourly?.enabled && costs.hourly >= limits.hourly.limit)
    || (limits.daily?.enabled && costs.daily >= limits.daily.limit)
    || (limits.weekly?.enabled && costs.weekly >= limits.weekly.limit)
    || (limits.monthly?.enabled && costs.monthly >= limits.monthly.limit)
    || (limits.total?.enabled && costs.total >= limits.total.limit)
  )
}

type RequestQuotaCostLookup = {
  key: string
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  statWeek: string
  statMonth: string
  hourlyWindowHours?: number
}

type CostRow = {
  system_account_id: string
  scope_type: string
  scope_id: string
  stat_date?: string
  stat_week?: string
  stat_month?: string
  window_hours?: number
  total_cost?: number
}

function uniqueRequestQuotaCostInputs(inputs: RequestQuotaCostInput[], timezone: string): RequestQuotaCostLookup[] {
  const byKey = new Map<string, RequestQuotaCostLookup>()
  for (const input of inputs) {
    const hourlyWindowHours = input.hourlyWindowHours === undefined ? undefined : Math.max(1, Math.trunc(input.hourlyWindowHours))
    const statDate = dateKey(input.now, timezone)
    const statWeek = weekKey(input.now, timezone)
    const statMonth = monthKey(input.now, timezone)
    const key = requestQuotaCostKeyFromParts(input.systemAccountId, input.scopeType, input.scopeId, statDate, statWeek, statMonth, hourlyWindowHours)
    if (!byKey.has(key)) {
      byKey.set(key, {
        key,
        systemAccountId: input.systemAccountId,
        scopeType: input.scopeType,
        scopeId: input.scopeId,
        statDate,
        statWeek,
        statMonth,
        hourlyWindowHours
      })
    }
  }
  return [...byKey.values()]
}

function requestQuotaCostKeyFromParts(systemAccountId: string, scopeType: string, scopeId: string, statDate?: string, statWeek?: string, statMonth?: string, hourlyWindowHours?: number): string {
  return [systemAccountId, scopeType, scopeId, statDate ?? '', statWeek ?? '', statMonth ?? '', hourlyWindowHours ?? ''].join('\u0000')
}

function loadCostRows(database: DatabaseSync, tableName: string, costAlias: string, columns: string[], tuples: Array<Array<string | number | undefined>>): CostRow[] {
  const normalizedTuples = uniqueTuples(tuples).filter((tuple) => tuple.every((value) => value !== undefined))
  if (!normalizedTuples.length) return []
  const rows: CostRow[] = []
  const chunkSize = Math.max(1, Math.floor(800 / Math.max(1, columns.length)))
  for (let index = 0; index < normalizedTuples.length; index += chunkSize) {
    const chunk = normalizedTuples.slice(index, index + chunkSize)
    const where = chunk
      .map(() => `(${columns.map((column) => `${column} = ?`).join(' AND ')})`)
      .join(' OR ')
    const selectColumns = new Set(['system_account_id', 'scope_type', 'scope_id', ...columns])
    rows.push(...database.prepare(`
      SELECT ${[...selectColumns].join(', ')}, COALESCE(total_cost_usd, 0) AS ${costAlias}
      FROM ${tableName}
      WHERE ${where}
    `).all(...chunk.flat()) as unknown as CostRow[])
  }
  return rows
}

async function loadCostRowsAsync(client: DatabaseClient, tableName: string, costAlias: string, columns: string[], tuples: Array<Array<string | number | undefined>>): Promise<CostRow[]> {
  const normalizedTuples = uniqueTuples(tuples).filter((tuple) => tuple.every((value) => value !== undefined))
  if (!normalizedTuples.length) return []
  const rows: CostRow[] = []
  const chunkSize = Math.max(1, Math.floor(800 / Math.max(1, columns.length)))
  for (let index = 0; index < normalizedTuples.length; index += chunkSize) {
    const chunk = normalizedTuples.slice(index, index + chunkSize)
    const where = chunk
      .map(() => `(${columns.map((column) => `${column} = ?`).join(' AND ')})`)
      .join(' OR ')
    const selectColumns = new Set(['system_account_id', 'scope_type', 'scope_id', ...columns])
    rows.push(...await client.query<CostRow>(`
      SELECT ${[...selectColumns].join(', ')}, COALESCE(total_cost_usd, 0) AS ${costAlias}
      FROM ${tableName}
      WHERE ${where}
    `, chunk.flat()))
  }
  return rows
}

function statsTableName(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

function requestKeysByTuple(requests: RequestQuotaCostLookup[], toTuple: (request: RequestQuotaCostLookup) => Array<string | number | undefined>): Map<string, string[]> {
  const output = new Map<string, string[]>()
  for (const request of requests) {
    const key = tupleKey(toTuple(request))
    output.set(key, [...(output.get(key) ?? []), request.key])
  }
  return output
}

function tupleKey(tuple: Array<string | number | undefined>): string {
  return tuple.map((value) => value ?? '').join('\u0000')
}

function splitTupleKey(key: string): Array<string | number> {
  return key.split('\u0000')
}

function uniqueTuples(tuples: Array<Array<string | number | undefined>>): Array<Array<string | number | undefined>> {
  const seen = new Set<string>()
  const output: Array<Array<string | number | undefined>> = []
  for (const tuple of tuples) {
    const key = tupleKey(tuple)
    if (seen.has(key)) continue
    seen.add(key)
    output.push(tuple)
  }
  return output
}
