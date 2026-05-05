<template>
  <div class="stats-page">
    <a-card class="page-card stats-header-card">
      <div class="page-toolbar stats-toolbar">
        <div class="toolbar-copy">
          <strong>统计概览</strong>
          <span>读取有效消耗缓存快照，排障类失败请到使用记录、审计日志和运行日志查看。</span>
        </div>
        <div class="page-toolbar-actions">
          <a-button :loading="loading" @click="loadData">刷新</a-button>
        </div>
      </div>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="14">
        <StatsChartCard title="有效请求 / Token / 平均响应（近 24 小时）" :loading="initialLoading" :has-data="hasUsageTrend" :empty-description="usageTrendEmptyDescription">
          <div ref="usageTrendChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
      <a-col :xs="24" :xl="10">
        <StatsChartCard title="模型分布（今日）" :loading="initialLoading" :has-data="hasModelDistribution" :empty-description="modelDistributionEmptyDescription">
          <div ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="10">
        <StatsChartCard title="消耗错误 Top 10（今日）" :loading="initialLoading" :has-data="hasErrors" :empty-description="errorEmptyDescription">
          <div ref="errorChartRef" class="chart-panel" />
        </StatsChartCard>
      </a-col>
      <a-col :xs="24" :xl="14">
        <StatsChartCard title="系统性能 / 网络吞吐趋势（近 24 小时）" :loading="systemInitialLoading" :has-data="hasVisibleSystemTrend" :empty-description="systemTrendEmptyDescription">
          <div ref="systemMetricsChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import * as echarts from 'echarts'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import type { ECharts } from 'echarts'
import type { Ref, ShallowRef } from 'vue'
import { message } from 'ant-design-vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { SystemMetricsOverview, UsageStatsOverview } from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import StatsSummaryCards from './StatsSummaryCards.vue'
import { buildErrorOption, buildModelDistributionOption, buildSystemMetricsOption, buildUsageTrendOption } from './statsChartOptions'
import { formatCompactInteger, formatCost, formatDuration, formatInteger, formatPercent, formatSeconds } from './statsFormatters'

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
const hasVisibleSystemTrend = computed(() => isAdmin.value && hasSystemTrend.value)
const hasUsageOverview = computed(() => Boolean(usageOverview.value))
const initialLoading = computed(() => loading.value && !hasUsageOverview.value)
const systemInitialLoading = computed(() => loading.value && isAdmin.value && !systemMetrics.value)
const hasHistoricalUsage = computed(() => (usageOverview.value?.totals.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasHistoricalUsage.value ? '近 24 小时暂无趋势数据，累计指标已在上方展示' : '暂无趋势数据')
const modelDistributionEmptyDescription = computed(() => hasHistoricalUsage.value ? '今日暂无模型调用，累计指标已在上方展示' : '今日暂无模型调用')
const errorEmptyDescription = computed(() => hasHistoricalUsage.value ? '今日暂无消耗错误，排障错误请查看日志' : '今日暂无消耗错误')
const systemTrendEmptyDescription = computed(() => isAdmin.value ? '等待后台监控采样' : '系统监控仅管理员可见')

const summaryCards = computed(() => {
  const today = usageOverview.value?.today
  const totals = usageOverview.value?.totals
  return [
    { key: 'requests', label: '今日有效请求', value: formatInteger(today?.requestCount), extra: `累计 ${formatInteger(totals?.requestCount)} / 消耗错误率 ${formatPercent((today?.errorRate ?? totals?.errorRate ?? 0) * 100)}` },
    { key: 'duration', label: '今日平均响应', value: formatDuration(today?.averageDurationMs), extra: `首 Token ${formatDuration(today?.averageFirstTokenMs)}` },
    { key: 'tokens', label: '今日 Token', value: formatCompactInteger(today?.totalTokens), extra: `累计 ${formatCompactInteger(totals?.totalTokens)} / 输入 ${formatCompactInteger(today?.inputTokens)}` },
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

  chart.setOption(buildUsageTrendOption(usageOverview.value.hourlyTrend), { notMerge: true })
}

function renderModelDistributionChart() {
  if (!hasModelDistribution.value) {
    disposeChart(modelDistributionChart)
    return
  }
  const chart = ensureChart(modelDistributionChartRef, modelDistributionChart)
  if (!chart || !usageOverview.value) return

  chart.setOption(buildModelDistributionOption(usageOverview.value.modelDistribution), { notMerge: true })
}

function renderErrorChart() {
  if (!hasErrors.value) {
    disposeChart(errorChart)
    return
  }
  const chart = ensureChart(errorChartRef, errorChart)
  if (!chart || !usageOverview.value) return

  chart.setOption(buildErrorOption(usageOverview.value.errors), { notMerge: true })
}

function renderSystemMetricsChart() {
  if (!isAdmin.value || !hasSystemTrend.value) {
    disposeChart(systemMetricsChart)
    return
  }
  const chart = ensureChart(systemMetricsChartRef, systemMetricsChart)
  if (!chart || !systemMetrics.value) return

  chart.setOption(buildSystemMetricsOption(systemMetrics.value.hourlyTrend), { notMerge: true })
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

.stats-section {
  margin-top: 0;
}

.chart-panel {
  width: 100%;
  height: 280px;
}

.chart-panel-large {
  height: 340px;
}

:global(.stats-error-tooltip) {
  cursor: text;
  line-height: 1.55;
  user-select: text;
}

:global(.stats-error-tooltip .stats-tooltip-content) {
  max-width: 360px;
}

:global(.stats-error-tooltip .stats-tooltip-title) {
  margin-bottom: 8px;
  color: #0f172a;
  font-weight: 700;
}

:global(.stats-error-tooltip .stats-tooltip-row) {
  display: grid;
  grid-template-columns: 54px minmax(0, 1fr);
  gap: 8px;
  margin-top: 4px;
}

:global(.stats-error-tooltip .stats-tooltip-label),
:global(.stats-error-tooltip .stats-tooltip-block-label) {
  color: #64748b;
}

:global(.stats-error-tooltip .stats-tooltip-value),
:global(.stats-error-tooltip .stats-tooltip-message) {
  color: #334155;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  word-break: break-word;
}

:global(.stats-error-tooltip .stats-tooltip-block) {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid #e8edf5;
}

:global(.stats-error-tooltip .stats-tooltip-message) {
  max-height: 128px;
  margin-top: 4px;
  overflow: auto;
}

@media (max-width: 768px) {
  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>




