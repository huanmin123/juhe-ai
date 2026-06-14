import type { EChartsOption } from 'echarts'

import { formatDateShortLabel } from '@/shared/dateRange'
import type { AccountUsageStatsOverview, AccountUsageStatsRow } from '@/types/domain'
import { axisNumberLabel, formatCost, formatInteger } from '@/views/stats/statsFormatters'
import { chartColors as aiPerformanceChartColors } from '@/views/ai-performance/aiPerformanceChartOptions'
import { metricValue, type UsageTrendMetric } from './usageTrendMetrics'

export type { UsageTrendMetric } from './usageTrendMetrics'
export const chartColors = aiPerformanceChartColors

export function buildAccountUsageTrendOption(overview: AccountUsageStatsOverview, metric: UsageTrendMetric, visibleRows?: AccountUsageStatsRow[]): EChartsOption {
  const rows = visibleRows ?? orderedUsageRows(overview.rows)
  const dates = rows[0]?.dailyUsage.map((point) => point.statDate) ?? overview.rows[0]?.dailyUsage.map((point) => point.statDate) ?? []
  const tooltipUsage = rows.map((row) => row.dailyUsage)
  return {
    color: chartColors,
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => usageTrendTooltip(params, metric, tooltipUsage)
    },
    legend: { show: false },
    grid: {
      left: 54,
      right: 28,
      top: 28,
      bottom: 36
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: dates.map(formatDateShortLabel),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: {
      type: 'value',
      name: metricAxisName(metric),
      axisLabel: { formatter: (value: number) => metricAxisLabel(value, metric), color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: rows.map((row) => ({
      name: row.name,
      type: 'line',
      smooth: true,
      connectNulls: false,
      symbol: 'circle',
      symbolSize: 5,
      emphasis: { focus: 'series' },
      data: row.dailyUsage.map((point) => normalizedMetricValue(point, metric))
    }))
  }
}

export function orderedUsageRows(rows: AccountUsageStatsRow[]): AccountUsageStatsRow[] {
  return [...rows].sort((left, right) => {
    const requestDelta = right.rangeUsage.requestCount - left.rangeUsage.requestCount
    if (requestDelta !== 0) return requestDelta
    const costDelta = right.rangeUsage.totalCost - left.rangeUsage.totalCost
    if (costDelta !== 0) return costDelta
    const tokenDelta = right.rangeUsage.totalTokens - left.rangeUsage.totalTokens
    if (tokenDelta !== 0) return tokenDelta
    return left.name.localeCompare(right.name, 'zh-CN') || left.id.localeCompare(right.id)
  })
}

function normalizedMetricValue(point: { requestCount: number; totalTokens: number; totalCost: number }, metric: UsageTrendMetric) {
  const value = Number(metricValue(point, metric))
  return Number.isFinite(value) ? value : 0
}

function metricAxisName(metric: UsageTrendMetric) {
  if (metric === 'cost') return '成本'
  if (metric === 'tokens') return 'Token'
  return '请求'
}

function metricAxisLabel(value: number, metric: UsageTrendMetric) {
  if (metric === 'cost') return `$${value >= 10 ? value.toFixed(1) : value.toFixed(2)}`
  return axisNumberLabel(value)
}

interface TooltipPoint {
  marker?: string
  seriesName?: string
  name?: string
  value?: unknown
  axisValueLabel?: string
  dataIndex?: number
  seriesIndex?: number
}

function usageTrendTooltip(params: unknown, metric: UsageTrendMetric, usageBySeriesIndex: Array<AccountUsageStatsRow['dailyUsage']>) {
  const points = Array.isArray(params) ? params as TooltipPoint[] : [params as TooltipPoint]
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const visiblePoints = points.filter((point) => {
    const value = pointValue(point)
    return value !== undefined && value > 0
  })
  if (!visiblePoints.length) {
    return [`<strong>${title}</strong>`, '本日暂无消耗'].join('<br/>')
  }
  const lines = [`<strong>${title}</strong>`]
  for (const point of visiblePoints) {
    const data = tooltipData(point, usageBySeriesIndex)
    const value = pointValue(point)
    const formatted = metric === 'cost' ? formatCost(value) : formatInteger(value)
    lines.push(`${point.marker ?? ''}${escapeHtml(point.seriesName ?? '')}: ${formatted}，请求 ${formatInteger(data?.requestCount)}，Token ${formatInteger(data?.totalTokens)}，成本 ${formatCost(data?.totalCost)}`)
  }
  return lines.join('<br/>')
}

function tooltipData(point: TooltipPoint | undefined, usageBySeriesIndex: Array<AccountUsageStatsRow['dailyUsage']>) {
  if (point?.seriesIndex === undefined || point.dataIndex === undefined) return undefined
  return usageBySeriesIndex[point.seriesIndex]?.[point.dataIndex]
}

function pointValue(point?: TooltipPoint) {
  return numberFromTooltip(point?.value)
}

function numberFromTooltip(value: unknown): number | undefined {
  if (typeof value === 'number') return value
  if (typeof value === 'string') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : undefined
  }
  if (Array.isArray(value)) {
    return numberFromTooltip(value[value.length - 1])
  }
  return undefined
}

function escapeHtml(value: unknown) {
  const htmlEscapes: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }
  return String(value ?? '').replace(/[&<>"']/g, (character) => htmlEscapes[character])
}
