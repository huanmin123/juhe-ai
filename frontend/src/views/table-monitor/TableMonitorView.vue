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
            class="table-history-range"
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

    <a-modal
      v-model:open="cleanupModalOpen"
      title="清理非业务数据"
      width="560px"
      ok-text="提交清理"
      cancel-text="取消"
      :confirm-loading="cleanupSubmitting"
      :ok-button-props="{ danger: true, disabled: cleanupSubmitting || !cleanupCutoffAt }"
      @ok="submitNonBusinessDataCleanup"
    >
      <a-alert
        show-icon
        type="warning"
        message="硬清理业务库之外的历史数据"
        description="系统会提交后台任务，按所选截止时间清理数据集目录库、统计结果库中具备时间列的全部非业务表，以及 usage shard 和审计 payload 外部文件；业务库不会清理。删除后 SQLite 文件大小不会立即变小，释放出的空闲页会留在库内供后续新增数据复用；只有需要归还磁盘时，才需要停服执行 VACUUM。"
      />
      <a-form class="cleanup-form" layout="vertical">
        <a-form-item label="清理这个时间之前的非业务数据" required>
          <a-date-picker
            v-model:value="cleanupCutoffAt"
            class="cleanup-date-picker"
            format="YYYY-MM-DD HH:mm:ss"
            show-time
            :disabled="cleanupSubmitting"
            :disabled-date="disabledCleanupDate"
            :disabled-time="disabledCleanupTime"
          />
        </a-form-item>
      </a-form>
      <a-alert
        v-if="cleanupResult"
        class="cleanup-result"
        show-icon
        :type="cleanupResultType"
        :message="cleanupResultMessage"
        :description="cleanupResultDescription"
      />
    </a-modal>

    <a-row :gutter="[16, 16]" class="database-summary-grid">
      <a-col v-for="item in databaseSummaryRows" :key="item.role" :xs="24" :lg="8">
        <a-card class="database-summary-card">
          <div class="database-summary-head">
            <a-tag :color="databaseRoleColor(item.role)">{{ databaseRoleLabel(item.role) }}</a-tag>
            <span class="database-path">{{ item.database?.databasePath ?? '等待采样后显示数据库路径' }}</span>
          </div>
          <div class="database-summary-value">{{ formatBytes(totalDatabaseBytes(item.database)) }}</div>
          <div class="database-summary-meta">
            <span>主库 {{ formatBytes(item.database?.fileBytes) }}</span>
            <span>WAL {{ formatBytes(item.database?.walBytes) }}</span>
            <span>空闲 {{ formatBytes(item.database?.freeBytes) }}</span>
            <span>表 {{ formatInteger(item.database?.tableCount) }}</span>
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="page-card history-card" title="三库增长趋势">
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
          :row-key="tableKey"
          :scroll-x="1180"
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
              </span>
            </template>
            <template v-else-if="column.key === 'rowCount'">
              {{ formatInteger(record.rowCount) }}
            </template>
            <template v-else-if="column.key === 'tableBytes'">
              {{ formatBytes(record.tableBytes) }}
            </template>
            <template v-else-if="column.key === 'indexBytes'">
              {{ formatBytes(record.indexBytes) }}
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
                <a-tag :color="databaseRoleColor(record.databaseRole)">{{ databaseRoleLabel(record.databaseRole) }}</a-tag>
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
import { computed, nextTick, ref, shallowRef } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { DeleteOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import DeferredRender from '@/components/DeferredRender.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { disposeChart, ensureChartFromElement, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatServerDateTimeInput, serverDateTimeTimestamp } from '@/shared/formatters'
import type { DatabaseStorageSnapshotSummary, MonitoredDatabaseRole, NonBusinessDataCleanupResult, TableStorageOverview, TableStorageSnapshotSummary } from '@/types/domain'

const columns = [
  { title: '库', key: 'databaseRole', width: 92, fixed: 'left' },
  { title: '表名', key: 'tableName', width: 240, fixed: 'left' },
  { title: '行数', key: 'rowCount', align: 'right', width: 120 },
  { title: '表大小', key: 'tableBytes', align: 'right', width: 120 },
  { title: '索引大小', key: 'indexBytes', align: 'right', width: 120 },
  { title: '总大小', key: 'totalBytes', align: 'right', width: 120 },
  { title: '1 小时增长', key: 'growth1h', width: 150 },
  { title: '24 小时增长', key: 'growth24h', width: 150 },
  { title: '采样时间', key: 'sampledAt', width: 190 }
]

const loading = ref(false)
const keyword = ref('')
const historyRange = ref<[Dayjs, Dayjs] | undefined>(defaultHistoryRange())
const overview = ref<TableStorageOverview>()
const cleanupModalOpen = ref(false)
const cleanupSubmitting = ref(false)
const cleanupCutoffAt = ref<Dayjs | undefined>(defaultCleanupCutoffAt())
const cleanupResult = ref<NonBusinessDataCleanupResult>()
const databaseSummaryRoles: MonitoredDatabaseRole[] = ['business', 'dataset', 'stats']
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

const cleanupResultType = computed(() => {
  if (!cleanupResult.value) return 'info'
  if (cleanupResult.value.queued) return 'info'
  if (cleanupResult.value.blockedReason) return 'warning'
  return cleanupResult.value.deletedRows > 0 ? 'success' : 'info'
})

const cleanupResultMessage = computed(() => {
  const result = cleanupResult.value
  if (!result) return ''
  if (result.queued) {
    return '后台清理任务已提交'
  }
  if (result.blockedReason) return '本次未清理'
  return result.deletedRows > 0
    ? `已清理 ${formatInteger(result.deletedRows)} 行非业务数据`
    : '没有可清理的非业务数据'
})

const cleanupResultDescription = computed(() => {
  const result = cleanupResult.value
  if (!result) return ''
  if (result.blockedReason) return result.blockedReason
  if (result.queued) {
    const details = [
      `截止时间：${formatDateTime(result.cutoffAt)}`,
      result.submittedAt ? `提交时间：${formatDateTime(result.submittedAt)}` : undefined,
      result.jobId ? `任务：${result.jobId}` : undefined,
      'worker 会在后台分批清理，稍后刷新表监控可查看数据集目录库、统计结果库和 usage shard 变化。'
    ].filter((item): item is string => Boolean(item))
    return details.join('；')
  }
  const details = [
    `截止时间：${formatDateTime(result.cutoffAt)}`,
    result.hasMore ? '本次达到批量上限，仍有可清理记录，可再次执行。' : '当前截止时间前没有更多待清理记录。'
  ].filter((item): item is string => Boolean(item))
  return details.join('；')
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
  if (cleanupCutoffAt.value?.isAfter(latestAllowedCleanupCutoff())) {
    message.warning('清理截止时间不能晚于当前时间')
    return
  }
  cleanupSubmitting.value = true
  try {
    const result = await api.tableMonitor.cleanupNonBusinessData({
      cutoffAt,
      batchSize: 10000,
      maxBatches: 100
    })
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
  keyword.value = ''
  historyRange.value = defaultHistoryRange()
  void loadData()
}

function matchesTableNameKeyword(tableName: string, keyword: string): boolean {
  if (!keyword) return true
  const normalizedTableName = tableName.toLowerCase()
  return normalizedTableName === keyword || normalizedTableName.startsWith(keyword)
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

function isDefaultHistoryRange(value: [Dayjs, Dayjs]) {
  const defaults = defaultHistoryRange()
  return value[0].isSame(defaults[0], 'day') && value[1].isSame(defaults[1], 'day')
}

function defaultCleanupCutoffAt() {
  return dayjs().subtract(7, 'day').endOf('day')
}

function disabledCleanupDate(current: Dayjs) {
  return current.isAfter(latestAllowedCleanupCutoff(), 'day')
}

function disabledCleanupTime(current?: Dayjs | null) {
  const latestAllowed = latestAllowedCleanupCutoff()
  if (!current?.isSame(latestAllowed, 'day')) {
    return {}
  }
  return {
    disabledHours: () => range(latestAllowed.hour() + 1, 24),
    disabledMinutes: (selectedHour: number) => selectedHour === latestAllowed.hour() ? range(latestAllowed.minute() + 1, 60) : [],
    disabledSeconds: (selectedHour: number, selectedMinute: number) => (
      selectedHour === latestAllowed.hour() && selectedMinute === latestAllowed.minute()
        ? range(latestAllowed.second() + 1, 60)
        : []
    )
  }
}

function latestAllowedCleanupCutoff() {
  return dayjs()
}

function range(start: number, end: number) {
  const output: number[] = []
  for (let value = Math.max(0, start); value < end; value += 1) {
    output.push(value)
  }
  return output
}

function renderHistoryChart() {
  if (!pageActive.value) return
  void nextTick(() => {
    if (!pageActive.value) return
    if (!hasHistoryRows.value) {
      disposeChart(historyChart)
      return
    }
    const chart = ensureChartFromElement(historyChartElement.value, historyChart)
    if (!chart) return
    const buckets = historyTimeBuckets()
    chart.setOption({
      color: ['#1677ff', '#fa8c16', '#722ed1'],
      tooltip: {
        trigger: 'axis',
        formatter: (params: unknown) => historyTooltip(params)
      },
      legend: {
        top: 4,
        data: databaseSummaryRoles.map(databaseRoleLabel)
      },
      grid: { left: 56, right: 24, top: 48, bottom: 42 },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: buckets.map((bucket) => formatSampleTime(bucket))
      },
      yAxis: {
        type: 'value',
        name: '大小',
        axisLabel: { formatter: (value: number) => formatBytes(value) },
        splitLine: { lineStyle: { color: '#edf2f7' } }
      },
      series: databaseSummaryRoles.map((role) => historySeries(role, buckets))
    }, { notMerge: true })
  })
}

function historyTimeBuckets() {
  return [...new Set(databaseHistoryRows.value.map((row) => row.sampledAt))].sort()
}

function historySeries(role: MonitoredDatabaseRole, buckets: string[]) {
  const rowsByTime = new Map(databaseHistoryRows.value
    .filter((row) => row.databaseRole === role)
    .map((row) => [row.sampledAt, row]))
  return {
    name: databaseRoleLabel(role),
    type: 'line',
    smooth: true,
    showSymbol: false,
    connectNulls: false,
    data: buckets.map((bucket) => {
      const row = rowsByTime.get(bucket)
      const totalBytes = totalDatabaseBytes(row)
      return totalBytes === undefined
        ? null
        : {
            value: totalBytes,
            fileBytes: row?.fileBytes,
            walBytes: row?.walBytes,
            freeBytes: row?.freeBytes,
            tableCount: row?.tableCount,
            sampledAt: row?.sampledAt
          }
    })
  }
}

function tableKey(row: TableStorageSnapshotSummary) {
  return `${row.databaseRole}:${row.tableName}`
}

function databaseRoleLabel(role: MonitoredDatabaseRole) {
  return {
    business: '业务库',
    dataset: '数据集目录库',
    stats: '统计结果库'
  }[role]
}

function databaseRoleColor(role: MonitoredDatabaseRole) {
  return {
    business: 'blue',
    dataset: 'orange',
    stats: 'purple'
  }[role]
}

function totalDatabaseBytes(database?: DatabaseStorageSnapshotSummary): number | undefined {
  if (!database) return undefined
  const total = (database.fileBytes ?? 0) + (database.walBytes ?? 0) + (database.shmBytes ?? 0)
  return total > 0 ? total : undefined
}

function growthColor(value?: number) {
  if (value === undefined || value === 0) return 'default'
  return value > 0 ? 'orange' : 'green'
}

function formatGrowthBytes(value?: number) {
  if (value === undefined) return '-'
  if (value === 0) return '0 B'
  return `${value > 0 ? '+' : ''}${formatBytes(value)}`
}

function formatGrowthRows(value?: number) {
  if (value === undefined) return ''
  if (value === 0) return '0 行'
  return `${value > 0 ? '+' : ''}${formatInteger(value)} 行`
}

function formatBytes(value?: number) {
  if (value === undefined || !Number.isFinite(value)) return '-'
  const sign = value < 0 ? '-' : ''
  const absolute = Math.abs(value)
  if (absolute >= 1024 ** 3) return `${sign}${(absolute / 1024 ** 3).toFixed(2)} GB`
  if (absolute >= 1024 ** 2) return `${sign}${(absolute / 1024 ** 2).toFixed(1)} MB`
  if (absolute >= 1024) return `${sign}${(absolute / 1024).toFixed(1)} KB`
  return `${sign}${Math.round(absolute)} B`
}

function formatInteger(value?: number) {
  return value === undefined ? '-' : new Intl.NumberFormat('zh-CN').format(Math.round(value))
}

function historyTooltip(params: unknown) {
  const points = Array.isArray(params) ? params as HistoryTooltipPoint[] : [params as HistoryTooltipPoint]
  const title = points[0]?.axisValueLabel ?? points[0]?.name ?? ''
  const lines = [`<strong>${escapeHtml(title)}</strong>`]
  for (const point of points) {
    const data = point.data && typeof point.data === 'object' ? point.data as HistoryTooltipData : undefined
    if (!data || !Number.isFinite(data.value)) continue
    const details = [
      `主库 ${formatBytes(data.fileBytes)}`,
      `WAL ${formatBytes(data.walBytes)}`,
      `空闲 ${formatBytes(data.freeBytes)}`,
      `表 ${formatInteger(data.tableCount)}`
    ].join(' / ')
    lines.push(`${point.marker ?? ''}${escapeHtml(point.seriesName ?? '')}: ${formatBytes(data.value)} · ${details}`)
  }
  return lines.join('<br/>')
}

interface HistoryTooltipPoint {
  marker?: string
  seriesName?: string
  name?: string
  axisValueLabel?: string
  data?: unknown
}

interface HistoryTooltipData {
  value: number
  fileBytes?: number
  walBytes?: number
  freeBytes?: number
  tableCount?: number
}

function escapeHtml(value: unknown) {
  return String(value ?? '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }[character] ?? character))
}

function formatSampleTime(value: string) {
  const timestamp = serverDateTimeTimestamp(value)
  if (timestamp === undefined) return '时间格式异常'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(timestamp)
}

function renderHistoryCharts() {
  renderHistoryChart()
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

.cleanup-form {
  margin-top: 16px;
}

.cleanup-date-picker {
  width: 100%;
}

.cleanup-result {
  margin-top: 12px;
}

.database-summary-grid :deep(.ant-col) {
  display: flex;
}

.database-summary-card {
  width: 100%;
  border: 1px solid #e8edf5;
  border-radius: 14px;
}

.database-summary-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.database-path {
  min-width: 0;
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
  align-items: center;
  gap: 8px;
  color: #0f172a;
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

@media (max-width: 768px) {
  .table-history-range {
    width: 100%;
  }

  .history-chart {
    height: 300px;
  }
}
</style>
