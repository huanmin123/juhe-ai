import type { EChartsOption } from 'echarts'

import { providerDisplayName } from '@/shared/providerDisplay'
import { formatDateLabel, formatDateShortLabel } from '@/shared/dateRange'
import type { GoRuntimeTrendItem, SystemMetricsTrendOverview, UsageStatsOverview, UsageStatsOverviewDailyTrendResult } from '@/types/domain'
import {
  axisNumberLabel,
  bytesToMiB,
  formatBytesMiB,
  bytesPerSecondToMbps,
  formatCompactInteger,
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
    color: ['#1677ff', '#ff4d4f', '#faad14'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => usageTrendTooltip(params)
    },
    legend: {
      bottom: 0,
      data: ['请求数', '失败请求', '平均总耗时']
    },
    grid: {
      left: 48,
      right: 56,
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
        name: '次数',
        position: 'left',
        axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
        splitLine: { lineStyle: { color: '#edf2f7' } }
      },
      {
        type: 'value',
        name: '响应',
        position: 'right',
        axisLabel: { formatter: durationAxisLabel, color: '#64748b' },
        splitLine: { show: false }
      }
    ],
    series: [
      {
        name: '请求数',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.requestCount),
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

export function buildDailyConsumptionOption(trend: UsageStatsOverviewDailyTrendResult['dailyTrend']): EChartsOption {
  const lastDate = trend[trend.length - 1]?.statDate
  return {
    color: ['#1677ff'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => dailyConsumptionTooltip(params, lastDate)
    },
    grid: {
      left: 56,
      right: 20,
      top: 16,
      bottom: 32
    },
    xAxis: {
      type: 'category',
      boundaryGap: true,
      data: trend.map((item) => formatDateShortLabel(item.statDate)),
      axisLabel: { color: '#64748b', hideOverlap: true },
      axisLine: { lineStyle: { color: '#d9e2ef' } },
      axisTick: { alignWithLabel: true }
    },
    yAxis: {
      type: 'value',
      name: 'Token',
      min: 0,
      axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: [{
      name: 'Token 消耗',
      type: 'bar',
      barMaxWidth: 24,
      itemStyle: { borderRadius: [4, 4, 0, 0] },
      emphasis: { itemStyle: { color: '#0958d9' } },
      data: trend.map((item) => ({
        value: item.totalTokens,
        statDate: item.statDate,
        totalTokens: item.totalTokens,
        totalCost: item.totalCost
      }))
    }]
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
          value: item.requestCount,
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

export function buildSystemMetricsOption(trend: SystemMetricsTrendOverview['hourlyTrend']): EChartsOption {
  return {
    color: ['#1677ff', '#52c41a', '#13c2c2', '#722ed1'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => systemMetricsTooltip(params)
    },
    legend: {
      bottom: 0,
      data: ['CPU 平均', '内存平均', '入站带宽', '出站带宽']
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
        name: 'Mbps',
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
      }
    ]
  }
}

export type GoRuntimeChartView = 'concurrency' | 'memory' | 'health'

type GoRuntimeSeries = {
  name: string
  yAxisIndex?: number
  data: Array<number | null>
}

function finiteMetric(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function goRuntimeSeries(trend: GoRuntimeTrendItem[], view: GoRuntimeChartView): GoRuntimeSeries[] {
  const series = (definitions: Array<{ name: string; yAxisIndex?: number; read: (item: GoRuntimeTrendItem) => number | null | undefined }>) => definitions
    .map(({ name, yAxisIndex, read }) => ({ name, yAxisIndex, data: trend.map((item) => finiteMetric(read(item))) }))
    .filter((item) => item.data.some((value) => value !== null))

  if (view === 'memory') {
    return series([
      { name: 'Heap Alloc 平均 (MiB)', yAxisIndex: 0, read: (item) => bytesToMiB(item.heapAllocBytesAvg) },
      { name: 'Heap Alloc 峰值 (MiB)', yAxisIndex: 0, read: (item) => bytesToMiB(item.heapAllocBytesMax) },
      { name: 'Heap Live 平均 (MiB)', yAxisIndex: 0, read: (item) => bytesToMiB(item.heapLiveBytesAvg) },
      { name: 'Heap Live 峰值 (MiB)', yAxisIndex: 0, read: (item) => bytesToMiB(item.heapLiveBytesMax) },
      { name: 'Heap Objects 平均（个）', yAxisIndex: 1, read: (item) => item.heapObjectsAvg },
      { name: 'Heap Objects 峰值（个）', yAxisIndex: 1, read: (item) => item.heapObjectsMax }
    ])
  }
  if (view === 'health') {
    return series([
      { name: 'Scheduler P95（毫秒）', yAxisIndex: 0, read: (item) => secondsToMilliseconds(item.schedulerLatencyP95SecondsAvg) },
      { name: 'Scheduler P99（毫秒）', yAxisIndex: 0, read: (item) => secondsToMilliseconds(item.schedulerLatencyP99SecondsAvg) },
      { name: 'GC Pause P95（毫秒）', yAxisIndex: 0, read: (item) => secondsToMilliseconds(item.gcPauseP95SecondsAvg) },
      { name: 'GC Pause P99（毫秒）', yAxisIndex: 0, read: (item) => secondsToMilliseconds(item.gcPauseP99SecondsAvg) }
    ])
  }
  return series([
    { name: 'Goroutine 平均（个）', read: (item) => item.goroutinesAvg },
    { name: 'Goroutine 峰值（个）', read: (item) => item.goroutinesMax },
    { name: 'Runnable 平均（个）', read: (item) => item.goroutinesRunnableAvg },
    { name: 'Runnable 峰值（个）', read: (item) => item.goroutinesRunnableMax },
    { name: 'Waiting 平均（个）', read: (item) => item.goroutinesWaitingAvg },
    { name: 'Waiting 峰值（个）', read: (item) => item.goroutinesWaitingMax },
    { name: '线程平均（个）', read: (item) => item.threadsAvg },
    { name: '线程峰值（个）', read: (item) => item.threadsMax },
    { name: 'GOMAXPROCS（个）', read: (item) => item.gomaxprocsAvg }
  ])
}

function secondsToMilliseconds(value: number | null | undefined): number | null {
  return finiteMetric(value) === null ? null : (value as number) * 1000
}

export function hasGoRuntimeChartData(trend: GoRuntimeTrendItem[], view: GoRuntimeChartView): boolean {
  return goRuntimeSeries(trend, view).length > 0
}

export function buildGoRuntimeOption(trend: GoRuntimeTrendItem[], timezone = 'Asia/Shanghai', view: GoRuntimeChartView = 'concurrency'): EChartsOption {
  const series = goRuntimeSeries(trend, view)
  const isMemoryView = view === 'memory'
  const isHealthView = view === 'health'
  return {
    color: isHealthView
      ? ['#1677ff', '#69b1ff', '#fa8c16', '#ffc069']
      : isMemoryView
        ? ['#52c41a', '#95de64', '#13c2c2', '#87e8de', '#1677ff', '#69b1ff', '#fa8c16', '#ffc069']
        : ['#1677ff', '#69b1ff', '#52c41a', '#95de64', '#fa8c16', '#ffc069', '#722ed1', '#b37feb'],
    tooltip: { trigger: 'axis', formatter: (params: unknown) => goRuntimeTooltip(params, trend) },
    legend: { type: 'scroll', bottom: 0, data: series.map((item) => item.name) },
    grid: { left: 56, right: 64, top: 28, bottom: 72 },
    xAxis: { type: 'category', data: trend.map((item) => goRuntimeWindowLabel(item.windowStart, timezone)), axisLabel: { color: '#64748b' }, axisLine: { lineStyle: { color: '#d9e2ef' } } },
    yAxis: isMemoryView
      ? [
        { type: 'value', name: 'MiB', axisLabel: { formatter: (value: number) => `${value}`, color: '#64748b' }, splitLine: { lineStyle: { color: '#edf2f7' } } },
        { type: 'value', name: '对象数（个）', axisLabel: { formatter: axisNumberLabel, color: '#64748b' }, splitLine: { show: false } }
      ]
      : { type: 'value', name: isHealthView ? '延迟（毫秒）' : '数量（个）', axisLabel: { formatter: isHealthView ? (value: number) => `${value}` : formatInteger, color: '#64748b' }, splitLine: { lineStyle: { color: '#edf2f7' } } },
    series: [
      ...series.map((item) => ({ ...item, type: 'line' as const, smooth: true, symbolSize: 6 }))
    ]
  }
}

function goRuntimeTooltip(params: unknown, trend: GoRuntimeTrendItem[]): string {
  const rows = Array.isArray(params) ? params as Array<{ axisValue?: unknown; seriesName?: unknown; value?: unknown; dataIndex?: unknown }> : []
  const axis = rows[0]?.axisValue == null ? '' : String(rows[0].axisValue)
  const dataIndex = typeof rows[0]?.dataIndex === 'number' ? rows[0].dataIndex : -1
  const sampleCount = dataIndex >= 0 ? trend[dataIndex]?.sampleCount : undefined
  const body = rows.map((item) => {
    const value = typeof item.value === 'number' && Number.isFinite(item.value) ? item.value.toFixed(2) : '暂无'
    return `${String(item.seriesName ?? '')}: ${value}`
  }).join('<br/>')
  const sample = typeof sampleCount === 'number' ? `<br/>采样数（次）: ${formatInteger(sampleCount)}` : ''
  return axis ? `${axis}${sample}<br/>${body}` : `${sample}${body}`
}

function goRuntimeWindowLabel(value: string, timezone: string): string {
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23'
    }).formatToParts(new Date(value))
    const part = (type: string) => parts.find((entry) => entry.type === type)?.value ?? ''
    return formatHourLabel(`${part('year')}-${part('month')}-${part('day')}T${part('hour')}:${part('minute')}`)
  } catch {
    return formatHourLabel(value.replace(/\.\d+Z$/, '').replace(/:00Z$/, '').replace(/Z$/, ''))
  }
}

export function buildProcessEventLoopOption(trend: SystemMetricsTrendOverview['processEventLoopTrend']): EChartsOption {
  const roles = processEventLoopRoles(trend)
  return {
    color: ['#faad14', '#eb2f96', '#13c2c2', '#2f54eb', '#52c41a'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => processEventLoopTooltip(params)
    },
    legend: {
      bottom: 0,
      data: roles.map(processRoleLabel)
    },
    grid: {
      left: 48,
      right: 24,
      top: 28,
      bottom: 56
    },
    xAxis: {
      type: 'category',
      data: processEventLoopBuckets(trend).map((item) => formatHourLabel(item)),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: {
      type: 'value',
      name: 's',
      axisLabel: { formatter: formatDuration, color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: roles.map((role) => ({
      name: processRoleLabel(role),
      type: 'line',
      smooth: true,
      symbol: 'circle',
      symbolSize: 6,
      data: processEventLoopBuckets(trend).map((bucketKey) => processEventLoopValue(trend, bucketKey, role))
    }))
  }
}

export function buildProcessMemoryOption(trend: SystemMetricsTrendOverview['processEventLoopTrend']): EChartsOption {
  const roles = processEventLoopRoles(trend).filter((role) => trend.some((item) => item.processRole === role && processMemoryValue(item) !== null))
  const buckets = processEventLoopBuckets(trend)
  return {
    color: ['#1677ff', '#faad14', '#52c41a', '#eb2f96', '#13c2c2', '#722ed1', '#2f54eb', '#fa541c', '#a0d911', '#8c8c8c'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => processMemoryTooltip(params)
    },
    legend: {
      type: 'scroll',
      bottom: 0,
      data: roles.map(processRoleLabel)
    },
    grid: {
      left: 64,
      right: 24,
      top: 28,
      bottom: 56
    },
    xAxis: {
      type: 'category',
      data: buckets.map((item) => formatHourLabel(item)),
      axisLabel: { color: '#64748b' },
      axisLine: { lineStyle: { color: '#d9e2ef' } }
    },
    yAxis: {
      type: 'value',
      name: 'RSS 峰值',
      axisLabel: { formatter: (value: number) => formatBytesMiB(value), color: '#64748b' },
      splitLine: { lineStyle: { color: '#edf2f7' } }
    },
    series: roles.map((role) => ({
      name: processRoleLabel(role),
      type: 'line',
      smooth: true,
      symbol: 'circle',
      symbolSize: 6,
      data: buckets.map((bucketKey) => processMemoryBucketValue(trend, bucketKey, role))
    }))
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

function dailyConsumptionTooltip(params: unknown, lastDate?: string) {
  const point = tooltipParams(params)[0]
  const data = tooltipData(point)
  const statDate = tooltipRawText(data.statDate)
  const totalTokens = numberFromTooltip(data.totalTokens)
  const currentDaySuffix = statDate && statDate === lastDate ? '（截至当前）' : ''
  return [
    `<strong>${formatDateLabel(statDate)}${currentDaySuffix}</strong>`,
    `${point?.marker ?? ''}Token：${formatCompactInteger(totalTokens)}`,
    `成本：${formatCost(numberFromTooltip(data.totalCost))}`
  ].join('<br/>')
}

function modelTooltip(params: unknown) {
  const point = tooltipParams(params)[0]
  const data = tooltipData(point)
  return [
    `<strong>${point?.name ?? ''}</strong>`,
    `供应商：${providerDisplayName(tooltipRawText(data.providerCode, ''))}`,
    `请求数：${formatInteger(numberFromTooltip(data.requestCount))}`,
    `Token 消耗：${formatCompactInteger(numberFromTooltip(data.totalTokens))}`,
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
    tooltipRow('供应商', providerDisplayName(tooltipRawText(data.providerCode, ''))),
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

function processEventLoopTooltip(params: unknown) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${title}</strong>`]
  for (const point of points) {
    lines.push(`${point.marker ?? ''}${String(point.seriesName ?? '')}: ${formatDuration(pointValue(point))}`)
  }
  return lines.join('<br/>')
}

function processMemoryTooltip(params: unknown) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${title}</strong>`]
  for (const point of points) {
    lines.push(`${point.marker ?? ''}${String(point.seriesName ?? '')}: ${formatBytesMiB(pointValue(point))}`)
  }
  return lines.join('<br/>')
}

function processEventLoopRoles(trend: SystemMetricsTrendOverview['processEventLoopTrend']) {
  return [...new Set(trend.map((item) => item.processRole))].sort()
}

function processEventLoopBuckets(trend: SystemMetricsTrendOverview['processEventLoopTrend']) {
  return [...new Set(trend.map(processEventLoopBucketKey))].sort()
}

function processEventLoopValue(trend: SystemMetricsTrendOverview['processEventLoopTrend'], bucketKey: string, processRole: string) {
  const row = trend.find((item) => processEventLoopBucketKey(item) === bucketKey && item.processRole === processRole)
  return row?.eventLoopLagMsMax ?? row?.eventLoopLagMsAvg ?? null
}

function processMemoryBucketValue(trend: SystemMetricsTrendOverview['processEventLoopTrend'], bucketKey: string, processRole: string) {
  const row = trend.find((item) => processEventLoopBucketKey(item) === bucketKey && item.processRole === processRole)
  return row ? processMemoryValue(row) : null
}

function processMemoryValue(row: SystemMetricsTrendOverview['processEventLoopTrend'][number]) {
  return row.processRssBytesMax ?? row.processRssBytesAvg ?? null
}

function processEventLoopBucketKey(row: SystemMetricsTrendOverview['processEventLoopTrend'][number]) {
  return row.statMinute
}

export function processRoleLabel(processRole: string) {
  if (processRole === 'server') return '主进程'
  if (processRole === 'ingest-worker') return '写入 worker'
  if (processRole === 'stats-worker') return '统计 worker'
  if (processRole === 'ops-worker') return '运维 worker'
  if (processRole === 'db-service') return 'DB service'
  const separatorIndex = processRole.indexOf(':')
  if (separatorIndex > 0) {
    const baseRole = processRole.slice(0, separatorIndex)
    const instance = processRole.slice(separatorIndex + 1)
    if (baseRole === 'gateway') return `Gateway ${instance}`
    if (baseRole === 'control') return `Control ${instance}`
    if (baseRole === 'db-service') return `DB service ${instance}`
    if (baseRole === 'usage-worker') return `Usage Worker ${instance}`
    if (baseRole === 'log-worker') return `Log Worker ${instance}`
    if (baseRole === 'stats-worker') return `Stats Worker ${instance}`
    if (baseRole === 'ops-worker') return `Ops Worker ${instance}`
  }
  return processRole
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
