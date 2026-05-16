<template>
  <div class="stats-page">
    <a-card class="page-card stats-header-card">
      <div class="page-toolbar stats-toolbar">
        <div class="stats-toolbar-filters">
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="stats-range-picker"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="selectedSystemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :disabled="loading"
            all-label="全部用户"
            class="stats-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountChange"
          />
        </div>
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
      <a-col :xs="24" :xl="showAdminDetailCharts ? 14 : 24">
        <StatsChartCard
          :title="`请求、失败、Token 消耗、平均总耗时（${currentWindowLabel}）`"
          :description="usageTrendDescription"
          :loading="initialLoading"
          :has-data="hasUsageTrend"
          :empty-description="usageTrendEmptyDescription"
        >
          <div ref="usageTrendChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
      <a-col v-if="showAdminDetailCharts" :xs="24" :xl="10">
        <StatsChartCard
          :title="`模型分布（${currentWindowLabel}）`"
          description="按模型汇总 Token 消耗；没有 Token 的记录会用请求次数参与展示。"
          :loading="initialLoading"
          :has-data="hasModelDistribution"
          :empty-description="modelDistributionEmptyDescription"
        >
          <div ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="showAdminDetailCharts ? 10 : 12">
        <StatsChartCard
          :title="`错误 Top 10（${currentWindowLabel}）`"
          description="统计窗口内失败请求按错误码聚合；悬浮可查看状态码和错误信息。"
          :loading="initialLoading"
          :has-data="hasErrors"
          :empty-description="errorEmptyDescription"
        >
          <div ref="errorChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
      <a-col v-if="!showAdminDetailCharts" :xs="24" :xl="12">
        <StatsChartCard
          :title="`模型分布（${currentWindowLabel}）`"
          description="按模型汇总 Token 消耗；没有 Token 的记录会用请求次数参与展示。"
          :loading="initialLoading"
          :has-data="hasModelDistribution"
          :empty-description="modelDistributionEmptyDescription"
        >
          <div ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
      <a-col v-if="showAdminDetailCharts" :xs="24" :xl="14">
        <StatsChartCard :title="`系统性能 / 网络吞吐趋势（${currentWindowLabel}）`" :loading="systemInitialLoading" :has-data="hasVisibleSystemTrend" :empty-description="systemTrendEmptyDescription">
          <div ref="systemMetricsChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { message } from '@/lib/antd'
import { ReloadOutlined } from '@ant-design/icons-vue'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { init, type ECharts } from '@/lib/echarts'
import type { SystemAccountSummary, SystemMetricsOverview, UsageStatsOverview } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from './StatsChartCard.vue'
import StatsSummaryCards from './StatsSummaryCards.vue'
import { buildErrorOption, buildModelDistributionOption, buildSystemMetricsOption, buildUsageTrendOption } from './statsChartOptions'
import { formatCompactInteger, formatCost, formatDurationSeconds, formatInteger, formatPercent, formatSeconds } from './statsFormatters'

const MAX_RANGE_DAYS = 31
type StatsPageState = {
  range?: {
    startDate: string
    endDate: string
  }
  selectedSystemAccountId: string
}
const defaultDateRange = (): [Dayjs, Dayjs] => {
  const today = dayjs().startOf('day')
  return [today, today]
}
const defaultStatsPageState = (): StatsPageState => {
  return {
    selectedSystemAccountId: allSystemAccountsValue
  }
}
const pageStateCache = usePageStateCache<StatsPageState>(undefined, defaultStatsPageState, { version: 3 })
const initialPageState = pageStateCache.read()

const loading = ref(false)
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const dateRangeExplicit = ref(Boolean(initialPageState.range?.startDate || initialPageState.range?.endDate))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const selectedSystemAccountId = ref(initialPageState.selectedSystemAccountId || allSystemAccountsValue)
const usageOverview = ref<UsageStatsOverview>()
const systemMetrics = ref<SystemMetricsOverview>()
const systemAccounts = ref<SystemAccountSummary[]>([])
const systemAccountsLoaded = ref(false)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

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
const hasVisibleSystemTrend = computed(() => showAdminDetailCharts.value && hasSystemTrend.value)
const hasUsageOverview = computed(() => Boolean(usageOverview.value))
const initialLoading = computed(() => loading.value && !hasUsageOverview.value)
const systemInitialLoading = computed(() => loading.value && isManagementView.value && !systemMetrics.value)
const showAdminDetailCharts = computed(() => isManagementView.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const currentWindowLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const hasWindowUsage = computed(() => (usageOverview.value?.summary.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无趋势数据，窗口指标已在上方展示` : `${currentWindowLabel.value}暂无趋势数据`)
const modelDistributionEmptyDescription = computed(() => `${currentWindowLabel.value}暂无模型调用`)
const errorEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无失败请求` : `${currentWindowLabel.value}暂无失败请求`)
const systemTrendEmptyDescription = computed(() => '等待后台监控采样')
const usageTrendDescription = computed(() => `${currentWindowLabel.value} Token 消耗 = 输入 Token + 输出 Token；缓存读取已包含在输入 Token 中，仅作为拆分指标展示。失败 = 失败请求次数；平均总耗时 = 网关记录的请求总耗时平均值。`)

const summaryCards = computed(() => {
  const summary = usageOverview.value?.summary
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `成功 ${formatInteger(summary?.successCount)} / 失败 ${formatInteger(summary?.errorCount)} / 失败率 ${formatPercent((summary?.errorRate ?? 0) * 100)}` },
    { key: 'firstToken', label: '平均首 Token', value: formatDurationSeconds(summary?.averageFirstTokenMs), extra: `平均总耗时 ${formatDurationSeconds(summary?.averageDurationMs)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出 ${formatCompactInteger(summary?.outputTokens)} / 缓存读取 ${formatCompactInteger(summary?.cacheReadTokens)}` },
    { key: 'cost', label: '成本', value: formatCost(summary?.totalCost), extra: `统计滞后 ${formatSeconds(usageOverview.value?.statsLagSeconds)}` }
  ]
})

