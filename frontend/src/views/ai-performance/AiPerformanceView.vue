<template>
  <div class="ai-performance-page">
    <a-card class="page-card ai-performance-header-card">
      <div class="page-toolbar ai-performance-toolbar">
        <div class="ai-performance-filters">
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
            class="ai-performance-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="ai-performance-range-picker"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
          <AccountAppendSelect
            v-model:value="addedAccountIds"
            :accounts="accounts"
            :selected-accounts="addedAccountSelections"
            class="ai-performance-account-select"
            :hidden-account-ids="accountPickerHiddenValues"
            :loading="accountsLoading"
            :disabled="loading"
            :max="20"
            max-tag-count="responsive"
            placeholder="输入账户名称添加账户"
            @change="handleAddedAccountsChange"
            @search="handleAccountSearch"
            @dropdown-visible-change="handleAccountDropdownVisibleChange"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button :loading="loading" @click="loadPerformance">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
      <div v-if="accountFilterItems.length" class="ai-performance-account-list" aria-label="性能账户筛选">
        <span
          v-for="item in accountFilterItems"
          :key="item.account.id"
          class="ai-performance-account-filter-entry"
          :class="{ active: item.selected, muted: hasActiveAccountFilter && !item.selected }"
        >
          <button
            class="ai-performance-account-filter-item"
            type="button"
            :aria-pressed="item.selected"
            @click="toggleAccountFilter(item.account.id)"
          >
            <span class="ai-performance-legend-dot" :style="{ backgroundColor: item.color }" />
            <span class="ai-performance-legend-name">{{ item.label }}</span>
          </button>
          <a-tooltip v-if="item.removable" title="移除">
            <button
              class="ai-performance-account-filter-remove"
              type="button"
              :aria-label="`移除${item.label}`"
              @click.stop="removeAddedAccount(item.account.id)"
            >
              <CloseOutlined />
            </button>
          </a-tooltip>
        </span>
      </div>
    </a-card>

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
import { CloseOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import AccountAppendSelect from '@/components/AccountAppendSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys } from '@/shared/dateRange'
import { accountSelectionForId, accountSelectOptionLabel, rememberAccountSelection, rememberAccountSelections, type AccountSelection } from '@/shared/accountLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountStatus, AiPerformanceAccountOption, AiPerformanceOverview } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDuration, formatInteger, formatSeconds } from '@/views/stats/statsFormatters'
import { buildAiPerformanceOption, chartColors, orderedAiPerformanceSeries, type AiPerformanceMetric } from './aiPerformanceChartOptions'

const MAX_RANGE_DAYS = 31
const DEFAULT_RANGE_DAYS = 3
type AiPerformanceAccountRow = AiPerformanceOverview['accounts'][number]
type AiPerformanceSeriesRow = AiPerformanceOverview['hourlySeries'][number]
const defaultDateRange = (): [Dayjs, Dayjs] => {
  const today = dayjs().startOf('day')
  return [today.subtract(DEFAULT_RANGE_DAYS - 1, 'day'), today]
}

const dateRange = ref<[Dayjs, Dayjs]>(defaultDateRange())
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const addedAccountIds = ref<string[]>([])
const addedAccountSelections = ref<AccountSelection[]>([])
const activeAccountIds = ref<string[]>([])
const overview = ref<AiPerformanceOverview>()
const accounts = ref<AiPerformanceAccountOption[]>([])
const selectedSystemAccountId = ref(allSystemAccountsValue)
const selectedSystemAccount = ref<PrincipalSelection | undefined>()
const loading = ref(false)
const accountsLoading = ref(false)
const accountSearchKeyword = ref('')
let accountSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let accountSearchSeq = 0
let loadedAccountOptionsKey: string | undefined
let loadingAccountOptionsKey: string | undefined
let loadingAccountOptionsPromise: Promise<void> | undefined
const accountOptionsCache = createShortLivedQueryCache<AiPerformanceAccountOption[]>({ ttlMs: 10_000 })
let performanceRequestSeq = 0
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
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

const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const currentWindowLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const responseAccounts = computed(() => overview.value?.accounts ?? [])
const responseAccountById = computed(() => new Map(responseAccounts.value.map((account) => [account.id, account])))
const accountOptionById = computed(() => new Map(accounts.value.map((account) => [account.id, account])))
const addedAccountSelectionById = computed(() => new Map(addedAccountSelections.value.map((account) => [account.id, account])))
const defaultAccountIdSet = computed(() => new Set([
  ...(overview.value?.defaultAccounts ?? []).map((account) => account.id),
  ...responseAccounts.value.filter((account) => account.defaultVisible).map((account) => account.id)
]))
const overviewAccounts = computed(() => dedupePerformanceAccounts([
  ...responseAccounts.value,
  ...addedAccountIds.value
    .map((id) => placeholderPerformanceAccount(id))
    .filter((account): account is AiPerformanceAccountRow => Boolean(account))
]))
const overviewHourlySeries = computed(() => {
  const responseSeries = overview.value?.hourlySeries ?? []
  const responseSeriesIds = new Set(responseSeries.map((series) => series.accountId))
  const placeholderSeries = addedAccountIds.value
    .filter((id) => !responseSeriesIds.has(id))
    .map((id) => placeholderPerformanceSeries(id))
    .filter((series): series is AiPerformanceSeriesRow => Boolean(series))
  return [...responseSeries, ...placeholderSeries]
})
const displayOverview = computed<AiPerformanceOverview | undefined>(() => {
  const currentOverview = overview.value
  if (!currentOverview) return undefined
  const accountIds = new Set(overviewAccounts.value.map((account) => account.id))
  return {
    ...currentOverview,
    accounts: overviewAccounts.value,
    defaultAccounts: currentOverview.defaultAccounts.filter((account) => accountIds.has(account.id)),
    selectedAccounts: overviewAccounts.value.filter((account) => account.selected && accountIds.has(account.id)),
    hourlySeries: overviewHourlySeries.value
  }
})
const activeAccountIdSet = computed(() => new Set(activeAccountIds.value))
const hasActiveAccountFilter = computed(() => activeAccountIds.value.length > 0)
const visibleAccounts = computed(() => {
  if (!hasActiveAccountFilter.value) return overviewAccounts.value
  return overviewAccounts.value.filter((account) => activeAccountIdSet.value.has(account.id))
})
const visibleAccountIdSet = computed(() => new Set(visibleAccounts.value.map((account) => account.id)))
const visibleHourlySeries = computed(() => overviewHourlySeries.value.filter((series) => visibleAccountIdSet.value.has(series.accountId)))
const visibleOverview = computed<AiPerformanceOverview | undefined>(() => {
  const currentOverview = displayOverview.value
  if (!currentOverview) return undefined
  const visibleIds = visibleAccountIdSet.value
  return {
    ...currentOverview,
    accounts: visibleAccounts.value,
    defaultAccounts: currentOverview.defaultAccounts.filter((account) => visibleIds.has(account.id)),
    selectedAccounts: currentOverview.selectedAccounts.filter((account) => visibleIds.has(account.id)),
    hourlySeries: visibleHourlySeries.value
  }
})
const hasAccounts = computed(() => visibleAccounts.value.length > 0)
const hasAverageFirstTokenData = computed(() => hasMetricData('averageFirstTokenMs'))
const hasMaxFirstTokenData = computed(() => hasMetricData('maxFirstTokenMs'))
const hasAverageDurationData = computed(() => hasMetricData('averageDurationMs'))
const hasMaxDurationData = computed(() => hasMetricData('maxDurationMs'))
const firstTokenEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无首 token 样本` : '最近 7 天暂无活跃 AI 账户')
const durationEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无总耗时样本` : '最近 7 天暂无活跃 AI 账户')

