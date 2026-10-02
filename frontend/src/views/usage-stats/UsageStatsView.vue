<template>
  <div class="usage-stats-page">
    <a-card class="page-card usage-stats-header-card">
      <div class="page-toolbar usage-stats-toolbar usage-stats-toolbar-desktop">
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
      <!-- 手机端紧凑条：指标切换常驻，用户/日期/添加账户收进底部抽屉 -->
      <div class="usage-stats-mobile-bar">
        <a-segmented v-model:value="selectedMetric" class="usage-stats-mobile-metric" :disabled="loading" :options="metricOptions" @change="handleMetricChange" />
        <a-button class="usage-stats-mobile-filter-button" @click="mobileFilterOpen = true">
          <template #icon>
            <FilterOutlined />
          </template>
          筛选
          <span v-if="mobileFilterActive" class="usage-stats-mobile-filter-dot" />
        </a-button>
      </div>
      <div
        v-if="accountFilterItems.length"
        class="usage-stats-account-list"
        :class="{ collapsed: mobileChipsCollapsed }"
        aria-label="账户筛选"
      >
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
      <button
        v-if="accountFilterItems.length > mobileChipCollapseThreshold"
        type="button"
        class="usage-stats-chips-toggle"
        @click="mobileChipsCollapsed = !mobileChipsCollapsed"
      >
        {{ mobileChipsCollapsed ? `展开全部 ${accountFilterItems.length} 个账户` : '收起账户列表' }}
        <CaretDownOutlined class="usage-stats-chips-toggle-icon" :class="{ open: !mobileChipsCollapsed }" />
      </button>
    </a-card>

    <a-drawer
      v-model:open="mobileFilterOpen"
      title="筛选统计"
      placement="bottom"
      height="min(66vh, 520px)"
      class="usage-stats-mobile-filter-drawer"
      :body-style="{ padding: '14px 16px 16px' }"
    >
      <div class="stats-mobile-filter-body">
        <div v-if="isManagementView" class="stats-mobile-filter-field">
          <span class="stats-mobile-filter-label">用户</span>
          <SystemPrincipalSelect
            v-model:value="filters.systemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :disabled="loading"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="filters.systemAccount"
            all-label="全部用户"
            class="stats-mobile-filter-control"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </div>
        <div class="stats-mobile-filter-field">
          <span class="stats-mobile-filter-label">时间范围</span>
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="stats-mobile-filter-control"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
        </div>
        <div class="stats-mobile-filter-field">
          <span class="stats-mobile-filter-label">添加趋势账户</span>
          <AccountAppendSelect
            v-model:value="addedTrendAccountIds"
            :accounts="accountOptionRows"
            :selected-accounts="addedTrendAccountSelections"
            class="stats-mobile-filter-control"
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
        <div class="stats-mobile-filter-actions">
          <a-button :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button type="primary" :loading="loading" @click="refreshUsageStats">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-drawer>

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
import { CaretDownOutlined, CloseOutlined, FilterOutlined, ReloadOutlined } from '@ant-design/icons-vue'
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
import { formatDateKey, formatDateLabel } from '@/shared/dateRange'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountUsageStatsListResult, AccountUsageStatsRow, AccountUsageStatsTrendOverview, AccountUsageSummary } from '@/types/domain'
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
  items: accountUsageRows,
  loading,
  mobileHasMore: accountUsageMobileHasMore,
  mobileLoadingMore: accountUsageMobileLoadingMore,
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
      loadUsageStatsWindow({
        force: options?.forceCache === true,
        viewScope: isManagementView.value ? 'admin' : 'self'
      })
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
  handleAccountOptionsSearch
} = useUsageStatsAccountOptions({
  isManagementView: () => isManagementView.value,
  systemAccountId: () => scopedSystemAccountId(filters.systemAccountId),
  selectedIds: () => addedTrendAccountIds.value,
  pageActive
})

