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
          <a-segmented
            :value="quickRangeValue ?? ''"
            :disabled="loading"
            :options="quickRangeOptions"
            class="stats-quick-range"
            @change="handleQuickRangeChange"
          />
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="selectedSystemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :disabled="loading"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="selectedSystemAccount"
            all-label="全部用户"
            class="stats-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button :loading="loading" @click="refreshData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-card>

    <StatsSummaryCards
      :cards="summaryCards"
      :loading="summaryLoading"
      :error="summaryError"
      :on-retry="() => loadData({ force: true })"
    />

    <div ref="dailyTrendSectionRef" class="stats-chart-section">
      <StatsChartCard
        :title="dailyTrendTitle"
        :loading="dailyTrendLoading"
        :has-data="hasDailyTrend"
        :empty-description="dailyTrendEmptyDescription"
        :error="dailyTrendError"
        :on-retry="() => loadDailyTrend(true)"
        compact
      >
        <div ref="dailyTrendChartRef" class="daily-consumption-chart" />
      </StatsChartCard>
    </div>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="14">
        <div ref="usageTrendSectionRef" class="stats-chart-section">
        <StatsChartCard
          :title="`请求、失败、平均总耗时（${currentWindowLabel}）`"
          :loading="usageTrendLoading"
          :has-data="hasUsageTrend"
          :empty-description="usageTrendEmptyDescription"
          :error="usageTrendError"
          :on-retry="() => loadChartSection('hourlyTrend', true)"
        >
          <div ref="usageTrendChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
        </div>
      </a-col>
      <a-col :xs="24" :xl="10">
        <div ref="modelDistributionSectionRef" class="stats-chart-section">
        <StatsChartCard
          :title="`模型分布（${currentWindowLabel}）`"
          :loading="modelDistributionLoading"
          :has-data="hasModelDistribution"
          :empty-description="modelDistributionEmptyDescription"
          :error="modelDistributionError"
          :on-retry="() => loadChartSection('modelDistribution', true)"
        >
          <div ref="modelDistributionChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
        </div>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24">
        <div ref="errorSectionRef" class="stats-chart-section">
        <StatsChartCard
          :title="`错误 Top 10（${currentWindowLabel}）`"
          :loading="errorsLoading"
          :has-data="hasErrors"
          :empty-description="errorEmptyDescription"
          :error="errorsError"
          :on-retry="() => loadChartSection('errors', true)"
        >
          <div ref="errorChartRef" class="chart-panel chart-panel-large" />
        </StatsChartCard>
        </div>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { message } from '@/lib/antd'
import { ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { authState } from '@/composables/useAuth'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { didUsageStatsWindowLoadFail, useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys, recentDateRange } from '@/shared/dateRange'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { UsageStatsOverview, UsageStatsOverviewDailyTrendResult } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from './StatsChartCard.vue'
import StatsSummaryCards from './StatsSummaryCards.vue'
import { buildDailyConsumptionOption, buildErrorOption, buildModelDistributionOption, buildUsageTrendOption } from './statsChartOptions'
import { formatCompactInteger, formatCost, formatDurationSeconds, formatInteger, formatPercent } from './statsFormatters'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
type RangeMode = 'auto' | QuickRange | 'custom'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]

type StatsPageState = {
  rangeMode?: RangeMode
  range?: {
    startDate: string
    endDate: string
  }
  selectedSystemAccountId: string
  selectedSystemAccount?: PrincipalSelection
}

function isRangeMode(value: unknown): value is RangeMode {
  return value === 'auto' || value === 'today' || value === 'recent7d' || value === 'recent1m' || value === 'custom'
}

