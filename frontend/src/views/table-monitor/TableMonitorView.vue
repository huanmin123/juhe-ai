<template>
  <div class="table-monitor-page">
    <a-card class="page-card table-monitor-toolbar-card">
      <div class="page-toolbar table-monitor-toolbar">
        <div class="table-monitor-filters">
          <a-input-search
            v-model:value="keyword"
            allow-clear
            class="table-search"
            placeholder="搜索表名"
            :disabled="loading"
            @change="handleFilterChange"
            @search="handleFilterChange"
          />
          <a-range-picker
            v-model:value="historyRange"
            allow-clear
            class="table-history-range"
            :disabled="loading"
            :placeholder="['开始日期', '结束日期']"
            @change="loadData"
          />
        </div>
        <div class="page-toolbar-actions">
          <a-button :loading="loading" @click="loadData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
    </a-card>

    <a-row :gutter="[16, 16]" class="database-summary-grid">
      <a-col v-for="item in databaseSummaryRows" :key="item.role" :xs="24" :lg="12">
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

    <a-row :gutter="[16, 16]" class="history-grid">
      <a-col v-for="item in historyCards" :key="item.role" :xs="24" :xl="12">
        <a-card class="page-card history-card" :title="item.title">
          <div v-if="item.rows.length > 0" :ref="item.setChartRef" class="history-chart" />
          <div v-else class="page-empty-card">
            <a-empty :description="`${databaseRoleLabel(item.role)}暂无增长历史`" />
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="page-card">
      <a-table
        :columns="columns"
        :custom-row="customTableRow"
        :data-source="filteredTables"
        :loading="loading"
        :pagination="tablePagination"
        :row-class-name="rowClassName"
        :row-key="tableKey"
        :scroll="{ x: 1180 }"
        class="table-monitor-table"
        size="middle"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'databaseRole'">
            <a-tag :color="databaseRoleColor(record.databaseRole)">{{ databaseRoleLabel(record.databaseRole) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'tableName'">
            <span class="mono-cell">{{ record.tableName }}</span>
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
      </a-table>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import type { ShallowRef } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { init, type ECharts } from '@/lib/echarts'
import { formatDateTime, formatServerDateTimeInput } from '@/shared/formatters'
import type { DatabaseStorageSnapshotSummary, MonitoredDatabaseRole, TableStorageOverview, TableStorageSnapshotSummary } from '@/types/domain'

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
const historyRange = ref<[Dayjs, Dayjs] | undefined>([dayjs().subtract(1, 'month').startOf('day'), dayjs().endOf('day')])
const overview = ref<TableStorageOverview>()
const databaseSummaryRoles: MonitoredDatabaseRole[] = ['business', 'records']
const selectedTableKeys = ref<Record<MonitoredDatabaseRole, string | undefined>>({
  business: undefined,
  records: undefined
})
const historyRowsByRole = ref<Record<MonitoredDatabaseRole, TableStorageSnapshotSummary[]>>({
  business: [],
  records: []
})
const historyChartElements: Record<MonitoredDatabaseRole, HTMLDivElement | undefined> = {
  business: undefined,
  records: undefined
}
const historyCharts: Record<MonitoredDatabaseRole, ShallowRef<ECharts | undefined>> = {
  business: shallowRef<ECharts>(),
  records: shallowRef<ECharts>()
}

const tablePagination = {
  pageSize: 20,
  showSizeChanger: true,
  pageSizeOptions: ['20', '50', '100']
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
    .filter((row) => !text || row.tableName.toLowerCase().includes(text))
})

const selectedTable = computed(() => {
  return {
    business: selectedTableForRole('business'),
    records: selectedTableForRole('records')
  }
})

const historyCards = computed(() => {
  return databaseSummaryRoles.map((role) => {
    const table = selectedTable.value[role]
    return {
      role,
      rows: historyRowsByRole.value[role],
      title: table ? `${databaseRoleLabel(role)} · ${table.tableName} 增长趋势` : `${databaseRoleLabel(role)}增长趋势`,
      setChartRef: (element: unknown) => {
        historyChartElements[role] = element instanceof HTMLDivElement ? element : undefined
      }
    }
  })
})

async function loadData() {
  loading.value = true
  try {
    overview.value = await api.tableMonitor.overview(historyRangeParams())
    ensureSelectedTable()
    await loadAllHistory()
  } catch (error) {
    console.error(error)
    message.error('表监控加载失败')
  } finally {
    loading.value = false
  }
}

function handleFilterChange() {
  ensureSelectedTable(true)
  void loadAllHistory()
}

async function loadAllHistory() {
  await Promise.all(databaseSummaryRoles.map((role) => loadHistoryForRole(role)))
}

async function loadHistoryForRole(role: MonitoredDatabaseRole) {
  const table = selectedTable.value[role]
  if (!table) {
    historyRowsByRole.value[role] = []
    renderHistoryChart(role)
    return
  }
  selectedTableKeys.value[role] = tableKey(table)
  try {
    historyRowsByRole.value[role] = await api.tableMonitor.history({
      databaseRole: table.databaseRole,
      tableName: table.tableName,
      ...historyRangeParams(),
      limit: 10000
    })
  } catch (error) {
    console.error(error)
    message.error(`${databaseRoleLabel(role)}增长历史加载失败`)
    historyRowsByRole.value[role] = []
  } finally {
    renderHistoryChart(role)
  }
}

function ensureSelectedTable(reset = false) {
  for (const role of databaseSummaryRoles) {
    if (reset || !selectedTableForRole(role)) {
      const firstTable = firstTableForRole(role)
      selectedTableKeys.value[role] = firstTable ? tableKey(firstTable) : undefined
    }
  }
}

function selectedTableForRole(role: MonitoredDatabaseRole): TableStorageSnapshotSummary | undefined {
  const current = selectedTableKeys.value[role]
  return filteredTables.value.find((row) => row.databaseRole === role && tableKey(row) === current) ?? firstTableForRole(role)
}

function firstTableForRole(role: MonitoredDatabaseRole): TableStorageSnapshotSummary | undefined {
  return filteredTables.value.find((row) => row.databaseRole === role)
}

function historyRangeParams() {
  return {
    startAt: formatServerDateTimeInput(historyRange.value?.[0]?.startOf('day')) ?? undefined,
    endAt: formatServerDateTimeInput(historyRange.value?.[1]?.endOf('day')) ?? undefined
  }
}

function customTableRow(record: TableStorageSnapshotSummary) {
  return {
    onClick: () => {
      selectedTableKeys.value[record.databaseRole] = tableKey(record)
      void loadHistoryForRole(record.databaseRole)
    }
  }
}

function rowClassName(record: TableStorageSnapshotSummary) {
  return tableKey(record) === selectedTableKeys.value[record.databaseRole] ? 'selected-monitor-row' : ''
}

function renderHistoryChart(role: MonitoredDatabaseRole) {
  void nextTick(() => {
    const rows = historyRowsByRole.value[role]
    if (!rows.length) {
      disposeChart(historyCharts[role])
      return
    }
    const chart = ensureChart(historyChartElements[role], historyCharts[role])
    if (!chart) return
    chart.setOption({
      tooltip: { trigger: 'axis' },
      legend: { top: 4, data: ['总大小', '行数'] },
      grid: { left: 56, right: 56, top: 44, bottom: 42 },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: rows.map((row) => formatSampleTime(row.sampledAt))
      },
      yAxis: [
        {
          type: 'value',
          name: '大小',
          axisLabel: { formatter: (value: number) => formatBytes(value) }
        },
        {
          type: 'value',
          name: '行数',
          axisLabel: { formatter: (value: number) => compactNumber(value) }
        }
      ],
      series: [
        {
          name: '总大小',
          type: 'line',
          smooth: true,
          showSymbol: false,
          data: rows.map((row) => row.totalBytes)
        },
        {
          name: '行数',
          type: 'line',
          yAxisIndex: 1,
          smooth: true,
          showSymbol: false,
          data: rows.map((row) => row.rowCount ?? null)
        }
      ]
    }, { notMerge: true })
  })
}

