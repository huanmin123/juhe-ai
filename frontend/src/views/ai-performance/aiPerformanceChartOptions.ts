import type { EChartsOption } from 'echarts'

import type { AiPerformanceOverview } from '@/types/domain'
import { axisNumberLabel, formatDuration, formatHourLabel, formatInteger } from '@/views/stats/statsFormatters'

const chartColors = ['#1677ff', '#52c41a', '#fa8c16', '#722ed1', '#13c2c2', '#eb2f96', '#2f54eb', '#a0d911', '#fa541c', '#8c8c8c', '#08979c', '#531dab']

export type AiPerformanceMetric = 'firstToken' | 'duration'

export function buildAiPerformanceOption(overview: AiPerformanceOverview, metric: AiPerformanceMetric): EChartsOption {
  const hours = overview.hourlySeries[0]?.points.map((point) => point.statHour) ?? []
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
    color: chartColors,
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => performanceTooltip(params, overview, metric)
    },
    legend: {
      type: 'scroll',
      bottom: 0,
      itemWidth: 10,
      itemHeight: 10,
      data: overview.hourlySeries.map((series) => displayName(series.accountId, series.accountName))
    },
    grid: {
      left: 52,
      right: 28,
      top: 28,
      bottom: 64
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
      name: 'ms',
      axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: overview.hourlySeries.map((series) => {
      const account = accountById.get(series.accountId)
      const seriesName = displayName(series.accountId, series.accountName)
      return {
        name: seriesName,
        type: 'line',
        smooth: true,
        connectNulls: false,
        symbol: 'circle',
        symbolSize: 5,
        emphasis: { focus: 'series' },
        data: series.points.map((point) => ({
          value: metric === 'firstToken' ? point.averageFirstTokenMs ?? null : point.averageDurationMs ?? null,
          accountId: series.accountId,
          accountName: series.accountName,
          accountDisplayName: seriesName,
          statHour: point.statHour,
          requestCount: point.requestCount,
          sampleCount: metric === 'firstToken' ? point.firstTokenCount : point.durationCount,
          defaultVisible: account?.defaultVisible,
          selected: account?.selected
        }))
      }
    })
  }
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
  const emptyMessage = metric === 'firstToken' ? '本小时暂无首 token 样本' : '本小时暂无总耗时样本'
  if (!visiblePoints.length) {
    return [`<strong>${title}</strong>`, emptyMessage].join('<br/>')
  }
  const accountById = new Map(overview.accounts.map((account) => [account.id, account]))
  const lines = [`<strong>${title}</strong>`]
  for (const point of visiblePoints) {
    const data = tooltipData(point)
    const accountName = String(data.accountDisplayName ?? point.seriesName ?? data.accountName ?? '')
    const account = accountById.get(String(data.accountId ?? ''))
    const badges = [
      account?.defaultVisible ? '默认' : '',
      account?.selected ? '指定' : ''
    ].filter(Boolean)
    const suffix = badges.length ? `（${badges.join('，')}）` : ''
    lines.push(`${point.marker ?? ''}${escapeHtml(accountName)}${suffix}: ${formatDuration(pointValue(point))}，样本 ${formatInteger(numberFromTooltip(data.sampleCount))}，请求 ${formatInteger(numberFromTooltip(data.requestCount))}`)
  }
  return lines.join('<br/>')
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
