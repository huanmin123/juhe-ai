<template>
  <div class="stats-page">
    <a-card class="page-card stats-header-card">
      <div class="page-toolbar stats-toolbar">
        <div class="toolbar-copy">
          <strong>统计概览</strong>
          <span>读取后台缓存快照，避免列表和图表实时扫使用记录。</span>
        </div>
        <div class="page-toolbar-actions">
          <a-button :loading="loading" @click="loadData">刷新</a-button>
        </div>
      </div>
    </a-card>

    <a-row :gutter="[16, 16]">
      <a-col v-for="item in summaryCards" :key="item.key" :xs="24" :sm="12" :lg="6">
        <a-card class="metric-card" :loading="initialLoading">
          <div class="metric-label">{{ item.label }}</div>
          <div class="metric-value">{{ item.value }}</div>
          <div class="metric-extra">{{ item.extra }}</div>
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="14">
        <a-card title="请求 / Token / 平均响应（近 24 小时）" class="page-card chart-card" :loading="initialLoading">
          <a-empty v-if="!hasUsageTrend" :description="usageTrendEmptyDescription" />
          <div v-else ref="usageTrendChartRef" class="chart-panel chart-panel-large" />
        </a-card>
      </a-col>
      <a-col :xs="24" :xl="10">
        <a-card title="模型分布（今日）" class="page-card chart-card" :loading="initialLoading">
          <a-empty v-if="!hasModelDistribution" :description="modelDistributionEmptyDescription" />
          <div v-else ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="10">
        <a-card title="错误 Top 10（今日）" class="page-card chart-card" :loading="initialLoading">
          <a-empty v-if="!hasErrors" :description="errorEmptyDescription" />
          <div v-else ref="errorChartRef" class="chart-panel" />
        </a-card>
      </a-col>
      <a-col :xs="24" :xl="14">
        <a-card title="系统性能 / 网络吞吐趋势（近 24 小时）" class="page-card chart-card" :loading="systemInitialLoading">
          <template v-if="isAdmin">
            <a-empty v-if="!hasSystemTrend" description="等待后台监控采样" />
            <div v-else ref="systemMetricsChartRef" class="chart-panel chart-panel-large" />
          </template>
          <a-empty v-else description="系统监控仅管理员可见" />
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import * as echarts from 'echarts'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import type { ECharts, EChartsOption } from 'echarts'
import type { Ref, ShallowRef } from 'vue'
import { message } from 'ant-design-vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { SystemMetricsOverview, UsageStatsOverview } from '@/types/domain'

const loading = ref(false)
const usageOverview = ref<UsageStatsOverview>()
const systemMetrics = ref<SystemMetricsOverview>()
const isAdmin = authState.isAdmin

const usageTrendChartRef = ref<HTMLDivElement>()
const modelDistributionChartRef = ref<HTMLDivElement>()
const errorChartRef = ref<HTMLDivElement>()
const systemMetricsChartRef = ref<HTMLDivElement>()

const usageTrendChart = shallowRef<ECharts>()
const modelDistributionChart = shallowRef<ECharts>()
const errorChart = shallowRef<ECharts>()
const systemMetricsChart = shallowRef<ECharts>()