function initialRangeMode(state: StatsPageState): RangeMode {
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

function readLegacyStatsPageState(pageKey: string): Pick<StatsPageState, 'range' | 'selectedSystemAccountId' | 'selectedSystemAccount'> | undefined {
  if (typeof window === 'undefined') return undefined
  const user = authState.currentUser.value
  const userKey = user?.id || user?.username || 'anonymous'
  const normalizedPageKey = pageKey.replace(/[^a-zA-Z0-9/_-]/g, '_')
  try {
    const cached = JSON.parse(window.localStorage.getItem(`juhe-ai:page-state:${userKey}:${normalizedPageKey}:v5`) || '{}') as {
      version?: unknown
      state?: Partial<StatsPageState>
    }
    const range = cached.state?.range
    if (cached.version !== 5 || !range?.startDate || !range.endDate) return undefined
    return {
      range,
      selectedSystemAccountId: cached.state?.selectedSystemAccountId || allSystemAccountsValue,
      selectedSystemAccount: cached.state?.selectedSystemAccount
    }
  } catch {
    return undefined
  }
}

const defaultDateRange = () => recentDateRange(MAX_RANGE_DAYS)
const defaultStatsPageState = (): StatsPageState => ({
  selectedSystemAccountId: allSystemAccountsValue,
  selectedSystemAccount: undefined
})
const route = useRoute()
const pageStateCache = usePageStateCache<StatsPageState>(undefined, defaultStatsPageState, { version: 6 })
const cachedInitialPageState = pageStateCache.read()
const legacyInitialPageState = isRangeMode(cachedInitialPageState.rangeMode) ? undefined : readLegacyStatsPageState(route.path)
const initialPageState: StatsPageState = legacyInitialPageState
  ? { ...cachedInitialPageState, ...legacyInitialPageState, rangeMode: 'custom' }
  : cachedInitialPageState

const loading = ref(false)
const summaryLoading = ref(false)
const summaryError = ref('')
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const rangeMode = ref<RangeMode>(initialRangeMode(initialPageState))
const dateRangeExplicit = ref(rangeMode.value !== 'auto')
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const selectedSystemAccountId = ref(initialPageState.selectedSystemAccountId || allSystemAccountsValue)
const selectedSystemAccount = ref<PrincipalSelection | undefined>(initialPageState.selectedSystemAccount)
const usageOverview = ref<UsageStatsOverview>()
const dailyTrend = ref<UsageStatsOverviewDailyTrendResult>()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const { usageStatsWindow, usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [selectedSystemAccountId.value]
})

const dailyTrendChartRef = ref<HTMLDivElement>()
const usageTrendChartRef = ref<HTMLDivElement>()
const modelDistributionChartRef = ref<HTMLDivElement>()
const errorChartRef = ref<HTMLDivElement>()
const dailyTrendChart = shallowRef<ECharts>()
const usageTrendChart = shallowRef<ECharts>()
const modelDistributionChart = shallowRef<ECharts>()
const errorChart = shallowRef<ECharts>()
let statsRequestSeq = 0
let dailyTrendRequestSeq = 0
const dailyTrendSectionRef = ref<HTMLDivElement>()
const usageTrendSectionRef = ref<HTMLDivElement>()
const modelDistributionSectionRef = ref<HTMLDivElement>()
const errorSectionRef = ref<HTMLDivElement>()
const dailyTrendLoaded = ref(false)
const usageTrendLoaded = ref(false)
const modelDistributionLoaded = ref(false)
const errorsLoaded = ref(false)
const dailyTrendLoading = ref(false)
const usageTrendLoading = ref(false)
const modelDistributionLoading = ref(false)
const errorsLoading = ref(false)
const dailyTrendError = ref('')
const usageTrendError = ref('')
const modelDistributionError = ref('')
const errorsError = ref('')
const chartRequestSeq = { hourlyTrend: 0, modelDistribution: 0, errors: 0 }
const chartSectionResolved = { hourlyTrend: false, modelDistribution: false, errors: false }
let dailyTrendResolved = false
let chartObserver: IntersectionObserver | undefined
let reloadOnActivate = false
let wasDeactivated = false
let disposed = false

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderStatsCharts,
  resizeCharts,
  disposeCharts,
  onMounted: loadData,
  onDeactivate: handlePageDeactivate
})