const accountPickerHiddenValues = computed(() => [
  ...overviewAccounts.value.map((account) => account.id),
  ...addedAccountIds.value
])
const addedAccountIdSet = computed(() => new Set(addedAccountIds.value))

const accountFilterItems = computed(() => {
  const currentOverview = displayOverview.value
  if (!currentOverview) return []
  const nameCounts = currentOverview.accounts.reduce((counts, account) => {
    counts.set(account.name, (counts.get(account.name) ?? 0) + 1)
    return counts
  }, new Map<string, number>())
  const accountById = new Map(currentOverview.accounts.map((account) => [account.id, account]))
  const activeIds = activeAccountIdSet.value
  return orderedAiPerformanceSeries(currentOverview).map((series, index) => {
    const account = accountById.get(series.accountId)
    const label = performanceAccountLabel(account, series, nameCounts)
    return {
      account: account ?? {
        id: series.accountId,
        name: series.accountName,
        status: 'active' as AccountStatus,
        providerCode: series.providerCode,
        systemAccountId: series.systemAccountId,
        requestCountLast7d: 0,
        selected: false,
        defaultVisible: false
      },
      label,
      color: chartColors[index % chartColors.length],
      selected: activeIds.has(series.accountId),
      removable: addedAccountIdSet.value.has(series.accountId) && !defaultAccountIdSet.value.has(series.accountId)
    }
  })
})
const seriesColorByAccountId = computed(() => new Map(accountFilterItems.value.map((item) => [item.account.id, item.color])))

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
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `统计滞后 ${formatSeconds(overview.value?.statsLagSeconds)}` },
    { key: 'firstToken', label: '平均首 token', value: formatDuration(summary?.averageFirstTokenMs), extra: `样本 ${formatInteger(summary?.firstTokenCount)}` },
    { key: 'maxFirstToken', label: '最大首 token', value: formatDuration(summary?.maxFirstTokenMs), extra: `样本 ${formatInteger(summary?.firstTokenCount)}` },
    { key: 'duration', label: '平均总耗时', value: formatDuration(summary?.averageDurationMs), extra: `样本 ${formatInteger(summary?.durationCount)}` },
    { key: 'maxDuration', label: '最大总耗时', value: formatDuration(summary?.maxDurationMs), extra: `样本 ${formatInteger(summary?.durationCount)}` }
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
    const performanceOverview = isManagementView.value
      ? await api.stats.aiPerformance(performanceParams)
      : await api.myStats.aiPerformance(performanceParams)
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

