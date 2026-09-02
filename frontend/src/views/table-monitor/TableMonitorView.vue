<template>
  <div class="table-monitor-page">
    <a-card class="page-card table-monitor-toolbar-card">
      <ResponsiveListToolbar
        v-model:keyword="keyword"
        search-placeholder="搜索表名前缀"
        filter-title="表监控筛选"
        :active-filter-count="activeFilterCount"
        :refresh-loading="loading"
        @search="handleFilterChange"
        @reset="resetFilters"
        @refresh="refreshTableMonitor"
      >
        <template #inline-filters>
          <a-range-picker
            v-model:value="historyRange"
            allow-clear
            class="table-history-range responsive-list-inline-filter"
            :disabled="loading"
            :placeholder="['开始日期', '结束日期']"
            @change="handleHistoryRangeChange"
          />
        </template>
        <template #filters>
          <a-form layout="vertical">
            <a-form-item label="历史日期范围">
              <a-range-picker
                v-model:value="historyRange"
                allow-clear
                class="drawer-range-picker"
                :disabled="loading"
                :placeholder="['开始日期', '结束日期']"
                @change="handleHistoryRangeChange"
              />
            </a-form-item>
          </a-form>
        </template>
        <template #actions>
          <a-button danger :disabled="loading || cleanupSubmitting" @click="openCleanupModal">
            <template #icon>
              <DeleteOutlined />
            </template>
            清理非业务数据
          </a-button>
        </template>
      </ResponsiveListToolbar>
    </a-card>

    <div v-if="overview?.sampledAt" class="table-monitor-freshness" role="status">
      数据截至 {{ formatDateTime(overview.sampledAt) }}；概览为监控快照，缓存最多复用 1 小时，实际新鲜度以采样时间为准
    </div>

    <TableMonitorCleanupModal
      v-model:cutoff-at="cleanupCutoffAt"
      v-model:open="cleanupModalOpen"
      :result="cleanupResult"
      :submitting="cleanupSubmitting"
      @submit="submitNonBusinessDataCleanup"
    />

    <TableMonitorTableHistoryModal
      v-model:open="tableHistoryOpen"
      :loading="tableHistoryLoading"
      :rows="tableHistoryRows"
      :table="selectedTable"
      @update:open="handleTableHistoryOpenChange"
    />

    <div class="database-summary-grid">
      <a-card v-for="item in databaseSummaryRows" :key="item.role" class="database-summary-card">
        <div class="database-summary-head">
          <a-tooltip :title="databaseRoleDetailLabel(item.role)">
            <a-tag :color="databaseRoleColor(item.role)" class="database-role-tag">
              {{ databaseRoleLabel(item.role) }}
            </a-tag>
          </a-tooltip>
          <a-tooltip :title="item.database?.databasePath ?? '等待采样后显示数据库路径'">
            <span class="database-path">{{ item.database?.databasePath ?? '等待采样后显示数据库路径' }}</span>
          </a-tooltip>
        </div>
        <div class="database-summary-value">{{ formatBytes(totalDatabaseBytes(item.database)) }}</div>
        <div class="database-summary-meta">
          <span>主库 {{ formatBytes(item.database?.fileBytes) }}</span>
          <span>WAL {{ formatBytes(item.database?.walBytes) }}</span>
          <span>空闲 {{ formatBytes(item.database?.freeBytes) }}</span>
          <span>表 {{ formatInteger(item.database?.tableCount) }}</span>
        </div>
      </a-card>
    </div>

    <div ref="historyCardElement" class="history-card-shell">
      <a-card class="page-card history-card" title="存储增长趋势">
        <DeferredRender
          v-if="hasHistoryRows"
          :active="pageActive"
          :delay-frames="2"
          :min-height="340"
          :reset-key="historyChartResetKey"
          reset-on-deactivate
          @ready="renderHistoryChart"
        >
          <div ref="historyChartElement" class="history-chart" />
        </DeferredRender>
        <div v-else class="page-empty-card">
          <a-spin v-if="historyLoading" size="small" />
          <a-empty v-else description="趋势图将在进入可视区域后加载" />
        </div>
      </a-card>
    </div>

    <a-card class="page-card">
      <DeferredRender :active="pageActive" :delay-frames="1" :min-height="360" reset-on-deactivate>
        <ResponsiveDataList
          :columns="columns"
          :data-source="filteredTables"
          :loading="loading"
          :loading-more="mobileLoadingMore"
          :mobile-has-more="mobileHasMore"
          :pagination="tablePagination"
          :row-key="tableMonitorRowKey"
          :scroll-x="1320"
          :table-scroll-enabled="false"
          :lock-body-scroll="false"
          row-clickable
          class="table-monitor-table"
          table-class="table-monitor-table"
          size="middle"
          mobile-pagination
          pull-refresh-enabled
          :refreshing="loading"
          @change="handleTableChange"
          @mobile-load-more="loadMoreMobile"
          @mobile-refresh="refreshTableMonitorMobile"
          @row-click="openTableHistory"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="当前条件下没有表监控数据。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'databaseRole'">
              <a-tag :color="databaseRoleColor(record.databaseRole)">{{ databaseRoleLabel(record.databaseRole) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'tableName'">
              <span class="table-name-cell">
                <span class="mono-cell">{{ record.tableName }}</span>
                <span v-if="record.parentTableName" class="table-parent-cell">{{ record.parentTableName }}</span>
              </span>
            </template>
            <template v-else-if="column.key === 'tableState'">
              <a-tag :color="tableStateColor(record)">{{ tableStateLabel(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'rowCount'">
              {{ formatInteger(record.rowCount) }}
            </template>
            <template v-else-if="column.key === 'tableBytes'">
              {{ formatBytes(record.tableBytes) }}
            </template>
            <template v-else-if="column.key === 'indexBytes'">
              {{ formatBytes(record.indexBytes) }}
              <span class="index-ratio">{{ formatRatioPercent(record.indexToTableRatio) }}</span>
            </template>
            <template v-else-if="column.key === 'totalBytes'">
              <strong>{{ formatBytes(record.totalBytes) }}</strong>
            </template>
            <template v-else-if="column.key === 'growth1h'">
              <a-tag :color="growthColor(record.growthBytes1h)">{{ formatGrowthBytes(record.growthBytes1h) }}</a-tag>
              <span class="growth-rows">{{ formatGrowthRows(record.growthRows1h) }}</span>
            </template>
            <template v-else-if="column.key === 'growth24h'">
              <a-tag :color="growthColor(record.growthBytes24h)">{{ formatGrowthBytes(record.growthBytes24h) }}</a-tag>
              <span class="growth-rows">{{ formatGrowthRows(record.growthRows24h) }}</span>
            </template>
            <template v-else-if="column.key === 'sampledAt'">
              {{ formatDateTime(record.sampledAt) }}
            </template>
          </template>
          <template #card="{ record }">
            <article
              class="table-monitor-mobile-card table-monitor-mobile-card-clickable"
              role="button"
              tabindex="0"
              @click="openTableHistory(record)"
              @keydown.enter="openTableHistory(record)"
              @keydown.space.prevent="openTableHistory(record)"
            >
              <div class="mobile-card-head">
                <span class="mono-cell">{{ record.tableName }}</span>
                <span class="mobile-card-tags">
                  <a-tag :color="databaseRoleColor(record.databaseRole)">{{ databaseRoleLabel(record.databaseRole) }}</a-tag>
                  <a-tag :color="tableStateColor(record)">{{ tableStateLabel(record) }}</a-tag>
                </span>
              </div>
              <div class="mobile-list-meta-grid">
                <div class="mobile-list-meta-item">
                  <span>行数</span>
                  <strong>{{ formatInteger(record.rowCount) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>总大小</span>
                  <strong>{{ formatBytes(record.totalBytes) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>索引/表</span>
                  <strong>{{ formatRatioPercent(record.indexToTableRatio) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>1 小时增长</span>
                  <strong>{{ formatGrowthBytes(record.growthBytes1h) }} {{ formatGrowthRows(record.growthRows1h) }}</strong>
                </div>
                <div class="mobile-list-meta-item">
                  <span>24 小时增长</span>
                  <strong>{{ formatGrowthBytes(record.growthBytes24h) }} {{ formatGrowthRows(record.growthRows24h) }}</strong>
                </div>
                <div class="mobile-list-meta-item mobile-list-meta-wide">
                  <span>采样时间</span>
                  <strong>{{ formatDateTime(record.sampledAt) }}</strong>
                </div>
              </div>
            </article>
          </template>
        </ResponsiveDataList>
        <template #placeholder>
          <div class="table-monitor-table-placeholder">
            <a-spin v-if="loading" size="small" />
          </div>
        </template>
      </DeferredRender>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { DeleteOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import DeferredRender from '@/components/DeferredRender.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { disposeChart, ensureChartFromElement, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList, type ResponsivePagedListResult } from '@/composables/useResponsivePagedList'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatServerDateTimeInput } from '@/shared/formatters'
import { stringOrFallback } from '@/shared/pageStateSanitizers'
import type { DatabaseStorageHistoryPoint, NonBusinessDataCleanupResult, TableStorageHistoryPoint, TableStorageOverview, TableStorageOverviewSummary } from '@/types/domain'

import TableMonitorCleanupModal from './TableMonitorCleanupModal.vue'
import TableMonitorTableHistoryModal from './TableMonitorTableHistoryModal.vue'
import {
  buildTableMonitorHistoryChartOption,
  databaseRoleColor,
  databaseRoleDetailLabel,
  databaseRoleLabel,
  formatBytes,
  formatGrowthBytes,
  formatGrowthRows,
  formatInteger,
  formatRatioPercent,
  growthColor,
  tableMonitorColumns,
  tableMonitorDatabaseRoles,
  tableMonitorRowKey,
  tableStateColor,
  tableStateLabel,
  totalDatabaseBytes
} from './tableMonitorDisplay'
import { createTableMonitorHistoryRequestGate } from './tableMonitorHistoryRequestGate'

interface TableMonitorPageState {
  historyRange: [string, string] | null
  keyword: string
}

const columns = tableMonitorColumns
const pageStateCache = usePageStateCache<TableMonitorPageState>(undefined, defaultTableMonitorPageState, {
  sanitize: sanitizeTableMonitorPageState,
  version: 1
})
const initialPageState = pageStateCache.read()

const keyword = ref(initialPageState.keyword)
const historyRange = ref<[Dayjs, Dayjs] | undefined>(parseCachedHistoryRange(initialPageState.historyRange))
const overview = ref<TableStorageOverview>()
const cleanupModalOpen = ref(false)
const cleanupSubmitting = ref(false)
const cleanupCutoffAt = ref<Dayjs | undefined>(defaultCleanupCutoffAt())
const cleanupResult = ref<NonBusinessDataCleanupResult>()
const selectedTable = ref<TableStorageOverviewSummary>()
const tableHistoryOpen = ref(false)
const tableHistoryLoading = ref(false)
const tableHistoryRows = ref<TableStorageHistoryPoint[]>([])
const databaseSummaryRoles = tableMonitorDatabaseRoles
const historyChartPointLimit = 720
const historyLoaded = ref(false)
const historyLoading = ref(false)
const databaseHistoryRows = ref<DatabaseStorageHistoryPoint[]>([])
const historyCardElement = ref<HTMLDivElement>()
const historyChartElement = ref<HTMLDivElement>()
const historyChart = shallowRef<ECharts>()
let historyObserver: IntersectionObserver | undefined
const historyRequestGate = createTableMonitorHistoryRequestGate()
const tableHistoryRequestGate = createTableMonitorHistoryRequestGate()
const forceOverviewRefresh = ref(false)
const tableMonitorOverviewMaxClientAgeMs = 60 * 60 * 1000
const overviewLastLoadedAtMs = ref(0)
let overviewLoadInterrupted = false
let overviewRefreshInterrupted = false
let historyLoadInterrupted = false
const { pageActive } = useEchartsPageLifecycle({
  renderCharts: renderHistoryCharts,
  resizeCharts: resizeHistoryChart,
  disposeCharts: disposeHistoryCharts,
  onMounted: onTableMonitorMounted,
  onDeactivate: deactivateTableMonitorPage,
  renderOnActivated: 'always'
})

type TableMonitorPagedResult = ResponsivePagedListResult<TableStorageOverviewSummary> & Pick<TableStorageOverview, 'sampledAt' | 'databases'>

const {
  items: tableRows,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  tablePagination: baseTablePagination,
  handleTableChange,
  invalidatePendingLoads,
  loadData: loadOverviewData,
  loadMoreMobile,
  resetPagination
} = useResponsivePagedList<TableStorageOverviewSummary>({
  pageSize: 10,
  showTotal: (total) => `共 ${formatInteger(total)} 张表`,
  fetchPage: async (_options, pagination) => {
    const result = await api.tableMonitor.overview({
      page: pagination.current,
      pageSize: pagination.pageSize,
      keyword: keyword.value.trim() || undefined,
      refresh: forceOverviewRefresh.value || undefined
    })
    return {
      items: result.tables,
      page: result.page,
      pageSize: result.pageSize,
      total: result.total,
      hasMore: result.hasMore,
      sampledAt: result.sampledAt,
      databases: result.databases
    } satisfies TableMonitorPagedResult
  },
  onLoaded: (result) => {
    const pageResult = result as TableMonitorPagedResult
    overviewLastLoadedAtMs.value = Date.now()
    overview.value = {
      sampledAt: pageResult.sampledAt,
      databases: pageResult.databases,
      tables: [],
      page: pageResult.page,
      pageSize: pageResult.pageSize,
      total: pageResult.total,
      hasMore: pageResult.hasMore ?? false
    }
  },
  onError: (error) => {
    console.error(error)
    message.error('表监控加载失败')
  },
  requestSignature: () => keyword.value.trim().toLowerCase()
})

const tablePagination = computed(() => ({
  ...baseTablePagination.value,
  showSizeChanger: true,
  pageSizeOptions: ['10', '20', '50', '100']
}))

const databaseSummaryRows = computed(() => {
  const databasesByRole = new Map((overview.value?.databases ?? []).map((database) => [database.databaseRole, database]))
  return databaseSummaryRoles.map((role) => ({
    role,
    database: databasesByRole.get(role)
  }))
})
const filteredTables = computed(() => {
  return tableRows.value
    .filter((row) => databaseSummaryRoles.includes(row.databaseRole))
})
const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (historyRange.value && !isDefaultHistoryRange(historyRange.value)) count += 1
  return count
})

const hasHistoryRows = computed(() => databaseHistoryRows.value.length > 0)
const historyChartResetKey = computed(() => {
  const latestSample = databaseHistoryRows.value.at(-1)?.sampledAt ?? ''
  return `${databaseHistoryRows.value.length}:${latestSample}`
})

async function loadData() {
  if (!pageActive.value) return
  await Promise.all([
    loadOverviewData({ shouldApply: () => pageActive.value }),
    historyLoaded.value ? loadHistoryData() : Promise.resolve()
  ])
}

async function loadHistoryData() {
  if (!pageActive.value || !historyLoaded.value) return
  const params = {
    ...historyRangeParams(),
    limit: historyChartPointLimit
  }
  const request = historyRequestGate.begin(JSON.stringify(params))
  historyLoading.value = true
  try {
    const rows = await api.tableMonitor.databaseHistory(params)
    if (!pageActive.value || !request.isCurrent(currentHistoryRequestSignature())) return
    databaseHistoryRows.value = rows
    await renderHistoryChart()
  } catch (error) {
    if (!pageActive.value || !request.isCurrent(currentHistoryRequestSignature())) return
    console.error(error)
    message.error('表监控趋势加载失败')
  } finally {
    if (request.isCurrent(currentHistoryRequestSignature())) historyLoading.value = false
  }
}

async function loadTableHistoryData() {
  const table = selectedTable.value
  if (!pageActive.value || !tableHistoryOpen.value || !table) return
  const params = {
    databaseRole: table.databaseRole,
    tableName: table.tableName,
    ...historyRangeParams(),
    limit: historyChartPointLimit
  }
  const request = tableHistoryRequestGate.begin(JSON.stringify(params))
  tableHistoryLoading.value = true
  try {
    const rows = await api.tableMonitor.history(params)
    if (!pageActive.value || !request.isCurrent(currentTableHistoryRequestSignature())) return
    tableHistoryRows.value = rows
  } catch (error) {
    if (!pageActive.value || !request.isCurrent(currentTableHistoryRequestSignature())) return
    console.error(error)
    message.error('表历史趋势加载失败')
  } finally {
    if (request.isCurrent(currentTableHistoryRequestSignature())) tableHistoryLoading.value = false
  }
}

function currentHistoryRequestSignature() {
  return JSON.stringify({
    ...historyRangeParams(),
    limit: historyChartPointLimit
  })
}

function currentTableHistoryRequestSignature() {
  const table = selectedTable.value
  return JSON.stringify({
    databaseRole: table?.databaseRole,
    tableName: table?.tableName,
    ...historyRangeParams(),
    limit: historyChartPointLimit
  })
}

async function onTableMonitorMounted() {
  await loadData()
  await nextTick()
  ensureHistoryObserver()
}

function ensureHistoryObserver() {
  if (!pageActive.value || historyLoaded.value || historyObserver || !historyCardElement.value) return
  if (!historyCardElement.value) return
  if (typeof IntersectionObserver === 'undefined') {
    historyLoaded.value = true
    void loadHistoryData()
    return
  }
  historyObserver = new IntersectionObserver((entries) => {
    if (!entries.some((entry) => entry.isIntersecting)) return
    historyLoaded.value = true
    void loadHistoryData()
    historyObserver?.disconnect()
    historyObserver = undefined
  }, { rootMargin: '240px 0px' })
  historyObserver.observe(historyCardElement.value)
}

function openTableHistory(table: TableStorageOverviewSummary) {
  selectedTable.value = table
  tableHistoryRows.value = []
  tableHistoryOpen.value = true
  void loadTableHistoryData()
}

function handleTableHistoryOpenChange(open: boolean) {
  if (open) return
  tableHistoryRequestGate.invalidate()
  tableHistoryLoading.value = false
  tableHistoryRows.value = []
  selectedTable.value = undefined
}

function openCleanupModal() {
  cleanupResult.value = undefined
  cleanupCutoffAt.value = cleanupCutoffAt.value ?? defaultCleanupCutoffAt()
  cleanupModalOpen.value = true
}

async function submitNonBusinessDataCleanup() {
  const cutoffAt = formatServerDateTimeInput(cleanupCutoffAt.value)
  if (!cutoffAt) {
    message.warning('请选择清理截止时间')
    return
  }
  if (cleanupCutoffAt.value?.isAfter(dayjs())) {
    message.warning('清理截止时间不能晚于当前时间')
    return
  }
  cleanupSubmitting.value = true
  try {
    const result = await api.tableMonitor.cleanupNonBusinessData({ cutoffAt })
    cleanupResult.value = result
    if (result.queued) {
      message.success('非业务数据清理任务已提交后台')
    } else if (result.blockedReason) {
      message.warning(result.blockedReason)
    } else {
      message.warning('非业务数据清理任务未提交')
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '清理非业务数据失败'))
  } finally {
    cleanupSubmitting.value = false
  }
}

function handleFilterChange() {
  resetPagination()
  void loadData()
}

function handleHistoryRangeChange() {
  if (!pageActive.value) return
  const requests: Array<Promise<void>> = []
  if (historyLoaded.value) requests.push(loadHistoryData())
  if (tableHistoryOpen.value) requests.push(loadTableHistoryData())
  void Promise.all(requests)
}

function resetFilters() {
  const defaults = defaultTableMonitorPageState()
  keyword.value = defaults.keyword
  historyRange.value = parseCachedHistoryRange(defaults.historyRange)
  pageStateCache.clear()
  resetPagination()
  void loadData()
}

async function refreshTableMonitor() {
  forceOverviewRefresh.value = true
  resetPagination()
  try {
    await loadData()
  } finally {
    forceOverviewRefresh.value = false
  }
}

async function refreshTableMonitorMobile() {
  await refreshTableMonitor()
}

function historyRangeParams() {
  return {
    startAt: formatServerDateTimeInput(historyRange.value?.[0]?.startOf('day')) ?? undefined,
    endAt: formatServerDateTimeInput(historyRange.value?.[1]?.endOf('day')) ?? undefined
  }
}

function defaultHistoryRange(): [Dayjs, Dayjs] {
  return [dayjs().subtract(1, 'month').startOf('day'), dayjs().endOf('day')]
}

function defaultTableMonitorPageState(): TableMonitorPageState {
  const range = defaultHistoryRange()
  return {
    historyRange: [formatDayKey(range[0]), formatDayKey(range[1])],
    keyword: ''
  }
}

function sanitizeTableMonitorPageState(value: unknown, fallback: TableMonitorPageState): TableMonitorPageState {
  const source = value && typeof value === 'object' ? value as Partial<TableMonitorPageState> : {}
  return {
    historyRange: source.historyRange === null ? null : sanitizeCachedHistoryRange(source.historyRange) ?? fallback.historyRange,
    keyword: stringOrFallback(source.keyword, fallback.keyword)
  }
}

function sanitizeCachedHistoryRange(value: unknown): [string, string] | undefined {
  if (!Array.isArray(value) || value.length !== 2) return undefined
  const [start, end] = value
  if (typeof start !== 'string' || typeof end !== 'string') return undefined
  const startDate = dayjs(start, 'YYYY-MM-DD', true)
  const endDate = dayjs(end, 'YYYY-MM-DD', true)
  if (!startDate.isValid() || !endDate.isValid() || startDate.isAfter(endDate, 'day')) return undefined
  return [formatDayKey(startDate), formatDayKey(endDate)]
}

function parseCachedHistoryRange(value: [string, string] | null): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0], 'YYYY-MM-DD', true)
  const end = dayjs(value[1], 'YYYY-MM-DD', true)
  return start.isValid() && end.isValid() && !start.isAfter(end, 'day')
    ? [start.startOf('day'), end.startOf('day')]
    : defaultHistoryRange()
}

function formatDayKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

function snapshotPageState(): TableMonitorPageState {
  return {
    historyRange: historyRange.value ? [formatDayKey(historyRange.value[0]), formatDayKey(historyRange.value[1])] : null,
    keyword: keyword.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

function isDefaultHistoryRange(value: [Dayjs, Dayjs]) {
  const defaults = defaultHistoryRange()
  return value[0].isSame(defaults[0], 'day') && value[1].isSame(defaults[1], 'day')
}

function defaultCleanupCutoffAt() {
  return dayjs().subtract(7, 'day').endOf('day')
}

async function renderHistoryChart() {
  if (!pageActive.value) return
  await nextTick()
  if (!pageActive.value) return
  if (!hasHistoryRows.value) {
    disposeChart(historyChart)
    return
  }
  const chart = await ensureChartFromElement(historyChartElement.value, historyChart, () => pageActive.value)
  if (!chart || !pageActive.value) return
  chart.setOption(buildTableMonitorHistoryChartOption({
    rows: databaseHistoryRows.value,
    roles: databaseSummaryRoles
  }), { notMerge: true })
}

async function renderHistoryCharts() {
  await renderHistoryChart()
}

function disposeHistoryCharts() {
  disposeChart(historyChart)
}

function resizeHistoryChart() {
  resizeEcharts([historyChart.value])
}

function overviewNeedsActivationRefresh(): boolean {
  if (!overview.value) return true
  const sampledAtMs = overview.value.sampledAt ? Date.parse(overview.value.sampledAt) : Number.NaN
  if (Number.isFinite(sampledAtMs)) {
    return Date.now() - sampledAtMs >= tableMonitorOverviewMaxClientAgeMs
  }
  return Date.now() - overviewLastLoadedAtMs.value >= tableMonitorOverviewMaxClientAgeMs
}

function deactivateTableMonitorPage() {
  if (loading.value) {
    overviewLoadInterrupted = true
    overviewRefreshInterrupted = forceOverviewRefresh.value
  }
  if (historyLoaded.value && historyLoading.value) historyLoadInterrupted = true
  invalidatePendingLoads()
  loading.value = false
  historyRequestGate.invalidate()
  tableHistoryRequestGate.invalidate()
  historyLoading.value = false
  tableHistoryLoading.value = false
  historyObserver?.disconnect()
  historyObserver = undefined
  tableHistoryOpen.value = false
  tableHistoryRows.value = []
  selectedTable.value = undefined
}

onActivated(() => {
  void nextTick(() => {
    const activationRefreshNeeded = overviewNeedsActivationRefresh()
    const retryOverview = overviewLoadInterrupted
      || (activationRefreshNeeded && (Boolean(overview.value) || !loading.value))
    const retryHistory = historyLoadInterrupted && historyLoaded.value
    const forceRetryOverview = overviewRefreshInterrupted || (Boolean(overview.value) && activationRefreshNeeded)
    overviewLoadInterrupted = false
    overviewRefreshInterrupted = false
    historyLoadInterrupted = false
    const requests: Array<Promise<unknown>> = []
    if (retryOverview) {
      if (forceRetryOverview) forceOverviewRefresh.value = true
      requests.push(loadOverviewData({ shouldApply: () => pageActive.value }).finally(() => {
        if (forceRetryOverview) forceOverviewRefresh.value = false
      }))
    }
    if (retryHistory) requests.push(loadHistoryData())
    if (requests.length > 0) {
      void Promise.all(requests).finally(() => {
        if (pageActive.value) ensureHistoryObserver()
      })
      return
    }
    ensureHistoryObserver()
  })
})

onBeforeUnmount(() => {
  invalidatePendingLoads()
  historyRequestGate.invalidate()
  tableHistoryRequestGate.invalidate()
  historyObserver?.disconnect()
  historyObserver = undefined
})
</script>

<style scoped>
.table-monitor-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.table-monitor-freshness {
  color: #64748b;
  font-size: 12px;
  line-height: 1.5;
}

.table-monitor-mobile-card-clickable {
  cursor: pointer;
}

.table-monitor-mobile-card-clickable:focus-visible {
  outline: 2px solid #1677ff;
  outline-offset: 2px;
}

.table-monitor-toolbar-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.table-history-range {
  width: 380px;
}

.drawer-range-picker {
  width: 100%;
}

.database-summary-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 16px;
}

.database-summary-card {
  width: 100%;
  border: 1px solid #e8edf5;
  border-radius: 8px;
}

.database-summary-card :deep(.ant-card-body) {
  padding: 20px 18px;
}

.database-summary-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.database-role-tag {
  flex: 0 0 auto;
}

.database-path {
  min-width: 0;
  cursor: default;
  overflow: hidden;
  color: #64748b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.database-summary-value {
  margin-top: 12px;
  color: #0f172a;
  font-size: 28px;
  font-weight: 800;
  line-height: 1.2;
}

.database-summary-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 14px;
  margin-top: 10px;
  color: #64748b;
  font-size: 13px;
}

.table-monitor-table-placeholder {
  display: flex;
  min-height: 360px;
  align-items: center;
  justify-content: center;
}

.table-name-cell {
  display: inline-flex;
  max-width: 100%;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  color: #0f172a;
}

.table-parent-cell,
.index-ratio {
  color: #64748b;
  font-size: 12px;
}

.index-ratio {
  display: block;
  margin-top: 2px;
}

.table-monitor-mobile-card {
  display: grid;
  width: 100%;
  gap: 10px;
  padding: 12px;
  text-align: left;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(15 23 42 / 4%);
}

.mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.mobile-card-tags {
  display: inline-flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
}

.growth-rows {
  display: inline-block;
  margin-left: 6px;
  color: #64748b;
  font-size: 12px;
}

.history-card :deep(.ant-card-body) {
  padding: 18px 20px 20px;
}

.history-card {
  width: 100%;
}

.history-chart {
  width: 100%;
  height: 340px;
}

@media (max-width: 1280px) {
  .database-summary-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 900px) {
  .database-summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 768px) {
  .table-history-range {
    width: 100%;
  }

  .history-chart {
    height: 300px;
  }
}

@media (max-width: 560px) {
  .database-summary-grid {
    grid-template-columns: 1fr;
  }
}
</style>
