<template>
  <div class="ai-performance-page">
    <AiPerformanceFilterToolbar
      v-model:added-account-ids="addedAccountIds"
      v-model:date-range="dateRange"
      v-model:selected-system-account="selectedSystemAccount"
      v-model:selected-system-account-id="selectedSystemAccountId"
      :account-filter-items="accountFilterItems"
      :account-picker-hidden-values="accountPickerHiddenValues"
      :accounts="accounts"
      :accounts-loading="accountsLoading"
      :added-account-selections="addedAccountSelections"
      :disabled-date="disabledDate"
      :has-active-account-filter="hasActiveAccountFilter"
      :is-management-view="isManagementView"
      :loading="loading"
      :system-account-options-loading="systemAccountOptionsLoading"
      :system-accounts="systemAccounts"
      @account-dropdown-visible-change="handleAccountDropdownVisibleChange"
      @account-search="handleAccountSearch"
      @added-accounts-change="handleAddedAccountsChange"
      @calendar-change="handleCalendarChange"
      @date-range-change="handleDateRangeChange"
      @date-range-open-change="handleDateRangeOpenChange"
      @refresh="loadPerformance"
      @remove-account="removeAddedAccount"
      @reset="resetFilters"
      @system-account-change="handleSystemAccountChange"
      @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @toggle-account="toggleAccountFilter"
    />

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" compact />

    <a-row :gutter="[16, 16]" class="ai-performance-section">
      <a-col v-for="chart in performanceCharts" :key="chart.key" :xs="24" :lg="12">
        <StatsChartCard :title="`${chart.title}（${currentWindowLabel}）`" :loading="initialLoading" :has-data="chart.hasData" :empty-description="chart.emptyDescription">
          <div :ref="chart.setRef" class="chart-panel" />
        </StatsChartCard>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, watch } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountSelection } from '@/shared/accountLabelCache'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys } from '@/shared/dateRange'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { stringOrFallback } from '@/shared/pageStateSanitizers'
import type { AiPerformanceOverview } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDuration, formatInteger } from '@/views/stats/statsFormatters'
import { buildAiPerformanceOption, type AiPerformanceMetric } from './aiPerformanceChartOptions'
import AiPerformanceFilterToolbar from './AiPerformanceFilterToolbar.vue'
import { useAiPerformanceAccountSelection } from './useAiPerformanceAccountSelection'

const MAX_RANGE_DAYS = 31
const DEFAULT_RANGE_DAYS = 3
const defaultDateRange = (): [Dayjs, Dayjs] => {
  const today = dayjs().startOf('day')
  return [today.subtract(DEFAULT_RANGE_DAYS - 1, 'day'), today]
}

interface AiPerformancePageState {
  activeAccountIds: string[]
  addedAccountIds: string[]
  addedAccountSelections: AccountSelection[]
  dateRange: [string, string]
  selectedSystemAccount?: PrincipalSelection
  selectedSystemAccountId: string
}

