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
            all-label="全部用户"
            class="ai-performance-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountChange"
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
          <a-select
            :value="accountPickerValue"
            class="ai-performance-account-select"
            mode="multiple"
            allow-clear
            show-search
            :max-tag-count="0"
            :options="accountOptions"
            :loading="accountsLoading"
            :disabled="loading"
            :filter-option="false"
            placeholder="搜索并添加账户"
            @select="handleAccountSelect"
            @search="handleAccountSearch"
            @dropdown-visible-change="handleAccountDropdownVisibleChange"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :disabled="(!addedAccountIds.length && !activeAccountIds.length) || loading" @click="resetAccounts">重置</a-button>
          <a-button :loading="loading" @click="loadPerformance">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
      <div v-if="accountFilterItems.length" class="ai-performance-account-list" aria-label="性能账户筛选">
        <button
          v-for="item in accountFilterItems"
          :key="item.account.id"
          class="ai-performance-account-filter-item"
          :class="{ active: item.selected, muted: hasActiveAccountFilter && !item.selected }"
          type="button"
          :aria-pressed="item.selected"
          @click="toggleAccountFilter(item.account.id)"
        >
          <span class="ai-performance-legend-dot" :style="{ backgroundColor: item.color }" />
          <span class="ai-performance-legend-name">{{ item.label }}</span>
        </button>
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
import { computed, ref, shallowRef } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys } from '@/shared/dateRange'
import type { AccountStatus, AiPerformanceAccountOption, AiPerformanceOverview, SystemAccountPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDuration, formatInteger, formatSeconds } from '@/views/stats/statsFormatters'
import { buildAiPerformanceOption, chartColors, orderedAiPerformanceSeries, type AiPerformanceMetric } from './aiPerformanceChartOptions'

const MAX_RANGE_DAYS = 31
const DEFAULT_RANGE_DAYS = 3
const defaultDateRange = (): [Dayjs, Dayjs] => {
  const today = dayjs().startOf('day')
  return [today.subtract(DEFAULT_RANGE_DAYS - 1, 'day'), today]
}

