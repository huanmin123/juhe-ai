import type { EChartsOption } from 'echarts'

import type { SystemMetricsOverview, UsageStatsOverview } from '@/types/domain'
import {
  axisNumberLabel,
  bytesPerSecondToMbps,
  formatCost,
  formatDuration,
  formatDurationSeconds,
  formatHourLabel,
  formatInteger,
  formatNetworkRateFromMbps,
  formatPercent
} from './statsFormatters'

const ERROR_TOOLTIP_MAX_WIDTH = 360
const ERROR_TOOLTIP_EDGE_GAP = 12

export function buildUsageTrendOption(trend: UsageStatsOverview['hourlyTrend']): EChartsOption {
  return {
    color: ['#1677ff', '#ff4d4f', '#52c41a', '#faad14'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => usageTrendTooltip(params)
    },
    legend: {
      bottom: 0,
      data: ['请求数', '失败请求', 'Token 消耗', '平均总耗时']
    },
    grid: {
      left: 48,
      right: 58,
      top: 28,
      bottom: 56
    },
    xAxis: {
      type: 'category',
      boundaryGap: true,
      data: trend.map((item) => formatHourLabel(item.statHour)),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: [
      {
        type: 'value',
        name: '次数 / Token',
        axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
        splitLine: { lineStyle: { color: '#edf2f7' } }
      },
      {
        type: 'value',
        name: '响应',
        axisLabel: { formatter: durationAxisLabel, color: '#64748b' },
        splitLine: { show: false }
      }
    ],
    series: [
      {
        name: '请求数',
        type: 'bar',
        barMaxWidth: 18,
        data: trend.map((item) => item.requestCount),
        itemStyle: { borderRadius: [4, 4, 0, 0] }
      },
      {
        name: '失败请求',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.errorCount)
      },
      {
        name: 'Token 消耗',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.totalTokens),
        areaStyle: { opacity: 0.08 }
      },
      {
        name: '平均总耗时',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.averageDurationMs ?? null)
      }
    ]
  }
}

export function buildModelDistributionOption(distribution: UsageStatsOverview['modelDistribution']): EChartsOption {
  return {
    color: ['#1677ff', '#52c41a', '#722ed1', '#faad14', '#13c2c2', '#eb2f96', '#fa541c', '#2f54eb', '#a0d911', '#8c8c8c'],
    tooltip: {
      trigger: 'item',
      formatter: (params: unknown) => modelTooltip(params)
    },
    legend: {
      type: 'scroll',
      bottom: 0,
      itemWidth: 10,
      itemHeight: 10
    },
    series: [
      {
        name: '模型分布',
        type: 'pie',
        radius: ['48%', '72%'],
        center: ['50%', '45%'],
        avoidLabelOverlap: true,
        minAngle: 8,
        label: {
          formatter: '{b}\n{d}%',
          color: '#334155'
        },
        labelLine: { length: 12, length2: 8 },
        data: distribution.map((item) => ({
          name: item.model,
          value: item.totalTokens > 0 ? item.totalTokens : item.requestCount,
          requestCount: item.requestCount,
          totalTokens: item.totalTokens,
          totalCost: item.totalCost,
          providerCode: item.providerCode
        }))
      }
    ]
  }
}

