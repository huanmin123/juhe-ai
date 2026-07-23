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
      :loading="contextLoading || baseLoading"
      :system-account-options-loading="systemAccountOptionsLoading"
      :system-accounts="systemAccounts"
      @account-dropdown-visible-change="handleAccountDropdownVisibleChange"
      @account-search="handleAccountSearch"
      @added-accounts-change="handleAddedAccountsChange"
      @calendar-change="handleCalendarChange"
      @date-range-change="handleDateRangeChange"
      @date-range-open-change="handleDateRangeOpenChange"
      @refresh="refreshPerformance"
      @remove-account="removeAddedAccount"
      @reset="resetFilters"
      @system-account-change="handleSystemAccountChange"
      @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @toggle-account="toggleAccountFilter"
    />

    <a-alert v-if="baseError" :message="baseError" type="error" show-icon>
      <template #action>
        <a-button size="small" @click="retryBase">重试基础数据</a-button>
      </template>
    </a-alert>
    <a-alert v-if="seriesError" :message="seriesError" type="warning" show-icon>
      <template #action>
        <a-button size="small" @click="retrySeries">重试追加账户</a-button>
      </template>
    </a-alert>
    <a-alert v-else-if="seriesLoading" message="正在加载追加账户性能序列" type="info" show-icon />

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
import { computed, onActivated, ref, shallowRef, watch } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { didUsageStatsWindowLoadFail, useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountSelection } from '@/shared/accountLabelCache'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys } from '@/shared/dateRange'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { stringOrFallback } from '@/shared/pageStateSanitizers'
import type { AiPerformanceBaseResult, AiPerformanceSeriesResult } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDuration, formatInteger } from '@/views/stats/statsFormatters'
import { buildAiPerformanceOption, type AiPerformanceMetric } from './aiPerformanceChartOptions'
import AiPerformanceFilterToolbar from './AiPerformanceFilterToolbar.vue'
import { buildAiPerformanceRequestSignature, createAiPerformanceRequestGate } from './aiPerformanceRequestGate'
import { useAiPerformanceAccountSelection } from './useAiPerformanceAccountSelection'

const MAX_RANGE_DAYS = 31
const DEFAULT_RANGE_DAYS = 3
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()
const defaultDateRange = (): [Dayjs, Dayjs] => {
  const today = (usageStatsWindowEndDate.value?.isValid() ? usageStatsWindowEndDate.value : dayjs()).startOf('day')
  return [today.subtract(DEFAULT_RANGE_DAYS - 1, 'day'), today]
}

interface AiPerformancePageState {
  activeAccountIds: string[]
  addedAccountIds: string[]
  addedAccountSelections: AccountSelection[]
  dateRange?: [string, string]
  selectedSystemAccount?: PrincipalSelection
  selectedSystemAccountId: string
}

const pageStateCache = usePageStateCache<AiPerformancePageState>(undefined, defaultAiPerformancePageState, {
  sanitize: sanitizeAiPerformancePageState,
  version: 2
})
const initialPageState = pageStateCache.read()
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.dateRange
  ? {
      startDate: initialPageState.dateRange[0],
      endDate: initialPageState.dateRange[1]
    }
  : undefined))
const dateRangeExplicit = ref(Boolean(initialPageState.dateRange))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const overview = ref<AiPerformanceBaseResult>()
const additionalSeries = ref<AiPerformanceSeriesResult>()
const selectedSystemAccountId = ref(initialPageState.selectedSystemAccountId)
const selectedSystemAccount = ref<PrincipalSelection | undefined>(initialPageState.selectedSystemAccount)
const baseLoading = ref(false)
const seriesLoading = ref(false)
const contextLoading = ref(false)
const baseError = ref('')
const seriesError = ref('')
const resolvedSeriesAccountIds = new Set<string>()
const performanceRequestGate = createAiPerformanceRequestGate()
let contextRequestSeq = 0
let reloadAfterActivate = false
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
    void loadPerformanceContext()
  },
  onDeactivate: () => {
    clearAccountSearchTimer()
    deactivatePerformanceRequests(true)
  },
  onBeforeUnmount: () => {
    clearAccountSearchTimer()
    deactivatePerformanceRequests(false)
  }
})