const hasDailyTrend = computed(() => dailyTrend.value?.dailyTrend.some((item) => item.totalTokens > 0) === true)
const hasUsageTrend = computed(() => (usageOverview.value?.hourlyTrend.length ?? 0) > 0)
const hasModelDistribution = computed(() => (usageOverview.value?.modelDistribution.length ?? 0) > 0)
const hasErrors = computed(() => (usageOverview.value?.errors.length ?? 0) > 0)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const quickRangeValue = computed<QuickRange | undefined>(() => {
  const mode = quickRangeModeForSelection(rangeMode.value)
  if (!mode) return undefined
  if (didUsageStatsWindowLoadFail(isManagementView.value ? 'admin' : 'self')) return undefined
  const [startDate, endDate] = selectedRange.value
  const range = quickRangeDateRange(mode)
  if (!range) return undefined
  return startDate === formatDateKey(range[0]) && endDate === formatDateKey(range[1]) ? mode : undefined
})
const currentWindowLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const dailyTrendRangeLabel = computed(() => dailyTrend.value
  ? `${formatDateLabel(dailyTrend.value.range.startDate)} 至 ${formatDateLabel(dailyTrend.value.range.endDate)}`
  : currentWindowLabel.value)
const dailyTrendTitle = computed(() => `Token 与成本（${dailyTrendRangeLabel.value}）`)
const dailyTrendEmptyDescription = computed(() => `${dailyTrendRangeLabel.value}暂无 Token 消耗`)
const hasWindowUsage = computed(() => (usageOverview.value?.summary.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无趋势数据，窗口指标已在上方展示` : `${currentWindowLabel.value}暂无趋势数据`)
const modelDistributionEmptyDescription = computed(() => `${currentWindowLabel.value}暂无模型调用`)
const errorEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无失败请求` : `${currentWindowLabel.value}暂无失败请求`)
const summaryCards = computed(() => {
  const summary = usageOverview.value?.summary
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `成功 ${formatInteger(summary?.successCount)} / 失败 ${formatInteger(summary?.errorCount)} / 失败率 ${formatPercent((summary?.errorRate ?? 0) * 100)}` },
    { key: 'firstToken', label: '平均首 Token', value: formatDurationSeconds(summary?.averageFirstTokenMs), extra: `平均总耗时 ${formatDurationSeconds(summary?.averageDurationMs)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出 ${formatCompactInteger(summary?.outputTokens)} / 缓存读 ${formatCompactInteger(summary?.cacheReadTokens)}` },
    { key: 'cost', label: '成本', value: formatCost(summary?.totalCost), extra: buildCostExtra(summary) }
  ]
})

function buildCostExtra(summary?: UsageStatsOverview['summary']) {
  const totalCost = summary?.totalCost ?? 0
  const requestCount = summary?.requestCount ?? 0
  const totalTokens = summary?.totalTokens ?? 0
  const averageRequestCost = requestCount > 0 ? totalCost / requestCount : undefined
  const costPerMillionTokens = totalTokens > 0 ? (totalCost / totalTokens) * 1_000_000 : undefined
  return `均次 ${formatOptionalCost(averageRequestCost)} / 每 1M Token ${formatOptionalCost(costPerMillionTokens)}`
}

function formatOptionalCost(value?: number) {
  return typeof value === 'number' && Number.isFinite(value) ? formatCost(value) : '-'
}