export function buildErrorOption(errors: UsageStatsOverview['errors']): EChartsOption {
  return {
    color: ['#ff4d4f'],
    tooltip: {
      trigger: 'item',
      triggerOn: 'mousemove|click',
      renderMode: 'html',
      enterable: true,
      confine: true,
      hideDelay: 1200,
      transitionDuration: 0,
      className: 'stats-error-tooltip',
      position: errorTooltipPosition,
      extraCssText: `max-width:${ERROR_TOOLTIP_MAX_WIDTH}px;white-space:normal;overflow-wrap:anywhere;word-break:break-word;user-select:text;`,
      formatter: (params: unknown) => errorTooltip(params)
    },
    grid: {
      left: 44,
      right: 20,
      top: 20,
      bottom: 78
    },
    xAxis: {
      type: 'category',
      data: errors.map((item, index) => errorAxisLabel(item, index)),
      axisLabel: { color: '#64748b', interval: 0, rotate: 35 },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: {
      type: 'value',
      name: '次数',
      axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: [
      {
        name: '失败请求次数',
        type: 'bar',
        barMaxWidth: 28,
        data: errors.map((item, index) => ({
          value: item.errorCount,
          rank: index + 1,
          errorCode: item.errorCode,
          statusCode: item.statusCode,
          errorMessage: item.errorMessage,
          providerCode: item.providerCode
        })),
        itemStyle: { borderRadius: [4, 4, 0, 0] }
      }
    ]
  }
}

export function buildSystemMetricsOption(trend: SystemMetricsOverview['hourlyTrend']): EChartsOption {
  return {
    color: ['#1677ff', '#52c41a', '#13c2c2', '#722ed1', '#faad14'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => systemMetricsTooltip(params)
    },
    legend: {
      bottom: 0,
      data: ['CPU 平均', '内存平均', '入站带宽', '出站带宽', '事件循环延迟']
    },
    grid: {
      left: 48,
      right: 58,
      top: 28,
      bottom: 56
    },
    xAxis: {
      type: 'category',
      data: trend.map((item) => formatHourLabel(item.statHour)),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: [
      {
        type: 'value',
        name: '百分比',
        max: 100,
        axisLabel: { formatter: '{value}%', color: '#64748b' },
        splitLine: { lineStyle: { color: '#edf2f7' } }
      },
      {
        type: 'value',
        name: 'Mbps / ms',
        axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
        splitLine: { show: false }
      }
    ],
    series: [
      {
        name: 'CPU 平均',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.cpuPercentAvg ?? null)
      },
      {
        name: '内存平均',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.memoryUsedPercentAvg ?? null)
      },
      {
        name: '入站带宽',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => bytesPerSecondToMbps(item.networkRxBytesPerSecondAvg))
      },
      {
        name: '出站带宽',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => bytesPerSecondToMbps(item.networkTxBytesPerSecondAvg))
      },
      {
        name: '事件循环延迟',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.eventLoopLagMsAvg ?? null)
      }
    ]
  }
}

function usageTrendTooltip(params: unknown) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${title}</strong>`]
  for (const point of points) {
    const name = String(point.seriesName ?? '')
    const value = pointValue(point)
    const formatted = name === '平均总耗时' ? formatDurationSeconds(value) : formatInteger(value)
    lines.push(`${point.marker ?? ''}${name}: ${formatted}`)
  }
  return lines.join('<br/>')
}

function modelTooltip(params: unknown) {
  const point = tooltipParams(params)[0]
  const data = tooltipData(point)
  return [
    `<strong>${point?.name ?? ''}</strong>`,
    `供应商：${data.providerCode ?? '-'}`,
    `请求数：${formatInteger(numberFromTooltip(data.requestCount))}`,
    `Token 消耗：${formatInteger(numberFromTooltip(data.totalTokens))}`,
    `成本：${formatCost(numberFromTooltip(data.totalCost))}`
  ].join('<br/>')
}

function errorTooltip(params: unknown) {
  const point = tooltipParams(params)[0]
  const data = tooltipData(point)
  const errorCode = tooltipRawText(data.errorCode ?? point?.name)
  const errorMessage = tooltipRawText(data.errorMessage, '')
  const shouldShowMessage = Boolean(errorMessage && errorMessage !== errorCode)
  const rows = [
    tooltipRow('错误码', errorCode),
    tooltipRow('供应商', data.providerCode),
    tooltipRow('状态码', data.statusCode),
    tooltipRow('次数', formatInteger(numberFromTooltip(data.value)))
  ].join('')
  const messageBlock = shouldShowMessage
    ? `<div class="stats-tooltip-block"><div class="stats-tooltip-block-label">错误信息</div><div class="stats-tooltip-message">${escapeHtml(errorMessage)}</div></div>`
    : ''

  return `<div class="stats-tooltip-content"><div class="stats-tooltip-title">错误详情 #${escapeHtml(tooltipRawText(data.rank, '-'))}</div>${rows}${messageBlock}</div>`
}

