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

    <a-card class="page-card usage-stats-table-card" :loading="initialLoading">
      <div class="usage-stats-table-head">
        <div>
          <h3>账户统计明细</h3>
          <p>账户类型仅作运行态参考；统计、会话亲和和缓存按本地 API Key 与分组连续。</p>
        </div>
      </div>
      <ResponsiveDataList
        class="usage-stats-responsive-list"
        table-class="usage-stats-table"
        :columns="columns"
        :data-source="displayRows"
        :mobile-data-source="displayRows"
        row-key="id"
        :loading="loading"
        :loading-more="displayMobileLoadingMore"
        :mobile-has-more="displayMobileHasMore"
        :pagination="displayTablePagination"
        :scroll-x="tableScrollX"
        :table-scroll-enabled="false"
        :lock-body-scroll="false"
        :mobile-pagination="!hasSelectedTrendAccounts"
        pull-refresh-enabled
        :refreshing="loading"
        @change="handleTableChange"
        @mobile-load-more="loadMoreMobileRows"
        @mobile-refresh="refreshMobileRows"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" :description="accountUsageEmptyDescription" />
        </template>
        <template #bodyCell="{ column, record, index }">
          <template v-if="column.key === 'rank'">
            <span class="usage-rank">{{ Number(index ?? 0) + 1 }}</span>
          </template>
          <template v-else-if="column.key === 'name'">
            <div class="usage-account-cell">
              <span class="usage-account-name-row">
                <span class="usage-account-name">{{ record.name }}</span>
                <a-tag v-if="record.accessType === 'authorized'" color="blue">{{ authorizationAccountTagText(record) }}</a-tag>
              </span>
              <span class="usage-account-meta">{{ statusText(record.status) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'type'">
            <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'providerCode'">
            <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'systemAccount'">
            <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ record.systemAccountName || '-' }}</span>
          </template>
          <template v-else-if="column.key === 'requests'">
            <span class="usage-number">{{ formatInteger(record.rangeUsage.requestCount) }}</span>
          </template>
          <template v-else-if="column.key === 'tokens'">
            <span class="usage-number">{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</span>
          </template>
          <template v-else-if="column.key === 'cacheRate'">
            <span class="usage-number">{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</span>
          </template>
          <template v-else-if="column.key === 'cacheCost'">
            <span class="usage-number">{{ formatCost(record.rangeUsage.cacheReadCost) }}</span>
          </template>
          <template v-else-if="column.key === 'cost'">
            <span class="usage-number">{{ formatCost(record.rangeUsage.totalCost) }}</span>
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            <span :class="record.rangeUsage.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</span>
          </template>
        </template>
        <template #card="{ record, index }">
          <article class="usage-mobile-card">
            <div class="usage-mobile-head">
              <div>
                <div class="usage-mobile-title">
                  <span class="usage-rank">{{ index + 1 }}</span>
                  <span>{{ record.name }}</span>
                </div>
                <div class="usage-mobile-subtitle">
                  <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
                  <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
                  <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
                  <a-tag v-if="record.accessType === 'authorized'" color="blue">{{ authorizationAccountTagText(record) }}</a-tag>
                </div>
              </div>
            </div>
            <div class="usage-mobile-grid">
              <div class="usage-mobile-metric">
                <span>请求</span>
                <strong>{{ formatInteger(record.rangeUsage.requestCount) }}</strong>
              </div>
              <div class="usage-mobile-metric">
                <span>Token</span>
                <strong>{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</strong>
              </div>
              <div class="usage-mobile-metric">
                <span>缓存率</span>
                <strong>{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</strong>
              </div>
              <div class="usage-mobile-metric">
                <span>缓存成本</span>
                <strong>{{ formatCost(record.rangeUsage.cacheReadCost) }}</strong>
              </div>
              <div class="usage-mobile-metric">
                <span>成本</span>
                <strong>{{ formatCost(record.rangeUsage.totalCost) }}</strong>
              </div>
              <div class="usage-mobile-metric">
                <span>最后使用</span>
                <strong>{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</strong>
              </div>
            </div>
          </article>
        </template>
      </ResponsiveDataList>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { CloseOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'
import { computed, reactive, ref, shallowRef, watch } from 'vue'

import { api } from '@/api/client'
import AccountAppendSelect from '@/components/AccountAppendSelect.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList, type ResponsivePagedListResult } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { accountSelectionForId, accountSelectOptionLabel, rememberAccountSelection, rememberAccountSelections, type AccountSelection } from '@/shared/accountLabelCache'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys, recentDateRange } from '@/shared/dateRange'
import { formatDateTime } from '@/shared/formatters'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountOptionSummary, AccountUsageStatsOverview, AccountUsageStatsRow, AccountUsageSummary, ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { accountTypeText, statusColor, statusText } from '@/views/accounts/accountFormatters'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatCompactInteger, formatCost, formatInteger, formatPercent, formatSeconds } from '@/views/stats/statsFormatters'
import { buildAccountUsageTrendOption, chartColors, orderedUsageRows, type UsageTrendMetric } from './usageTrendChartOptions'

interface UsageStatsFilters {
  systemAccountId: string
  systemAccount?: PrincipalSelection
}

type UsageStatsPageState = {
  filters: UsageStatsFilters
  metric: UsageTrendMetric
  range?: {
    startDate: string
    endDate: string
  }
}
type AccountUsagePageState = { current: number; pageSize: number }

const MAX_RANGE_DAYS = 31
const accountUsagePageSize = 10
const maxAddedTrendAccounts = 20
const OPENAI_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

const metricOptions: Array<{ label: string; value: UsageTrendMetric }> = [
  { label: '成本', value: 'cost' },
  { label: 'Token', value: 'tokens' },
  { label: '请求', value: 'requests' }
]

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const defaultDateRange = (): [Dayjs, Dayjs] => {
  return recentDateRange(MAX_RANGE_DAYS)
}
const defaultUsageStatsPageState = (): UsageStatsPageState => {
  return {
    filters: { systemAccountId: allSystemAccountsValue, systemAccount: undefined },
    metric: 'cost'
  }
}

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
const selectedMetric = ref<UsageTrendMetric>(metricOptions.some((item) => item.value === initialPageState.metric) ? initialPageState.metric : 'cost')
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const dateRangeExplicit = ref(Boolean(initialPageState.range?.startDate || initialPageState.range?.endDate))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const selectedTrendAccountIds = ref<string[]>([])
const addedTrendAccountIds = ref<string[]>([])
const addedTrendAccountSelections = ref<AccountSelection[]>([])
const accountOptionRows = ref<AccountOptionSummary[]>([])
const accountOptionsLoading = ref(false)
const accountOptionsKeyword = ref('')
let accountOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let accountOptionsRequestSeq = 0
let accountOptionsLoadingKey: string | undefined
let accountOptionsLoadingPromise: Promise<void> | undefined
const accountOptionsCache = createShortLivedQueryCache<AccountOptionSummary[]>({ ttlMs: 10_000 })
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
  onDeactivate: clearAccountOptionsSearchTimer,
  onBeforeUnmount: clearAccountOptionsSearchTimer
})

const availableProviders = computed(() => providers.value.length ? providers.value : [OPENAI_PROVIDER])
const rows = computed(() => orderedUsageRows(accountUsageRows.value))
const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
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
  return addedTrendAccountIds.value
    .map((id) => rowsById.value.get(id) ?? placeholderTrendRow(id))
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
const tableScrollX = computed(() => isManagementView.value ? 1620 : 1450)
const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '排名', key: 'rank', width: 76 },
    { title: 'AI账户名称', dataIndex: 'name', key: 'name', width: 240 },
    { title: '账户类型', dataIndex: 'type', key: 'type', width: 110 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 },
    { title: '状态', key: 'status', width: 110 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 170 })
  }
  baseColumns.push(
    { title: '请求', key: 'requests', width: 120, align: 'right' },
    { title: 'Token', key: 'tokens', width: 130, align: 'right' },
    { title: '缓存率', key: 'cacheRate', width: 120, align: 'right' },
    { title: '缓存成本', key: 'cacheCost', width: 130, align: 'right' },
    { title: '成本', key: 'cost', width: 130, align: 'right' },
    { title: '最后使用', key: 'lastUsedAt', width: 180 }
  )
  return baseColumns
})
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
  const summary = displaySummary.value
  return [
    { key: 'requests', label: '范围请求', value: formatInteger(summary?.requestCount), extra: `统计滞后 ${formatSeconds(overview.value?.statsLagSeconds)}` },
    { key: 'tokens', label: 'Token 消耗', value: formatCompactInteger(summary?.totalTokens), extra: `输入 ${formatCompactInteger(summary?.inputTokens)} / 输出 ${formatCompactInteger(summary?.outputTokens)} / 缓存读取 ${formatCompactInteger(summary?.cacheReadTokens)}` },
    { key: 'cacheRate', label: '缓存率', value: formatPercent(cacheReadRate(summary)), extra: `缓存成本 ${formatCost(summary?.cacheReadCost)}` },
    { key: 'cost', label: '成本', value: formatCost(summary?.totalCost), extra: `最后使用 ${formatDateTime(summary?.lastUsedAt)}` }
  ]
})