async function loadData(options: { force?: boolean; forceUsageWindow?: boolean } = {}) {
  const requestSeq = ++statsRequestSeq
  resetDailyTrend()
  chartRequestSeq.hourlyTrend += 1
  chartRequestSeq.modelDistribution += 1
  chartRequestSeq.errors += 1
  loading.value = true
  summaryLoading.value = true
  summaryError.value = ''
  usageOverview.value = undefined
  usageTrendLoading.value = false
  modelDistributionLoading.value = false
  errorsLoading.value = false
  usageTrendError.value = ''
  modelDistributionError.value = ''
  errorsError.value = ''
  chartSectionResolved.hourlyTrend = false
  chartSectionResolved.modelDistribution = false
  chartSectionResolved.errors = false
  try {
    const viewScope = isManagementView.value ? 'admin' : 'self'
    const windowLoad = loadUsageStatsWindow({ force: options.forceUsageWindow === true, viewScope })
    if (isDynamicRangeMode(rangeMode.value)) {
      await windowLoad
      if (requestSeq !== statsRequestSeq) return
      if (didUsageStatsWindowLoadFail(viewScope)) {
        throw new Error('统计日期窗口加载失败')
      }
      syncDynamicDateRangeToStatsWindow()
    } else if (dateRangeExplicit.value) {
      void windowLoad.catch(() => undefined)
    } else {
      await windowLoad
      if (requestSeq !== statsRequestSeq) return
      if (didUsageStatsWindowLoadFail(viewScope)) {
        throw new Error('统计日期窗口加载失败')
      }
    }
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
    const rangeParams = selectedRangeParams()
    const nextSummary = isManagementView.value
      ? await api.stats.usageOverviewSummary({ ...rangeParams, systemAccountId })
      : await api.myStats.usageOverviewSummary(rangeParams)
    if (requestSeq !== statsRequestSeq) return
    usageOverview.value = {
      range: nextSummary.range,
      summary: nextSummary.summary,
      hourlyTrend: [],
      modelDistribution: [],
      errors: []
    }
    syncDateRangeFromResponse(nextSummary.range)
    summaryError.value = ''
    renderCharts()
    if (dailyTrendLoaded.value) void loadDailyTrend(options.force === true)
    if (usageTrendLoaded.value) void loadChartSection('hourlyTrend', options.force === true)
    if (modelDistributionLoaded.value) void loadChartSection('modelDistribution', options.force === true)
    if (errorsLoaded.value) void loadChartSection('errors', options.force === true)
  } catch (error) {
    if (requestSeq !== statsRequestSeq) return
    console.error(error)
    summaryError.value = '统计摘要加载失败，请重试'
    message.error('统计数据加载失败')
  } finally {
    if (requestSeq === statsRequestSeq) {
      loading.value = false
      summaryLoading.value = false
      renderCharts()
    }
  }
}

async function loadDailyTrend(force = false): Promise<void> {
  if (!usageOverview.value || (!force && dailyTrendResolved) || dailyTrendLoading.value) return
  const requestSeq = ++dailyTrendRequestSeq
  const pageSeq = statsRequestSeq
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
  const rangeParams = resolvedOverviewRangeParams()
  const signature = JSON.stringify([pageSeq, ...currentAuthSignature(), isManagementView.value ? 'admin' : 'self', systemAccountId ?? '', rangeParams.startDate ?? '', rangeParams.endDate ?? ''])
  dailyTrendLoading.value = true
  dailyTrendError.value = ''
  try {
    const result = isManagementView.value
      ? await api.stats.usageOverviewDailyTrend({ ...rangeParams, systemAccountId })
      : await api.myStats.usageOverviewDailyTrend(rangeParams)
    const currentSystemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
    const currentRangeParams = resolvedOverviewRangeParams()
    const currentSignature = JSON.stringify([statsRequestSeq, ...currentAuthSignature(), isManagementView.value ? 'admin' : 'self', currentSystemAccountId ?? '', currentRangeParams.startDate ?? '', currentRangeParams.endDate ?? ''])
    if (requestSeq !== dailyTrendRequestSeq || signature !== currentSignature) return
    const currentOverview = usageOverview.value
    if (!currentOverview || rangeSignature(result.range) !== rangeSignature(currentOverview.range)) {
      dailyTrendError.value = '图表范围已变化，请重试'
      return
    }
    dailyTrend.value = result
    dailyTrendResolved = true
    renderCharts()
  } catch (error) {
    if (requestSeq !== dailyTrendRequestSeq) return
    console.error(error)
    dailyTrendError.value = 'Token 与成本趋势加载失败，请重试'
  } finally {
    if (requestSeq === dailyTrendRequestSeq) dailyTrendLoading.value = false
  }
}

type StatsChartSection = 'hourlyTrend' | 'modelDistribution' | 'errors'

