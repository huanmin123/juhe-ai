import type { EChartsOption } from 'echarts'

import { serverDateTimeTimestamp } from '@/shared/formatters'
import type { DatabaseStorageSnapshotSummary, MonitoredDatabaseRole, TableStorageSnapshotSummary } from '@/types/domain'

export const tableMonitorColumns = [
  { title: '库', key: 'databaseRole', width: 92, fixed: 'left' },
  { title: '表名', key: 'tableName', width: 240, fixed: 'left' },
  { title: '行数', key: 'rowCount', align: 'right', width: 120 },
  { title: '表大小', key: 'tableBytes', align: 'right', width: 120 },
  { title: '索引大小', key: 'indexBytes', align: 'right', width: 120 },
  { title: '总大小', key: 'totalBytes', align: 'right', width: 120 },
  { title: '1 小时增长', key: 'growth1h', width: 150 },
  { title: '24 小时增长', key: 'growth24h', width: 150 },
  { title: '采样时间', key: 'sampledAt', width: 190 }
]

export const tableMonitorDatabaseRoles: MonitoredDatabaseRole[] = ['business', 'dataset', 'stats']

export function tableMonitorRowKey(row: TableStorageSnapshotSummary) {
  return `${row.databaseRole}:${row.tableName}`
}

export function databaseRoleLabel(role: MonitoredDatabaseRole) {
  return {
    business: '业务库',
    dataset: '数据集目录库',
    stats: '统计结果库'
  }[role]
}

export function databaseRoleColor(role: MonitoredDatabaseRole) {
  return {
    business: 'blue',
    dataset: 'orange',
    stats: 'purple'
  }[role]
}

export function totalDatabaseBytes(database?: DatabaseStorageSnapshotSummary): number | undefined {
  if (!database) return undefined
  const total = (database.fileBytes ?? 0) + (database.walBytes ?? 0) + (database.shmBytes ?? 0)
  return total > 0 ? total : undefined
}

export function growthColor(value?: number) {
  if (value === undefined || value === 0) return 'default'
  return value > 0 ? 'orange' : 'green'
}

export function formatGrowthBytes(value?: number) {
  if (value === undefined) return '-'
  if (value === 0) return '0 B'
  return `${value > 0 ? '+' : ''}${formatBytes(value)}`
}

export function formatGrowthRows(value?: number) {
  if (value === undefined) return ''
  if (value === 0) return '0 行'
  return `${value > 0 ? '+' : ''}${formatInteger(value)} 行`
}

export function formatBytes(value?: number) {
  if (value === undefined || !Number.isFinite(value)) return '-'
  const sign = value < 0 ? '-' : ''
  const absolute = Math.abs(value)
  if (absolute >= 1024 ** 3) return `${sign}${(absolute / 1024 ** 3).toFixed(2)} GB`
  if (absolute >= 1024 ** 2) return `${sign}${(absolute / 1024 ** 2).toFixed(1)} MB`
  if (absolute >= 1024) return `${sign}${(absolute / 1024).toFixed(1)} KB`
  return `${sign}${Math.round(absolute)} B`
}

export function formatInteger(value?: number) {
  return value === undefined ? '-' : new Intl.NumberFormat('zh-CN').format(Math.round(value))
}

export function matchesTableNameKeyword(tableName: string, keyword: string): boolean {
  if (!keyword) return true
  const normalizedTableName = tableName.toLowerCase()
  return normalizedTableName === keyword || normalizedTableName.startsWith(keyword)
}

export function buildTableMonitorHistoryChartOption(input: {
  rows: Array<DatabaseStorageSnapshotSummary & { databaseRole: MonitoredDatabaseRole }>
  roles: MonitoredDatabaseRole[]
}): EChartsOption {
  const buckets = [...new Set(input.rows.map((row) => row.sampledAt))].sort()
  return {
    color: ['#1677ff', '#fa8c16', '#722ed1'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => historyTooltip(params)
    },
    legend: {
      top: 4,
      data: input.roles.map(databaseRoleLabel)
    },
    grid: { left: 56, right: 24, top: 48, bottom: 42 },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: buckets.map((bucket) => formatSampleTime(bucket))
    },
    yAxis: {
      type: 'value',
      name: '大小',
      axisLabel: { formatter: (value: number) => formatBytes(value) },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: input.roles.map((role) => historySeries(input.rows, role, buckets))
  }
}

function historySeries(
  rows: Array<DatabaseStorageSnapshotSummary & { databaseRole: MonitoredDatabaseRole }>,
  role: MonitoredDatabaseRole,
  buckets: string[]
) {
  const rowsByTime = new Map(rows.filter((row) => row.databaseRole === role).map((row) => [row.sampledAt, row]))
  return {
    name: databaseRoleLabel(role),
    type: 'line' as const,
    smooth: true,
    showSymbol: false,
    connectNulls: false,
    data: buckets.map((bucket) => {
      const row = rowsByTime.get(bucket)
      const total = totalDatabaseBytes(row)
      return total === undefined
        ? null
        : {
            value: total,
            fileBytes: row?.fileBytes,
            walBytes: row?.walBytes,
            freeBytes: row?.freeBytes,
            tableCount: row?.tableCount,
            sampledAt: row?.sampledAt
          }
    })
  }
}

interface HistoryTooltipPoint {
  marker?: string
  seriesName?: string
  name?: string
  axisValueLabel?: string
  data?: unknown
}

interface HistoryTooltipData {
  value: number
  fileBytes?: number
  walBytes?: number
  freeBytes?: number
  tableCount?: number
}

function historyTooltip(params: unknown) {
  const points = Array.isArray(params) ? params as HistoryTooltipPoint[] : [params as HistoryTooltipPoint]
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${escapeHtml(title)}</strong>`]
  for (const point of points) {
    const data = point.data && typeof point.data === 'object' ? point.data as HistoryTooltipData : undefined
    if (!data || !Number.isFinite(data.value)) continue
    const details = [
      `主库 ${formatBytes(data.fileBytes)}`,
      `WAL ${formatBytes(data.walBytes)}`,
      `空闲 ${formatBytes(data.freeBytes)}`,
      `表 ${formatInteger(data.tableCount)}`
    ].join(' / ')
    lines.push(`${point.marker ?? ''}${escapeHtml(point.seriesName ?? '')}: ${formatBytes(data.value)} · ${details}`)
  }
  return lines.join('<br/>')
}

function escapeHtml(value: unknown) {
  return String(value ?? '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }[character] ?? character))
}

function formatSampleTime(value: string) {
  const timestamp = serverDateTimeTimestamp(value)
  if (timestamp === undefined) return '时间格式异常'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(timestamp)
}
