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
            @dropdown-visible-change="handleAccountOptionsDropdown"
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

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" compact />

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
import { accountSelectionForId, accountSelectOptionLabel, rememberAccountSelection, rememberAccountSelections, type AccountSelection } from '@/shared/accountLabelCache'
import { formatDateKey, formatDateLabel } from '@/shared/dateRange'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountUsageStatsOverview, AccountUsageStatsRow, ProviderDefinition } from '@/types/domain'
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
  dedupeRowsById,
  metricText,
  metricValue,
  placeholderTrendRow,
  usageTrendDateKeys
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
import { buildAccountUsageTrendOption, chartColors, orderedUsageRows, type UsageTrendMetric } from './usageTrendChartOptions'

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const overview = ref<AccountUsageStatsOverview>()
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
const selectedTrendAccountIds = ref<string[]>([])
const addedTrendAccountIds = ref<string[]>([])
const addedTrendAccountSelections = ref<AccountSelection[]>([])
const {
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
} = useResponsivePagedList<AccountUsageStatsRow, { forceOptions?: boolean }>({
  pageSize: accountUsagePageSize,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatInteger(range?.[1] ?? Math.max(0, total - 1))} 条账户消耗，还有更多`
    : `共 ${formatInteger(total)} 条账户消耗`,
  fetchPage: async (options, pageState): Promise<ResponsivePagedListResult<AccountUsageStatsRow>> => {
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
    const [usageOverview] = await Promise.all([
      isManagementView.value ? api.stats.accountUsage(accountUsageParams(systemAccountId, pageState)) : api.myStats.accountUsage(accountUsageParams(undefined, pageState)),
      loadUsageStatsOptions(options.forceOptions === true)
    ])
    overview.value = usageOverview
    syncDateRangeFromResponse(usageOverview.range)
    pruneSelectedTrendAccounts(usageOverview.rows)
    return {
      items: usageOverview.rows,
      page: usageOverview.page,
      pageSize: usageOverview.pageSize || accountUsagePageSize,
      total: usageOverview.total,
      hasMore: usageOverview.hasMore
    }
  },
  mergeItems: (currentRows, nextRows) => dedupeRowsById([...currentRows, ...nextRows]),
  onLoaded: () => renderChart(),
  onError: (error) => {
    console.error(error)
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
const rowsById = computed(() => new Map(rows.value.map((row) => [row.id, row])))
const accountOptionById = computed(() => new Map(accountOptionRows.value.map((account) => [account.id, account])))
const addedTrendSelectionById = computed(() => new Map(addedTrendAccountSelections.value.map((account) => [account.id, account])))
const defaultTrendRows = computed(() => (overview.value?.defaultTrendAccountIds ?? [])
  .map((id) => rowsById.value.get(id))
  .filter((row): row is AccountUsageStatsRow => Boolean(row)))
const defaultTrendAccountIdSet = computed(() => new Set(overview.value?.defaultTrendAccountIds ?? defaultTrendRows.value.map((account) => account.id)))
const addedTrendAccountIdSet = computed(() => new Set(addedTrendAccountIds.value))
const addedTrendRows = computed(() => {
  const dateKeys = usageTrendDateKeys(selectedRange.value)
  return addedTrendAccountIds.value
    .map((id) => rowsById.value.get(id) ?? placeholderTrendRow(id, {
      accountOptionById: accountOptionById.value,
      addedTrendSelectionById: addedTrendSelectionById.value,
      dateKeys
    }))
    .filter((row): row is AccountUsageStatsRow => Boolean(row))
})
const trendAccountRows = computed(() => dedupeRowsById([...defaultTrendRows.value, ...addedTrendRows.value]))
const accountPickerHiddenValues = computed(() => [
  ...trendAccountRows.value.map((account) => account.id),
  ...addedTrendAccountIds.value
])
const selectedTrendRows = computed(() => {
  const selectedIds = new Set(selectedTrendAccountIds.value)
  return trendAccountRows.value.filter((row) => selectedIds.has(row.id))
})
const visibleTrendRows = computed(() => selectedTrendAccountIds.value.length ? selectedTrendRows.value : trendAccountRows.value)
const hasSelectedTrendAccounts = computed(() => selectedTrendAccountIds.value.length > 0)
const displayRows = computed(() => hasSelectedTrendAccounts.value ? orderedUsageRows(selectedTrendRows.value) : rows.value)
const displayTablePagination = computed(() => hasSelectedTrendAccounts.value ? false : tablePagination.value)
const displayMobileHasMore = computed(() => hasSelectedTrendAccounts.value ? false : accountUsageMobileHasMore.value)
const displayMobileLoadingMore = computed(() => hasSelectedTrendAccounts.value ? false : accountUsageMobileLoadingMore.value)
const displaySummary = computed(() => hasSelectedTrendAccounts.value
  ? aggregateUsageSummaries(displayRows.value.map((row) => row.rangeUsage))
  : overview.value?.summary)
const accountUsageEmptyDescription = computed(() => hasSelectedTrendAccounts.value
  ? '当前已选账户在日期范围内暂无用量。'
  : '当前日期范围暂无账户用量，等待后台聚合后会显示结果。')
const trendEmptyDescription = computed(() => visibleTrendRows.value.length ? `${rangeLabel.value} 暂无${metricText(selectedMetric.value)}消耗趋势` : '暂无可展示账户')
const hasTrendData = computed(() => visibleTrendRows.value.some((row) => row.dailyUsage.some((point) => metricValue(point, selectedMetric.value) > 0)))
const tableScrollX = computed(() => accountUsageStatsTableScrollX(isManagementView.value))
const columns = computed(() => accountUsageStatsTableColumns(isManagementView.value))
const accountFilterItems = computed(() => {
  const selectedIds = new Set(selectedTrendAccountIds.value)
  return trendAccountRows.value.map((account, index) => ({
    account,
    label: trendAccountLabel(account),
    color: chartColors[index % chartColors.length],
    selected: selectedIds.has(account.id),
    removable: addedTrendAccountIdSet.value.has(account.id) && !defaultTrendAccountIdSet.value.has(account.id)
  }))
})
const summaryCards = computed(() => {
  return buildAccountUsageSummaryCards({
    summary: displaySummary.value,
    statsLagSeconds: overview.value?.statsLagSeconds
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
  const [providerList] = await Promise.all([
    api.providers.options()
  ])
  providers.value = providerList.length ? providerList : FALLBACK_PROVIDERS
  usageStatsOptionsLoaded.value = true
  usageStatsOptionsScopeKey.value = scopeKey
}

function refreshUsageStats() {
  resetAccountUsagePagination()
  void loadData({ forceOptions: true })
}

function resetFilters() {
  const defaults = defaultUsageStatsPageState()
  Object.assign(filters, defaults.filters)
  selectedMetric.value = defaults.metric
  dateRange.value = parseUsageStatsDateRange(defaults.range)
  dateRangeExplicit.value = false
  selectedTrendAccountIds.value = []
  addedTrendAccountIds.value = []
  addedTrendAccountSelections.value = []
  accountOptionRows.value = []
  accountOptionsKeyword.value = ''
  clearAccountOptionsSearchTimer()
  resetAccountUsagePagination()
  resetSystemAccountOptionsSearch()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
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
  await loadData({ forceOptions: true })
}

function accountUsageParams(systemAccountId: string | undefined, pageState: AccountUsagePageState) {
  return buildAccountUsageStatsParams({
    systemAccountId,
    dateRange: selectedRange.value,
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
  if (!trendAccountRows.value.some((row) => row.id === id)) return
  selectedTrendAccountIds.value = selectedTrendAccountIds.value.includes(id)
    ? selectedTrendAccountIds.value.filter((accountId) => accountId !== id)
    : [...selectedTrendAccountIds.value, id]
  renderChart()
}

function handleAddedTrendAccountsChange(value: string[], previousValue: string[]) {
  accountOptionsKeyword.value = ''
  const previousIds = new Set(previousValue)
  const acceptedIds = value.filter((id) => !defaultTrendAccountIdSet.value.has(id))
  for (const id of acceptedIds) {
    rememberAddedTrendAccountSelection(id)
  }
  addedTrendAccountIds.value = acceptedIds
  syncAddedTrendAccountSelections()
  const newlyAddedIds = acceptedIds.filter((id) => !previousIds.has(id))
  const visibleIds = new Set([...defaultTrendAccountIdSet.value, ...acceptedIds])
  const nextSelectedIds = selectedTrendAccountIds.value.filter((id) => visibleIds.has(id))
  selectedTrendAccountIds.value = selectedTrendAccountIds.value.length
    ? [...new Set([...nextSelectedIds, ...newlyAddedIds])]
    : nextSelectedIds
  void loadAccountOptions()
  void loadData({ quiet: true })
}

function removeAddedTrendAccount(id: string) {
  if (!addedTrendAccountIdSet.value.has(id)) return
  addedTrendAccountIds.value = addedTrendAccountIds.value.filter((accountId) => accountId !== id)
  addedTrendAccountSelections.value = addedTrendAccountSelections.value.filter((selection) => selection.id !== id)
  selectedTrendAccountIds.value = selectedTrendAccountIds.value.filter((accountId) => accountId !== id)
  void loadAccountOptions()
  void loadData({ quiet: true })
  renderChart()
}

function clearTrendAccountState() {
  selectedTrendAccountIds.value = []
  addedTrendAccountIds.value = []
  addedTrendAccountSelections.value = []
  accountOptionRows.value = []
  accountOptionsKeyword.value = ''
  clearAccountOptionsSearchTimer()
}

function disabledDate(current: Dayjs) {
  return isUsageStatsDateDisabled(current, calendarRange.value)
}

function providerName(providerCode?: string) {
  return providerDisplayName(providerCode, availableProviders.value)
}

function trendAccountLabel(account: AccountUsageStatsRow) {
  if (account.accessType === 'authorized') {
    return accountSelectOptionLabel(account)
  }
  const sameNameCount = rows.value.filter((row) => row.name === account.name).length
  if (sameNameCount <= 1) return account.name
  const suffix = isManagementView.value && account.systemAccountName
    ? account.systemAccountName
    : providerName(account.providerCode)
  return `${account.name}（${suffix}）`
}

function renderUsageTrendChart() {
  if (!overview.value || !hasTrendData.value) {
    disposeChart(trendChart)
    return
  }
  const chart = ensureChart(trendChartRef, trendChart)
  if (!chart) return
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

function pruneSelectedTrendAccounts(currentRows: AccountUsageStatsRow[]) {
  const currentIds = new Set(currentRows.map((row) => row.id))
  syncAddedTrendAccountSelections()
  const defaultIds = new Set((overview.value?.defaultTrendAccountIds ?? []).filter((id) => currentIds.has(id)))
  const visibleIds = new Set([...defaultIds, ...addedTrendAccountIds.value])
  selectedTrendAccountIds.value = selectedTrendAccountIds.value.filter((id) => visibleIds.has(id))
}

function rememberAddedTrendAccountSelection(id: string) {
  const selection = accountSelectionForId(id, [...accountOptionRows.value, ...rows.value])
  rememberAccountSelection(selection)
  if (!selection || addedTrendAccountSelections.value.some((item) => item.id === selection.id)) return
  addedTrendAccountSelections.value = [...addedTrendAccountSelections.value, selection]
}

function syncAddedTrendAccountSelections() {
  const existing = new Map(addedTrendAccountSelections.value.map((selection) => [selection.id, selection]))
  addedTrendAccountSelections.value = addedTrendAccountIds.value
    .map((id) => accountSelectionForId(id, [...accountOptionRows.value, ...rows.value]) ?? existing.get(id))
    .filter((selection): selection is AccountSelection => Boolean(selection))
  rememberAccountSelections(addedTrendAccountSelections.value)
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => filters.systemAccount, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(addedTrendAccountSelections, (selections) => rememberAccountSelections(selections), { deep: true, immediate: true })
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
    flex-direction: column;
  }

  .usage-stats-system-account-select,
  .usage-stats-range-picker,
  .usage-stats-metric-segmented,
  .usage-stats-account-select {
    width: 100%;
    min-width: 0;
    max-width: none;
  }

  .chart-panel {
    height: 300px;
  }
}
</style>
