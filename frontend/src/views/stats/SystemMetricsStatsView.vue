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
            :value="quickRangeValue ?? ''"
            :disabled="loading"
            :options="quickRangeOptions"
            class="system-metrics-quick-range"
            @change="handleQuickRangeChange"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button :loading="loading" @click="loadPageData">
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
        <StatsChartCard :title="`系统性能 / 网络吞吐趋势（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasSystemTrend" :empty-description="systemTrendEmptyDescription" :error="trendError" :on-retry="loadData">
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
          :error="trendError"
          :on-retry="loadData"
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
          :error="trendError"
          :on-retry="loadData"
        >
          <div ref="processMemoryChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
      </a-col>
    </a-row>

    <div ref="backgroundJobsSectionRef">
      <a-row :gutter="[16, 16]" class="system-metrics-section">
        <a-col :xs="24">
          <StatsBackgroundJobsCard
            :empty-description="backgroundJobEmptyDescription"
            :has-data="hasBackgroundJobs"
            :loading="backgroundJobsInitialLoading"
            :pagination="backgroundJobPagination"
            :rows="backgroundJobRows"
            :runtime-alert-description="systemRuntimeAlertDescription"
            :runtime-alert-visible="systemRuntimeAlertVisible"
            :error="backgroundJobsError"
            :on-retry="loadBackgroundJobs"
            @change="handleBackgroundJobTableChange"
          />
        </a-col>
      </a-row>
    </div>

    <div ref="backgroundQueuesSectionRef">
      <a-row :gutter="[16, 16]" class="system-metrics-section">
        <a-col :xs="24">
          <StatsBackgroundQueuesCard
            :empty-description="backgroundQueueEmptyDescription"
            :has-data="hasBackgroundQueues"
            :loading="backgroundQueuesInitialLoading"
            :pagination="backgroundQueuePagination"
            :rows="backgroundQueueRows"
            :runtime-alert-description="systemRuntimeAlertDescription"
            :runtime-alert-visible="systemRuntimeAlertVisible"
            :error="backgroundQueuesError"
            :on-retry="loadBackgroundQueues"
            @change="handleBackgroundQueueTableChange"
          />
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onActivated, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { message } from '@/lib/antd'
import { ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateRangeKeys, todayDateRange } from '@/shared/dateRange'
import type {
  SystemMetricsRuntimeJobsResult,
  SystemMetricsRuntimeQueuesResult,
  SystemMetricsRuntimeSummary,
  SystemMetricsTrendOverview
} from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import { buildProcessEventLoopOption, buildProcessMemoryOption, buildSystemMetricsOption } from './statsChartOptions'
import { buildProcessEventLoopRows, hasProcessEventLoopRowSample } from './statsProcessEventLoop'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
type RangeMode = 'auto' | QuickRange | 'custom'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]
const StatsBackgroundJobsCard = defineAsyncComponent(() => import('./StatsBackgroundJobsCard.vue'))
const StatsBackgroundQueuesCard = defineAsyncComponent(() => import('./StatsBackgroundQueuesCard.vue'))
const StatsProcessEventLoopTable = defineAsyncComponent(() => import('./StatsProcessEventLoopTable.vue'))

type SystemMetricsPageState = {
  rangeMode?: RangeMode
  range?: {
    startDate: string
    endDate: string
  }
}

function isRangeMode(value: unknown): value is RangeMode {
  return value === 'auto' || value === 'today' || value === 'recent7d' || value === 'recent1m' || value === 'custom'
}

function initialRangeMode(state: SystemMetricsPageState): RangeMode {
  if (isRangeMode(state.rangeMode)) return state.rangeMode
  // 旧缓存只保存了一组固定日期，不能可靠推断其原本是否来自快捷项。
  return state.range ? 'custom' : 'auto'
}

function isDynamicRangeMode(value: RangeMode): value is Exclude<RangeMode, 'custom'> {
  return value !== 'custom'
}

function isQuickRangeMode(value: RangeMode): value is QuickRange {
  return value === 'today' || value === 'recent7d' || value === 'recent1m'
}