async function loadAccounts() {
  const request = currentAccountOptionsRequest()
  if (loadingAccountOptionsKey === request.key && loadingAccountOptionsPromise) {
    return loadingAccountOptionsPromise
  }
  const requestSeq = ++accountSearchSeq
  if (loadedAccountOptionsKey === request.key) {
    loadingAccountOptionsKey = undefined
    loadingAccountOptionsPromise = undefined
    accountsLoading.value = false
    return
  }
  const cachedAccounts = accountOptionsCache.get(request.key)
  if (cachedAccounts) {
    accounts.value = cachedAccounts
    loadedAccountOptionsKey = request.key
    loadingAccountOptionsKey = undefined
    loadingAccountOptionsPromise = undefined
    accountsLoading.value = false
    return
  }
  accountsLoading.value = true
  loadingAccountOptionsKey = request.key
  const loadingPromise = (async () => {
    try {
      const accountParams = {
        systemAccountId: request.systemAccountId,
        keyword: request.keyword,
        accountIds: request.accountIds,
        limit: 50
      }
      const nextAccounts = isManagementView.value
        ? await api.stats.aiPerformanceAccounts(accountParams)
        : await api.myStats.aiPerformanceAccounts(accountParams)
      accountOptionsCache.set(request.key, nextAccounts)
      if (requestSeq !== accountSearchSeq) return
      accounts.value = nextAccounts
      loadedAccountOptionsKey = request.key
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, 'AI 账户列表加载失败'))
    } finally {
      if (loadingAccountOptionsKey === request.key) {
        loadingAccountOptionsPromise = undefined
        loadingAccountOptionsKey = undefined
      }
      if (requestSeq === accountSearchSeq) {
        accountsLoading.value = false
      }
    }
  })()
  loadingAccountOptionsPromise = loadingPromise
  return loadingPromise
}

