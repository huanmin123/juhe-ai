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
            all-label="全部用户"
            class="usage-stats-system-account-select"
            include-all
            placeholder="筛选用户"
            @change="handleSystemAccountFilterChange"
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
          <a-select
            :value="accountPickerValue"
            allow-clear
            class="usage-stats-account-select"
            mode="multiple"
            show-search
            :disabled="loading || !rows.length"
            :filter-option="filterAccountOption"
            :max-tag-count="0"
            :options="accountOptions"
            placeholder="搜索并添加账户"
            @select="handleAccountSelect"
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
      <div v-if="accountFilterItems.length" class="usage-stats-account-list" aria-label="趋势账户筛选">
        <button
          v-for="item in accountFilterItems"
          :key="item.account.id"
          class="usage-stats-account-filter-item"
          :class="{ active: item.selected, muted: hasSelectedTrendAccounts && !item.selected }"
          type="button"
          @click="toggleTrendAccount(item.account.id)"
        >
          <span class="usage-stats-legend-dot" :style="{ backgroundColor: item.color }" />
          <span class="usage-stats-legend-name">{{ item.label }}</span>
        </button>
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
        </div>
      </div>
      <ResponsiveDataList
        class="usage-stats-responsive-list"
        table-class="usage-stats-table"
        :columns="columns"
        :data-source="rows"
        :mobile-data-source="rows"
        row-key="id"
        :loading="loading"
        :pagination="tablePagination"
        :scroll-x="tableScrollX"
        :table-scroll-enabled="false"
        :lock-body-scroll="false"
        pull-refresh-enabled
        :refreshing="mobileRefreshing"
        @change="handleTableChange"
        @mobile-refresh="refreshMobileRows"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="当前日期范围暂无账户用量，等待后台聚合后会显示结果。" />
        </template>
        <template #bodyCell="{ column, record, index }">
          <template v-if="column.key === 'rank'">
            <span class="usage-rank">{{ Number(index ?? 0) + 1 }}</span>
          </template>
          <template v-else-if="column.key === 'name'">
            <div class="usage-account-cell">
              <span class="usage-account-name-row">
                <span class="usage-account-name">{{ record.name }}</span>
                <a-tag v-if="record.accessType === 'authorized'" color="blue">来自授权</a-tag>
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
            <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ record.systemAccountName || record.systemAccountId || '-' }}</span>
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
                  <a-tag v-if="record.accessType === 'authorized'" color="blue">来自授权</a-tag>
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
import { ReloadOutlined } from '@ant-design/icons-vue'
import dayjs, { type Dayjs } from 'dayjs'
import { computed, reactive, ref, shallowRef, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { disposeChart, ensureChart, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys } from '@/shared/dateRange'
import { formatDateTime } from '@/shared/formatters'
import type { AccountUsageStatsOverview, AccountUsageStatsRow, AccountUsageSummary, ProviderDefinition, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { accountTypeText, statusColor, statusText } from '@/views/accounts/accountFormatters'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatCompactInteger, formatCost, formatInteger, formatPercent, formatSeconds } from '@/views/stats/statsFormatters'
import { buildAccountUsageTrendOption, chartColors, orderedUsageRows, type UsageTrendMetric } from './usageTrendChartOptions'

interface UsageStatsFilters {
  systemAccountId: string
}

type UsageStatsPageState = {
  filters: UsageStatsFilters
  metric: UsageTrendMetric
  range?: {
    startDate: string
    endDate: string
  }
}

const MAX_RANGE_DAYS = 31
const accountUsagePageSize = 10
const FALLBACK_PROVIDER: ProviderDefinition = {
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
  const today = dayjs().startOf('day')
  const start = isManagementView.value ? today : today.subtract(MAX_RANGE_DAYS - 1, 'day')
  return [start, today]
}
const defaultUsageStatsPageState = (): UsageStatsPageState => {
  return {
    filters: { systemAccountId: allSystemAccountsValue },
    metric: 'cost'
  }
}

const loading = ref(false)
const mobileRefreshing = ref(false)
const overview = ref<AccountUsageStatsOverview>()
const systemAccounts = ref<SystemAccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const usageStatsOptionsLoaded = ref(false)
const usageStatsOptionsScopeKey = ref('')
const pageStateCache = usePageStateCache<UsageStatsPageState>(undefined, defaultUsageStatsPageState, { version: 5 })
const initialPageState = pageStateCache.read()
const filters = reactive<UsageStatsFilters>({ ...initialPageState.filters })
const selectedMetric = ref<UsageTrendMetric>(metricOptions.some((item) => item.value === initialPageState.metric) ? initialPageState.metric : 'cost')
const dateRange = ref<[Dayjs, Dayjs]>(parseDateRange(initialPageState.range))
const dateRangeExplicit = ref(Boolean(initialPageState.range?.startDate || initialPageState.range?.endDate))
const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
const selectedTrendAccountIds = ref<string[]>([])
const addedTrendAccountIds = ref<string[]>([])
const accountPickerValue = ref<string[]>([])
const accountUsagePagination = reactive({
  current: 1,
  pageSize: accountUsagePageSize,
  total: 0
})

const trendChartRef = ref<HTMLDivElement>()
const trendChart = shallowRef<ECharts>()
const { pageActive, requestRender: renderChart } = useEchartsPageLifecycle({
  renderCharts: renderUsageTrendChart,
  resizeCharts,
  disposeCharts,
  onMounted: loadData
})

const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const rows = computed(() => orderedUsageRows(overview.value?.rows ?? []))
const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const selectedRange = computed(() => normalizedDateRange(dateRange.value))
const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
const rangeLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)
const rowsById = computed(() => new Map(rows.value.map((row) => [row.id, row])))
const defaultTrendRows = computed(() => (overview.value?.defaultTrendAccountIds ?? [])
  .map((id) => rowsById.value.get(id))
  .filter((row): row is AccountUsageStatsRow => Boolean(row)))