function readLegacySystemMetricsPageState(): Pick<SystemMetricsPageState, 'range'> | undefined {
  if (typeof window === 'undefined') return undefined
  const user = authState.currentUser.value
  const userKey = user?.id || user?.username || 'anonymous'
  try {
    const cached = JSON.parse(window.localStorage.getItem(`juhe-ai:page-state:${userKey}:system-metrics-stats:v2`) || '{}') as {
      version?: unknown
      state?: Partial<SystemMetricsPageState>
    }
    const range = cached.state?.range
    return cached.version === 2 && range?.startDate && range.endDate ? { range } : undefined
  } catch {
    return undefined
  }
}

const defaultDateRange = todayDateRange
const defaultSystemMetricsPageState = (): SystemMetricsPageState => ({})
const pageStateCache = usePageStateCache<SystemMetricsPageState>('system-metrics-stats', defaultSystemMetricsPageState, { version: 3 })
const cachedInitialPageState = pageStateCache.read()
const legacyInitialPageState = isRangeMode(cachedInitialPageState.rangeMode) ? undefined : readLegacySystemMetricsPageState()
const initialPageState: SystemMetricsPageState = legacyInitialPageState
  ? { ...cachedInitialPageState, ...legacyInitialPageState, rangeMode: 'custom' }
  : cachedInitialPageState

const loading = ref(false)
const backgroundJobsLoading = ref(false)
const backgroundQueuesLoading = ref(false)
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const rangeMode = ref<RangeMode>(initialRangeMode(initialPageState))
const dateRangeExplicit = ref(rangeMode.value !== 'auto')
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const systemMetrics = ref<SystemMetricsTrendOverview>()
const runtimeSummary = ref<SystemMetricsRuntimeSummary>()
const backgroundJobsResult = ref<SystemMetricsRuntimeJobsResult>()
const backgroundQueuesResult = ref<SystemMetricsRuntimeQueuesResult>()
const { usageStatsWindow, usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()

const systemMetricsChartRef = ref<HTMLDivElement>()
const processEventLoopChartRef = ref<HTMLDivElement>()
const processMemoryChartRef = ref<HTMLDivElement>()
const systemMetricsChart = shallowRef<ECharts>()
const processEventLoopChart = shallowRef<ECharts>()
const processMemoryChart = shallowRef<ECharts>()
const backgroundJobsSectionRef = ref<HTMLDivElement>()
const backgroundQueuesSectionRef = ref<HTMLDivElement>()
const backgroundJobsSectionLoaded = ref(false)
const backgroundQueuesSectionLoaded = ref(false)
const backgroundJobPageSize = 10
const backgroundJobPage = ref(1)
const backgroundQueuePageSize = 10
const backgroundQueuePage = ref(1)
let requestSeq = 0
let pageLoadGeneration = 0
let runtimeSummaryRequestSeq = 0
let backgroundJobsRequestSeq = 0
let backgroundQueuesRequestSeq = 0
let trendAbortController: AbortController | undefined
let runtimeSummaryAbortController: AbortController | undefined
let backgroundJobsAbortController: AbortController | undefined
let backgroundQueuesAbortController: AbortController | undefined
let runtimeSummaryPromise: Promise<void> | undefined
let backgroundJobsObserver: IntersectionObserver | undefined
let backgroundQueuesObserver: IntersectionObserver | undefined
let disposed = false

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderSystemCharts,
  resizeCharts,
  disposeCharts,
  onMounted: loadPageData,
  onDeactivate: () => {
    trendAbortController?.abort()
    runtimeSummaryAbortController?.abort()
    backgroundJobsAbortController?.abort()
    backgroundQueuesAbortController?.abort()
    trendAbortController = undefined
    runtimeSummaryAbortController = undefined
    backgroundJobsAbortController = undefined
    backgroundQueuesAbortController = undefined
    runtimeSummaryPromise = undefined
    pageLoadGeneration += 1
    requestSeq += 1
    runtimeSummaryRequestSeq += 1
    backgroundJobsRequestSeq += 1
    backgroundQueuesRequestSeq += 1
    loading.value = false
    backgroundJobsLoading.value = false
    backgroundQueuesLoading.value = false
    disconnectRuntimeObservers()
  }
})