const dateRange = ref<[Dayjs, Dayjs]>(defaultDateRange())
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const addedAccountIds = ref<string[]>([])
const activeAccountIds = ref<string[]>([])
const accountPickerValue = ref<string[]>([])
const overview = ref<AiPerformanceOverview>()
const accounts = ref<AiPerformanceAccountOption[]>([])
const systemAccounts = ref<SystemAccountPrincipalSummary[]>([])
const systemAccountsLoaded = ref(false)
const selectedSystemAccountId = ref(allSystemAccountsValue)
const loading = ref(false)
const accountsLoading = ref(false)
const accountSearchKeyword = ref('')
let accountSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let accountSearchSeq = 0
let performanceRequestSeq = 0
let systemAccountsLoadingPromise: Promise<void> | undefined
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

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
    void loadAccounts()
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
const overviewAccounts = computed(() => overview.value?.accounts ?? [])
const activeAccountIdSet = computed(() => new Set(activeAccountIds.value))
const hasActiveAccountFilter = computed(() => activeAccountIds.value.length > 0)
const visibleAccounts = computed(() => {
  if (!hasActiveAccountFilter.value) return overviewAccounts.value
  return overviewAccounts.value.filter((account) => activeAccountIdSet.value.has(account.id))
})
const visibleAccountIdSet = computed(() => new Set(visibleAccounts.value.map((account) => account.id)))
const visibleHourlySeries = computed(() => (overview.value?.hourlySeries ?? []).filter((series) => visibleAccountIdSet.value.has(series.accountId)))
const visibleOverview = computed<AiPerformanceOverview | undefined>(() => {
  const currentOverview = overview.value
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

const accountOptions = computed(() => accounts.value
  .map((account) => ({
    label: accountOptionLabel(account),
    value: account.id
  })))

const accountFilterItems = computed(() => {
  const currentOverview = overview.value
  if (!currentOverview) return []
  const nameCounts = currentOverview.accounts.reduce((counts, account) => {
    counts.set(account.name, (counts.get(account.name) ?? 0) + 1)
    return counts
  }, new Map<string, number>())
  const accountById = new Map(currentOverview.accounts.map((account) => [account.id, account]))
  const activeIds = activeAccountIdSet.value
  return orderedAiPerformanceSeries(currentOverview).map((series, index) => {
    const account = accountById.get(series.accountId)
    const accountName = account?.name ?? series.accountName
    const label = isManagementView.value && account?.systemAccountName
      ? `${accountName}（${account.systemAccountName}）`
      : (nameCounts.get(accountName) ?? 0) > 1 && account?.providerCode
      ? `${accountName}（${account.providerCode}）`
      : accountName
    return {
      account: account ?? {
        id: series.accountId,
        name: series.accountName,
        status: 'active' as AccountStatus,
        providerCode: 'openai',
        systemAccountId: series.systemAccountId,
        requestCountLast7d: 0,
        selected: false,
        defaultVisible: false
      },
      label,
      color: chartColors[index % chartColors.length],
      selected: activeIds.has(series.accountId)
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
    await loadSystemAccounts()
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
    message.error('AI性能监控数据加载失败')
  } finally {
    if (requestSeq === performanceRequestSeq) {
      loading.value = false
      renderCharts()
    }
  }
}

async function loadAccounts() {
  const requestSeq = ++accountSearchSeq
  accountsLoading.value = true
  try {
    await loadSystemAccounts()
    const keyword = accountSearchKeyword.value.trim()
    const systemAccountId = selectedPerformanceSystemAccountId()
    const accountParams = {
      systemAccountId,
      keyword,
      accountIds: addedAccountIds.value,
      limit: 30
    }
    const nextAccounts = isManagementView.value
      ? await api.stats.aiPerformanceAccounts(accountParams)
      : await api.myStats.aiPerformanceAccounts(accountParams)
    if (requestSeq !== accountSearchSeq) return
    accounts.value = nextAccounts
  } catch (error) {
    console.error(error)
    message.error('AI账户列表加载失败')
  } finally {
    if (requestSeq === accountSearchSeq) {
      accountsLoading.value = false
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

function loadSystemAccounts(): Promise<void> {
  if (!isManagementView.value || systemAccountsLoaded.value) return Promise.resolve()
  if (systemAccountsLoadingPromise) return systemAccountsLoadingPromise
  systemAccountsLoadingPromise = api.systemAccounts.options()
    .then((accounts) => {
      systemAccounts.value = accounts
      systemAccountsLoaded.value = true
    })
    .finally(() => {
      systemAccountsLoadingPromise = undefined
    })
  return systemAccountsLoadingPromise
}

function handleSystemAccountChange() {
  clearAccountState()
  void loadAccounts()
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

function handleAccountSelect(value: unknown) {
  accountPickerValue.value = []
  const id = String(value ?? '').trim()
  accountSearchKeyword.value = ''
  if (!id) return
  const currentAccountIds = new Set(overviewAccounts.value.map((account) => account.id))
  const needsBackendAppend = !currentAccountIds.has(id) && !addedAccountIds.value.includes(id)
  if (needsBackendAppend) {
    const ids = [...addedAccountIds.value, id]
    if (ids.length > 20) {
      message.warning('添加账户最多 20 个')
      return
    }
    addedAccountIds.value = ids
  }
  if (hasActiveAccountFilter.value && !activeAccountIds.value.includes(id)) {
    activeAccountIds.value = [...activeAccountIds.value, id]
  }
  void loadAccounts()
  if (needsBackendAppend) {
    void loadPerformance()
  } else {
    renderCharts()
  }
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

function resetAccounts() {
  clearAccountState()
  void loadAccounts()
  void loadPerformance()
}

function clearAccountState() {
  addedAccountIds.value = []
  activeAccountIds.value = []
  accountPickerValue.value = []
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

function accountOptionLabel(account: AiPerformanceAccountOption) {
  const statusText = account.status === 'active' ? '' : `（${accountStatusText(account.status)}）`
  const ownerText = isManagementView.value && account.systemAccountName ? ` · ${account.systemAccountName}` : ''
  return `${account.name}${statusText}${ownerText} · 近7天 ${formatInteger(account.requestCountLast7d)} 次`
}

function accountStatusText(status: AccountStatus) {
  const labels: Record<AccountStatus, string> = {
    active: '正常',
    disabled: '已停用',
    error: '异常',
    rate_limited: '限流',
    temporary_unavailable: '临时不可用'
  }
  return labels[status] ?? status
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

function pruneAccountState() {
  const currentOverview = overview.value
  if (!currentOverview) return
  const currentIds = new Set(currentOverview.accounts.map((account) => account.id))
  const backendAddedIds = new Set(currentOverview.selectedAccounts.map((account) => account.id))
  addedAccountIds.value = addedAccountIds.value.filter((id) => backendAddedIds.has(id))
  activeAccountIds.value = activeAccountIds.value.filter((id) => currentIds.has(id))
}

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
  flex: 1 1 360px;
  min-width: 320px;
  max-width: 560px;
}

.ai-performance-account-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 12px;
}

.ai-performance-account-filter-item {
  display: inline-flex;
  align-items: center;
  max-width: min(360px, 100%);
  gap: 6px;
  padding: 2px 8px;
  border: 1px solid transparent;
  border-radius: 6px;
  color: #334155;
  background: transparent;
  font-size: 13px;
  line-height: 20px;
  cursor: pointer;
  transition: background-color 0.16s ease, border-color 0.16s ease, opacity 0.16s ease;
}

.ai-performance-account-filter-item:hover,
.ai-performance-account-filter-item.active {
  border-color: #91caff;
  background: #e6f4ff;
}

.ai-performance-account-filter-item.muted {
  opacity: 0.46;
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