async function loadData() {
  loading.value = true
  try {
    await loadSystemAccounts()
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
    const rangeParams = selectedRangeParams()
    const overview = isManagementView.value
      ? await api.stats.usageOverview({ ...rangeParams, systemAccountId })
      : await api.myStats.usageOverview(rangeParams)
    usageOverview.value = overview
    if (isManagementView.value) {
      systemMetrics.value = await api.stats.systemMetrics(rangeParams)
    } else {
      systemMetrics.value = undefined
    }
    syncDateRangeFromResponse(overview.range)
  } catch (error) {
    console.error(error)
    message.error('统计数据加载失败')
  } finally {
    loading.value = false
    renderCharts()
  }
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  dateRangeExplicit.value = true
  void loadData()
}

function handleCalendarChange(value: Array<Dayjs | null> | null) {
  calendarRange.value = [value?.[0] ?? null, value?.[1] ?? null]
}

function handleDateRangeOpenChange(open: boolean) {
  if (!open) {
    calendarRange.value = [null, null]
  }
}

function handleSystemAccountChange() {
  void loadData()
}

async function loadSystemAccounts(): Promise<void> {
  if (!isManagementView.value || systemAccountsLoaded.value) return
  systemAccounts.value = await api.systemAccounts.list()
  systemAccountsLoaded.value = true
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
  if (!showAdminDetailCharts.value || !hasSystemTrend.value) {
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
    chartRef.value = init(element)
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

function snapshotPageState(): StatsPageState {
  const [startDate, endDate] = selectedRange.value
  return {
    range: dateRangeExplicit.value ? { startDate, endDate } : undefined,
    selectedSystemAccountId: selectedSystemAccountId.value
  }
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  if (!dateRangeExplicit.value) return {}
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

function disabledDate(current: Dayjs) {
  if (!current) return false
  const today = dayjs().startOf('day')
  if (current.isAfter(today, 'day')) return true
  if (current.isBefore(today.subtract(MAX_RANGE_DAYS - 1, 'day'), 'day')) return true
  const anchor = calendarRange.value[0] ?? calendarRange.value[1]
  if (!anchor) return false
  return Math.abs(current.startOf('day').diff(anchor.startOf('day'), 'day')) > MAX_RANGE_DAYS - 1
}

function parseDateRange(value?: { startDate?: string; endDate?: string }): [Dayjs, Dayjs] {
  const [defaultStart, defaultEnd] = defaultDateRange()
  const start = parseDateKey(value?.startDate) ?? defaultStart
  const end = parseDateKey(value?.endDate) ?? defaultEnd
  const [normalizedStart, normalizedEnd] = normalizedDateRange([start, end])
  return [dayjs(normalizedStart), dayjs(normalizedEnd)]
}

function syncDateRangeFromResponse(value?: { startDate?: string; endDate?: string }) {
  const start = parseDateKey(value?.startDate)
  const end = parseDateKey(value?.endDate)
  if (!start || !end || start.isAfter(end, 'day')) return
  dateRange.value = [start.startOf('day'), end.startOf('day')]
}

function normalizedDateRange(value: [Dayjs, Dayjs]): [string, string] {
  const [defaultStart, defaultEnd] = defaultDateRange()
  let start = (value[0] ?? defaultStart).startOf('day')
  let end = (value[1] ?? defaultEnd).startOf('day')
  if (start.isAfter(end, 'day')) start = end
  if (end.diff(start, 'day') > MAX_RANGE_DAYS - 1) {
    start = end.subtract(MAX_RANGE_DAYS - 1, 'day')
  }
  return [formatDateKey(start), formatDateKey(end)]
}

function parseDateKey(value?: string): Dayjs | undefined {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return undefined
  const [year, month, day] = value.split('-').map((part) => Number(part))
  const parsed = dayjs(new Date(year, month - 1, day)).startOf('day')
  return parsed.year() === year && parsed.month() === month - 1 && parsed.date() === day ? parsed : undefined
}

function formatDateKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

function formatDateLabel(value: string) {
  const parsed = parseDateKey(value)
  return parsed ? parsed.format('M月D日') : value
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

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

.stats-toolbar-filters {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.stats-range-picker {
  width: 250px;
}

.stats-system-account-select {
  width: 220px;
}

.stats-section {
  margin-top: 0;
}

.stats-section :deep(.ant-col) {
  display: flex;
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
  .stats-toolbar {
    align-items: stretch;
  }

  .stats-toolbar-filters {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
  }

  .stats-range-picker {
    width: 100%;
    min-width: 0;
  }

  .stats-system-account-select {
    width: 100%;
  }

  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>