async function loadChartSection(section: StatsChartSection, force = false): Promise<void> {
  if (!usageOverview.value || (!force && chartSectionResolved[section])) return
  const sectionLoading = sectionLoadingRef(section)
  if (sectionLoading.value) return
  const requestSeq = ++chartRequestSeq[section]
  const pageSeq = statsRequestSeq
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
  const rangeParams = resolvedOverviewRangeParams()
  const signature = JSON.stringify([pageSeq, ...currentAuthSignature(), isManagementView.value ? 'admin' : 'self', systemAccountId ?? '', rangeParams.startDate ?? '', rangeParams.endDate ?? '', section])
  sectionLoading.value = true
  sectionErrorRef(section).value = ''
  try {
    const result = isManagementView.value
      ? await loadAdminChartSection(section, { ...rangeParams, systemAccountId })
      : await loadSelfChartSection(section, rangeParams)
    const currentRangeParams = resolvedOverviewRangeParams()
    const currentSignature = JSON.stringify([statsRequestSeq, ...currentAuthSignature(), isManagementView.value ? 'admin' : 'self', isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) ?? '' : '', currentRangeParams.startDate ?? '', currentRangeParams.endDate ?? '', section])
    if (requestSeq !== chartRequestSeq[section] || signature !== currentSignature) return
    const currentOverview = usageOverview.value
    if (!currentOverview) return
    if (rangeSignature(result.range) !== rangeSignature(currentOverview.range)) {
      sectionErrorRef(section).value = '图表范围已变化，请重试'
      return
    }
    usageOverview.value = {
      ...currentOverview,
      range: result.range,
      ...(section === 'hourlyTrend' && 'hourlyTrend' in result ? { hourlyTrend: result.hourlyTrend } : {}),
      ...(section === 'modelDistribution' && 'modelDistribution' in result ? { modelDistribution: result.modelDistribution } : {}),
      ...(section === 'errors' && 'errors' in result ? { errors: result.errors } : {})
    }
    chartSectionResolved[section] = true
    renderCharts()
  } catch (error) {
    if (requestSeq !== chartRequestSeq[section]) return
    console.error(error)
    sectionErrorRef(section).value = '图表加载失败，请重试'
  } finally {
    if (requestSeq === chartRequestSeq[section]) sectionLoading.value = false
  }
}

function rangeSignature(range: { startDate: string; endDate: string; days: number; maxDays: number }): string {
  return JSON.stringify([range.startDate, range.endDate, range.days, range.maxDays])
}

function sectionLoadingRef(section: StatsChartSection) {
  return section === 'hourlyTrend' ? usageTrendLoading : section === 'modelDistribution' ? modelDistributionLoading : errorsLoading
}

function sectionErrorRef(section: StatsChartSection) {
  return section === 'hourlyTrend' ? usageTrendError : section === 'modelDistribution' ? modelDistributionError : errorsError
}

function loadAdminChartSection(section: StatsChartSection, params: { startDate?: string; endDate?: string; systemAccountId?: string }) {
  if (section === 'hourlyTrend') return api.stats.usageOverviewHourlyTrend(params)
  if (section === 'modelDistribution') return api.stats.usageOverviewModelDistribution(params)
  return api.stats.usageOverviewErrors(params)
}

function loadSelfChartSection(section: StatsChartSection, params: { startDate?: string; endDate?: string }) {
  if (section === 'hourlyTrend') return api.myStats.usageOverviewHourlyTrend(params)
  if (section === 'modelDistribution') return api.myStats.usageOverviewModelDistribution(params)
  return api.myStats.usageOverviewErrors(params)
}

onMounted(async () => {
  disposed = false
  await nextTick()
  setupChartObservers()
})

function setupChartObservers(): void {
  chartObserver?.disconnect()
  if (disposed || !pageActive.value) return
  const targets: Array<[HTMLDivElement | undefined, StatsObservedSection]> = [
    [dailyTrendSectionRef.value, 'dailyTrend'],
    [usageTrendSectionRef.value, 'hourlyTrend'],
    [modelDistributionSectionRef.value, 'modelDistribution'],
    [errorSectionRef.value, 'errors']
  ]
  if (typeof IntersectionObserver === 'undefined') {
    for (const [, section] of targets) {
      markSectionLoaded(section)
      if (section === 'dailyTrend') void loadDailyTrend()
      else void loadChartSection(section)
    }
    return
  }
  chartObserver = new IntersectionObserver((entries) => {
    if (disposed || !pageActive.value) return
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      const section = targets.find(([element]) => element === entry.target)?.[1]
      if (!section) continue
      markSectionLoaded(section)
      chartObserver?.unobserve(entry.target)
      if (section === 'dailyTrend') void loadDailyTrend()
      else void loadChartSection(section)
    }
  }, { rootMargin: '240px 0px' })
  for (const [element] of targets) {
    if (element) chartObserver.observe(element)
  }
}