const pageStateCache = usePageStateCache<AiPerformancePageState>(undefined, defaultAiPerformancePageState, {
  sanitize: sanitizeAiPerformancePageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange({
  startDate: initialPageState.dateRange[0],
  endDate: initialPageState.dateRange[1]
}))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const overview = ref<AiPerformanceOverview>()
const selectedSystemAccountId = ref(initialPageState.selectedSystemAccountId)
const selectedSystemAccount = ref<PrincipalSelection | undefined>(initialPageState.selectedSystemAccount)
const loading = ref(false)
let performanceRequestSeq = 0
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

const averageFirstTokenChartRef = ref<HTMLDivElement>()
const maxFirstTokenChartRef = ref<HTMLDivElement>()
const averageDurationChartRef = ref<HTMLDivElement>()
const maxDurationChartRef = ref<HTMLDivElement>()
const averageFirstTokenChart = shallowRef<ECharts>()
const maxFirstTokenChart = shallowRef<ECharts>()
const averageDurationChart = shallowRef<ECharts>()
const maxDurationChart = shallowRef<ECharts>()
let clearAccountSearchTimerFromSelection = () => {}

function clearAccountSearchTimer() {
  clearAccountSearchTimerFromSelection()
}

const { pageActive, requestRender: renderCharts } = useEchartsPageLifecycle({
  renderCharts: renderPerformanceCharts,
  resizeCharts,
  disposeCharts,
  onMounted: () => {
    void loadPerformance()
  },
  onDeactivate: clearAccountSearchTimer,
  onBeforeUnmount: clearAccountSearchTimer
})

const accountSelection = useAiPerformanceAccountSelection({
  isManagementView,
  isPageActive: () => pageActive.value,
  overview,
  reloadPerformance: () => {
    void loadPerformance()
  },
  requestRender: renderCharts,
  selectedSystemAccountId: selectedPerformanceSystemAccountId
})
clearAccountSearchTimerFromSelection = accountSelection.clearAccountSearchTimer

const {
  accounts,
  accountsLoading,
  accountFilterItems,
  accountPickerHiddenValues,
  activeAccountIds,
  addedAccountIds,
  addedAccountSelections,
  clearAccountState,
  handleAccountDropdownVisibleChange,
  handleAccountSearch,
  handleAddedAccountsChange,
  hasActiveAccountFilter,
  pruneAccountState,
  removeAddedAccount,
  seriesColorByAccountId,
  toggleAccountFilter,
  visibleAccounts,
  visibleHourlySeries,
  visibleOverview
} = accountSelection
activeAccountIds.value = [...initialPageState.activeAccountIds]
addedAccountIds.value = [...initialPageState.addedAccountIds]
addedAccountSelections.value = [...initialPageState.addedAccountSelections]

const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const currentWindowLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const hasAccounts = computed(() => visibleAccounts.value.length > 0)
const hasAverageFirstTokenData = computed(() => hasMetricData('averageFirstTokenMs'))
const hasMaxFirstTokenData = computed(() => hasMetricData('maxFirstTokenMs'))
const hasAverageDurationData = computed(() => hasMetricData('averageDurationMs'))
const hasMaxDurationData = computed(() => hasMetricData('maxDurationMs'))
const firstTokenEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无首 token 数据` : '最近 7 天暂无活跃 AI 账户')
const durationEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无总耗时数据` : '最近 7 天暂无活跃 AI 账户')

const performanceCharts = computed(() => [
  {
    key: 'averageFirstToken',
    title: '平均首token耗时监控图',
    metric: 'averageFirstToken' as AiPerformanceMetric,
    chartRef: averageFirstTokenChart,
    hasData: hasAverageFirstTokenData.value,
    emptyDescription: firstTokenEmptyDescription.value,
    setRef: setAverageFirstTokenChartRef
  },
  {
    key: 'maxFirstToken',
    title: '最大首token耗时监控图',
    metric: 'maxFirstToken' as AiPerformanceMetric,
    chartRef: maxFirstTokenChart,
    hasData: hasMaxFirstTokenData.value,
    emptyDescription: firstTokenEmptyDescription.value,
    setRef: setMaxFirstTokenChartRef
  },
  {
    key: 'averageDuration',
    title: '平均总耗时监控图',
    metric: 'averageDuration' as AiPerformanceMetric,
    chartRef: averageDurationChart,
    hasData: hasAverageDurationData.value,
    emptyDescription: durationEmptyDescription.value,
    setRef: setAverageDurationChartRef
  },
  {
    key: 'maxDuration',
    title: '最大总耗时监控图',
    metric: 'maxDuration' as AiPerformanceMetric,
    chartRef: maxDurationChart,
    hasData: hasMaxDurationData.value,
    emptyDescription: durationEmptyDescription.value,
    setRef: setMaxDurationChartRef
  }
])

const summaryCards = computed(() => {
  const summary = overview.value?.summary
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `监控账户 ${formatInteger(visibleAccounts.value.length)} / ${currentWindowLabel.value}` },
    { key: 'firstToken', label: '平均首 token', value: formatDuration(summary?.averageFirstTokenMs), extra: `最大首 token ${formatDuration(summary?.maxFirstTokenMs)}` },
    { key: 'maxFirstToken', label: '最大首 token', value: formatDuration(summary?.maxFirstTokenMs), extra: `平均首 token ${formatDuration(summary?.averageFirstTokenMs)}` },
    { key: 'duration', label: '平均总耗时', value: formatDuration(summary?.averageDurationMs), extra: `最大总耗时 ${formatDuration(summary?.maxDurationMs)}` },
    { key: 'maxDuration', label: '最大总耗时', value: formatDuration(summary?.maxDurationMs), extra: `平均总耗时 ${formatDuration(summary?.averageDurationMs)}` }
  ]
})