function cacheReadRate(summary?: AccountUsageSummary) {
  const inputTokens = summary?.inputTokens ?? 0
  if (inputTokens <= 0) return 0
  return ((summary?.cacheReadTokens ?? 0) / inputTokens) * 100
}

function aggregateUsageSummaries(summaries: AccountUsageSummary[]): AccountUsageSummary {
  const summary = zeroUsageSummary()
  let lastUsedAt: string | undefined
  for (const item of summaries) {
    summary.requestCount += item.requestCount
    summary.inputTokens += item.inputTokens
    summary.outputTokens += item.outputTokens
    summary.cacheReadTokens += item.cacheReadTokens
    summary.cacheReadCost += item.cacheReadCost
    summary.totalCost += item.totalCost
    if (item.lastUsedAt && (!lastUsedAt || item.lastUsedAt > lastUsedAt)) {
      lastUsedAt = item.lastUsedAt
    }
  }
  summary.totalTokens = summary.inputTokens + summary.outputTokens
  summary.lastUsedAt = lastUsedAt
  return summary
}

async function loadUsageStatsOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (force) {
    resetSystemAccountOptionsSearch()
  }
  if (!force && usageStatsOptionsLoaded.value && usageStatsOptionsScopeKey.value === scopeKey) {
    return
  }
  if (!isManagementView.value) {
    providers.value = [OPENAI_PROVIDER]
    usageStatsOptionsLoaded.value = true
    usageStatsOptionsScopeKey.value = scopeKey
    return
  }

  const [providerList] = await Promise.all([
    api.providers.list()
  ])
  providers.value = providerList.length ? providerList : [OPENAI_PROVIDER]
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
  dateRange.value = parseDateRange(defaults.range)
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
  const [startDate, endDate] = selectedRange.value
  const params: {
    systemAccountId?: string
    startDate?: string
    endDate?: string
    accountIds?: string[]
    page: number
    pageSize: number
  } = {
    systemAccountId,
    accountIds: addedTrendAccountIds.value,
    page: pageState.current,
    pageSize: pageState.pageSize
  }
  params.startDate = startDate
  params.endDate = endDate
  return params
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
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