const hasUsageTrend = computed(() => (usageOverview.value?.hourlyTrend.length ?? 0) > 0)
const hasModelDistribution = computed(() => (usageOverview.value?.modelDistribution.length ?? 0) > 0)
const hasErrors = computed(() => (usageOverview.value?.errors.length ?? 0) > 0)
const hasSystemTrend = computed(() => (systemMetrics.value?.hourlyTrend.length ?? 0) > 0)
const hasUsageOverview = computed(() => Boolean(usageOverview.value))
const initialLoading = computed(() => loading.value && !hasUsageOverview.value)
const systemInitialLoading = computed(() => loading.value && isAdmin.value && !systemMetrics.value)
const hasHistoricalUsage = computed(() => (usageOverview.value?.totals.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasHistoricalUsage.value ? '近 24 小时暂无趋势数据，累计指标已在上方展示' : '暂无趋势数据')
const modelDistributionEmptyDescription = computed(() => hasHistoricalUsage.value ? '今日暂无模型调用，累计指标已在上方展示' : '今日暂无模型调用')
const errorEmptyDescription = computed(() => hasHistoricalUsage.value ? '今日暂无错误，累计错误率已在上方展示' : '今日暂无错误')

const summaryCards = computed(() => {
  const today = usageOverview.value?.today
  const totals = usageOverview.value?.totals
  return [
    { key: 'requests', label: '今日请求', value: formatInteger(today?.requestCount), extra: `累计 ${formatInteger(totals?.requestCount)} / 错误率 ${formatPercent((today?.errorRate ?? totals?.errorRate ?? 0) * 100)}` },
    { key: 'duration', label: '平均响应', value: formatDuration(today?.averageDurationMs ?? totals?.averageDurationMs), extra: `首 Token ${formatDuration(today?.averageFirstTokenMs ?? totals?.averageFirstTokenMs)}` },
    { key: 'tokens', label: '今日 Token', value: formatInteger(today?.totalTokens), extra: `累计 ${formatInteger(totals?.totalTokens)} / 输入 ${formatInteger(today?.inputTokens)}` },
    { key: 'cost', label: '今日成本', value: formatCost(today?.totalCost), extra: `累计 ${formatCost(totals?.totalCost)} / 滞后 ${formatSeconds(usageOverview.value?.statsLagSeconds)}` }
  ]
})

async function loadData() {
  loading.value = true
  try {
    usageOverview.value = await api.stats.usageOverview()
    if (isAdmin.value) {
      systemMetrics.value = await api.stats.systemMetrics()
    } else {
      systemMetrics.value = undefined
    }
  } catch (error) {
    console.error(error)
    message.error('统计数据加载失败')
  } finally {
    loading.value = false
    renderCharts()
  }
}

function renderCharts() {
  void nextTick(() => {
    renderUsageTrendChart()
    renderModelDistributionChart()
    renderErrorChart()
    renderSystemMetricsChart()
    resizeCharts()
  })
}

function renderUsageTrendChart() {
  if (!hasUsageTrend.value) {
    disposeChart(usageTrendChart)
    return
  }
  const chart = ensureChart(usageTrendChartRef, usageTrendChart)
  if (!chart || !usageOverview.value) return

  const trend = usageOverview.value.hourlyTrend
  const option: EChartsOption = {
    color: ['#1677ff', '#52c41a', '#faad14'],
    tooltip: {
      trigger: 'axis',
      formatter: (params: unknown) => usageTrendTooltip(params)
    },
    legend: {
      bottom: 0,
      data: ['请求数', 'Token', '平均响应']
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
        name: '请求 / Token',
        axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
        splitLine: { lineStyle: { color: '#edf2f7' } }
      },
      {
        type: 'value',
        name: '响应 ms',
        axisLabel: { formatter: axisNumberLabel, color: '#64748b' },
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
        name: 'Token',
        type: 'line',
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.totalTokens),
        areaStyle: { opacity: 0.08 }
      },
      {
        name: '平均响应',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        data: trend.map((item) => item.averageDurationMs ?? null)
      }
    ]
  }
  chart.setOption(option, { notMerge: true })
}