async function loadPerformance() {
  const requestSeq = ++performanceRequestSeq
  loading.value = true
  try {
    if (requestSeq !== performanceRequestSeq) return
    const systemAccountId = selectedPerformanceSystemAccountId()
    const rangeParams = selectedRangeParams()
    const performanceParams = {
      ...rangeParams,
      systemAccountId,
      accountIds: addedAccountIds.value
    }
    const [performanceOverview] = await Promise.all([
      isManagementView.value ? api.stats.aiPerformance(performanceParams) : api.myStats.aiPerformance(performanceParams),
      loadUsageStatsWindow()
    ])
    if (requestSeq !== performanceRequestSeq) return
    overview.value = performanceOverview
    syncDateRangeFromResponse(performanceOverview.range)
    pruneAccountState()
  } catch (error) {
    if (requestSeq !== performanceRequestSeq) return
    console.error(error)
    message.error(extractApiErrorMessage(error, 'AI 性能监控数据加载失败'))
  } finally {
    if (requestSeq === performanceRequestSeq) {
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
  void loadPerformance()
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  const [startDate, endDate] = selectedRange.value
  return { startDate, endDate }
}

function selectedPerformanceSystemAccountId(): string | undefined {
  return isManagementView.value ? scopedSystemAccountId(selectedSystemAccountId.value) : undefined
}

function handleSystemAccountChange() {
  if (selectedSystemAccountId.value === allSystemAccountsValue) {
    selectedSystemAccount.value = undefined
  }
  clearAccountState()
  void loadPerformance()
}

function handleCalendarChange(value: Array<Dayjs | null> | null) {
  calendarRange.value = [value?.[0] ?? null, value?.[1] ?? null]
}

function handleDateRangeOpenChange(open: boolean) {
  if (!open) {
    calendarRange.value = [null, null]
  }
}

function resetFilters() {
  dateRange.value = parseDateRange()
  calendarRange.value = [null, null]
  selectedSystemAccountId.value = allSystemAccountsValue
  selectedSystemAccount.value = undefined
  resetSystemAccountOptionsSearch()
  clearAccountState()
  pageStateCache.clear()
  void loadPerformance()
}

function defaultAiPerformancePageState(): AiPerformancePageState {
  const range = defaultDateRange()
  return {
    activeAccountIds: [],
    addedAccountIds: [],
    addedAccountSelections: [],
    dateRange: [formatDateKey(range[0]), formatDateKey(range[1])],
    selectedSystemAccount: undefined,
    selectedSystemAccountId: allSystemAccountsValue
  }
}

function sanitizeAiPerformancePageState(value: unknown, fallback: AiPerformancePageState): AiPerformancePageState {
  const source = value && typeof value === 'object' ? value as Partial<AiPerformancePageState> : {}
  return {
    activeAccountIds: sanitizeStringArray(source.activeAccountIds),
    addedAccountIds: sanitizeStringArray(source.addedAccountIds),
    addedAccountSelections: Array.isArray(source.addedAccountSelections)
      ? source.addedAccountSelections.map(sanitizeAccountSelection).filter((selection): selection is AccountSelection => Boolean(selection))
      : [],
    dateRange: sanitizeDateRange(source.dateRange) ?? fallback.dateRange,
    selectedSystemAccount: sanitizeSystemAccountSelection(source.selectedSystemAccount),
    selectedSystemAccountId: stringOrFallback(source.selectedSystemAccountId, fallback.selectedSystemAccountId) || fallback.selectedSystemAccountId
  }
}

function sanitizeStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string' && Boolean(item.trim())).map((item) => item.trim())
    : []
}

function sanitizeDateRange(value: unknown): [string, string] | undefined {
  if (!Array.isArray(value) || value.length !== 2) return undefined
  const [startDate, endDate] = value
  if (typeof startDate !== 'string' || typeof endDate !== 'string') return undefined
  const parsed = parseDateRange({ startDate, endDate })
  return [formatDateKey(parsed[0]), formatDateKey(parsed[1])]
}

function sanitizeSystemAccountSelection(value: unknown): PrincipalSelection | undefined {
  if (!value || typeof value !== 'object') return undefined
  const selection = value as Partial<PrincipalSelection>
  const id = stringOrFallback(selection.id).trim()
  const name = stringOrFallback(selection.name).trim()
  if (!id || !name || selection.kind !== 'system_account') return undefined
  return { id, name, kind: 'system_account' }
}

function sanitizeAccountSelection(value: unknown): AccountSelection | undefined {
  if (!value || typeof value !== 'object') return undefined
  const selection = value as Partial<AccountSelection>
  const id = stringOrFallback(selection.id).trim()
  const name = stringOrFallback(selection.name).trim()
  if (!id || !name) return undefined
  const accessType = selection.accessType === 'owner' || selection.accessType === 'authorized' ? selection.accessType : undefined
  const ownerSystemAccountName = stringOrFallback(selection.ownerSystemAccountName).trim() || undefined
  return ownerSystemAccountName
    ? { id, name, accessType, ownerSystemAccountName }
    : { id, name, accessType }
}

function snapshotPageState(): AiPerformancePageState {
  return {
    activeAccountIds: [...activeAccountIds.value],
    addedAccountIds: [...addedAccountIds.value],
    addedAccountSelections: [...addedAccountSelections.value],
    dateRange: [...displayRange.value],
    selectedSystemAccount: selectedSystemAccount.value,
    selectedSystemAccountId: selectedSystemAccountId.value
  }
}

async function renderPerformanceCharts() {
  await Promise.all(performanceCharts.value.map((chart) => renderPerformanceChart(chart.metric, chart.chartRef, chart.hasData)))
}

async function renderPerformanceChart(metric: AiPerformanceMetric, chartRef: ShallowRef<ECharts | undefined>, hasData: boolean) {
  if (!visibleOverview.value || !hasData) {
    disposeChart(chartRef)
    return
  }
  const chart = await ensureChart(metricElementRef(metric), chartRef, () => pageActive.value)
  if (!chart || !visibleOverview.value || !pageActive.value) return
  chart.setOption(buildAiPerformanceOption(visibleOverview.value, metric, { colorByAccountId: seriesColorByAccountId.value }), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts(performanceCharts.value.map((item) => item.chartRef.value))
}

function disposeCharts() {
  for (const chart of performanceCharts.value) {
    disposeChart(chart.chartRef)
  }
}

function hasMetricData(metricKey: 'averageFirstTokenMs' | 'maxFirstTokenMs' | 'averageDurationMs' | 'maxDurationMs') {
  return visibleHourlySeries.value.some((series) => series.points.some((point) => point[metricKey] !== undefined))
}

function setAverageFirstTokenChartRef(element: unknown) {
  averageFirstTokenChartRef.value = element instanceof HTMLDivElement ? element : undefined
}

function setMaxFirstTokenChartRef(element: unknown) {
  maxFirstTokenChartRef.value = element instanceof HTMLDivElement ? element : undefined
}

function setAverageDurationChartRef(element: unknown) {
  averageDurationChartRef.value = element instanceof HTMLDivElement ? element : undefined
}

function setMaxDurationChartRef(element: unknown) {
  maxDurationChartRef.value = element instanceof HTMLDivElement ? element : undefined
}

function metricElementRef(metric: AiPerformanceMetric): Ref<HTMLDivElement | undefined> {
  switch (metric) {
    case 'averageFirstToken':
      return averageFirstTokenChartRef
    case 'maxFirstToken':
      return maxFirstTokenChartRef
    case 'averageDuration':
      return averageDurationChartRef
    case 'maxDuration':
      return maxDurationChartRef
  }
}

function disabledDate(current: Dayjs) {
  return isRecentWindowDateDisabled(current, calendarRange.value, usageStatsWindowMaxDays.value, usageStatsWindowEndDate.value)
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

watch(selectedSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
</script>

<style scoped>
.ai-performance-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.ai-performance-section {
  margin-top: 0;
}

.chart-panel {
  width: 100%;
  height: 320px;
}

@media (max-width: 768px) {
  .chart-panel {
    height: 300px;
  }
}
</style>
