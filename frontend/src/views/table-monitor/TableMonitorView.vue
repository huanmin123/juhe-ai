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
        @refresh="loadData"
      >
        <template #inline-filters>
          <a-range-picker
            v-model:value="historyRange"
            allow-clear
            class="table-history-range responsive-list-inline-filter"
            :disabled="loading"
            :placeholder="['开始日期', '结束日期']"
            @change="loadData"
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
                @change="loadData"
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

    <TableMonitorCleanupModal
      v-model:cutoff-at="cleanupCutoffAt"
      v-model:open="cleanupModalOpen"
      :result="cleanupResult"
      :submitting="cleanupSubmitting"
      @submit="submitNonBusinessDataCleanup"
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
        <a-empty description="暂无增长历史" />
      </div>
    </a-card>

    <a-card class="page-card">
      <DeferredRender :active="pageActive" :delay-frames="1" :min-height="360" reset-on-deactivate>
        <ResponsiveDataList
          :columns="columns"
          :data-source="filteredTables"
          :loading="loading"
          :pagination="tablePagination"
          :row-key="tableMonitorRowKey"
          :scroll-x="1320"
          :table-scroll-enabled="false"
          :lock-body-scroll="false"
          class="table-monitor-table"
          table-class="table-monitor-table"
          size="middle"
          mobile-pagination
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
            <article class="table-monitor-mobile-card">
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
import { computed, nextTick, ref, shallowRef, watch } from 'vue'
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
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatServerDateTimeInput } from '@/shared/formatters'
import { stringOrFallback } from '@/shared/pageStateSanitizers'
import type { DatabaseStorageSnapshotSummary, NonBusinessDataCleanupResult, TableStorageOverview } from '@/types/domain'

import TableMonitorCleanupModal from './TableMonitorCleanupModal.vue'
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
  matchesTableNameKeyword,
  tableMonitorColumns,
  tableMonitorDatabaseRoles,
  tableMonitorRowKey,
  tableStateColor,
  tableStateLabel,
  totalDatabaseBytes
} from './tableMonitorDisplay'

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

const loading = ref(false)
const keyword = ref(initialPageState.keyword)
const historyRange = ref<[Dayjs, Dayjs] | undefined>(parseCachedHistoryRange(initialPageState.historyRange))
const overview = ref<TableStorageOverview>()
const cleanupModalOpen = ref(false)
const cleanupSubmitting = ref(false)
const cleanupCutoffAt = ref<Dayjs | undefined>(defaultCleanupCutoffAt())
const cleanupResult = ref<NonBusinessDataCleanupResult>()
const databaseSummaryRoles = tableMonitorDatabaseRoles
const historyChartPointLimit = 720
const databaseHistoryRows = ref<DatabaseStorageSnapshotSummary[]>([])
const historyChartElement = ref<HTMLDivElement>()
const historyChart = shallowRef<ECharts>()
const { pageActive } = useEchartsPageLifecycle({
  renderCharts: renderHistoryCharts,
  resizeCharts: resizeHistoryChart,
  disposeCharts: disposeHistoryCharts,
  onMounted: loadData,
  renderOnActivated: 'always'
})

const tablePagination = {
  pageSize: 10,
  showSizeChanger: true,
  pageSizeOptions: ['10', '20', '50', '100']
}

const databaseSummaryRows = computed(() => {
  const databasesByRole = new Map((overview.value?.databases ?? []).map((database) => [database.databaseRole, database]))
  return databaseSummaryRoles.map((role) => ({
    role,
    database: databasesByRole.get(role)
  }))
})
const filteredTables = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  return (overview.value?.tables ?? [])
    .filter((row) => databaseSummaryRoles.includes(row.databaseRole))
    .filter((row) => matchesTableNameKeyword(row.tableName, text))
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
  loading.value = true
  try {
    const [nextOverview, nextDatabaseHistory] = await Promise.all([
      api.tableMonitor.overview(historyRangeParams()),
      api.tableMonitor.databaseHistory({
        ...historyRangeParams(),
        limit: historyChartPointLimit
      })
    ])
    overview.value = nextOverview
    databaseHistoryRows.value = nextDatabaseHistory
    renderHistoryChart()
  } catch (error) {
    console.error(error)
    message.error('表监控加载失败')
  } finally {
    loading.value = false
  }
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
    } else if (result.deletedRows > 0) {
      message.success(`已清理 ${formatInteger(result.deletedRows)} 行非业务数据`)
      await loadData()
    } else if (result.blockedReason) {
      message.warning(result.blockedReason)
    } else {
      message.info('没有可清理的非业务数据')
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '清理非业务数据失败'))
  } finally {
    cleanupSubmitting.value = false
  }
}

function handleFilterChange() {
  renderHistoryChart()
}

function resetFilters() {
  const defaults = defaultTableMonitorPageState()
  keyword.value = defaults.keyword
  historyRange.value = parseCachedHistoryRange(defaults.historyRange)
  pageStateCache.clear()
  void loadData()
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
</script>

<style scoped>
.table-monitor-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
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
