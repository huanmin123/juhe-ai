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
        <StatsChartCard
          title="运行状态"
          :loading="healthSnapshotLoading && !healthSnapshot"
          :has-data="Boolean(healthSnapshot) || Boolean(healthSnapshotError)"
          :empty-description="healthSnapshotEmptyDescription"
        >
          <a-alert v-if="healthSnapshotError" class="health-error" type="error" show-icon :message="healthSnapshotError">
            <template #action>
              <a-button type="link" size="small" @click="loadHealthSnapshot">重试</a-button>
            </template>
          </a-alert>
          <template v-if="healthSnapshot">
            <div class="health-meta">检查时间：{{ formatDateTime(healthSnapshot.checkedAt) }}</div>
            <div v-for="group in healthStatusGroups" :key="group.key" class="health-group">
              <div class="health-group-title">{{ group.title }}</div>
              <a-alert
                v-if="group.unavailable"
                type="warning"
                show-icon
                :message="`jobs 健康面不可达：${group.reason || '原因未知'}`"
              />
              <div v-else-if="group.entries.length" class="health-kv-grid">
                <div v-for="[key, value] in group.entries" :key="key" class="health-kv-item">
                  <span class="health-kv-key">{{ key }}</span>
                  <span v-if="typeof value === 'boolean'" class="health-kv-value">
                    <span class="health-status-dot" :class="value ? 'health-status-ok' : 'health-status-off'" />
                    {{ value ? '正常' : '未启用' }}
                  </span>
                  <span v-else class="health-kv-value">{{ healthValueText(value) }}</span>
                </div>
              </div>
              <a-empty v-else class="health-group-empty" description="暂无状态明细" />
            </div>
          </template>
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard
          :title="`Go Runtime 指标趋势（${currentWindowLabel}）`"
          :description="goRuntimeDescription"
          :loading="goRuntimeLoading && !goRuntimeTrend"
          :has-data="hasGoRuntimeTrend || Boolean(goRuntimeError)"
          :empty-description="goRuntimeEmptyDescription"
        >
          <a-alert v-if="goRuntimeError" class="go-runtime-error" type="error" show-icon :message="goRuntimeError">
            <template #action>
              <a-button type="link" size="small" @click="loadGoRuntimeTrend">重试</a-button>
            </template>
          </a-alert>
          <div v-if="hasGoRuntimeTrend" class="go-runtime-view-toolbar">
            <a-segmented v-model:value="goRuntimeChartView" size="small" :options="goRuntimeChartViewOptions" />
            <span v-if="goRuntimeViewUnavailable" class="go-runtime-view-hint">当前 Go 数据未提供该组指标</span>
          </div>
          <div v-if="goRuntimeSummaryItems.length" class="go-runtime-summary" aria-label="Go Runtime 最新摘要">
            <div v-for="metric in goRuntimeSummaryItems" :key="metric.label" class="go-runtime-summary-item">
              <span>{{ metric.label }}</span>
              <strong>{{ metric.value }}</strong>
            </div>
          </div>
          <div v-if="hasGoRuntimeChartDataForView" ref="goRuntimeChartRef" class="chart-panel chart-panel-large" />
          <a-empty v-else-if="hasGoRuntimeTrend && !goRuntimeError" class="go-runtime-view-empty" description="该组指标暂无可用采样" />
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
            :error="backgroundJobsError"
            :on-retry="loadBackgroundJobs"
            @change="handleBackgroundJobTableChange"
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
import { formatDateTime } from '@/shared/formatters'
import type {
  SystemMetricsHealthSnapshot,
  SystemMetricsRuntimeJobsResult,
  GoRuntimeTrendOverview
} from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import { buildGoRuntimeOption, hasGoRuntimeChartData, type GoRuntimeChartView } from './statsChartOptions'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
type RangeMode = 'auto' | QuickRange | 'custom'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]
const StatsBackgroundJobsCard = defineAsyncComponent(() => import('./StatsBackgroundJobsCard.vue'))

interface HealthStatusGroup {
  key: string
  title: string
  unavailable: boolean
  reason?: string
  entries: Array<[string, unknown]>
}

type SystemMetricsPageState = {
  rangeMode: RangeMode
  range?: {
    startDate: string
    endDate: string
  }
}

function isDynamicRangeMode(value: RangeMode): value is Exclude<RangeMode, 'custom'> {
  return value !== 'custom'
}

function isQuickRangeMode(value: RangeMode): value is QuickRange {
  return value === 'today' || value === 'recent7d' || value === 'recent1m'
}