async function loadAccountOptions(keyword = accountOptionsKeyword.value, force = false): Promise<void> {
  accountOptionsKeyword.value = keyword
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
  const requestKeyword = keyword.trim() || undefined
  const selectedIds = [...addedTrendAccountIds.value].sort()
  const requestKey = JSON.stringify([isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self', requestKeyword ?? '', selectedIds])
  if (!force && accountOptionsLoadingKey === requestKey && accountOptionsLoadingPromise) {
    return accountOptionsLoadingPromise
  }
  const requestSeq = ++accountOptionsRequestSeq
  if (!force) {
    const cachedOptions = accountOptionsCache.get(requestKey)
    if (cachedOptions) {
      accountOptionsLoadingKey = undefined
      accountOptionsLoadingPromise = undefined
      accountOptionsLoading.value = false
      accountOptionRows.value = cachedOptions
      return
    }
  }
  accountOptionsLoading.value = true
  accountOptionsLoadingKey = requestKey
  accountOptionsLoadingPromise = (async () => {
    try {
      let nextOptions = isManagementView.value
        ? await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
        : await api.myAccounts.options({ keyword: requestKeyword, limit: 50 })
      nextOptions = await ensureSelectedAccountOptions(nextOptions, systemAccountId)
      accountOptionsCache.set(requestKey, nextOptions)
      if (requestSeq !== accountOptionsRequestSeq) return
      accountOptionRows.value = nextOptions
    } catch (error) {
      if (requestSeq !== accountOptionsRequestSeq) return
      console.error(error)
      message.error('账户筛选项加载失败')
    } finally {
      if (accountOptionsLoadingKey === requestKey) {
        accountOptionsLoadingKey = undefined
        accountOptionsLoadingPromise = undefined
      }
      if (requestSeq === accountOptionsRequestSeq) {
        accountOptionsLoading.value = false
      }
    }
  })()
  return accountOptionsLoadingPromise
}

async function ensureSelectedAccountOptions(nextOptions: AccountOptionSummary[], systemAccountId: string | undefined): Promise<AccountOptionSummary[]> {
  const selectedIds = [...new Set(addedTrendAccountIds.value)]
  const missingIds = selectedIds.filter((id) => !nextOptions.some((account) => account.id === id))
  if (!missingIds.length) return nextOptions
  try {
    const selectedOptions = isManagementView.value
      ? await api.accounts.options({ systemAccountId, ids: missingIds, limit: 50 })
      : await api.myAccounts.options({ ids: missingIds, limit: 50 })
    return mergeOptionsById(selectedOptions, nextOptions)
  } catch {
    return nextOptions
  }
}

function handleAccountOptionsSearch(value: string) {
  accountOptionsKeyword.value = value
  clearAccountOptionsSearchTimer()
  accountOptionsSearchTimer = window.setTimeout(() => {
    accountOptionsSearchTimer = undefined
    if (!pageActive.value) return
    void loadAccountOptions(accountOptionsKeyword.value)
  }, 250)
}

function handleAccountOptionsDropdown(open: boolean) {
  if (open) {
    void loadAccountOptions()
  }
}

function clearAccountOptionsSearchTimer() {
  if (accountOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(accountOptionsSearchTimer)
    accountOptionsSearchTimer = undefined
  }
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
  return isRecentWindowDateDisabled(current, calendarRange.value, MAX_RANGE_DAYS)
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
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

function authorizationAccountTagText(account: Pick<AccountUsageStatsRow, 'ownerSystemAccountName'>) {
  const ownerName = account.ownerSystemAccountName?.trim()
  return ownerName ? `来自：${ownerName}` : '来自授权'
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

function metricText(metric: UsageTrendMetric) {
  if (metric === 'cost') return '成本'
  if (metric === 'tokens') return 'Token'
  return '请求'
}

function metricValue(point: { requestCount: number; totalTokens: number; totalCost: number }, metric: UsageTrendMetric) {
  if (metric === 'cost') return point.totalCost
  if (metric === 'tokens') return point.totalTokens
  return point.requestCount
}

function zeroUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function trendDateKeys(): string[] {
  const [startDate, endDate] = selectedRange.value
  const start = parseDateKey(startDate)
  const end = parseDateKey(endDate)
  if (!start || !end || start.isAfter(end, 'day')) return []
  const keys: string[] = []
  for (let current = start.startOf('day'); current.isSame(end, 'day') || current.isBefore(end, 'day'); current = current.add(1, 'day')) {
    keys.push(formatDateKey(current))
  }
  return keys
}

function placeholderTrendRow(id: string): AccountUsageStatsRow | undefined {
  const option = accountOptionById.value.get(id)
  if (!option?.name?.trim() || !option.providerCode || !option.type || !option.status) return undefined
  const ownerSystemAccountId = option.ownerSystemAccountId ?? option.systemAccountId
  if (!ownerSystemAccountId) return undefined
  const selection = addedTrendSelectionById.value.get(id)
  return {
    id,
    systemAccountId: option?.systemAccountId,
    systemAccountName: option?.systemAccountName,
    ownerSystemAccountId,
    ownerSystemAccountName: option?.ownerSystemAccountName ?? selection?.ownerSystemAccountName,
    providerCode: option.providerCode,
    name: option.name.trim(),
    type: option.type,
    status: option.status,
    accessType: option?.accessType ?? selection?.accessType,
    rangeUsage: zeroUsageSummary(),
    dailyUsage: trendDateKeys().map((statDate) => ({ ...zeroUsageSummary(), statDate })),
    authorizationUsageAvailable: false,
    authorizationCount: 0,
    authorizationTeamCount: 0
  }
}

function dedupeRowsById(items: AccountUsageStatsRow[]): AccountUsageStatsRow[] {
  const seen = new Set<string>()
  const result: AccountUsageStatsRow[] = []
  for (const item of items) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    result.push(item)
  }
  return result
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
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

.usage-stats-table-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.usage-stats-table-head {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.usage-stats-table-head h3 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.usage-stats-table-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.usage-account-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.usage-account-name-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.usage-account-name {
  display: inline-block;
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-account-meta {
  color: #64748b;
  font-size: 12px;
}

.usage-rank {
  display: inline-flex;
  min-width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: #f1f5f9;
  color: #475569;
  font-size: 12px;
  font-weight: 700;
}

.usage-number {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
}

.usage-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #fff;
}

.usage-mobile-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.usage-mobile-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #0f172a;
  font-weight: 700;
}

.usage-mobile-subtitle {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.usage-mobile-metric {
  min-width: 0;
  padding: 10px;
  border: 1px solid #eef2f7;
  border-radius: 8px;
  background: #f8fafc;
}

.usage-mobile-metric span {
  display: block;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-metric strong {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
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

@media (max-width: 768px) {
  .usage-mobile-grid {
    grid-template-columns: 1fr;
  }
}
</style>