function systemMetricsTooltip(params: unknown) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${title}</strong>`]
  for (const point of points) {
    const name = String(point.seriesName ?? '')
    const value = pointValue(point)
    const formatted = name.includes('CPU') || name.includes('内存') ? formatPercent(value) : name.includes('带宽') ? formatNetworkRateFromMbps(value) : formatDuration(value)
    lines.push(`${point.marker ?? ''}${name}: ${formatted}`)
  }
  return lines.join('<br/>')
}

interface TooltipPoint {
  marker?: string
  seriesName?: string
  name?: string
  value?: unknown
  axisValueLabel?: string
  data?: unknown
}

function tooltipParams(params: unknown): TooltipPoint[] {
  return Array.isArray(params) ? params as TooltipPoint[] : [params as TooltipPoint]
}

function tooltipData(point?: TooltipPoint): Record<string, unknown> {
  return point?.data && typeof point.data === 'object' ? point.data as Record<string, unknown> : {}
}

function tooltipRow(label: string, value: unknown) {
  return `<div class="stats-tooltip-row"><span class="stats-tooltip-label">${escapeHtml(label)}</span><span class="stats-tooltip-value">${escapeHtml(tooltipRawText(value))}</span></div>`
}

function tooltipRawText(value: unknown, fallback = '-') {
  if (value === undefined || value === null || value === '') return fallback
  return String(value)
}

function escapeHtml(value: unknown) {
  const htmlEscapes: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }
  return tooltipRawText(value, '').replace(/[&<>"']/g, (character) => htmlEscapes[character])
}

function errorTooltipPosition(
  _point: [number, number],
  _params: unknown,
  _element: unknown,
  rect: TooltipRectLike | null,
  size: { contentSize: [number, number]; viewSize: [number, number] }
) {
  const contentWidth = Math.min(size.contentSize[0] || ERROR_TOOLTIP_MAX_WIDTH, ERROR_TOOLTIP_MAX_WIDTH)
  const contentHeight = size.contentSize[1] || 0
  const viewWidth = size.viewSize[0] || 0
  const viewHeight = size.viewSize[1] || 0
  const preferredLeft = rect ? rect.x + rect.width + ERROR_TOOLTIP_EDGE_GAP : ERROR_TOOLTIP_EDGE_GAP
  const fallbackLeft = rect ? rect.x - contentWidth - ERROR_TOOLTIP_EDGE_GAP : viewWidth - contentWidth - ERROR_TOOLTIP_EDGE_GAP
  const left = preferredLeft + contentWidth + ERROR_TOOLTIP_EDGE_GAP <= viewWidth ? preferredLeft : Math.max(ERROR_TOOLTIP_EDGE_GAP, fallbackLeft)
  const preferredTop = rect ? rect.y + rect.height / 2 - contentHeight / 2 : ERROR_TOOLTIP_EDGE_GAP
  const maxTop = Math.max(ERROR_TOOLTIP_EDGE_GAP, viewHeight - contentHeight - ERROR_TOOLTIP_EDGE_GAP)
  const top = Math.max(ERROR_TOOLTIP_EDGE_GAP, Math.min(preferredTop, maxTop))
  return [left, top]
}

interface TooltipRectLike {
  x: number
  y: number
  width: number
  height: number
}

function pointValue(point?: TooltipPoint) {
  return numberFromTooltip(point?.value)
}

function durationAxisLabel(value: number) {
  return formatDurationSeconds(value)
}

function numberFromTooltip(value: unknown): number | undefined {
  if (typeof value === 'number') return value
  if (typeof value === 'string') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : undefined
  }
  if (Array.isArray(value)) {
    const candidate = value[value.length - 1]
    return numberFromTooltip(candidate)
  }
  return undefined
}

function errorAxisLabel(row: UsageStatsOverview['errors'][number], index: number) {
  const label = `${index + 1}. ${truncateText(row.errorCode, 16)}`
  return row.statusCode ? `${label}\n${row.statusCode}` : label
}

function truncateText(value: string, maxLength: number) {
  return value.length > maxLength ? `${value.slice(0, maxLength)}…` : value
}
