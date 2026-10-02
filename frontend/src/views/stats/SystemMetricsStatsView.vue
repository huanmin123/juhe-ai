<template>
  <div class="system-metrics-page">
    <a-card class="page-card system-metrics-header-card">
      <div class="page-toolbar system-metrics-toolbar system-metrics-toolbar-desktop">
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
      <!-- 手机端紧凑条：快捷时间段常驻，精确日期与重置/刷新收进抽屉 -->
      <div class="system-metrics-mobile-bar">
        <a-segmented
          :value="quickRangeValue ?? ''"
          :disabled="loading"
          :options="quickRangeOptions"
          class="system-metrics-mobile-quick-range"
          @change="handleQuickRangeChange"
        />
        <a-button class="system-metrics-mobile-filter-button" @click="mobileFilterOpen = true">
          <template #icon>
            <FilterOutlined />
          </template>
          筛选
          <span v-if="mobileFilterActive" class="system-metrics-mobile-filter-dot" />
        </a-button>
      </div>
    </a-card>

    <a-drawer
      v-model:open="mobileFilterOpen"
      title="筛选统计"
      placement="bottom"
      height="min(52vh, 420px)"
      class="system-metrics-mobile-filter-drawer"
      :body-style="{ padding: '14px 16px 16px' }"
    >
      <div class="system-metrics-mobile-filter-body">
        <div class="system-metrics-mobile-filter-field">
          <span class="system-metrics-mobile-filter-label">时间范围</span>
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="system-metrics-mobile-filter-control"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
        </div>
        <div class="system-metrics-mobile-filter-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button type="primary" :loading="loading" @click="loadPageData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-drawer>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard
          title="进程状态"
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
            <div class="health-process-list">
              <section v-for="process in healthProcessSections" :key="process.key" class="health-process-card" :class="{ degraded: process.display.conclusion !== 'ok' }">
                <header class="health-process-head">
                  <span class="health-process-title">{{ process.title }}</span>
                  <span v-if="process.display.ownerMode" class="health-process-owner">{{ process.display.ownerMode }}</span>
                  <a-tag :color="healthConclusionColor(process.display.conclusion)">
                    {{ healthConclusionText(process.display.conclusion) }}
                  </a-tag>
                </header>
                <a-alert
                  v-if="process.display.conclusion === 'unreachable'"
                  class="health-process-alert"
                  type="warning"
                  show-icon
                  :message="`${process.title} 健康面不可达：${process.display.reason || '原因未知'}`"
                />
                <template v-else>
                  <!-- 真异常优先：已启用但不就绪的功能，醒目提示 -->
                  <div v-if="process.display.unhealthyLabels.length" class="health-line health-line-unhealthy">
                    <span class="health-line-label">未就绪</span>
                    <span>{{ process.display.unhealthyLabels.join('、') }}</span>
                  </div>
                  <!-- 就绪汇总：全绿时一行带过 -->
                  <div v-if="process.display.readyLabels.length" class="health-line">
                    <span class="health-line-label">已就绪 {{ process.display.readyLabels.length }} 项</span>
                    <span class="health-line-items">{{ process.display.readyLabels.join(' · ') }}</span>
                  </div>
                  <!-- 未启用：部署形态预期配置（项内附原因），中性呈现，不算异常 -->
                  <div v-if="process.display.disabledLabels.length" class="health-line health-line-muted">
                    <span class="health-line-label">未启用 {{ process.display.disabledLabels.length }} 项 · 非故障</span>
                    <span class="health-line-items">{{ process.display.disabledLabels.join(' · ') }}</span>
                  </div>
                  <!-- 已停用任务：一行摘要，展开后按根因归并（一行一问题） -->
                  <a-collapse v-if="process.display.disabledJobs.length" class="health-disabled-jobs" ghost>
                    <a-collapse-panel key="jobs" :header="`此部署形态下有 ${process.display.disabledJobs.length} 个任务不注册（${process.display.disabledJobGroups.length} 类原因）`">
                      <div v-for="group in process.display.disabledJobGroups" :key="group.cause" class="health-disabled-job-group">
                        <span class="health-disabled-job-cause">{{ group.cause }}（{{ group.jobs.length }} 个）</span>
                        <span class="health-disabled-job-names">{{ group.jobs.join(' · ') }}</span>
                      </div>
                    </a-collapse-panel>
                  </a-collapse>
                  <a-collapse v-if="process.display.diagnostics.length" class="health-diagnostics" ghost>
                    <a-collapse-panel key="diagnostics" :header="`诊断明细（${process.display.diagnostics.length} 项运维遥测）`">
                      <div class="health-kv-grid">
                        <div v-for="item in process.display.diagnostics" :key="item.key" class="health-kv-item">
                          <span class="health-kv-key">{{ item.label }}</span>
                          <span class="health-kv-value">{{ item.value }}</span>
                        </div>
                      </div>
                    </a-collapse-panel>
                  </a-collapse>
                </template>
              </section>
            </div>
          </template>
        </StatsChartCard>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="system-metrics-section">
      <a-col :xs="24">
        <StatsChartCard
          :title="`Go Runtime 指标趋势（${currentWindowLabel}）`"
          :loading="goRuntimeLoading && !goRuntimeTrend"
          :has-data="hasGoRuntimeTrend || Boolean(goRuntimeError)"
          :empty-description="goRuntimeEmptyDescription"
        >
          <a-alert v-if="goRuntimeError" class="go-runtime-error" type="error" show-icon :message="goRuntimeError">
            <template #action>
              <a-button type="link" size="small" @click="loadGoRuntimeTrend">重试</a-button>
            </template>
          </a-alert>
          <a-result
            v-if="goRuntimeSamplingDisabled"
            class="go-runtime-sampling-disabled"
            status="info"
            title="Go Runtime 指标采样未启用"
          >
            <template #extra>
              <div class="go-runtime-sampling-guide">
                <p>监控默认跟随主存储开启；当前被显式关闭（JUHE_AI_GO_RUNTIME_METRICS_STORE=disabled）。</p>
                <p>移除该配置或改为 sqlite/postgres 并重启 gateway/jobs 即可恢复；详见 docs/develop/运行说明.md。</p>
              </div>
            </template>
          </a-result>
          <template v-else>
            <a-tabs v-if="goRuntimeRoles.length" v-model:active-key="goRuntimeActiveRole" class="go-runtime-role-tabs">
              <a-tab-pane v-for="roleTrend in goRuntimeRoles" :key="roleTrend.role" :tab="goRuntimeRoleLabel(roleTrend.role)">
                <div v-if="goRuntimeSummaryItems.length" class="go-runtime-summary" aria-label="Go Runtime 最新摘要">
                  <div v-for="metric in goRuntimeSummaryItems" :key="metric.label" class="go-runtime-summary-item">
                    <span>{{ metric.label }}</span>
                    <strong>{{ metric.value }}</strong>
                  </div>
                </div>
                <div class="go-runtime-view-toolbar">
                  <a-segmented v-model:value="goRuntimeChartView" size="small" :options="goRuntimeChartViewOptions" />
                  <span v-if="goRuntimeViewUnavailable" class="go-runtime-view-hint">当前 Go 数据未提供该组指标</span>
                </div>
              </a-tab-pane>
            </a-tabs>
            <div v-if="hasGoRuntimeChartDataForView" ref="goRuntimeChartRef" class="chart-panel chart-panel-large" />
            <a-empty v-else-if="hasGoRuntimeTrend && !goRuntimeError" class="go-runtime-view-empty" description="该组指标暂无可用采样" />
          </template>
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
            :status="backgroundJobStatus"
            :error="backgroundJobsError"
            :on-retry="loadBackgroundJobs"
            @change="handleBackgroundJobTableChange"
            @status-change="handleBackgroundJobStatusChange"
            @refresh="handleBackgroundJobsRefresh"
          />
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onActivated, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { message } from '@/lib/antd'
import { FilterOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateRangeKeys, todayDateRange } from '@/shared/dateRange'
import { formatDateTime } from '@/shared/formatters'
import type {
  SystemMetricsHealthSnapshot,
  SystemMetricsRuntimeJobsResult,
  GoRuntimeTrendOverview,
  GoRuntimeTrendRole
} from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import { buildGoRuntimeOption, hasGoRuntimeChartData, type GoRuntimeChartView } from './statsChartOptions'
import { bytesToMiB, formatInteger } from './statsFormatters'
import {
  gatewayHealthDisplay,
  healthConclusionColor,
  healthConclusionText,
  jobsHealthDisplay
} from './systemMetricsHealth'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
type RangeMode = 'auto' | QuickRange | 'custom'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]
const StatsBackgroundJobsCard = defineAsyncComponent(() => import('./StatsBackgroundJobsCard.vue'))

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
const goRuntimeActiveRole = ref<GoRuntimeTrendRole>('gateway')
const healthSnapshot = ref<SystemMetricsHealthSnapshot>()
const backgroundJobsResult = ref<SystemMetricsRuntimeJobsResult>()
const backgroundJobStatus = ref('')
const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()

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
// 手机端筛选抽屉开合；自定义日期（快捷段未命中）时打点。
const mobileFilterOpen = ref(false)
const mobileFilterActive = computed(() => quickRangeValue.value === undefined)
const goRuntimeLoading = ref(false)
const goRuntimeError = ref('')
const healthSnapshotLoading = ref(false)
const healthSnapshotError = ref('')
const backgroundJobsError = ref('')
const backgroundJobsInitialLoading = computed(() => backgroundJobsLoading.value && !backgroundJobsResult.value)
const hasGoRuntimeTrend = computed(() => Boolean(goRuntimeTrend.value))
const goRuntimeRoles = computed(() => goRuntimeTrend.value?.roles ?? [])
const goRuntimeSamplingDisabled = computed(() => goRuntimeTrend.value?.samplingEnabled === false)
const goRuntimeActiveRoleTrend = computed(() => {
  const roles = goRuntimeRoles.value
  return roles.find((roleTrend) => roleTrend.role === goRuntimeActiveRole.value) ?? roles[0]
})
const activeGoRuntimeRoleItems = computed(() => goRuntimeActiveRoleTrend.value?.items ?? [])
const hasGoRuntimeChartDataForView = computed(() => hasGoRuntimeChartData(activeGoRuntimeRoleItems.value, goRuntimeChartView.value))
const goRuntimeSummaryItems = computed(() => {
  const items = activeGoRuntimeRoleItems.value
  const latest = [...items].reverse().find((item) => item.sampleCount > 0)
  if (!latest) return []
  const result: Array<{ label: string; value: string }> = []
  if (isFiniteMetric(latest.cpuPercentAvg)) result.push({ label: 'CPU（单核）', value: `${latest.cpuPercentAvg!.toFixed(1)}%` })
  if (isFiniteMetric(latest.heapAllocBytesAvg)) result.push({ label: 'Heap Alloc（MiB）', value: `${bytesToMiB(latest.heapAllocBytesAvg)!.toFixed(1)} MiB` })
  if (isFiniteMetric(latest.goroutinesAvg)) result.push({ label: 'Goroutines（个）', value: formatInteger(latest.goroutinesAvg) })
  if (isFiniteMetric(latest.threadsAvg)) result.push({ label: '线程（个）', value: formatInteger(latest.threadsAvg) })
  if (isFiniteMetric(latest.fdCountAvg)) result.push({ label: 'FD（个）', value: formatInteger(latest.fdCountAvg) })
  if (isFiniteMetric(latest.uptimeSecondsAvg)) result.push({ label: '运行时长', value: formatUptime(latest.uptimeSecondsAvg!) })
  return result
})
const goRuntimeChartViewOptions = computed(() => [
  { label: '并发（个）', value: 'concurrency' },
  { label: '内存（MiB / 个）', value: 'memory' },
  { label: 'CPU（%）', value: 'resource' }
])
const goRuntimeViewUnavailable = computed(() => activeGoRuntimeRoleItems.value.length > 0 && !hasGoRuntimeChartDataForView.value)
const goRuntimeEmptyDescription = computed(() => `${currentWindowLabel.value}暂无 Go runtime 采样`)
const healthSnapshotEmptyDescription = computed(() => '暂无进程状态快照')
const healthProcessSections = computed(() => {
  const snapshot = healthSnapshot.value
  if (!snapshot) return []
  return [
    { key: 'gateway', title: 'Gateway', display: gatewayHealthDisplay(snapshot.gateway) },
    { key: 'jobs', title: 'Jobs', display: jobsHealthDisplay(snapshot.jobs) }
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

function goRuntimeRoleLabel(role: GoRuntimeTrendRole): string {
  if (role === 'gateway') return 'Gateway'
  if (role === 'jobs') return 'Jobs'
  return role
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
    message.error(extractApiErrorMessage(error, 'Go runtime 指标加载失败'))
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
    const result = await api.stats.systemMetricsRuntimeJobs({ page: backgroundJobPage.value, pageSize: backgroundJobPageSize, status: backgroundJobStatus.value || undefined }, { signal: controller.signal })
    if (currentRequestSeq !== backgroundJobsRequestSeq) return
    backgroundJobsResult.value = result
  } catch (error) {
    if (controller.signal.aborted) return
    if (currentRequestSeq !== backgroundJobsRequestSeq) return
    console.error(error)
    backgroundJobsError.value = '后台任务状态加载失败'
    message.error(extractApiErrorMessage(error, '后台任务状态加载失败'))
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

function handleBackgroundJobStatusChange(status: string) {
  if (status === backgroundJobStatus.value) return
  backgroundJobStatus.value = status
  backgroundJobPage.value = 1
  void loadBackgroundJobs()
}

function handleBackgroundJobsRefresh() {
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
  chart.setOption(buildGoRuntimeOption(activeGoRuntimeRoleItems.value, goRuntimeTrend.value.timezone, goRuntimeChartView.value), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts([goRuntimeChart.value])
}

watch(goRuntimeChartView, () => renderCharts())
watch(goRuntimeActiveRole, () => renderCharts())

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

.health-process-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.health-process-card {
  padding: 10px 14px 8px;
  border: 1px solid #edf2f7;
  border-radius: 8px;
}

.health-process-card.degraded {
  border-color: #ffd591;
  background: #fffbe6;
}

.health-process-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.health-process-title {
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.health-process-owner {
  color: #94a3b8;
  font-size: 12px;
}

.health-process-head .ant-tag {
  margin-left: auto;
}

/* 汇总行：标签列固定宽，内容列可换行——全绿时安静收拢，异常行醒目 */
.health-line {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 3px 0;
  font-size: 12.5px;
  line-height: 20px;
}

.health-line-label {
  flex: 0 0 auto;
  color: #94a3b8;
  font-size: 12px;
  white-space: nowrap;
}

.health-line-items {
  min-width: 0;
  overflow-wrap: anywhere;
  color: #475569;
}

.health-line-unhealthy {
  padding: 5px 10px;
  border-radius: 6px;
  background: #fff1f0;
}

.health-line-unhealthy .health-line-label {
  color: #cf1322;
  font-weight: 600;
}

.health-line-unhealthy .health-line-items {
  color: #a8071a;
  font-weight: 500;
}

.health-line-muted .health-line-items {
  color: #94a3b8;
}

.health-disabled-jobs :deep(.ant-collapse-header),
.health-diagnostics :deep(.ant-collapse-header) {
  padding: 4px 0 !important;
  color: #64748b !important;
  font-size: 12px;
}

.health-disabled-jobs :deep(.ant-collapse-content-box) {
  padding: 2px 0 6px !important;
}

.health-disabled-job-group {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 5px 10px;
  border-left: 2px solid #e2e8f0;
  margin-bottom: 4px;
}

.health-disabled-job-cause {
  color: #475569;
  font-size: 12px;
  font-weight: 500;
}

.health-disabled-job-names {
  color: #94a3b8;
  font-size: 11.5px;
  line-height: 17px;
  overflow-wrap: anywhere;
}

.health-process-alert {
  margin-bottom: 0;
}

.health-kv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 8px;
}

/* 诊断明细折叠区：与主状态区视觉区隔（弱化边框），避免运维遥测喧宾夺主 */
.health-diagnostics {
  margin-top: 4px;
}

.health-diagnostics :deep(.ant-collapse-content-box) {
  padding: 2px 0 6px !important;
}

.health-kv-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
  padding: 6px 10px;
  border: 1px solid #f1f5f9;
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
  flex: 1 1 auto;
  white-space: pre-line;
  text-align: left;
  overflow-wrap: anywhere;
  color: #1f2937;
  font-size: 12px;
}

.go-runtime-error {
  margin-bottom: 12px;
}

.go-runtime-sampling-disabled {
  padding: 32px 0;
}

.go-runtime-sampling-guide {
  max-width: 560px;
  margin: 0 auto;
  color: #64748b;
  font-size: 13px;
  line-height: 1.8;
  text-align: left;
}

.go-runtime-sampling-guide p {
  margin: 0 0 6px;
}

.go-runtime-role-tabs {
  margin-bottom: 8px;
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

/* 手机端紧凑筛选条（桌面隐藏） */
.system-metrics-mobile-bar {
  display: none;
}

.system-metrics-mobile-quick-range {
  flex: 1 1 auto;
  min-width: 0;
  max-width: 100%;
}

.system-metrics-mobile-bar :deep(.ant-segmented-item-label) {
  padding: 6px 12px;
}

.system-metrics-mobile-filter-button {
  position: relative;
  flex: 0 0 auto;
}

.system-metrics-mobile-filter-dot {
  position: absolute;
  top: 6px;
  right: 6px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--juhe-coral, #a6755e);
}

.system-metrics-mobile-filter-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.system-metrics-mobile-filter-label {
  color: #64748b;
  font-size: 12px;
}

.system-metrics-mobile-filter-control {
  width: 100%;
}

.system-metrics-mobile-filter-actions {
  display: flex;
  gap: 12px;
  margin-top: 4px;
}

.system-metrics-mobile-filter-actions .ant-btn {
  flex: 1 1 0;
}

@media (max-width: 768px) {
  .system-metrics-toolbar-desktop {
    display: none;
  }

  .system-metrics-mobile-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
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
@media (min-width: 769px) {
  .system-metrics-mobile-filter-drawer {
    display: none;
  }
}
</style>