const rows = computed(() => orderedUsageRows(accountUsageRows.value))
const providerNamesByCode = computed(() => new Map(accountOptionRows.value.map((account) => [account.providerCode, account.providerName])))
const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizeUsageStatsDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const rangeLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
// 手机端：筛选抽屉开合；账户 chips（图例兼筛选）超过阈值折叠，展开按钮显示总数。
// 筛选打点：指定到具体用户时提示（'all' 是全部用户哨兵；日期为必选无「非默认」态）。
const mobileFilterOpen = ref(false)
const mobileChipsCollapsed = ref(true)
const mobileChipCollapseThreshold = 4
const mobileFilterActive = computed(() => Boolean(filters.systemAccountId) && filters.systemAccountId !== 'all')
const {
  accountFilterItems,
  accountPickerHiddenValues,
  accountUsageEmptyDescription,
  addedTrendAccountSelections,
  displayRows,
  hasSelectedTrendAccounts,
  hasTrendData,
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
  void loadData({ quiet: true })
}

function removeAddedTrendAccount(id: string) {
  if (!removeAddedTrendAccountSelection(id)) return
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
  const normalizedCode = providerCode?.trim()
  return normalizedCode
    ? providerNamesByCode.value.get(normalizedCode) ?? providerDisplayName(normalizedCode, FALLBACK_PROVIDERS)
    : providerDisplayName(providerCode, FALLBACK_PROVIDERS)
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
  flex: 0 1 380px;
  width: min(380px, 100%);
  min-width: 0;
  max-width: 100%;
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
  border-color: var(--juhe-border-strong);
  background: var(--juhe-accent-soft);
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
  color: var(--juhe-fg-soft);
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
  color: var(--juhe-muted);
  background: transparent;
  font-size: 12px;
  cursor: pointer;
  transition: background-color 0.16s ease, color 0.16s ease;
}

.usage-stats-account-filter-remove:hover {
  color: var(--juhe-danger);
  background: var(--juhe-danger-soft);
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

/* 手机端紧凑筛选条与账户 chips 折叠（桌面隐藏） */
.usage-stats-mobile-bar {
  display: none;
}

.usage-stats-mobile-metric {
  flex: 1 1 auto;
  min-width: 0;
  max-width: 100%;
}

.usage-stats-mobile-bar :deep(.ant-segmented-item-label) {
  padding: 6px 12px;
}

.usage-stats-mobile-filter-button {
  position: relative;
  flex: 0 0 auto;
}

.usage-stats-mobile-filter-dot {
  position: absolute;
  top: 6px;
  right: 6px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--juhe-coral, var(--juhe-coral));
}

.usage-stats-chips-toggle {
  display: none;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: 100%;
  min-height: 32px;
  margin-top: 8px;
  border: 0;
  border-radius: var(--juhe-radius-sm, 8px);
  background: transparent;
  color: var(--juhe-muted);
  font-size: 12px;
  cursor: pointer;
}

.usage-stats-chips-toggle-icon {
  font-size: 10px;
  transition: transform 0.18s ease;
}

.usage-stats-chips-toggle-icon.open {
  transform: rotate(180deg);
}

.stats-mobile-filter-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.stats-mobile-filter-label {
  color: var(--juhe-muted);
  font-size: 12px;
}

.stats-mobile-filter-control {
  width: 100%;
}

.stats-mobile-filter-actions {
  display: flex;
  gap: 12px;
  margin-top: 4px;
}

.stats-mobile-filter-actions .ant-btn {
  flex: 1 1 0;
}

.chart-panel {
  width: 100%;
  height: 360px;
}

@media (max-width: 900px) {
  .usage-stats-toolbar-desktop {
    display: none;
  }

  .usage-stats-mobile-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    min-width: 0;
  }

  .usage-stats-account-list.collapsed {
    max-height: 84px;
    overflow: hidden;
  }

  .usage-stats-chips-toggle {
    display: flex;
  }

  .chart-panel {
    height: 300px;
  }
}

@media (min-width: 901px) {
  .usage-stats-mobile-filter-drawer {
    display: none;
  }
}
</style>