function ensureChart(element: HTMLDivElement | undefined, chartRef: ShallowRef<ECharts | undefined>) {
  if (!element) return undefined
  if (!chartRef.value || chartRef.value.isDisposed()) {
    chartRef.value = init(element)
  }
  return chartRef.value
}

function disposeChart(chartRef: ShallowRef<ECharts | undefined>) {
  if (chartRef.value && !chartRef.value.isDisposed()) {
    chartRef.value.dispose()
  }
  chartRef.value = undefined
}

function tableKey(row: TableStorageSnapshotSummary) {
  return `${row.databaseRole}:${row.tableName}`
}

function databaseRoleLabel(role: MonitoredDatabaseRole) {
  return role === 'business' ? '业务库' : '记录库'
}

function databaseRoleColor(role: MonitoredDatabaseRole) {
  return role === 'business' ? 'blue' : 'purple'
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

function compactNumber(value?: number) {
  if (value === undefined || !Number.isFinite(value)) return '-'
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (absolute >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (absolute >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return `${Math.round(value)}`
}

function formatSampleTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

onMounted(() => {
  void loadData()
  window.addEventListener('resize', resizeHistoryChart)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resizeHistoryChart)
  for (const role of databaseSummaryRoles) {
    disposeChart(historyCharts[role])
  }
})

function resizeHistoryChart() {
  for (const role of databaseSummaryRoles) {
    const chart = historyCharts[role].value
    if (chart && !chart.isDisposed()) {
      chart.resize()
    }
  }
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

.table-monitor-toolbar {
  margin: 0;
}

.table-monitor-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.table-search {
  width: 240px;
}

.table-history-range {
  width: 380px;
}

.database-summary-grid :deep(.ant-col) {
  display: flex;
}

.history-grid :deep(.ant-col) {
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

.table-monitor-table :deep(.ant-table-row) {
  cursor: pointer;
}

.table-monitor-table :deep(.selected-monitor-row > td) {
  background: #eef6ff !important;
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
  height: 320px;
}

@media (max-width: 768px) {
  .table-monitor-filters,
  .table-search,
  .table-history-range {
    width: 100%;
  }

  .history-chart {
    height: 280px;
  }
}
</style>