const defaultTrendAccountIdSet = computed(() => new Set(defaultTrendRows.value.map((account) => account.id)))
const addedTrendRows = computed(() => {
  return addedTrendAccountIds.value
    .map((id) => rowsById.value.get(id))
    .filter((row): row is AccountUsageStatsRow => Boolean(row))
})
const trendAccountRows = computed(() => dedupeRowsById([...defaultTrendRows.value, ...addedTrendRows.value]))
const selectedTrendRows = computed(() => {
  const selectedIds = new Set(selectedTrendAccountIds.value)
  return trendAccountRows.value.filter((row) => selectedIds.has(row.id))
})
const visibleTrendRows = computed(() => selectedTrendAccountIds.value.length ? selectedTrendRows.value : trendAccountRows.value)
const hasSelectedTrendAccounts = computed(() => selectedTrendAccountIds.value.length > 0)
const trendEmptyDescription = computed(() => visibleTrendRows.value.length ? `${rangeLabel.value} 暂无${metricText(selectedMetric.value)}消耗趋势` : '暂无可展示账户')
const hasTrendData = computed(() => visibleTrendRows.value.some((row) => row.dailyUsage.some((point) => metricValue(point, selectedMetric.value) > 0)))
const tableScrollX = computed(() => isManagementView.value ? 1620 : 1450)
const tablePagination = computed(() => ({
  current: accountUsagePagination.current,
  pageSize: accountUsagePagination.pageSize,
  total: accountUsagePagination.total,
  hideOnSinglePage: false,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${formatInteger(total)} 条`
}))
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
const accountOptions = computed(() => rows.value.map((account) => ({
  label: accountOptionLabel(account),
  value: account.id
})))
const accountFilterItems = computed(() => {
  const selectedIds = new Set(selectedTrendAccountIds.value)
  return trendAccountRows.value.map((account, index) => ({
    account,
    label: trendAccountLabel(account),
    color: chartColors[index % chartColors.length],
    selected: selectedIds.has(account.id)
  }))
})
const summaryCards = computed(() => {
  const summary = overview.value?.summary
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

async function loadData(options: { quiet?: boolean; forceOptions?: boolean } = {}) {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
    const [usageOverview] = await Promise.all([
      isManagementView.value ? api.stats.accountUsage(accountUsageParams(systemAccountId)) : api.myStats.accountUsage(accountUsageParams()),
      loadUsageStatsOptions(options.forceOptions === true)
    ])
    overview.value = usageOverview
    syncDateRangeFromResponse(usageOverview.range)
    accountUsagePagination.current = usageOverview.page
    accountUsagePagination.pageSize = usageOverview.pageSize || accountUsagePageSize
    accountUsagePagination.total = usageOverview.total
    pruneSelectedTrendAccounts(usageOverview.rows)
  } catch (error) {
    console.error(error)
    message.error('用量统计加载失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
    renderChart()
  }
}

async function loadUsageStatsOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (!force && usageStatsOptionsLoaded.value && usageStatsOptionsScopeKey.value === scopeKey) {
    return
  }
  if (!isManagementView.value) {
    providers.value = [FALLBACK_PROVIDER]
    systemAccounts.value = []
    usageStatsOptionsLoaded.value = true
    usageStatsOptionsScopeKey.value = scopeKey
    return
  }

  const [providerList, systemAccountList] = await Promise.all([
    api.providers.list(),
    api.systemAccounts.list()
  ])
  providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
  systemAccounts.value = systemAccountList
  usageStatsOptionsLoaded.value = true
  usageStatsOptionsScopeKey.value = scopeKey
}

function refreshUsageStats() {
  accountUsagePagination.current = 1
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
  accountPickerValue.value = []
  accountUsagePagination.current = 1
  accountUsagePagination.pageSize = accountUsagePageSize
  accountUsagePagination.total = 0
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  accountUsagePagination.current = 1
  void loadData()
}

async function refreshMobileRows() {
  mobileRefreshing.value = true
  try {
    await loadData({ forceOptions: true })
  } finally {
    mobileRefreshing.value = false
  }
}

function accountUsageParams(systemAccountId?: string) {
  const params: {
    systemAccountId?: string
    startDate?: string
    endDate?: string
    page: number
    pageSize: number
  } = {
    systemAccountId,
    page: accountUsagePagination.current,
    pageSize: accountUsagePagination.pageSize
  }
  if (shouldSendDateRangeParams()) {
    const [startDate, endDate] = selectedRange.value
    params.startDate = startDate
    params.endDate = endDate
  }
  return params
}

function handleDateRangeChange() {
  dateRange.value = parseDateRange({
    startDate: formatDateKey(dateRange.value[0]),
    endDate: formatDateKey(dateRange.value[1])
  })
  dateRangeExplicit.value = true
  accountUsagePagination.current = 1
  void loadData()
}

function handleTableChange(paginationInfo: unknown) {
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  accountUsagePagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  accountUsagePagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : accountUsagePageSize
  void loadData({ quiet: true })
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

function handleAccountSelect(value: unknown) {
  accountPickerValue.value = []
  const id = String(value ?? '').trim()
  if (!rows.value.some((row) => row.id === id)) return
  if (!defaultTrendAccountIdSet.value.has(id) && !addedTrendAccountIds.value.includes(id)) {
    addedTrendAccountIds.value = [...addedTrendAccountIds.value, id]
  }
  if (selectedTrendAccountIds.value.length && !selectedTrendAccountIds.value.includes(id)) {
    selectedTrendAccountIds.value = [...selectedTrendAccountIds.value, id]
  }
  renderChart()
}

function filterAccountOption(input: string, option?: { label?: unknown; value?: unknown }) {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  return `${option?.label ?? ''} ${option?.value ?? ''}`.toLowerCase().includes(keyword)
}

function disabledDate(current: Dayjs) {
  return isRecentWindowDateDisabled(current, calendarRange.value, MAX_RANGE_DAYS)
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function accountOptionLabel(account: AccountUsageStatsRow) {
  const statusSuffix = account.status === 'active' ? '' : `（${statusText(account.status)}）`
  return `${account.name}${statusSuffix} · 本范围 ${formatInteger(account.rangeUsage.requestCount)} 次`
}

function trendAccountLabel(account: AccountUsageStatsRow) {
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

function shouldSendDateRangeParams(): boolean {
  return dateRangeExplicit.value || !isManagementView.value
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

function pruneSelectedTrendAccounts(currentRows: AccountUsageStatsRow[]) {
  const currentIds = new Set(currentRows.map((row) => row.id))
  addedTrendAccountIds.value = addedTrendAccountIds.value.filter((id) => currentIds.has(id))
  const defaultIds = new Set((overview.value?.defaultTrendAccountIds ?? []).filter((id) => currentIds.has(id)))
  const visibleIds = new Set([...defaultIds, ...addedTrendAccountIds.value])
  selectedTrendAccountIds.value = selectedTrendAccountIds.value.filter((id) => visibleIds.has(id))
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
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
  flex: 1 1 360px;
  min-width: 320px;
  max-width: 560px;
}

.usage-stats-account-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 12px;
}

.usage-stats-account-filter-item {
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

.usage-stats-account-filter-item:hover,
.usage-stats-account-filter-item.active {
  border-color: #91caff;
  background: #e6f4ff;
}

.usage-stats-account-filter-item.muted {
  opacity: 0.46;
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