function currentAuthSignature(): [number, string, string] {
  const user = authState.currentUser.value
  return [authState.revision.value, user?.id ?? 'anonymous', user?.role ?? 'anonymous']
}

function invalidateStatsRequests(): void {
  statsRequestSeq += 1
  dailyTrendRequestSeq += 1
  chartRequestSeq.hourlyTrend += 1
  chartRequestSeq.modelDistribution += 1
  chartRequestSeq.errors += 1
  loading.value = false
  summaryLoading.value = false
  dailyTrendLoading.value = false
  usageTrendLoading.value = false
  modelDistributionLoading.value = false
  errorsLoading.value = false
}

function handlePageDeactivate(): void {
  reloadOnActivate = loading.value || summaryLoading.value || dailyTrendLoading.value || usageTrendLoading.value || modelDistributionLoading.value || errorsLoading.value
  wasDeactivated = true
  invalidateStatsRequests()
  chartObserver?.disconnect()
  chartObserver = undefined
}

type StatsObservedSection = 'dailyTrend' | StatsChartSection

function markSectionLoaded(section: StatsObservedSection): void {
  if (section === 'dailyTrend') dailyTrendLoaded.value = true
  else if (section === 'hourlyTrend') usageTrendLoaded.value = true
  else if (section === 'modelDistribution') modelDistributionLoaded.value = true
  else errorsLoaded.value = true
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

function handleSystemAccountChange() {
  if (selectedSystemAccountId.value === allSystemAccountsValue) {
    selectedSystemAccount.value = undefined
  }
  void loadData()
}

function resetFilters() {
  const defaults = defaultStatsPageState()
  dateRange.value = parseDateRange(defaults.range)
  rangeMode.value = 'auto'
  dateRangeExplicit.value = false
  calendarRange.value = [null, null]
  selectedSystemAccountId.value = defaults.selectedSystemAccountId
  selectedSystemAccount.value = defaults.selectedSystemAccount
  resetSystemAccountOptionsSearch()
  pageStateCache.clear()
  void loadData()
}

function refreshData(): void {
  void loadData({
    force: true,
    forceUsageWindow: isDynamicRangeMode(rangeMode.value)
  })
}

async function renderStatsCharts() {
  await Promise.all([
    renderDailyTrendChart(),
    renderUsageTrendChart(),
    renderModelDistributionChart(),
    renderErrorChart()
  ])
}

async function renderDailyTrendChart() {
  if (!hasDailyTrend.value) {
    disposeChart(dailyTrendChart)
    return
  }
  const chart = await ensureChart(dailyTrendChartRef, dailyTrendChart, () => pageActive.value)
  if (!chart || !dailyTrend.value || !pageActive.value) return
  chart.setOption(buildDailyConsumptionOption(dailyTrend.value.dailyTrend), { notMerge: true })
}

async function renderUsageTrendChart() {
  if (!hasUsageTrend.value) {
    disposeChart(usageTrendChart)
    return
  }
  const chart = await ensureChart(usageTrendChartRef, usageTrendChart, () => pageActive.value)
  if (!chart || !usageOverview.value || !pageActive.value) return
  chart.setOption(buildUsageTrendOption(usageOverview.value.hourlyTrend), { notMerge: true })
}

async function renderModelDistributionChart() {
  if (!hasModelDistribution.value) {
    disposeChart(modelDistributionChart)
    return
  }
  const chart = await ensureChart(modelDistributionChartRef, modelDistributionChart, () => pageActive.value)
  if (!chart || !usageOverview.value || !pageActive.value) return
  chart.setOption(buildModelDistributionOption(usageOverview.value.modelDistribution), { notMerge: true })
}

async function renderErrorChart() {
  if (!hasErrors.value) {
    disposeChart(errorChart)
    return
  }
  const chart = await ensureChart(errorChartRef, errorChart, () => pageActive.value)
  if (!chart || !usageOverview.value || !pageActive.value) return
  chart.setOption(buildErrorOption(usageOverview.value.errors), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts([dailyTrendChart.value, usageTrendChart.value, modelDistributionChart.value, errorChart.value])
}

function disposeCharts() {
  disposeChart(dailyTrendChart)
  disposeChart(usageTrendChart)
  disposeChart(modelDistributionChart)
  disposeChart(errorChart)
}

function resetDailyTrend(): void {
  dailyTrendRequestSeq += 1
  dailyTrend.value = undefined
  dailyTrendLoading.value = false
  dailyTrendError.value = ''
  dailyTrendResolved = false
  disposeChart(dailyTrendChart)
}

function snapshotPageState(): StatsPageState {
  const [startDate, endDate] = selectedRange.value
  return {
    rangeMode: rangeMode.value,
    range: rangeMode.value !== 'auto' ? { startDate, endDate } : undefined,
    selectedSystemAccountId: selectedSystemAccountId.value,
    selectedSystemAccount: selectedSystemAccount.value
  }
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  if (!dateRangeExplicit.value) return {}
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

function resolvedOverviewRangeParams(): { startDate?: string; endDate?: string } {
  const range = usageOverview.value?.range
  return range ? { startDate: range.startDate, endDate: range.endDate } : selectedRangeParams()
}

async function handleQuickRangeChange(value: string | number) {
  await loadUsageStatsWindow({ force: true, viewScope: isManagementView.value ? 'admin' : 'self' })
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

function quickRangeModeForSelection(value: RangeMode): QuickRange | undefined {
  if (value === 'auto') return 'recent1m'
  return isQuickRangeMode(value) ? value : undefined
}

function syncDynamicDateRangeToStatsWindow(): void {
  if (!isQuickRangeMode(rangeMode.value)) return
  const range = quickRangeDateRange(rangeMode.value)
  if (!range) return
  dateRange.value = [range[0].startOf('day'), range[1].startOf('day')]
}

function parseDateRange(value?: { startDate?: string; endDate?: string }): [Dayjs, Dayjs] {
  return parseDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: MAX_RANGE_DAYS })
}

function syncDateRangeFromResponse(value?: { startDate?: string; endDate?: string }) {
  const start = parseDateKey(value?.startDate)
  const end = parseDateKey(value?.endDate)
  if (!start || !end || start.isAfter(end, 'day')) return
  dateRange.value = [start.startOf('day'), end.startOf('day')]
}

function normalizedDateRange(value: [Dayjs, Dayjs]): [string, string] {
  return normalizeDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: MAX_RANGE_DAYS })
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(selectedSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(() => authState.revision.value, () => {
  if (pageActive.value) void loadData()
  else reloadOnActivate = true
})
watch(pageActive, async (active) => {
  if (!active) return
  if (!wasDeactivated) return
  wasDeactivated = false
  if (reloadOnActivate) {
    reloadOnActivate = false
    await loadData({ forceUsageWindow: isDynamicRangeMode(rangeMode.value) })
  }
  await nextTick()
  setupChartObservers()
})

onBeforeUnmount(() => {
  disposed = true
  invalidateStatsRequests()
  chartObserver?.disconnect()
  chartObserver = undefined
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

.stats-chart-section {
  width: 100%;
}

.chart-panel {
  width: 100%;
  height: 280px;
}

.chart-panel-large {
  height: 340px;
}

.daily-consumption-chart {
  width: 100%;
  height: 180px;
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

  .stats-range-picker,
  .stats-quick-range,
  .stats-system-account-select {
    width: 100%;
    min-width: 0;
  }

  .chart-panel,
  .chart-panel-large {
    height: 280px;
  }
}
</style>