const defaultDateRange = todayDateRange
const defaultSystemMetricsPageState = (): SystemMetricsPageState => ({ rangeMode: 'auto' })
const pageStateCache = usePageStateCache<SystemMetricsPageState>('system-metrics-stats', defaultSystemMetricsPageState, { version: 4 })
const initialPageState = pageStateCache.read()

const loading = ref(false)
const backgroundJobsLoading = ref(false)
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const rangeMode = ref<RangeMode>(initialPageState.rangeMode)
const dateRangeExplicit = ref(rangeMode.value !== 'auto')
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const goRuntimeTrend = ref<GoRuntimeTrendOverview>()
const goRuntimeChartView = ref<GoRuntimeChartView>('concurrency')
const healthSnapshot = ref<SystemMetricsHealthSnapshot>()
const backgroundJobsResult = ref<SystemMetricsRuntimeJobsResult>()
const { usageStatsWindow, usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()

const goRuntimeChartRef = ref<HTMLDivElement>()
const goRuntimeChart = shallowRef<ECharts>()
const backgroundJobsSectionRef = ref<HTMLDivElement>()
const backgroundJobsSectionLoaded = ref(false)
const backgroundJobPageSize = 10
const backgroundJobPage = ref(1)
let pageLoadGeneration = 0
let goRuntimeRequestSeq = 0
let healthSnapshotRequestSeq = 0
let backgroundJobsRequestSeq = 0
let goRuntimeAbortController: AbortController | undefined
let healthSnapshotAbortController: AbortController | undefined
let backgroundJobsAbortController: AbortController | undefined
let backgroundJobsObserver: IntersectionObserver | undefined
let disposed = false

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderSystemCharts,
  resizeCharts,
  disposeCharts,
  onMounted: loadPageData,
  onDeactivate: () => {
    goRuntimeAbortController?.abort()
    healthSnapshotAbortController?.abort()
    backgroundJobsAbortController?.abort()
    goRuntimeAbortController = undefined
    healthSnapshotAbortController = undefined
    backgroundJobsAbortController = undefined
    pageLoadGeneration += 1
    goRuntimeRequestSeq += 1
    healthSnapshotRequestSeq += 1
    backgroundJobsRequestSeq += 1
    loading.value = false
    goRuntimeLoading.value = false
    healthSnapshotLoading.value = false
    backgroundJobsLoading.value = false
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
const goRuntimeLoading = ref(false)
const goRuntimeError = ref('')
const healthSnapshotLoading = ref(false)
const healthSnapshotError = ref('')
const backgroundJobsError = ref('')
const backgroundJobsInitialLoading = computed(() => backgroundJobsLoading.value && !backgroundJobsResult.value)
const hasGoRuntimeTrend = computed(() => (goRuntimeTrend.value?.items.length ?? 0) > 0)
const hasGoRuntimeChartDataForView = computed(() => hasGoRuntimeChartData(goRuntimeTrend.value?.items ?? [], goRuntimeChartView.value))
const goRuntimeSummaryItems = computed(() => {
  const items = goRuntimeTrend.value?.items ?? []
  const latest = [...items].reverse().find((item) => item.sampleCount > 0)
  if (!latest) return []
  const result: Array<{ label: string; value: string }> = []
  if (isFiniteMetric(latest.cpuPercentAvg)) result.push({ label: 'Go CPU（单核）', value: `${latest.cpuPercentAvg!.toFixed(1)}%` })
  if (isFiniteMetric(latest.uptimeSecondsAvg)) result.push({ label: '运行时长', value: formatUptime(latest.uptimeSecondsAvg!) })
  if (isFiniteMetric(latest.gomaxprocsAvg)) result.push({ label: 'GOMAXPROCS', value: Math.round(latest.gomaxprocsAvg!).toLocaleString('zh-CN') })
  return result
})
const goRuntimeChartViewOptions = computed(() => [
  { label: '并发（个）', value: 'concurrency' },
  { label: '内存（MiB / 个）', value: 'memory' }
])
const goRuntimeViewUnavailable = computed(() => hasGoRuntimeTrend.value && !hasGoRuntimeChartDataForView.value)
const goRuntimeDescription = computed(() => {
  const trend = goRuntimeTrend.value
  return trend ? `${trend.service} / ${trend.role} · runtimeKind=${trend.runtimeKind}` : undefined
})
const goRuntimeEmptyDescription = computed(() => `${currentWindowLabel.value}暂无 Go runtime 采样`)
const healthSnapshotEmptyDescription = computed(() => '暂无运行状态快照')
const healthStatusGroups = computed<HealthStatusGroup[]>(() => {
  const snapshot = healthSnapshot.value
  if (!snapshot) return []
  return [
    {
      key: 'gateway',
      title: 'Gateway 就绪状态',
      unavailable: false,
      entries: Object.entries(snapshot.gateway ?? {})
    },
    {
      key: 'jobs',
      title: '后台任务健康（jobs）',
      unavailable: snapshot.jobs.available === false,
      reason: snapshot.jobs.reason,
      entries: snapshot.jobs.available ? Object.entries(snapshot.jobs.payload ?? {}) : []
    }
  ]
})
const backgroundJobRows = computed(() => backgroundJobsResult.value?.items ?? [])
const backgroundJobPagination = computed(() => ({
  current: backgroundJobPage.value,
  pageSize: backgroundJobPageSize,
  total: backgroundJobsResult.value?.total ?? 0,
  showSizeChanger: false
}))
const hasBackgroundJobs = computed(() => backgroundJobRows.value.length > 0)
const backgroundJobEmptyDescription = computed(() => '暂无后台任务执行记录')

function isFiniteMetric(value: number | null | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function formatUptime(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)} 秒`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时`
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时`
}

function healthValueText(value: unknown): string {
  if (value === null || value === undefined || value === '') return '-'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

async function loadGoRuntimeTrend() {
  goRuntimeAbortController?.abort()
  const controller = new AbortController()
  goRuntimeAbortController = controller
  const currentRequestSeq = ++goRuntimeRequestSeq
  goRuntimeLoading.value = true
  goRuntimeError.value = ''
  try {
    const rangeParams = selectedRangeParams()
    const result = await api.stats.goRuntimeTrend(rangeParams, { signal: controller.signal })
    if (currentRequestSeq !== goRuntimeRequestSeq) return
    if (result.runtimeKind !== 'go') throw new Error('Go runtime metrics response has an invalid runtimeKind')
    syncImplicitDateRangeToStatsWindow()
    goRuntimeTrend.value = result
  } catch (error) {
    if (controller.signal.aborted || currentRequestSeq !== goRuntimeRequestSeq) return
    console.error(error)
    goRuntimeError.value = 'Go runtime 指标加载失败'
    message.error('Go runtime 指标加载失败')
  } finally {
    if (goRuntimeAbortController === controller) goRuntimeAbortController = undefined
    if (currentRequestSeq === goRuntimeRequestSeq) {
      goRuntimeLoading.value = false
      renderCharts()
    }
  }
}

async function loadHealthSnapshot() {
  healthSnapshotAbortController?.abort()
  const controller = new AbortController()
  healthSnapshotAbortController = controller
  const currentRequestSeq = ++healthSnapshotRequestSeq
  healthSnapshotLoading.value = true
  healthSnapshotError.value = ''
  try {
    const result = await api.stats.systemMetricsHealthSnapshot({ signal: controller.signal })
    if (currentRequestSeq !== healthSnapshotRequestSeq) return
    healthSnapshot.value = result
  } catch (error) {
    if (controller.signal.aborted || currentRequestSeq !== healthSnapshotRequestSeq) return
    console.error(error)
    healthSnapshotError.value = '运行状态加载失败'
  } finally {
    if (healthSnapshotAbortController === controller) healthSnapshotAbortController = undefined
    if (currentRequestSeq === healthSnapshotRequestSeq) healthSnapshotLoading.value = false
  }
}

async function loadTrendData() {
  loading.value = true
  try {
    await Promise.all([loadGoRuntimeTrend(), loadHealthSnapshot()])
  } finally {
    loading.value = false
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
  return loadTrendData()
}

function setupRuntimeObservers(): void {
  disconnectRuntimeObservers()
  if (disposed || !pageActive.value) return
  if (typeof IntersectionObserver === 'undefined') {
    if (!backgroundJobsSectionLoaded.value) {
      backgroundJobsSectionLoaded.value = true
      void loadBackgroundJobs()
    }
    return
  }
  observeRuntimeSection(backgroundJobsSectionRef.value, backgroundJobsSectionLoaded, () => {
    void loadBackgroundJobs()
  })
}

function observeRuntimeSection(
  target: HTMLDivElement | undefined,
  loaded: { value: boolean },
  onVisible: () => void
): void {
  if (!target || loaded.value) return
  const observer = new IntersectionObserver((entries) => {
    if (disposed || !pageActive.value || !entries.some((entry) => entry.isIntersecting)) return
    loaded.value = true
    observer.disconnect()
    backgroundJobsObserver = undefined
    onVisible()
  }, { rootMargin: '240px 0px' })
  observer.observe(target)
  backgroundJobsObserver = observer
}

function disconnectRuntimeObservers(): void {
  backgroundJobsObserver?.disconnect()
  backgroundJobsObserver = undefined
}

async function loadBackgroundJobs() {
  backgroundJobsAbortController?.abort()
  const controller = new AbortController()
  backgroundJobsAbortController = controller
  const currentRequestSeq = ++backgroundJobsRequestSeq
  backgroundJobsLoading.value = true
  backgroundJobsError.value = ''
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

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  rangeMode.value = 'custom'
  dateRangeExplicit.value = true
  void loadTrendData()
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
  void loadTrendData()
}

function resetFilters() {
  const defaults = defaultSystemMetricsPageState()
  dateRange.value = parseDateRange(defaults.range)
  rangeMode.value = 'auto'
  dateRangeExplicit.value = false
  calendarRange.value = [null, null]
  pageStateCache.clear()
  void loadPageData()
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

async function renderSystemCharts() {
  await renderGoRuntimeChart()
}

async function renderGoRuntimeChart() {
  if (!hasGoRuntimeChartDataForView.value) {
    disposeChart(goRuntimeChart)
    return
  }
  const chart = await ensureChart(goRuntimeChartRef, goRuntimeChart, () => pageActive.value)
  if (!chart || !goRuntimeTrend.value || !pageActive.value) return
  chart.setOption(buildGoRuntimeOption(goRuntimeTrend.value.items, goRuntimeTrend.value.timezone, goRuntimeChartView.value), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts([goRuntimeChart.value])
}

watch(goRuntimeChartView, () => renderCharts())

function disposeCharts() {
  disposeChart(goRuntimeChart)
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
  goRuntimeAbortController?.abort()
  healthSnapshotAbortController?.abort()
  backgroundJobsAbortController?.abort()
  goRuntimeAbortController = undefined
  healthSnapshotAbortController = undefined
  backgroundJobsAbortController = undefined
  pageLoadGeneration += 1
  goRuntimeRequestSeq += 1
  healthSnapshotRequestSeq += 1
  backgroundJobsRequestSeq += 1
  loading.value = false
  goRuntimeLoading.value = false
  healthSnapshotLoading.value = false
  backgroundJobsLoading.value = false
  goRuntimeTrend.value = undefined
  healthSnapshot.value = undefined
  backgroundJobsResult.value = undefined
  goRuntimeError.value = ''
  healthSnapshotError.value = ''
  backgroundJobsError.value = ''
})
watch(() => backgroundJobsResult.value?.total, (total) => {
  if (typeof total !== 'number' || !Number.isFinite(total) || total < 0) return
  const maxPage = Math.max(1, Math.ceil(total / backgroundJobPageSize))
  if (backgroundJobPage.value > maxPage) {
    backgroundJobPage.value = maxPage
    void loadBackgroundJobs()
  }
})

onBeforeUnmount(() => {
  disposed = true
  goRuntimeAbortController?.abort()
  healthSnapshotAbortController?.abort()
  backgroundJobsAbortController?.abort()
  goRuntimeAbortController = undefined
  healthSnapshotAbortController = undefined
  backgroundJobsAbortController = undefined
  pageLoadGeneration += 1
  goRuntimeRequestSeq += 1
  healthSnapshotRequestSeq += 1
  backgroundJobsRequestSeq += 1
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

.health-error {
  margin-bottom: 12px;
}

.health-meta {
  margin-bottom: 12px;
  color: #64748b;
  font-size: 12px;
}

.health-group {
  margin-bottom: 12px;
}

.health-group:last-child {
  margin-bottom: 0;
}

.health-group-title {
  margin-bottom: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.health-kv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 8px;
}

.health-kv-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
  padding: 6px 10px;
  border: 1px solid #edf2f7;
  border-radius: 6px;
}

.health-kv-key {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.health-kv-value {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  color: #1f2937;
  font-size: 12px;
}

.health-status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.health-status-ok {
  background: #52c41a;
}

.health-status-off {
  background: #d9d9d9;
}

.health-group-empty {
  padding: 12px 0;
}

.go-runtime-error {
  margin-bottom: 12px;
}

.go-runtime-view-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.go-runtime-view-hint {
  color: #8c8c8c;
  font-size: 12px;
}

.go-runtime-summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(132px, 1fr));
  gap: 8px;
  margin-bottom: 8px;
}

.go-runtime-summary-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid #edf2f7;
  border-radius: 6px;
  color: #64748b;
  font-size: 12px;
}

.go-runtime-summary-item strong {
  overflow: hidden;
  color: #1f2937;
  font-size: 15px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.go-runtime-view-empty {
  min-height: 220px;
  padding-top: 56px;
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

  .go-runtime-view-toolbar {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }
}
</style>
