<template>
  <div class="stats-page">
    <a-card class="page-card stats-header-card">
      <div class="page-toolbar stats-toolbar">
        <a-segmented v-model:value="selectedWindow" class="stats-window-segmented" :options="windowOptions" :disabled="loading" @change="handleWindowChange" />
        <div class="page-toolbar-actions">
          <a-button :loading="loading" @click="loadData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="14">
        <StatsChartCard :title="`有效请求 / Token / 平均响应（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasUsageTrend" :empty-description="usageTrendEmptyDescription">
          <div ref="usageTrendChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
      <a-col :xs="24" :xl="10">
        <StatsChartCard :title="`模型分布（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasModelDistribution" :empty-description="modelDistributionEmptyDescription">
          <div ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="10">
        <StatsChartCard :title="`消耗错误 Top 10（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasErrors" :empty-description="errorEmptyDescription">
          <div ref="errorChartRef" class="chart-panel" />
        </StatsChartCard>
      </a-col>
      <a-col :xs="24" :xl="14">
        <StatsChartCard :title="`系统性能 / 网络吞吐趋势（${currentWindowLabel}）`" :loading="systemInitialLoading" :has-data="hasVisibleSystemTrend" :empty-description="systemTrendEmptyDescription">
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
import { ReloadOutlined } from '@ant-design/icons-vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { SystemMetricsOverview, UsageOverviewWindowKey, UsageStatsOverview } from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import StatsSummaryCards from './StatsSummaryCards.vue'
import { buildErrorOption, buildModelDistributionOption, buildSystemMetricsOption, buildUsageTrendOption } from './statsChartOptions'
import { formatCompactInteger, formatCost, formatDuration, formatInteger, formatPercent, formatSeconds } from './statsFormatters'

const windowOptions: Array<{ label: string; value: UsageOverviewWindowKey }> = [
  { label: '近一天', value: 'last1d' },
  { label: '近三天', value: 'last3d' },
  { label: '近一周', value: 'last7d' },
  { label: '近一月', value: 'last30d' }
]

const loading = ref(false)
const selectedWindow = ref<UsageOverviewWindowKey>('last1d')
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
const currentWindowLabel = computed(() => usageOverview.value?.window.label ?? windowOptions.find((item) => item.value === selectedWindow.value)?.label ?? '近一天')
const hasWindowUsage = computed(() => (usageOverview.value?.summary.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无趋势数据，窗口指标已在上方展示` : `${currentWindowLabel.value}暂无趋势数据`)
const modelDistributionEmptyDescription = computed(() => `${currentWindowLabel.value}暂无模型调用`)
const errorEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无消耗错误，排障错误请查看日志` : `${currentWindowLabel.value}暂无消耗错误`)
const systemTrendEmptyDescription = computed(() => isAdmin.value ? '等待后台监控采样' : '系统监控仅管理员可见')

const summaryCards = computed(() => {
  const summary = usageOverview.value?.summary
  const windowLabel = currentWindowLabel.value
  return [
    { key: 'requests', label: `${windowLabel}有效请求`, value: formatInteger(summary?.requestCount), extra: `消耗错误率 ${formatPercent((summary?.errorRate ?? 0) * 100)} / 错误 ${formatInteger(summary?.errorCount)}` },
    { key: 'duration', label: `${windowLabel}平均响应`, value: formatDuration(summary?.averageDurationMs), extra: `首 Token ${formatDuration(summary?.averageFirstTokenMs)}` },
    { key: 'tokens', label: `${windowLabel} Token`, value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出+缓存 ${formatCompactInteger((summary?.outputTokens ?? 0) + (summary?.cacheReadTokens ?? 0))}` },
    { key: 'cost', label: `${windowLabel}成本`, value: formatCost(summary?.totalCost), extra: `统计滞后 ${formatSeconds(usageOverview.value?.statsLagSeconds)}` }
  ]
})

async function loadData() {
  loading.value = true
  try {
    usageOverview.value = await api.stats.usageOverview({ window: selectedWindow.value })
    if (isAdmin.value) {
      systemMetrics.value = await api.stats.systemMetrics({ window: selectedWindow.value })
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

function handleWindowChange(value: string | number) {
  selectedWindow.value = value as UsageOverviewWindowKey
  void loadData()
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

.stats-window-segmented {
  width: max-content;
  max-width: 100%;
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
  .stats-window-segmented {
    width: 100%;
    min-width: 0;
  }

  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>