onMounted(async () => {
  disposed = false
  await nextTick()
  setupRuntimeObservers()
})

onActivated(async () => {
  await nextTick()
  setupRuntimeObservers()
})

const hasOverview = computed(() => Boolean(systemMetrics.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const trendError = ref('')
const runtimeSummaryError = ref('')
const backgroundJobsError = ref('')
const backgroundQueuesError = ref('')
const backgroundJobsInitialLoading = computed(() => backgroundJobsLoading.value && !backgroundJobsResult.value)
const backgroundQueuesInitialLoading = computed(() => backgroundQueuesLoading.value && !backgroundQueuesResult.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const quickRangeValue = computed<QuickRange | undefined>(() => {
  if (!isQuickRangeMode(rangeMode.value)) return undefined
  const [startDate, endDate] = selectedRange.value
  const range = quickRangeDateRange(rangeMode.value)
  if (!range) return undefined
  return startDate === formatDateKey(range[0]) && endDate === formatDateKey(range[1]) ? rangeMode.value : undefined
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
const backgroundJobsAvailable = computed(() => runtimeSummary.value?.jobsAvailable === true || Boolean(backgroundJobsResult.value))
const backgroundJobRows = computed(() => backgroundJobsResult.value?.items ?? [])
const backgroundJobPagination = computed(() => ({
  current: backgroundJobPage.value,
  pageSize: backgroundJobPageSize,
  total: backgroundJobsResult.value?.total ?? 0,
  showSizeChanger: false
}))
const hasBackgroundJobs = computed(() => backgroundJobsAvailable.value && backgroundJobRows.value.length > 0)
const backgroundJobEmptyDescription = computed(() => backgroundJobsAvailable.value ? '暂无后台任务' : '暂时无法获取后台 worker 任务状态')
const backgroundQueuesAvailable = computed(() => runtimeSummary.value?.queuesAvailable === true || Boolean(backgroundQueuesResult.value))
const backgroundQueueRows = computed(() => backgroundQueuesResult.value?.items ?? [])
const backgroundQueuePagination = computed(() => ({
  current: backgroundQueuePage.value,
  pageSize: backgroundQueuePageSize,
  total: backgroundQueuesResult.value?.total ?? 0,
  showSizeChanger: false
}))
const hasBackgroundQueues = computed(() => backgroundQueuesAvailable.value && backgroundQueueRows.value.length > 0)
const backgroundQueueEmptyDescription = computed(() => backgroundQueuesAvailable.value ? '暂无后台队列' : '暂时无法获取后台 worker 队列状态')
const systemRuntimeAlertVisible = computed(() => Boolean(
  runtimeSummaryError.value
  || (runtimeSummary.value && (
    !runtimeSummary.value.runtimeSnapshotAvailable
    || runtimeSummary.value.runtimeSnapshotStale === true
    || runtimeSummary.value.ingestWorkerSnapshotAvailable === false
    || runtimeSummary.value.statsWorkerSnapshotAvailable === false
    || runtimeSummary.value.opsWorkerSnapshotAvailable === false
    || !runtimeSummary.value.jobsAvailable
    || !runtimeSummary.value.queuesAvailable
  ))
))
const systemRuntimeAlertDescription = computed(() => {
  if (runtimeSummaryError.value) return `${runtimeSummaryError.value}。`
  const metrics = runtimeSummary.value
  if (!metrics) return '正在获取后台运行态摘要。'
  const reasons: string[] = []
  if (metrics.runtimeSnapshotStale) reasons.push('运行态快照已过期')
  if (!metrics.runtimeSnapshotAvailable) {
    reasons.push('服务运行态不可用')
  } else {
    if (metrics.ingestWorkerSnapshotAvailable === false) reasons.push('写入 worker 快照不可用')
    if (metrics.statsWorkerSnapshotAvailable === false) reasons.push('统计 worker 快照不可用')
    if (metrics.opsWorkerSnapshotAvailable === false) reasons.push('运维 worker 快照不可用')
    if (!metrics.jobsAvailable) reasons.push('后台任务状态不可用')
    if (!metrics.queuesAvailable) reasons.push('后台队列状态不可用')
  }
  return `${reasons.join('；') || '运行态状态未知'}。`
})

async function loadData() {
  trendAbortController?.abort()
  const controller = new AbortController()
  trendAbortController = controller
  const currentRequestSeq = ++requestSeq
  loading.value = true
  trendError.value = ''
  try {
    const rangeParams = selectedRangeParams()
    const metrics = await api.stats.systemMetricsTrend(rangeParams, { signal: controller.signal })
    if (currentRequestSeq !== requestSeq) return
    syncImplicitDateRangeToStatsWindow()
    systemMetrics.value = metrics
  } catch (error) {
    if (controller.signal.aborted) return
    if (currentRequestSeq !== requestSeq) return
    console.error(error)
    trendError.value = '系统指标趋势加载失败'
    message.error('系统指标统计加载失败')
  } finally {
    if (trendAbortController === controller) trendAbortController = undefined
    if (currentRequestSeq === requestSeq) {
      loading.value = false
      renderCharts()
    }
  }
}

async function loadPageData(options: { forceUsageWindow?: boolean } = {}) {
  const currentPageLoadGeneration = ++pageLoadGeneration
  const windowLoad = loadUsageStatsWindow({ force: options.forceUsageWindow === true, viewScope: 'admin' })
  if (isDynamicRangeMode(rangeMode.value)) {
    await windowLoad
    if (currentPageLoadGeneration !== pageLoadGeneration) return
    syncDynamicDateRangeToStatsWindow()
  }
  if (currentPageLoadGeneration !== pageLoadGeneration) return
  if (backgroundJobsSectionLoaded.value) void loadBackgroundJobs()
  if (backgroundQueuesSectionLoaded.value) void loadBackgroundQueues()
  return loadData()
}

function setupRuntimeObservers(): void {
  disconnectRuntimeObservers()
  if (disposed || !pageActive.value) return
  if (typeof IntersectionObserver === 'undefined') {
    if (!backgroundJobsSectionLoaded.value) {
      backgroundJobsSectionLoaded.value = true
      void loadBackgroundJobs()
    }
    if (!backgroundQueuesSectionLoaded.value) {
      backgroundQueuesSectionLoaded.value = true
      void loadBackgroundQueues()
    }
    return
  }
  observeRuntimeSection(backgroundJobsSectionRef.value, backgroundJobsSectionLoaded, (observer) => {
    backgroundJobsObserver = observer
    void loadBackgroundJobs()
  }, (observer) => {
    if (backgroundJobsObserver === observer) backgroundJobsObserver = undefined
  })
  observeRuntimeSection(backgroundQueuesSectionRef.value, backgroundQueuesSectionLoaded, (observer) => {
    backgroundQueuesObserver = observer
    void loadBackgroundQueues()
  }, (observer) => {
    if (backgroundQueuesObserver === observer) backgroundQueuesObserver = undefined
  })
}

function observeRuntimeSection(
  target: HTMLDivElement | undefined,
  loaded: { value: boolean },
  onVisible: (observer: IntersectionObserver) => void,
  onDisconnect: (observer: IntersectionObserver) => void
): void {
  if (!target || loaded.value) return
  const observer = new IntersectionObserver((entries) => {
    if (disposed || !pageActive.value || !entries.some((entry) => entry.isIntersecting)) return
    loaded.value = true
    observer.disconnect()
    onDisconnect(observer)
    onVisible(observer)
  }, { rootMargin: '240px 0px' })
  observer.observe(target)
}

function disconnectRuntimeObservers(): void {
  backgroundJobsObserver?.disconnect()
  backgroundQueuesObserver?.disconnect()
  backgroundJobsObserver = undefined
  backgroundQueuesObserver = undefined
}

async function loadRuntimeSummary(): Promise<void> {
  if (runtimeSummaryPromise) return await runtimeSummaryPromise
  const controller = new AbortController()
  runtimeSummaryAbortController?.abort()
  runtimeSummaryAbortController = controller
  const currentRequestSeq = ++runtimeSummaryRequestSeq
  const request = (async () => {
    runtimeSummaryError.value = ''
    try {
      const summary = await api.stats.systemMetricsRuntimeSummary({ signal: controller.signal })
      if (currentRequestSeq !== runtimeSummaryRequestSeq) return
      runtimeSummary.value = summary
    } catch (error) {
      if (controller.signal.aborted || currentRequestSeq !== runtimeSummaryRequestSeq) return
      console.error(error)
      runtimeSummaryError.value = '后台运行状态摘要加载失败'
    } finally {
      if (runtimeSummaryAbortController === controller) runtimeSummaryAbortController = undefined
    }
  })()
  runtimeSummaryPromise = request
  try {
    await request
  } finally {
    if (runtimeSummaryPromise === request) runtimeSummaryPromise = undefined
  }
}

async function loadBackgroundJobs() {
  backgroundJobsAbortController?.abort()
  const controller = new AbortController()
  backgroundJobsAbortController = controller
  const currentRequestSeq = ++backgroundJobsRequestSeq
  backgroundJobsLoading.value = true
  backgroundJobsError.value = ''
  void loadRuntimeSummary()
  try {
    const result = await api.stats.systemMetricsRuntimeJobs({ page: backgroundJobPage.value, pageSize: backgroundJobPageSize }, { signal: controller.signal })
    if (currentRequestSeq !== backgroundJobsRequestSeq) return
    backgroundJobsResult.value = result
  } catch (error) {
    if (controller.signal.aborted) return
    if (currentRequestSeq !== backgroundJobsRequestSeq) return
    console.error(error)
    backgroundJobsError.value = '后台任务状态加载失败'
    message.error('后台任务状态加载失败')
  } finally {
    if (backgroundJobsAbortController === controller) backgroundJobsAbortController = undefined
    if (currentRequestSeq === backgroundJobsRequestSeq) backgroundJobsLoading.value = false
  }
}

async function loadBackgroundQueues() {
  backgroundQueuesAbortController?.abort()
  const controller = new AbortController()
  backgroundQueuesAbortController = controller
  const currentRequestSeq = ++backgroundQueuesRequestSeq
  backgroundQueuesLoading.value = true
  backgroundQueuesError.value = ''
  void loadRuntimeSummary()
  try {
    const result = await api.stats.systemMetricsRuntimeQueues({ page: backgroundQueuePage.value, pageSize: backgroundQueuePageSize }, { signal: controller.signal })
    if (currentRequestSeq !== backgroundQueuesRequestSeq) return
    backgroundQueuesResult.value = result
  } catch (error) {
    if (controller.signal.aborted) return
    if (currentRequestSeq !== backgroundQueuesRequestSeq) return
    console.error(error)
    backgroundQueuesError.value = '后台队列状态加载失败'
    message.error('后台队列状态加载失败')
  } finally {
    if (backgroundQueuesAbortController === controller) backgroundQueuesAbortController = undefined
    if (currentRequestSeq === backgroundQueuesRequestSeq) backgroundQueuesLoading.value = false
  }
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  rangeMode.value = 'custom'
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

async function handleQuickRangeChange(value: string | number) {
  await loadUsageStatsWindow({ force: true, viewScope: 'admin' })
  const mode = value as QuickRange
  const range = quickRangeDateRange(mode)
  if (!range) return
  dateRange.value = parseDateRange({
    startDate: formatDateKey(range[0]),
    endDate: formatDateKey(range[1])
  })
  rangeMode.value = mode
  dateRangeExplicit.value = true
  void loadData()
}

function resetFilters() {
  const defaults = defaultSystemMetricsPageState()
  dateRange.value = parseDateRange(defaults.range)
  rangeMode.value = 'auto'
  dateRangeExplicit.value = false
  calendarRange.value = [null, null]
  pageStateCache.clear()
  void loadData()
}

function handleBackgroundJobTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown }
  const current = Number(next.current)
  const nextPage = Number.isFinite(current) && current > 0 ? Math.trunc(current) : 1
  if (nextPage === backgroundJobPage.value) return
  backgroundJobPage.value = nextPage
  void loadBackgroundJobs()
}

function handleBackgroundQueueTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown }
  const current = Number(next.current)
  const nextPage = Number.isFinite(current) && current > 0 ? Math.trunc(current) : 1
  if (nextPage === backgroundQueuePage.value) return
  backgroundQueuePage.value = nextPage
  void loadBackgroundQueues()
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
  if (!dateRangeExplicit.value) return {}
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

function syncImplicitDateRangeToStatsWindow() {
  if (dateRangeExplicit.value) return
  const end = statsWindowEndDate()
  if (!end) return
  dateRange.value = [end, end]
}

function syncDynamicDateRangeToStatsWindow() {
  if (rangeMode.value === 'auto') {
    syncImplicitDateRangeToStatsWindow()
    return
  }
  if (!isQuickRangeMode(rangeMode.value)) return
  const range = quickRangeDateRange(rangeMode.value)
  if (!range) return
  dateRange.value = [range[0].startOf('day'), range[1].startOf('day')]
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
    rangeMode: rangeMode.value,
    range: rangeMode.value !== 'auto' ? { startDate, endDate } : undefined
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => authState.revision.value, () => {
  trendAbortController?.abort()
  runtimeSummaryAbortController?.abort()
  backgroundJobsAbortController?.abort()
  backgroundQueuesAbortController?.abort()
  trendAbortController = undefined
  runtimeSummaryAbortController = undefined
  backgroundJobsAbortController = undefined
  backgroundQueuesAbortController = undefined
  runtimeSummaryPromise = undefined
  pageLoadGeneration += 1
  requestSeq += 1
  runtimeSummaryRequestSeq += 1
  backgroundJobsRequestSeq += 1
  backgroundQueuesRequestSeq += 1
  loading.value = false
  backgroundJobsLoading.value = false
  backgroundQueuesLoading.value = false
  systemMetrics.value = undefined
  runtimeSummary.value = undefined
  backgroundJobsResult.value = undefined
  backgroundQueuesResult.value = undefined
  trendError.value = ''
  runtimeSummaryError.value = ''
  backgroundJobsError.value = ''
  backgroundQueuesError.value = ''
})
watch(() => backgroundJobsResult.value?.total, (total) => {
  if (typeof total !== 'number' || !Number.isFinite(total) || total < 0) return
  const maxPage = Math.max(1, Math.ceil(total / backgroundJobPageSize))
  if (backgroundJobPage.value > maxPage) {
    backgroundJobPage.value = maxPage
    void loadBackgroundJobs()
  }
})
watch(() => backgroundQueuesResult.value?.total, (total) => {
  if (typeof total !== 'number' || !Number.isFinite(total) || total < 0) return
  const maxPage = Math.max(1, Math.ceil(total / backgroundQueuePageSize))
  if (backgroundQueuePage.value > maxPage) {
    backgroundQueuePage.value = maxPage
    void loadBackgroundQueues()
  }
})

onBeforeUnmount(() => {
  disposed = true
  trendAbortController?.abort()
  runtimeSummaryAbortController?.abort()
  backgroundJobsAbortController?.abort()
  backgroundQueuesAbortController?.abort()
  trendAbortController = undefined
  runtimeSummaryAbortController = undefined
  backgroundJobsAbortController = undefined
  backgroundQueuesAbortController = undefined
  runtimeSummaryPromise = undefined
  pageLoadGeneration += 1
  requestSeq += 1
  runtimeSummaryRequestSeq += 1
  backgroundJobsRequestSeq += 1
  backgroundQueuesRequestSeq += 1
  disconnectRuntimeObservers()
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
