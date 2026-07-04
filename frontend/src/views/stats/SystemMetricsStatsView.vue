<template>
  <div class="system-metrics-page">
    <a-card class="page-card system-metrics-header-card">
      <div class="page-toolbar system-metrics-toolbar">
        <div class="system-metrics-filters">
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="system-metrics-range-picker"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
          <a-segmented
            :value="quickRangeValue"
            :disabled="loading"
            :options="quickRangeOptions"
            class="system-metrics-quick-range"
            @change="handleQuickRangeChange"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button :loading="loading" @click="loadData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-card>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard :title="`系统性能 / 网络吞吐趋势（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasSystemTrend" :empty-description="systemTrendEmptyDescription">
          <div ref="systemMetricsChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard
          :title="`进程事件循环延迟（${currentWindowLabel}）`"
          :loading="initialLoading"
          :has-data="hasProcessEventLoopData"
          :empty-description="processEventLoopEmptyDescription"
        >
          <div v-if="hasProcessEventLoopTrend" ref="processEventLoopChartRef" class="chart-panel chart-panel-large" />
          <a-empty v-else class="process-event-loop-trend-empty" :description="processEventLoopTrendEmptyDescription" />
          <StatsProcessEventLoopTable :rows="processEventLoopRows" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard
          :title="`进程 RSS 峰值趋势（${currentWindowLabel}）`"
          :loading="initialLoading"
          :has-data="hasProcessMemoryTrend"
          :empty-description="processMemoryTrendEmptyDescription"
        >
          <div ref="processMemoryChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsBackgroundJobsCard
          :empty-description="backgroundJobEmptyDescription"
          :has-data="hasBackgroundJobs"
          :loading="initialLoading"
          :pagination="backgroundJobPagination"
          :rows="backgroundJobRows"
          :runtime-alert-description="systemRuntimeAlertDescription"
          :runtime-alert-visible="systemRuntimeAlertVisible"
          @change="handleBackgroundJobTableChange"
        />
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsBackgroundQueuesCard
          :empty-description="backgroundQueueEmptyDescription"
          :has-data="hasBackgroundQueues"
          :loading="initialLoading"
          :pagination="backgroundQueuePagination"
          :rows="backgroundQueueRows"
          :runtime-alert-description="systemRuntimeAlertDescription"
          :runtime-alert-visible="systemRuntimeAlertVisible"
          @change="handleBackgroundQueueTableChange"
        />
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, shallowRef, watch } from 'vue'
import { message } from '@/lib/antd'
import { ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'

import { api } from '@/api/client'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateRangeKeys, todayDateRange } from '@/shared/dateRange'
import type { SystemMetricsOverview } from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import { buildBackgroundQueueRows } from './statsBackgroundQueues'
import { buildProcessEventLoopOption, buildProcessMemoryOption, buildSystemMetricsOption } from './statsChartOptions'
import { buildProcessEventLoopRows, hasProcessEventLoopRowSample } from './statsProcessEventLoop'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]
const StatsBackgroundJobsCard = defineAsyncComponent(() => import('./StatsBackgroundJobsCard.vue'))
const StatsBackgroundQueuesCard = defineAsyncComponent(() => import('./StatsBackgroundQueuesCard.vue'))
const StatsProcessEventLoopTable = defineAsyncComponent(() => import('./StatsProcessEventLoopTable.vue'))

type SystemMetricsPageState = {
  range?: {
    startDate: string
    endDate: string
  }
}

const defaultDateRange = todayDateRange
const defaultSystemMetricsPageState = (): SystemMetricsPageState => ({})
const pageStateCache = usePageStateCache<SystemMetricsPageState>('system-metrics-stats', defaultSystemMetricsPageState, { version: 1 })
const initialPageState = pageStateCache.read()

const loading = ref(false)
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const systemMetrics = ref<SystemMetricsOverview>()
const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()

const systemMetricsChartRef = ref<HTMLDivElement>()
const processEventLoopChartRef = ref<HTMLDivElement>()
const processMemoryChartRef = ref<HTMLDivElement>()
const systemMetricsChart = shallowRef<ECharts>()
const processEventLoopChart = shallowRef<ECharts>()
const processMemoryChart = shallowRef<ECharts>()
const backgroundJobPageSize = 10
const backgroundJobPage = ref(1)
const backgroundQueuePageSize = 10
const backgroundQueuePage = ref(1)
let requestSeq = 0

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderSystemCharts,
  resizeCharts,
  disposeCharts,
  onMounted: loadData
})

const hasOverview = computed(() => Boolean(systemMetrics.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const quickRangeValue = computed<QuickRange | undefined>(() => {
  const [startDate, endDate] = selectedRange.value
  const windowEnd = statsWindowEndDate()
  if (!windowEnd || endDate !== formatDateKey(windowEnd)) return undefined
  if (startDate === formatDateKey(windowEnd)) return 'today'
  if (startDate === formatDateKey(windowEnd.subtract(6, 'day'))) return 'recent7d'
  if (startDate === formatDateKey(windowEnd.subtract((usageStatsWindowMaxDays.value || MAX_RANGE_DAYS) - 1, 'day'))) return 'recent1m'
  return undefined
})
const currentWindowLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const hasSystemTrend = computed(() => (systemMetrics.value?.hourlyTrend.length ?? 0) > 0)
const hasProcessEventLoopTrend = computed(() => (systemMetrics.value?.processEventLoopTrend ?? []).some((item) => item.eventLoopLagMsAvg !== undefined || item.eventLoopLagMsMax !== undefined))
const hasProcessMemoryTrend = computed(() => (systemMetrics.value?.processEventLoopTrend ?? []).some((item) => item.processRssBytesAvg !== undefined || item.processRssBytesMax !== undefined))
const processEventLoopRows = computed(() => buildProcessEventLoopRows(systemMetrics.value))
const hasProcessEventLoopData = computed(() => hasProcessEventLoopTrend.value || hasProcessEventLoopRowSample(processEventLoopRows.value))
const systemTrendEmptyDescription = computed(() => '等待后台监控采样')
const processEventLoopEmptyDescription = computed(() => '等待进程事件循环采样')
const processEventLoopTrendEmptyDescription = computed(() => `${currentWindowLabel.value}暂无事件循环趋势，等待后台窗口缓存刷新`)
const processMemoryTrendEmptyDescription = computed(() => `${currentWindowLabel.value}暂无进程内存趋势，等待后台窗口缓存刷新`)
const backgroundJobsAvailable = computed(() => systemMetrics.value?.backgroundJobsAvailable === true)
const backgroundJobRows = computed(() => {
  return (systemMetrics.value?.backgroundJobs ?? []).filter(isBackgroundTaskRow).sort((left, right) => {
    if (left.failureCount !== right.failureCount) return right.failureCount - left.failureCount
    const leftDuration = left.maxDurationMs ?? -1
    const rightDuration = right.maxDurationMs ?? -1
    if (leftDuration !== rightDuration) return rightDuration - leftDuration
    return left.name.localeCompare(right.name)
  })
})
const backgroundJobPagination = computed(() => ({
  current: backgroundJobPage.value,
  pageSize: backgroundJobPageSize,
  total: backgroundJobRows.value.length,
  showSizeChanger: false
}))
const hasBackgroundJobs = computed(() => backgroundJobsAvailable.value && backgroundJobRows.value.length > 0)
const backgroundJobEmptyDescription = computed(() => backgroundJobsAvailable.value ? '暂无后台任务' : '暂时无法获取后台 worker 任务状态')
const backgroundQueueRows = computed(() => buildBackgroundQueueRows(systemMetrics.value))
const backgroundQueuePagination = computed(() => ({
  current: backgroundQueuePage.value,
  pageSize: backgroundQueuePageSize,
  total: backgroundQueueRows.value.length,
  showSizeChanger: false
}))
const hasBackgroundQueues = computed(() => backgroundJobsAvailable.value && backgroundQueueRows.value.length > 0)
const backgroundQueueEmptyDescription = computed(() => backgroundJobsAvailable.value ? '暂无后台队列' : '暂时无法获取后台 worker 队列状态')
const systemRuntimeAlertVisible = computed(() => Boolean(systemMetrics.value && (
  !systemMetrics.value.runtimeSnapshotAvailable
  || systemMetrics.value.ingestWorkerSnapshotAvailable === false
  || systemMetrics.value.statsWorkerSnapshotAvailable === false
  || systemMetrics.value.opsWorkerSnapshotAvailable === false
  || !systemMetrics.value.backgroundJobsAvailable
)))
const systemRuntimeAlertDescription = computed(() => {
  const metrics = systemMetrics.value
  if (!metrics) return ''
  const reasons: string[] = []
  if (!metrics.runtimeSnapshotAvailable) {
    reasons.push('服务运行态不可用')
  } else {
    if (metrics.ingestWorkerSnapshotAvailable === false) reasons.push('写入 worker 快照不可用')
    if (metrics.statsWorkerSnapshotAvailable === false) reasons.push('统计 worker 快照不可用')
    if (metrics.opsWorkerSnapshotAvailable === false) reasons.push('运维 worker 快照不可用')
    if (!metrics.backgroundJobsAvailable) reasons.push('后台任务状态不可用')
  }
  return `${reasons.join('；') || '运行态状态未知'}。`
})

async function loadData() {
  const currentRequestSeq = ++requestSeq
  loading.value = true
  try {
    const rangeParams = selectedRangeParams()
    const [metrics] = await Promise.all([
      api.stats.systemMetrics(rangeParams),
      loadUsageStatsWindow()
    ])
    if (currentRequestSeq !== requestSeq) return
    systemMetrics.value = metrics
  } catch (error) {
    if (currentRequestSeq !== requestSeq) return
    console.error(error)
    message.error('系统指标统计加载失败')
  } finally {
    if (currentRequestSeq === requestSeq) {
      loading.value = false
      renderCharts()
    }
  }
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
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

function handleQuickRangeChange(value: string | number) {
  const range = quickRangeDateRange(value as QuickRange)
  if (!range) return
  dateRange.value = parseDateRange({
    startDate: formatDateKey(range[0]),
    endDate: formatDateKey(range[1])
  })
  void loadData()
}

function resetFilters() {
  const defaults = defaultSystemMetricsPageState()
  dateRange.value = parseDateRange(defaults.range)
  calendarRange.value = [null, null]
  pageStateCache.clear()
  void loadData()
}

function handleBackgroundJobTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown }
  const current = Number(next.current)
  backgroundJobPage.value = Number.isFinite(current) && current > 0 ? Math.trunc(current) : 1
}

function handleBackgroundQueueTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown }
  const current = Number(next.current)
  backgroundQueuePage.value = Number.isFinite(current) && current > 0 ? Math.trunc(current) : 1
}

async function renderSystemCharts() {
  await Promise.all([
    renderSystemMetricsChart(),
    renderProcessEventLoopChart(),
    renderProcessMemoryChart()
  ])
}

async function renderSystemMetricsChart() {
  if (!hasSystemTrend.value) {
    disposeChart(systemMetricsChart)
    return
  }
  const chart = await ensureChart(systemMetricsChartRef, systemMetricsChart, () => pageActive.value)
  if (!chart || !systemMetrics.value || !pageActive.value) return
  chart.setOption(buildSystemMetricsOption(systemMetrics.value.hourlyTrend), { notMerge: true })
}

async function renderProcessEventLoopChart() {
  if (!hasProcessEventLoopTrend.value) {
    disposeChart(processEventLoopChart)
    return
  }
  const chart = await ensureChart(processEventLoopChartRef, processEventLoopChart, () => pageActive.value)
  if (!chart || !systemMetrics.value || !pageActive.value) return
  chart.setOption(buildProcessEventLoopOption(systemMetrics.value.processEventLoopTrend), { notMerge: true })
}

async function renderProcessMemoryChart() {
  if (!hasProcessMemoryTrend.value) {
    disposeChart(processMemoryChart)
    return
  }
  const chart = await ensureChart(processMemoryChartRef, processMemoryChart, () => pageActive.value)
  if (!chart || !systemMetrics.value || !pageActive.value) return
  chart.setOption(buildProcessMemoryOption(systemMetrics.value.processEventLoopTrend), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts([systemMetricsChart.value, processEventLoopChart.value, processMemoryChart.value])
}

function disposeCharts() {
  disposeChart(systemMetricsChart)
  disposeChart(processEventLoopChart)
  disposeChart(processMemoryChart)
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

function isBackgroundTaskRow(row: NonNullable<SystemMetricsOverview['backgroundJobs']>[number]): boolean {
  return row.intervalMs > 0 && !row.name.endsWith('-queue')
}

function disabledDate(current: Dayjs) {
  return isRecentWindowDateDisabled(current, calendarRange.value, usageStatsWindowMaxDays.value, usageStatsWindowEndDate.value)
}

function statsWindowEndDate(): Dayjs | undefined {
  return usageStatsWindowEndDate.value?.isValid() ? usageStatsWindowEndDate.value.startOf('day') : undefined
}

function quickRangeDateRange(value: QuickRange): [Dayjs, Dayjs] | undefined {
  const end = statsWindowEndDate()
  if (!end) return undefined
  if (value === 'today') return [end, end]
  if (value === 'recent7d') return [end.subtract(6, 'day'), end]
  return [end.subtract((usageStatsWindowMaxDays.value || MAX_RANGE_DAYS) - 1, 'day'), end]
}

function parseDateRange(value?: { startDate?: string; endDate?: string }): [Dayjs, Dayjs] {
  return parseDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: MAX_RANGE_DAYS })
}

function normalizedDateRange(value: [Dayjs, Dayjs]): [string, string] {
  return normalizeDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: MAX_RANGE_DAYS })
}

function snapshotPageState(): SystemMetricsPageState {
  const [startDate, endDate] = selectedRange.value
  return {
    range: { startDate, endDate }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => backgroundJobRows.value.length, (total) => {
  const maxPage = Math.max(1, Math.ceil(total / backgroundJobPageSize))
  if (backgroundJobPage.value > maxPage) {
    backgroundJobPage.value = maxPage
  }
})
watch(() => backgroundQueueRows.value.length, (total) => {
  const maxPage = Math.max(1, Math.ceil(total / backgroundQueuePageSize))
  if (backgroundQueuePage.value > maxPage) {
    backgroundQueuePage.value = maxPage
  }
})
</script>

<style scoped>
.system-metrics-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.system-metrics-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.system-metrics-toolbar {
  margin: 0;
}

.system-metrics-filters {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.system-metrics-range-picker {
  width: 250px;
}

.system-metrics-section {
  margin-top: 0;
}

.system-metrics-section :deep(.ant-col) {
  display: flex;
}

.chart-panel {
  width: 100%;
  height: 280px;
}

.chart-panel-large {
  height: 340px;
}

.process-event-loop-trend-empty {
  display: flex;
  flex: 1;
  flex-direction: column;
  justify-content: center;
  min-height: 220px;
}

@media (max-width: 768px) {
  .system-metrics-toolbar {
    align-items: stretch;
  }

  .system-metrics-filters {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
  }

  .system-metrics-range-picker,
  .system-metrics-quick-range {
    width: 100%;
    min-width: 0;
  }

  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>