function renderModelDistributionChart() {
  if (!hasModelDistribution.value) {
    disposeChart(modelDistributionChart)
    return
  }
  const chart = ensureChart(modelDistributionChartRef, modelDistributionChart)
  if (!chart || !usageOverview.value) return

  const option: EChartsOption = {
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
        data: usageOverview.value.modelDistribution.map((item) => ({
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
  chart.setOption(option, { notMerge: true })
}

function renderErrorChart() {
  if (!hasErrors.value) {
    disposeChart(errorChart)
    return
  }
  const chart = ensureChart(errorChartRef, errorChart)
  if (!chart || !usageOverview.value) return

  const errors = usageOverview.value.errors
  const option: EChartsOption = {
    color: ['#ff4d4f'],
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
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
      data: errors.map((item) => errorLabel(item)),
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
        name: '错误次数',
        type: 'bar',
        barMaxWidth: 28,
        data: errors.map((item) => ({
          value: item.errorCount,
          errorCode: item.errorCode,
          statusCode: item.statusCode,
          errorMessage: item.errorMessage,
          providerCode: item.providerCode
        })),
        itemStyle: { borderRadius: [4, 4, 0, 0] }
      }
    ]
  }
  chart.setOption(option, { notMerge: true })
}

function renderSystemMetricsChart() {
  if (!isAdmin.value || !hasSystemTrend.value) {
    disposeChart(systemMetricsChart)
    return
  }
  const chart = ensureChart(systemMetricsChartRef, systemMetricsChart)
  if (!chart || !systemMetrics.value) return

  const trend = systemMetrics.value.hourlyTrend
  const option: EChartsOption = {
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
  chart.setOption(option, { notMerge: true })
}

function ensureChart(elementRef: Ref<HTMLDivElement | undefined>, chartRef: ShallowRef<ECharts | undefined>) {
  const element = elementRef.value
  if (!element) return undefined
  if (!chartRef.value || chartRef.value.isDisposed()) {
    chartRef.value = echarts.init(element)
  }
  return chartRef.value
}

function disposeChart(chartRef: ShallowRef<ECharts | undefined>) {
  if (chartRef.value && !chartRef.value.isDisposed()) {
    chartRef.value.dispose()
  }
  chartRef.value = undefined
}

function resizeCharts() {
  for (const chart of [usageTrendChart.value, modelDistributionChart.value, errorChart.value, systemMetricsChart.value]) {
    if (chart && !chart.isDisposed()) chart.resize()
  }
}

function usageTrendTooltip(params: unknown) {
  const points = tooltipParams(params)
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${title}</strong>`]
  for (const point of points) {
    const name = String(point.seriesName ?? '')
    const value = pointValue(point)
    const formatted = name === '平均响应' ? formatDuration(value) : formatInteger(value)
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
    `请求：${formatInteger(numberFromTooltip(data.requestCount))}`,
    `Token：${formatInteger(numberFromTooltip(data.totalTokens))}`,
    `成本：${formatCost(numberFromTooltip(data.totalCost))}`
  ].join('<br/>')
}

function errorTooltip(params: unknown) {
  const point = tooltipParams(params)[0]
  const data = tooltipData(point)
  return [
    `<strong>${data.errorCode ?? point?.name ?? ''}</strong>`,
    `供应商：${data.providerCode ?? '-'}`,
    `状态码：${data.statusCode ?? '-'}`,
    `次数：${formatInteger(numberFromTooltip(data.value))}`,
    data.errorMessage ? `摘要：${data.errorMessage}` : undefined
  ].filter(Boolean).join('<br/>')
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
    const candidate = value[value.length - 1]
    return numberFromTooltip(candidate)
  }
  return undefined
}

function errorLabel(row: UsageStatsOverview['errors'][number]) {
  return row.statusCode ? `${row.errorCode}\n${row.statusCode}` : row.errorCode
}

function formatInteger(value?: number) {
  return new Intl.NumberFormat('zh-CN').format(Math.round(value ?? 0))
}

function formatCost(value?: number) {
  return `$${(value ?? 0).toFixed(4)}`
}

function formatPercent(value?: number) {
  if (value === undefined) return '-'
  return `${value.toFixed(1)}%`
}

function formatDuration(value?: number) {
  return value === undefined ? '-' : `${Math.round(value)} ms`
}

function formatSeconds(value?: number) {
  return value === undefined ? '-' : `${Math.round(value)} 秒`
}

function bytesPerSecondToMbps(value?: number) {
  return value === undefined ? null : (value * 8) / 1_000_000
}

function formatNetworkRateFromMbps(value?: number) {
  return value === undefined ? '-' : `${value.toFixed(value >= 10 ? 1 : 2)} Mbps`
}


function formatHourLabel(value: string) {
  const date = new Date(`${value}:00:00.000Z`)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
}

function axisNumberLabel(value: number) {
  return compactNumber(value)
}

function compactNumber(value: number) {
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (absolute >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (absolute >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return `${Math.round(value)}`
}

onMounted(() => {
  window.addEventListener('resize', resizeCharts)
  void loadData()
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resizeCharts)
  disposeChart(usageTrendChart)
  disposeChart(modelDistributionChart)
  disposeChart(errorChart)
  disposeChart(systemMetricsChart)
})
</script>

<style scoped>
.stats-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.stats-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.stats-toolbar {
  margin: 0;
}

.toolbar-copy {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: #64748b;
  font-size: 13px;
}

.toolbar-copy strong {
  color: #0f172a;
  font-size: 16px;
}

.metric-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.metric-label {
  color: #64748b;
  font-size: 13px;
}

.metric-value {
  margin-top: 8px;
  color: #0f172a;
  font-size: 26px;
  font-weight: 800;
}

.metric-extra {
  margin-top: 6px;
  color: #94a3b8;
  font-size: 12px;
}

.stats-section {
  margin-top: 0;
}

.chart-card :deep(.ant-card-body) {
  min-height: 328px;
}

.chart-panel {
  width: 100%;
  height: 280px;
}

.chart-panel-large {
  height: 340px;
}

@media (max-width: 768px) {
  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>




