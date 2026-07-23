<template>
  <div class="usage-stats-page">
    <a-card class="page-card usage-stats-header-card">
      <div class="page-toolbar usage-stats-toolbar">
        <div class="usage-stats-filters">
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="filters.systemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :disabled="loading"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="filters.systemAccount"
            all-label="全部用户"
            class="usage-stats-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="usage-stats-range-picker"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
          <a-segmented v-model:value="selectedMetric" class="usage-stats-metric-segmented" :disabled="loading" :options="metricOptions" @change="handleMetricChange" />
          <AccountAppendSelect
            v-model:value="addedTrendAccountIds"
            :accounts="accountOptionRows"
            :selected-accounts="addedTrendAccountSelections"
            class="usage-stats-account-select"
            :disabled="loading"
            :hidden-account-ids="accountPickerHiddenValues"
            :loading="accountOptionsLoading"
            :max="maxAddedTrendAccounts"
            max-tag-count="responsive"
            placeholder="输入账户名称添加账户"
            @change="handleAddedTrendAccountsChange"
            @dropdown-visible-change="handleProviderAwareAccountOptionsDropdown"
            @search="handleAccountOptionsSearch"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button @click="resetFilters">重置</a-button>
          <a-button :loading="loading" @click="refreshUsageStats">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
      <div v-if="accountFilterItems.length" class="usage-stats-account-list" aria-label="账户筛选">
        <span
          v-for="item in accountFilterItems"
          :key="item.account.id"
          class="usage-stats-account-filter-entry"
          :class="{ active: item.selected, muted: hasSelectedTrendAccounts && !item.selected }"
        >
          <button
            class="usage-stats-account-filter-item"
            type="button"
            :aria-pressed="item.selected"
            @click="toggleTrendAccount(item.account.id)"
          >
            <span class="usage-stats-legend-dot" :style="{ backgroundColor: item.color }" />
            <span class="usage-stats-legend-name">{{ item.label }}</span>
          </button>
          <a-tooltip v-if="item.removable" title="移除">
            <button
              class="usage-stats-account-filter-remove"
              type="button"
              :aria-label="`移除${item.label}`"
              @click.stop="removeAddedTrendAccount(item.account.id)"
            >
              <CloseOutlined />
            </button>
          </a-tooltip>
        </span>
      </div>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="summaryCardsLoading" compact />

    <StatsChartCard
      :title="`账户每日消耗趋势（${rangeLabel}）`"
      :loading="initialLoading"
      :has-data="hasTrendData"
      :empty-description="trendEmptyDescription"
    >
      <div ref="trendChartRef" class="chart-panel" />
    </StatsChartCard>

    <AccountUsageStatsTable
      :authorization-account-tag-text="authorizationAccountTagText"
      :cache-read-rate="cacheReadRate"
      :columns="columns"
      :empty-description="accountUsageEmptyDescription"
      :has-selected-trend-accounts="hasSelectedTrendAccounts"
      :initial-loading="initialLoading"
      :loading="loading"
      :mobile-has-more="displayMobileHasMore"
      :mobile-loading-more="displayMobileLoadingMore"
      :pagination="displayTablePagination"
      :provider-name="providerName"
      :rows="displayRows"
      :scroll-x="tableScrollX"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRows"
      @mobile-refresh="refreshMobileRows"
    />
  </div>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { CloseOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'
import { computed, reactive, ref, shallowRef, watch } from 'vue'

import { api } from '@/api/client'
import AccountAppendSelect from '@/components/AccountAppendSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList, type ResponsivePagedListResult } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import { formatDateKey, formatDateLabel } from '@/shared/dateRange'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountUsageStatsListResult, AccountUsageStatsRow, AccountUsageStatsTrendOverview, AccountUsageSummary, ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { FALLBACK_PROVIDERS } from '@/views/accounts/accountOptions'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatInteger } from '@/views/stats/statsFormatters'
import AccountUsageStatsTable from './AccountUsageStatsTable.vue'
import {
  aggregateUsageSummaries,
  authorizationAccountTagText,
  buildAccountUsageSummaryCards,
  cacheReadRate,
  dedupeRowsById
} from './usageStatsHelpers'
import {
  accountUsageStatsParams as buildAccountUsageStatsParams,
  accountUsageStatsTableColumns,
  accountUsageStatsTableScrollX,
  type AccountUsagePageState
} from './usageStatsPageConfig'
import {
  accountUsagePageSize,
  defaultUsageStatsPageState,
  initialUsageStatsMetric,
  isUsageStatsDateDisabled,
  maxAddedTrendAccounts,
  normalizeUsageStatsDateRange,
  parseUsageStatsDateRange,
  responseUsageStatsDateRange,
  usageStatsMetricOptions,
  type UsageStatsFilters,
  type UsageStatsPageState
} from './usageStatsPageState'
import { useUsageStatsAccountOptions } from './useUsageStatsAccountOptions'
import { useUsageStatsTrendAccountSelection } from './useUsageStatsTrendAccountSelection'
import { buildAccountUsageTrendOption, orderedUsageRows, type UsageTrendMetric } from './usageTrendChartOptions'

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()

const overview = ref<AccountUsageStatsListResult>()
const rangeSummary = ref<AccountUsageSummary>()
const rangeSummaryLoading = ref(false)
const providers = ref<ProviderDefinition[]>([])
const usageStatsOptionsLoaded = ref(false)
const usageStatsOptionsScopeKey = ref('')
const pageStateCache = usePageStateCache<UsageStatsPageState>(undefined, defaultUsageStatsPageState, { version: 6 })
const initialPageState = pageStateCache.read()
const filters = reactive<UsageStatsFilters>({ ...initialPageState.filters })
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [filters.systemAccountId]
})
const metricOptions = usageStatsMetricOptions
const selectedMetric = ref<UsageTrendMetric>(initialUsageStatsMetric(initialPageState.metric))
const dateRange = ref<[Dayjs, Dayjs]>(parseUsageStatsDateRange(initialPageState.range))
const dateRangeExplicit = ref(Boolean(initialPageState.range?.startDate || initialPageState.range?.endDate))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const addedTrendAccountIds = ref<string[]>([])
let usageStatsResourceRequestSeq = 0
let usageStatsTrendRequestSeq = 0
let usageStatsSummaryRequestSeq = 0
const {
  applyResult: applyAccountUsageResult,
  items: accountUsageRows,
  loading,
  mobileHasMore: accountUsageMobileHasMore,
  mobileLoadingMore: accountUsageMobileLoadingMore,
  pagination: accountUsagePagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileRows,
  resetPagination: resetAccountUsagePagination
} = useResponsivePagedList<AccountUsageStatsRow, { forceOptions?: boolean; forceCache?: boolean }>({
  pageSize: accountUsagePageSize,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatInteger(range?.[1] ?? Math.max(0, total - 1))} 条账户消耗，还有更多`
    : `共 ${formatInteger(total)} 条账户消耗`,
  fetchPage: async (options, pageState): Promise<ResponsivePagedListResult<AccountUsageStatsRow>> => {
    const resourceRequestSeq = ++usageStatsResourceRequestSeq
    if (pageState.current === 1) {
      usageStatsSummaryRequestSeq += 1
      usageStatsTrendRequestSeq += 1
      rangeSummary.value = undefined
      rangeSummaryLoading.value = true
    }
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
    const query = accountUsageParams(isManagementView.value ? systemAccountId : undefined, pageState)
    let usageOverview: AccountUsageStatsListResult | undefined
    await Promise.all([
      (async () => {
        const nextOverview = isManagementView.value
          ? await api.stats.accountUsage(query)
          : await api.myStats.accountUsage(query)
        if (resourceRequestSeq !== usageStatsResourceRequestSeq) return
        const normalizedOverview = normalizeAccountUsageListOverview(nextOverview)
        usageOverview = normalizedOverview
        overview.value = normalizedOverview
        syncDateRangeFromResponse(normalizedOverview.range)
        pruneLoadedTrendAccounts(normalizedOverview.rows)
        if (pageState.current === 1) {
          void loadAccountUsageSummary(normalizedOverview.range, systemAccountId)
        }
      })(),
      loadUsageStatsWindow()
    ])
    if (!usageOverview) {
      if (resourceRequestSeq !== usageStatsResourceRequestSeq) {
        return {
          items: [],
          page: pageState.current,
          pageSize: pageState.pageSize,
          total: 0,
          hasMore: false
        }
      }
      throw new Error('账户用量统计接口未返回数据')
    }
    void loadAccountUsageTrend(usageOverview, systemAccountId)
    return accountUsagePageResult(usageOverview)
  },
  requestSignature: (_options, pageState) => {
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
    return [
      isManagementView.value ? 'management' : 'self',
      accountUsageParams(isManagementView.value ? systemAccountId : undefined, pageState)
    ]
  },
  mergeItems: (currentRows, nextRows) => dedupeRowsById([...currentRows, ...nextRows]),
  onLoaded: () => renderChart(),
  onError: (error) => {
    console.error(error)
    rangeSummaryLoading.value = false
    message.error('用量统计加载失败')
    renderChart()
  }
})

const trendChartRef = ref<HTMLDivElement>()
const trendChart = shallowRef<ECharts>()
const { pageActive, requestRender: renderChart } = useEchartsPageLifecycle({
  renderCharts: renderUsageTrendChart,
  resizeCharts,
  disposeCharts,
  onMounted: () => {
    void loadData()
  },
  onDeactivate: () => clearAccountOptionsSearchTimer(),
  onBeforeUnmount: () => clearAccountOptionsSearchTimer()
})
const {
  accountOptionRows,
  accountOptionsLoading,
  accountOptionsKeyword,
  clearAccountOptionsSearchTimer,
  handleAccountOptionsDropdown,
  handleAccountOptionsSearch,
  loadAccountOptions
} = useUsageStatsAccountOptions({
  isManagementView: () => isManagementView.value,
  systemAccountId: () => scopedSystemAccountId(filters.systemAccountId),
  selectedIds: () => addedTrendAccountIds.value,
  pageActive
})

const availableProviders = computed(() => providers.value.length ? providers.value : FALLBACK_PROVIDERS)
const rows = computed(() => orderedUsageRows(accountUsageRows.value))
const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizeUsageStatsDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const rangeLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const {
  accountFilterItems,
  accountPickerHiddenValues,
  accountUsageEmptyDescription,
  addedTrendAccountSelections,
  displayRows,
  hasSelectedTrendAccounts,
  hasTrendData,
  selectedTrendAccountIds,
  trendEmptyDescription,
  visibleTrendRows,
  clearTrendAccountState: clearTrendAccountSelectionState,
  pruneSelectedTrendAccounts,
  removeAddedTrendAccount: removeAddedTrendAccountSelection,
  toggleTrendAccount: toggleTrendAccountSelection,
  updateAddedTrendAccounts
} = useUsageStatsTrendAccountSelection({
  overview,
  rows,
  accountOptionRows,
  addedTrendAccountIds,
  selectedRange,
  selectedMetric,
  rangeLabel,
  isManagementView: () => isManagementView.value,
  providerName
})
const displayTablePagination = computed(() => hasSelectedTrendAccounts.value ? false : tablePagination.value)
const displayMobileHasMore = computed(() => hasSelectedTrendAccounts.value ? false : accountUsageMobileHasMore.value)
const displayMobileLoadingMore = computed(() => hasSelectedTrendAccounts.value ? false : accountUsageMobileLoadingMore.value)
const tableScrollX = computed(() => accountUsageStatsTableScrollX(isManagementView.value))
const columns = computed(() => accountUsageStatsTableColumns(isManagementView.value))
const displaySummary = computed(() => hasSelectedTrendAccounts.value
  ? aggregateUsageSummaries(displayRows.value.map((row) => row.rangeUsage))
  : rangeSummary.value)
const summaryCardsLoading = computed(() => hasSelectedTrendAccounts.value ? false : rangeSummaryLoading.value)
const summaryCards = computed(() => {
  return buildAccountUsageSummaryCards({
    summary: displaySummary.value
  })
})

async function loadUsageStatsOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (force) {
    resetSystemAccountOptionsSearch()
  }
  if (!force && usageStatsOptionsLoaded.value && usageStatsOptionsScopeKey.value === scopeKey) {
    return
  }
  const providerList = await loadProviderOptionsResource({
    apply: (nextProviders) => {
      providers.value = nextProviders.length ? nextProviders : FALLBACK_PROVIDERS
    },
    force,
    isManagementView: isManagementView.value
  })
  providers.value = providerList.data.length ? providerList.data : FALLBACK_PROVIDERS
  usageStatsOptionsLoaded.value = true
  usageStatsOptionsScopeKey.value = scopeKey
}

function handleProviderAwareAccountOptionsDropdown(open: boolean): void {
  handleAccountOptionsDropdown(open)
  if (open) {
    void loadUsageStatsOptions()
  }
}

function refreshUsageStats() {
  resetAccountUsagePagination()
  void loadData({ forceCache: true })
}

function resetFilters() {
  const defaults = defaultUsageStatsPageState()
  Object.assign(filters, defaults.filters)
  selectedMetric.value = defaults.metric
  dateRange.value = parseUsageStatsDateRange(defaults.range)
  dateRangeExplicit.value = false
  clearTrendAccountState()
  resetAccountUsagePagination()
  resetSystemAccountOptionsSearch()
  pageStateCache.clear()
  void loadData()
}

function handleSystemAccountFilterChange() {
  if (filters.systemAccountId === allSystemAccountsValue) {
    filters.systemAccount = undefined
  }
  resetAccountUsagePagination()
  clearTrendAccountState()
  void loadData()
}

async function refreshMobileRows() {
  resetAccountUsagePagination()
  await loadData({ forceCache: true })
}

function accountUsagePageResult(usageOverview: AccountUsageStatsListResult): ResponsivePagedListResult<AccountUsageStatsRow> {
  return {
    items: usageOverview.rows,
    page: usageOverview.page,
    pageSize: usageOverview.pageSize || accountUsagePageSize,
    total: usageOverview.total,
    hasMore: usageOverview.hasMore
  }
}

function normalizeAccountUsageListOverview(nextOverview: AccountUsageStatsListResult): AccountUsageStatsListResult {
  const previousDailyUsageById = new Map((overview.value?.rows ?? []).map((row) => [row.id, row.dailyUsage]))
  const defaultTrendAccountIds = nextOverview.rows
    .map((row) => row.id)
    .filter((id) => !addedTrendAccountIds.value.includes(id))
    .slice(0, 10)
  return {
    ...nextOverview,
    defaultTrendAccountIds,
    rows: nextOverview.rows.map((row) => ({
      ...row,
      dailyUsage: previousDailyUsageById.get(row.id) ?? []
    }))
  }
}

async function loadAccountUsageTrend(usageOverview: AccountUsageStatsListResult, systemAccountId?: string): Promise<void> {
  const requestSeq = ++usageStatsTrendRequestSeq
  const accountIds = [...new Set([
    ...addedTrendAccountIds.value,
    ...usageOverview.defaultTrendAccountIds
  ])].slice(0, 10)
  if (!accountIds.length) {
    renderChart()
    return
  }
  try {
    const params = {
      systemAccountId,
      startDate: usageOverview.range.startDate,
      endDate: usageOverview.range.endDate,
      accountIds
    }
    const trend = isManagementView.value
      ? await api.stats.accountUsageTrend(params)
      : await api.myStats.accountUsageTrend(params)
    if (requestSeq !== usageStatsTrendRequestSeq) return
    applyAccountUsageTrend(trend)
  } catch (error) {
    if (requestSeq !== usageStatsTrendRequestSeq) return
    console.error(error)
    message.error('账户趋势加载失败')
  }
}

async function loadAccountUsageSummary(range: AccountUsageStatsListResult['range'], systemAccountId?: string): Promise<void> {
  const requestSeq = ++usageStatsSummaryRequestSeq
  rangeSummaryLoading.value = true
  try {
    const params = { systemAccountId, startDate: range.startDate, endDate: range.endDate }
    const result = isManagementView.value
      ? await api.stats.accountUsageSummary(params)
      : await api.myStats.accountUsageSummary(params)
    if (requestSeq !== usageStatsSummaryRequestSeq) return
    rangeSummary.value = result.summary
  } catch (error) {
    if (requestSeq !== usageStatsSummaryRequestSeq) return
    console.error(error)
    rangeSummary.value = undefined
    message.error('用量汇总加载失败')
  } finally {
    if (requestSeq === usageStatsSummaryRequestSeq) rangeSummaryLoading.value = false
  }
}

function applyAccountUsageTrend(trend: AccountUsageStatsTrendOverview): void {
  const dailyUsageById = new Map(trend.rows.map((row) => [row.id, row.dailyUsage]))
  const mergeDailyUsage = (row: AccountUsageStatsRow): AccountUsageStatsRow => ({
    ...row,
    dailyUsage: dailyUsageById.get(row.id) ?? row.dailyUsage
  })
  accountUsageRows.value = accountUsageRows.value.map(mergeDailyUsage)
  if (overview.value) {
    overview.value = {
      ...overview.value,
      rows: overview.value.rows.map(mergeDailyUsage)
    }
  }
  renderChart()
}

function accountUsageParams(systemAccountId: string | undefined, pageState: AccountUsagePageState) {
  return buildAccountUsageStatsParams({
    systemAccountId,
    dateRange: dateRangeExplicit.value ? selectedRange.value : undefined,
    accountIds: addedTrendAccountIds.value,
    pageState
  })
}

function handleDateRangeChange() {
  dateRange.value = parseUsageStatsDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  dateRangeExplicit.value = true
  resetAccountUsagePagination()
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

function handleMetricChange() {
  renderChart()
}

function toggleTrendAccount(id: string) {
  if (toggleTrendAccountSelection(id)) {
    renderChart()
  }
}

function handleAddedTrendAccountsChange(value: string[], previousValue: string[]) {
  accountOptionsKeyword.value = ''
  updateAddedTrendAccounts(value, previousValue)
  void loadAccountOptions()
  void loadData({ quiet: true })
}

function removeAddedTrendAccount(id: string) {
  if (!removeAddedTrendAccountSelection(id)) return
  void loadAccountOptions()
  void loadData({ quiet: true })
  renderChart()
}

function clearTrendAccountState() {
  clearTrendAccountSelectionState()
  accountOptionRows.value = []
  accountOptionsKeyword.value = ''
  clearAccountOptionsSearchTimer()
}

function disabledDate(current: Dayjs) {
  return isUsageStatsDateDisabled(current, calendarRange.value, usageStatsWindowEndDate.value, usageStatsWindowMaxDays.value)
}

function providerName(providerCode?: string) {
  return providerDisplayName(providerCode, availableProviders.value)
}

async function renderUsageTrendChart() {
  if (!overview.value || !hasTrendData.value) {
    disposeChart(trendChart)
    return
  }
  const chart = await ensureChart(trendChartRef, trendChart, () => pageActive.value)
  if (!chart || !overview.value || !pageActive.value) return
  chart.setOption(buildAccountUsageTrendOption(overview.value, selectedMetric.value, visibleTrendRows.value), { notMerge: true })
}

function resizeCharts() {
  resizeEcharts([trendChart.value])
}

function disposeCharts() {
  disposeChart(trendChart)
}

function snapshotPageState(): UsageStatsPageState {
  const [startDate, endDate] = selectedRange.value
  return {
    filters: { ...filters },
    metric: selectedMetric.value,
    range: dateRangeExplicit.value ? { startDate, endDate } : undefined
  }
}

function syncDateRangeFromResponse(value?: { startDate?: string; endDate?: string }) {
  const responseRange = responseUsageStatsDateRange(value)
  if (!responseRange) return
  dateRange.value = responseRange
}

function pruneLoadedTrendAccounts(currentRows: AccountUsageStatsRow[]) {
  pruneSelectedTrendAccounts(currentRows)
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => filters.systemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
</script>

<style scoped>
.usage-stats-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.usage-stats-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.usage-stats-toolbar {
  margin: 0;
}

.usage-stats-filters {
  display: flex;
  flex: 1 1 820px;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.usage-stats-system-account-select {
  width: 220px;
}

.usage-stats-range-picker {
  width: 250px;
}

.usage-stats-metric-segmented {
  width: max-content;
  max-width: 100%;
}

.usage-stats-account-select {
  flex: 1 1 320px;
  width: auto;
  min-width: 280px;
  max-width: none;
}

.usage-stats-account-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 12px;
}

.usage-stats-account-filter-entry {
  display: inline-flex;
  align-items: center;
  max-width: min(360px, 100%);
  border: 1px solid transparent;
  border-radius: 6px;
  transition: background-color 0.16s ease, border-color 0.16s ease, opacity 0.16s ease;
}

.usage-stats-account-filter-entry:hover,
.usage-stats-account-filter-entry.active {
  border-color: #91caff;
  background: #e6f4ff;
}

.usage-stats-account-filter-entry.muted {
  opacity: 0.46;
}

.usage-stats-account-filter-item {
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

.usage-stats-account-filter-remove {
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

.usage-stats-account-filter-remove:hover {
  color: #cf1322;
  background: #fff1f0;
}

.usage-stats-legend-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.usage-stats-legend-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chart-panel {
  width: 100%;
  height: 360px;
}

@media (max-width: 900px) {
  .usage-stats-toolbar,
  .usage-stats-filters {
    align-items: stretch;
  }

  .usage-stats-filters {
    width: 100%;
    flex: none;
    flex-direction: column;
  }

  .usage-stats-system-account-select,
  .usage-stats-range-picker,
  .usage-stats-metric-segmented,
  .usage-stats-account-select {
    flex: none;
    width: 100%;
    min-width: 0;
    max-width: none;
  }

  .chart-panel {
    height: 300px;
  }
}
</style>
