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
          <a-button :loading="loading" @click="loadData({ force: true })">
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
      <a-col :xs="24" :xl="10">
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
      <a-col :xs="24">
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
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, watch } from 'vue'
import { message } from '@/lib/antd'
import { ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'

import { api } from '@/api/client'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys, todayDateRange } from '@/shared/dateRange'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { UsageStatsOverview } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from './StatsChartCard.vue'
import StatsSummaryCards from './StatsSummaryCards.vue'
import { buildErrorOption, buildModelDistributionOption, buildUsageTrendOption } from './statsChartOptions'
import { formatCompactInteger, formatCost, formatDurationSeconds, formatInteger, formatPercent } from './statsFormatters'
import { loadStatsPageDataResource } from './statsPageDataResource'

const MAX_RANGE_DAYS = 31
type QuickRange = 'today' | 'recent7d' | 'recent1m'
const quickRangeOptions: Array<{ label: string; value: QuickRange }> = [
  { label: '今天', value: 'today' },
  { label: '近7天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]

type StatsPageState = {
  range?: {
    startDate: string
    endDate: string
  }
  selectedSystemAccountId: string
  selectedSystemAccount?: PrincipalSelection
}

const defaultDateRange = todayDateRange
const defaultStatsPageState = (): StatsPageState => ({
  selectedSystemAccountId: allSystemAccountsValue,
  selectedSystemAccount: undefined
})
const pageStateCache = usePageStateCache<StatsPageState>(undefined, defaultStatsPageState, { version: 5 })
const initialPageState = pageStateCache.read()

const loading = ref(false)
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const dateRangeExplicit = ref(Boolean(initialPageState.range?.startDate || initialPageState.range?.endDate))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const selectedSystemAccountId = ref(initialPageState.selectedSystemAccountId || allSystemAccountsValue)
const selectedSystemAccount = ref<PrincipalSelection | undefined>(initialPageState.selectedSystemAccount)
const usageOverview = ref<UsageStatsOverview>()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()
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

const usageTrendChartRef = ref<HTMLDivElement>()
const modelDistributionChartRef = ref<HTMLDivElement>()
const errorChartRef = ref<HTMLDivElement>()
const usageTrendChart = shallowRef<ECharts>()
const modelDistributionChart = shallowRef<ECharts>()
const errorChart = shallowRef<ECharts>()
let statsRequestSeq = 0

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderStatsCharts,
  resizeCharts,
  disposeCharts,
  onMounted: loadData
})

const hasUsageTrend = computed(() => (usageOverview.value?.hourlyTrend.length ?? 0) > 0)
const hasModelDistribution = computed(() => (usageOverview.value?.modelDistribution.length ?? 0) > 0)
const hasErrors = computed(() => (usageOverview.value?.errors.length ?? 0) > 0)
const hasUsageOverview = computed(() => Boolean(usageOverview.value))
const initialLoading = computed(() => loading.value && !hasUsageOverview.value)
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
const hasWindowUsage = computed(() => (usageOverview.value?.summary.requestCount ?? 0) > 0)
const usageTrendEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无趋势数据，窗口指标已在上方展示` : `${currentWindowLabel.value}暂无趋势数据`)
const modelDistributionEmptyDescription = computed(() => `${currentWindowLabel.value}暂无模型调用`)
const errorEmptyDescription = computed(() => hasWindowUsage.value ? `${currentWindowLabel.value}暂无失败请求` : `${currentWindowLabel.value}暂无失败请求`)
const usageTrendDescription = computed(() => '请求和失败按次数统计；Token 为输入 + 输出；平均总耗时取网关均值。')
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

async function loadData(options: { force?: boolean } = {}) {
  const requestSeq = ++statsRequestSeq
  loading.value = true
  try {
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
    const rangeParams = selectedRangeParams()
    await Promise.all([
      loadStatsPageDataResource<UsageStatsOverview>({
        apply: (nextOverview) => {
          usageOverview.value = nextOverview
          syncDateRangeFromResponse(nextOverview.range)
          renderCharts()
        },
        domain: 'stats.overview',
        force: options.force,
        isCurrent: () => requestSeq === statsRequestSeq,
        isManagementView: isManagementView.value,
        loadNetwork: () => isManagementView.value
          ? api.stats.usageOverview({ ...rangeParams, systemAccountId })
          : api.myStats.usageOverview(rangeParams),
        query: { ...rangeParams, systemAccountId },
        route: isManagementView.value ? '/stats/usage-overview' : '/my-stats/usage-overview',
        targetSystemAccountId: systemAccountId
      }),
      loadUsageStatsWindow({ force: true })
    ])
    if (requestSeq !== statsRequestSeq) return
  } catch (error) {
    if (requestSeq !== statsRequestSeq) return
    console.error(error)
    message.error('统计数据加载失败')
  } finally {
    if (requestSeq === statsRequestSeq) {
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
  dateRangeExplicit.value = false
  calendarRange.value = [null, null]
  selectedSystemAccountId.value = defaults.selectedSystemAccountId
  selectedSystemAccount.value = defaults.selectedSystemAccount
  resetSystemAccountOptionsSearch()
  pageStateCache.clear()
  void loadData()
}

async function renderStatsCharts() {
  await Promise.all([
    renderUsageTrendChart(),
    renderModelDistributionChart(),
    renderErrorChart()
  ])
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
  resizeEcharts([usageTrendChart.value, modelDistributionChart.value, errorChart.value])
}

function disposeCharts() {
  disposeChart(usageTrendChart)
  disposeChart(modelDistributionChart)
  disposeChart(errorChart)
}

function snapshotPageState(): StatsPageState {
  const [startDate, endDate] = selectedRange.value
  return {
    range: dateRangeExplicit.value ? { startDate, endDate } : undefined,
    selectedSystemAccountId: selectedSystemAccountId.value,
    selectedSystemAccount: selectedSystemAccount.value
  }
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  if (!dateRangeExplicit.value) return {}
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

async function handleQuickRangeChange(value: string | number) {
  await loadUsageStatsWindow({ force: true })
  const range = quickRangeDateRange(value as QuickRange)
  if (!range) return
  dateRange.value = parseDateRange({
    startDate: formatDateKey(range[0]),
    endDate: formatDateKey(range[1])
  })
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
