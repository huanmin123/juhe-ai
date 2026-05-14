import type { EChartsOption } from 'echarts'

import type { AiPerformanceOverview } from '@/types/domain'
import { formatHourLabel, formatInteger } from '@/views/stats/statsFormatters'

export const chartColors = ['#1677ff', '#52c41a', '#fa8c16', '#722ed1', '#13c2c2', '#eb2f96', '#2f54eb', '#a0d911', '#fa541c', '#8c8c8c', '#08979c', '#531dab']

export type AiPerformanceMetric = 'averageFirstToken' | 'maxFirstToken' | 'averageDuration' | 'maxDuration'
type AiPerformanceSeries = AiPerformanceOverview['hourlySeries'][number]
type AiPerformancePoint = AiPerformanceSeries['points'][number]

interface AiPerformanceOptionContext {
  colorByAccountId?: Map<string, string>
}

export function buildAiPerformanceOption(overview: AiPerformanceOverview, metric: AiPerformanceMetric, context: AiPerformanceOptionContext = {}): EChartsOption {
  const hours = overview.hourlySeries[0]?.points.map((point) => point.statHour) ?? []
  const orderedSeries = orderedAiPerformanceSeries(overview)
  const colors = orderedSeries.map((series, index) => context.colorByAccountId?.get(series.accountId) ?? chartColors[index % chartColors.length])
  const accountById = new Map(overview.accounts.map((account) => [account.id, account]))
  const nameCounts = overview.accounts.reduce((counts, account) => {
    counts.set(account.name, (counts.get(account.name) ?? 0) + 1)
    return counts
  }, new Map<string, number>())
  const displayName = (accountId: string, accountName: string) => {
    const account = accountById.get(accountId)
    return (nameCounts.get(accountName) ?? 0) > 1 && account?.providerCode
      ? `${accountName}（${account.providerCode}）`
      : accountName
  }
  return {
    color: colors.length ? colors : chartColors,
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => performanceTooltip(params, overview, metric)
    },
    legend: { show: false },
    grid: {
      left: 52,
      right: 28,
      top: 28,
      bottom: 36
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: hours.map(formatHourLabel),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: {
      type: 'value',
      name: '耗时',
      axisLabel: { formatter: durationAxisLabel, color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: orderedSeries.map((series, index) => {
      const account = accountById.get(series.accountId)
      const seriesName = displayName(series.accountId, series.accountName)
      const color = colors[index]
      return {
        name: seriesName,
        type: 'line',
        smooth: true,
        connectNulls: true,
        symbol: 'circle',
        symbolSize: 5,
        lineStyle: { color },
        itemStyle: { color },
        emphasis: { focus: 'series' },
        data: series.points.map((point) => ({
          value: metricPointValue(point, metric) ?? null,
          accountId: series.accountId,
          accountName: series.accountName,
          accountDisplayName: seriesName,
          statHour: point.statHour,
          requestCount: point.requestCount,
          sampleCount: metricSampleCount(point, metric),
          defaultVisible: account?.defaultVisible,
          selected: account?.selected
        }))
      }
    })
  }
}

export function orderedAiPerformanceSeries(overview: AiPerformanceOverview): AiPerformanceSeries[] {
  const accountById = new Map(overview.accounts.map((account) => [account.id, account]))
  const originalIndexById = new Map(overview.hourlySeries.map((series, index) => [series.accountId, index]))
  return [...overview.hourlySeries].sort((left, right) => {
    const leftRank = accountLegendRank(accountById.get(left.accountId))
    const rightRank = accountLegendRank(accountById.get(right.accountId))
    return leftRank - rightRank
      || (originalIndexById.get(left.accountId) ?? 0) - (originalIndexById.get(right.accountId) ?? 0)
  })
}

function accountLegendRank(account?: AiPerformanceOverview['accounts'][number]) {
  if (account?.defaultVisible) return 0
  if (account?.selected) return 1
  return 2
}

interface TooltipPoint {
  marker?: string
  seriesName?: string
  name?: string
  value?: unknown
  axisValueLabel?: string
  data?: unknown
}

function performanceTooltip(params: unknown, overview: AiPerformanceOverview, metric: AiPerformanceMetric) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const visiblePoints = points.filter((point) => pointValue(point) !== undefined)
  const emptyMessage = isFirstTokenMetric(metric) ? '本小时暂无首 token 样本' : '本小时暂无总耗时样本'
  if (!visiblePoints.length) {
    return [`<strong>${title}</strong>`, emptyMessage].join('<br/>')
  }
  const lines = [`<strong>${title}</strong>`]
  for (const point of visiblePoints) {
    const data = tooltipData(point)
    const accountName = String(data.accountDisplayName ?? point.seriesName ?? data.accountName ?? '')
    lines.push(`${point.marker ?? ''}${escapeHtml(accountName)}: ${formatDurationSeconds(pointValue(point))}，样本 ${formatInteger(numberFromTooltip(data.sampleCount))}，请求 ${formatInteger(numberFromTooltip(data.requestCount))}`)
  }
  return lines.join('<br/>')
}

function metricPointValue(point: AiPerformancePoint, metric: AiPerformanceMetric) {
  switch (metric) {
    case 'averageFirstToken':
      return point.averageFirstTokenMs
    case 'maxFirstToken':
      return point.maxFirstTokenMs
    case 'averageDuration':
      return point.averageDurationMs
    case 'maxDuration':
      return point.maxDurationMs
  }
}

function metricSampleCount(point: AiPerformancePoint, metric: AiPerformanceMetric) {
  return isFirstTokenMetric(metric) ? point.firstTokenCount : point.durationCount
}

function isFirstTokenMetric(metric: AiPerformanceMetric) {
  return metric === 'averageFirstToken' || metric === 'maxFirstToken'
}

function tooltipParams(params: unknown): TooltipPoint[] {
  return Array.isArray(params) ? params as TooltipPoint[] : [params as TooltipPoint]
}

function tooltipData(point?: TooltipPoint): Record<string, unknown> {
  return point?.data && typeof point.data === 'object' ? point.data as Record<string, unknown> : {}
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

function durationAxisLabel(value: number) {
  if (!Number.isFinite(value)) return ''
  return formatDurationSeconds(value)
}

function formatDurationSeconds(value?: number) {
  if (value === undefined || !Number.isFinite(value)) return '-'
  const seconds = value / 1000
  if (seconds === 0) return '0s'
  if (seconds < 1) return `${seconds.toFixed(2)}s`
  if (seconds < 10) return `${seconds.toFixed(1)}s`
  return `${Math.round(seconds)}s`
}