const accountSelection = useAiPerformanceAccountSelection({
  isManagementView,
  isPageActive: () => pageActive.value,
  base: overview,
  series: additionalSeries,
  loadMissingSeries: (accountIds) => {
    void loadAdditionalSeries(accountIds)
  },
  clearSeries: clearAdditionalSeries,
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
  invalidateAccountOptions,
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
const initialLoading = computed(() => (contextLoading.value || baseLoading.value) && !hasOverview.value)
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

async function loadPerformanceContext(options: { force?: boolean } = {}) {
  const contextSeq = ++contextRequestSeq
  invalidatePerformanceRequests()
  contextLoading.value = true
  try {
    const windowScope = isManagementView.value ? 'admin' : 'self'
    await loadUsageStatsWindow({
      force: options.force === true,
      viewScope: windowScope
    })
    if (didUsageStatsWindowLoadFail(windowScope)) throw new Error('统计窗口加载失败')
    if (contextSeq !== contextRequestSeq || !pageActive.value) return
    await Promise.allSettled([
      loadPerformanceBase(),
      loadAdditionalSeries(addedAccountIds.value)
    ])
  } catch (error) {
    if (contextSeq !== contextRequestSeq || !pageActive.value) return
    console.error(error)
    baseError.value = extractApiErrorMessage(error, '统计窗口加载失败')
    seriesError.value = baseError.value
  } finally {
    if (contextSeq === contextRequestSeq) contextLoading.value = false
  }
}

function refreshPerformance() {
  void loadPerformanceContext({ force: true })
}

async function loadPerformanceBase() {
  const request = currentPerformanceRequest('base')
  const token = performanceRequestGate.begin('base', request.signature)
  baseLoading.value = true
  baseError.value = ''
  try {
    const result = isManagementView.value
      ? await api.stats.aiPerformance(request.params)
      : await api.myStats.aiPerformance(request.params)
    if (!performanceRequestGate.acceptsRange(token, currentPerformanceRequest('base').signature, result.range, request.params)) return
    overview.value = result
    syncDateRangeFromResponse(result.range)
    pruneAccountState()
    renderCharts()
  } catch (error) {
    if (!performanceRequestGate.isCurrent(token, currentPerformanceRequest('base').signature)) return
    console.error(error)
    baseError.value = extractApiErrorMessage(error, 'AI 性能基础数据加载失败')
  } finally {
    if (performanceRequestGate.isCurrent(token, currentPerformanceRequest('base').signature)) {
      baseLoading.value = false
      renderCharts()
    }
  }
}

async function loadAdditionalSeries(candidateIds: string[]) {
  const requestedIds = missingSeriesAccountIds(candidateIds)
  if (!requestedIds.length) return
  const request = currentPerformanceRequest('series', requestedIds)
  const token = performanceRequestGate.begin('series', request.signature)
  seriesLoading.value = true
  seriesError.value = ''
  try {
    const params = { ...request.params, accountIds: requestedIds }
    const result = isManagementView.value
      ? await api.stats.aiPerformanceSeries(params)
      : await api.myStats.aiPerformanceSeries(params)
    if (!performanceRequestGate.acceptsRange(token, currentPerformanceRequest('series', requestedIds).signature, result.range, request.params)) return
    mergeAdditionalSeries(result, requestedIds)
    renderCharts()
  } catch (error) {
    if (!performanceRequestGate.isCurrent(token, currentPerformanceRequest('series', requestedIds).signature)) return
    console.error(error)
    seriesError.value = extractApiErrorMessage(error, '追加账户性能序列加载失败')
  } finally {
    if (performanceRequestGate.isCurrent(token, currentPerformanceRequest('series', requestedIds).signature)) {
      seriesLoading.value = false
      renderCharts()
    }
  }
}

function currentPerformanceRequest(channel: 'base' | 'series', accountIds: string[] = []) {
  const user = authState.currentUser.value
  const params = {
    ...selectedRangeParams(),
    systemAccountId: selectedPerformanceSystemAccountId()
  }
  return {
    params,
    signature: buildAiPerformanceRequestSignature({
      channel,
      scope: isManagementView.value ? 'admin' : 'self',
      authRevision: authState.revision.value,
      viewerId: user?.id,
      viewerRole: user?.role,
      ownerSystemAccountId: params.systemAccountId,
      startDate: params.startDate,
      endDate: params.endDate,
      accountIds
    })
  }
}

function mergeAdditionalSeries(result: AiPerformanceSeriesResult, requestedIds: string[]) {
  const desiredIds = new Set(addedAccountIds.value)
  const previousAccounts = additionalSeries.value?.accounts ?? []
  const previousSeries = additionalSeries.value?.hourlySeries ?? []
  const accounts = dedupeById([...previousAccounts, ...result.accounts], (item) => item.id)
    .filter((account) => desiredIds.has(account.id))
  const hourlySeries = dedupeById([...previousSeries, ...result.hourlySeries], (item) => item.accountId)
    .filter((series) => desiredIds.has(series.accountId))
  additionalSeries.value = { range: result.range, accounts, hourlySeries }
  for (const id of requestedIds) {
    if (desiredIds.has(id)) resolvedSeriesAccountIds.add(id)
  }
}

function clearAdditionalSeries() {
  additionalSeries.value = undefined
  resolvedSeriesAccountIds.clear()
  seriesError.value = ''
  seriesLoading.value = false
  performanceRequestGate.invalidate('series')
}

function missingSeriesAccountIds(accountIds: string[]) {
  const defaultIds = new Set(overview.value?.accounts.map((account) => account.id) ?? [])
  return [...new Set(accountIds)]
    .filter((id) => id && !defaultIds.has(id) && !resolvedSeriesAccountIds.has(id))
    .slice(0, 20)
}

function retryBase() {
  void loadPerformanceBase()
}

function retrySeries() {
  resolvedSeriesAccountIds.clear()
  void loadAdditionalSeries(addedAccountIds.value)
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  dateRangeExplicit.value = true
  void loadPerformanceContext()
}

function selectedRangeParams(): { startDate?: string; endDate?: string } {
  if (!dateRangeExplicit.value) {
    const [startDate, endDate] = defaultDateRange()
    return {
      startDate: formatDateKey(startDate),
      endDate: formatDateKey(endDate)
    }
  }
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
  invalidateAccountOptions()
  clearAdditionalSeries()
  void loadPerformanceContext()
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
  dateRangeExplicit.value = false
  calendarRange.value = [null, null]
  selectedSystemAccountId.value = allSystemAccountsValue
  selectedSystemAccount.value = undefined
  resetSystemAccountOptionsSearch()
  clearAccountState()
  pageStateCache.clear()
  void loadPerformanceContext()
}

function defaultAiPerformancePageState(): AiPerformancePageState {
  return {
    activeAccountIds: [],
    addedAccountIds: [],
    addedAccountSelections: [],
    dateRange: undefined,
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
    dateRange: sanitizeDateRange(source.dateRange),
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
    dateRange: dateRangeExplicit.value ? [displayRange.value[0], displayRange.value[1]] : undefined,
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

function invalidatePerformanceRequests() {
  performanceRequestGate.invalidate()
  overview.value = undefined
  additionalSeries.value = undefined
  resolvedSeriesAccountIds.clear()
  baseLoading.value = false
  seriesLoading.value = false
  baseError.value = ''
  seriesError.value = ''
  renderCharts()
}

function deactivatePerformanceRequests(shouldReloadOnActivate: boolean) {
  const hadInflightRequest = contextLoading.value || baseLoading.value || seriesLoading.value
  contextRequestSeq += 1
  reloadAfterActivate = shouldReloadOnActivate && hadInflightRequest
  performanceRequestGate.deactivate()
  contextLoading.value = false
  baseLoading.value = false
  seriesLoading.value = false
}

function dedupeById<T>(items: T[], idFor: (item: T) => string): T[] {
  const byId = new Map<string, T>()
  for (const item of items) byId.set(idFor(item), item)
  return [...byId.values()]
}

onActivated(() => {
  performanceRequestGate.activate()
  if (!reloadAfterActivate) return
  reloadAfterActivate = false
  void loadPerformanceContext()
})

watch(selectedSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => authState.revision.value, () => {
  invalidateAccountOptions()
  if (!pageActive.value) {
    reloadAfterActivate = true
    return
  }
  void loadPerformanceContext()
})
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