function currentAccountOptionsRequest(): { key: string; systemAccountId?: string; keyword: string; accountIds: string[] } {
  const keyword = accountSearchKeyword.value.trim()
  const systemAccountId = selectedPerformanceSystemAccountId()
  const accountIds = [...addedAccountIds.value]
  return {
    key: JSON.stringify([isManagementView.value ? 'management' : 'self', systemAccountId ?? '', keyword, accountIds]),
    systemAccountId,
    keyword,
    accountIds
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

function handleAddedAccountsChange(value: string[], previousValue: string[]) {
  accountSearchKeyword.value = ''
  const previousIds = new Set(previousValue)
  const acceptedIds = value.filter((id) => !defaultAccountIdSet.value.has(id))
  for (const id of acceptedIds) {
    rememberAddedAccountSelection(id)
  }
  addedAccountIds.value = acceptedIds
  syncAddedAccountSelections()
  const newlyAddedIds = acceptedIds.filter((id) => !previousIds.has(id))
  const visibleIds = new Set([...overviewAccounts.value.map((account) => account.id), ...acceptedIds])
  const nextActiveIds = activeAccountIds.value.filter((id) => visibleIds.has(id))
  activeAccountIds.value = hasActiveAccountFilter.value
    ? [...new Set([...nextActiveIds, ...newlyAddedIds])]
    : nextActiveIds
  void loadAccounts()
  if (newlyAddedIds.length || acceptedIds.length !== previousValue.length) {
    void loadPerformance()
  } else {
    renderCharts()
  }
}

function removeAddedAccount(id: string) {
  if (!addedAccountIdSet.value.has(id)) return
  addedAccountIds.value = addedAccountIds.value.filter((accountId) => accountId !== id)
  addedAccountSelections.value = addedAccountSelections.value.filter((selection) => selection.id !== id)
  activeAccountIds.value = activeAccountIds.value.filter((accountId) => accountId !== id)
  void loadAccounts()
  void loadPerformance()
  renderCharts()
}

function handleAccountSearch(value: string) {
  accountSearchKeyword.value = value
  clearAccountSearchTimer()
  accountSearchTimer = window.setTimeout(() => {
    accountSearchTimer = undefined
    if (!pageActive.value) return
    void loadAccounts()
  }, 250)
}

function handleAccountDropdownVisibleChange(open: boolean) {
  if (open) {
    void loadAccounts()
  }
}

function resetFilters() {
  dateRange.value = parseDateRange()
  calendarRange.value = [null, null]
  selectedSystemAccountId.value = allSystemAccountsValue
  selectedSystemAccount.value = undefined
  resetSystemAccountOptionsSearch()
  clearAccountState()
  void loadPerformance()
}

function clearAccountState() {
  clearAccountSearchTimer()
  addedAccountIds.value = []
  addedAccountSelections.value = []
  activeAccountIds.value = []
  accountSearchKeyword.value = ''
}

function toggleAccountFilter(id: string) {
  if (!overviewAccounts.value.some((account) => account.id === id)) return
  activeAccountIds.value = activeAccountIds.value.includes(id)
    ? activeAccountIds.value.filter((accountId) => accountId !== id)
    : [...activeAccountIds.value, id]
  renderCharts()
}

function renderPerformanceCharts() {
  for (const chart of performanceCharts.value) {
    renderPerformanceChart(chart.metric, chart.chartRef, chart.hasData)
  }
}

function renderPerformanceChart(metric: AiPerformanceMetric, chartRef: ShallowRef<ECharts | undefined>, hasData: boolean) {
  if (!visibleOverview.value || !hasData) {
    disposeChart(chartRef)
    return
  }
  const chart = ensureChart(metricElementRef(metric), chartRef)
  if (!chart) return
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

function clearAccountSearchTimer() {
  if (accountSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(accountSearchTimer)
    accountSearchTimer = undefined
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
  return isRecentWindowDateDisabled(current, calendarRange.value, MAX_RANGE_DAYS)
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

function dedupePerformanceAccounts(items: AiPerformanceAccountRow[]): AiPerformanceAccountRow[] {
  const seen = new Set<string>()
  const result: AiPerformanceAccountRow[] = []
  for (const item of items) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    result.push(item)
  }
  return result
}

function placeholderPerformanceAccount(id: string): AiPerformanceAccountRow | undefined {
  const responseAccount = responseAccountById.value.get(id)
  if (responseAccount) return responseAccount
  const option = accountOptionById.value.get(id)
  const selection = addedAccountSelectionById.value.get(id)
  const name = option?.name?.trim() || selection?.name?.trim()
  if (!name || !option?.providerCode) return undefined
  return {
    id,
    name,
    status: option?.status ?? 'active',
    providerCode: option.providerCode,
    systemAccountId: option?.systemAccountId ?? selectedPerformanceSystemAccountId() ?? '',
    systemAccountName: option?.systemAccountName,
    ownerSystemAccountId: option?.ownerSystemAccountId,
    ownerSystemAccountName: option?.ownerSystemAccountName ?? selection?.ownerSystemAccountName,
    accessType: option?.accessType ?? selection?.accessType,
    requestCountLast7d: option?.requestCountLast7d ?? 0,
    selected: true,
    defaultVisible: false
  }
}

function performanceAccountLabel(
  account: AiPerformanceAccountRow | undefined,
  series: AiPerformanceSeriesRow,
  nameCounts: Map<string, number>
): string {
  const accountName = account?.name ?? series.accountName
  if (account?.accessType === 'authorized') {
    return accountSelectOptionLabel(account)
  }
  if (isManagementView.value && account?.systemAccountName) {
    return `${accountName}（${account.systemAccountName}）`
  }
  if ((nameCounts.get(accountName) ?? 0) > 1 && account?.providerCode) {
    return `${accountName}（${providerDisplayName(account.providerCode)}）`
  }
  return accountName
}

function placeholderPerformanceSeries(id: string): AiPerformanceSeriesRow | undefined {
  const account = responseAccountById.value.get(id) ?? placeholderPerformanceAccount(id)
  if (!account) return undefined
  return {
    accountId: id,
    accountName: account.name,
    providerCode: account.providerCode,
    systemAccountId: account.systemAccountId,
    points: []
  }
}

function pruneAccountState() {
  const currentOverview = overview.value
  if (!currentOverview) return
  syncAddedAccountSelections()
  const visibleIds = new Set([...overviewAccounts.value.map((account) => account.id), ...addedAccountIds.value])
  activeAccountIds.value = activeAccountIds.value.filter((id) => visibleIds.has(id))
}

function rememberAddedAccountSelection(id: string) {
  const selection = accountSelectionForId(id, [...accounts.value, ...overviewAccounts.value])
  rememberAccountSelection(selection)
  if (!selection || addedAccountSelections.value.some((item) => item.id === selection.id)) return
  addedAccountSelections.value = [...addedAccountSelections.value, selection]
}

function syncAddedAccountSelections() {
  const existing = new Map(addedAccountSelections.value.map((selection) => [selection.id, selection]))
  addedAccountSelections.value = addedAccountIds.value
    .map((id) => accountSelectionForId(id, [...accounts.value, ...overviewAccounts.value]) ?? existing.get(id))
    .filter((selection): selection is AccountSelection => Boolean(selection))
  rememberAccountSelections(addedAccountSelections.value)
}

watch(selectedSystemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(addedAccountSelections, (selections) => rememberAccountSelections(selections), { deep: true, immediate: true })
</script>

<style scoped>
.ai-performance-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.ai-performance-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.ai-performance-toolbar {
  margin: 0;
}

.ai-performance-filters {
  display: flex;
  flex: 1 1 720px;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.ai-performance-system-account-select {
  width: 240px;
}

.ai-performance-range-picker {
  width: 250px;
}

.ai-performance-account-select {
  flex: 1 1 320px;
  width: auto;
  min-width: 280px;
  max-width: none;
}

.ai-performance-account-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 12px;
}

.ai-performance-account-filter-entry {
  display: inline-flex;
  align-items: center;
  max-width: min(360px, 100%);
  border: 1px solid transparent;
  border-radius: 6px;
  transition: background-color 0.16s ease, border-color 0.16s ease, opacity 0.16s ease;
}

.ai-performance-account-filter-entry:hover,
.ai-performance-account-filter-entry.active {
  border-color: #91caff;
  background: #e6f4ff;
}

.ai-performance-account-filter-entry.muted {
  opacity: 0.46;
}

.ai-performance-account-filter-item {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  gap: 6px;
  padding: 2px 8px;
  border: 0;
  color: #334155;
  background: transparent;
  font-size: 13px;
  line-height: 20px;
  cursor: pointer;
}

.ai-performance-account-filter-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  margin-left: -4px;
  padding: 0;
  border: 0;
  border-radius: 5px;
  color: #64748b;
  background: transparent;
  font-size: 12px;
  cursor: pointer;
  transition: background-color 0.16s ease, color 0.16s ease;
}

.ai-performance-account-filter-remove:hover {
  color: #cf1322;
  background: #fff1f0;
}

.ai-performance-legend-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.ai-performance-legend-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-performance-section {
  margin-top: 0;
}

.chart-panel {
  width: 100%;
  height: 320px;
}

@media (max-width: 768px) {
  .ai-performance-filters {
    flex: 1 1 auto;
  }

  .ai-performance-system-account-select,
  .ai-performance-range-picker,
  .ai-performance-account-select {
    width: 100%;
    min-width: 0;
    max-width: none;
  }

  .chart-panel {
    height: 300px;
  }
}
</style>
