<template>
  <div class="table-monitor-page">
    <a-card class="page-card table-monitor-toolbar-card">
      <div class="page-toolbar table-monitor-toolbar">
        <div class="table-monitor-filters">
          <a-segmented v-model:value="selectedRole" :options="roleOptions" :disabled="loading || sampling" @change="handleFilterChange" />
          <a-input-search
            v-model:value="keyword"
            allow-clear
            class="table-search"
            placeholder="搜索表名"
            :disabled="loading || sampling"
            @change="handleFilterChange"
            @search="handleFilterChange"
          />
          <a-segmented v-model:value="historyLimit" :options="historyLimitOptions" :disabled="loading || sampling" @change="loadHistoryForCurrent" />
        </div>
        <div class="page-toolbar-actions">
          <a-button :loading="loading" @click="loadData">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
          <a-button type="primary" :loading="sampling" @click="sampleNow">
            <template #icon>
              <DatabaseOutlined />
            </template>
            立即采样
          </a-button>
        </div>
      </div>
    </a-card>

    <a-row :gutter="[16, 16]" class="database-summary-grid">
      <a-col v-for="database in overview?.databases ?? []" :key="database.databaseRole" :xs="24" :lg="12">
        <a-card class="database-summary-card">
          <div class="database-summary-head">
            <a-tag :color="databaseRoleColor(database.databaseRole)">{{ databaseRoleLabel(database.databaseRole) }}</a-tag>
            <span class="database-path">{{ database.databasePath }}</span>
          </div>
          <div class="database-summary-value">{{ formatBytes(totalDatabaseBytes(database)) }}</div>
          <div class="database-summary-meta">
            <span>主库 {{ formatBytes(database.fileBytes) }}</span>
            <span>WAL {{ formatBytes(database.walBytes) }}</span>
            <span>空闲 {{ formatBytes(database.freeBytes) }}</span>
            <span>表 {{ formatInteger(database.tableCount) }}</span>
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="page-card">
      <a-table
        :columns="columns"
        :custom-row="customTableRow"
        :data-source="filteredTables"
        :loading="loading || sampling"
        :pagination="{ pageSize: 50, showSizeChanger: true }"
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

    <a-card class="page-card history-card" :title="historyTitle">
      <div v-if="historyRows.length > 0" ref="historyChartRef" class="history-chart" />
      <div v-else class="page-empty-card">
        <a-empty description="暂无增长历史" />
      </div>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { DatabaseOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { init, type ECharts } from '@/lib/echarts'
import { formatDateTime } from '@/shared/formatters'
import type { DatabaseStorageSnapshotSummary, MonitoredDatabaseRole, TableStorageOverview, TableStorageSnapshotSummary } from '@/types/domain'

type RoleFilter = 'all' | MonitoredDatabaseRole

const roleOptions: Array<{ label: string; value: RoleFilter }> = [
  { label: '全部', value: 'all' },
  { label: '业务库', value: 'business' },
  { label: '记录库', value: 'records' }
]
const historyLimitOptions = [
  { label: '近 24 小时', value: 288 },
  { label: '近 3 天', value: 864 }
]
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
const sampling = ref(false)
const selectedRole = ref<RoleFilter>('all')
const keyword = ref('')
const historyLimit = ref(288)
const overview = ref<TableStorageOverview>()
const selectedTableKey = ref<string>()
const historyRows = ref<TableStorageSnapshotSummary[]>([])
const historyChartRef = ref<HTMLDivElement>()
const historyChart = shallowRef<ECharts>()

const filteredTables = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  return (overview.value?.tables ?? [])
    .filter((row) => selectedRole.value === 'all' || row.databaseRole === selectedRole.value)
    .filter((row) => !text || row.tableName.toLowerCase().includes(text))
})

const selectedTable = computed(() => {
  const current = selectedTableKey.value
  return filteredTables.value.find((row) => tableKey(row) === current) ?? filteredTables.value[0]
})

const historyTitle = computed(() => {
  const table = selectedTable.value
  return table ? `${databaseRoleLabel(table.databaseRole)} · ${table.tableName} 增长趋势` : '增长趋势'
})

async function loadData() {
  loading.value = true
  try {
    overview.value = await api.tableMonitor.overview()
    ensureSelectedTable()
    await loadHistoryForCurrent()
  } catch (error) {
    console.error(error)
    message.error('表监控加载失败')
  } finally {
    loading.value = false
  }
}

async function sampleNow() {
  sampling.value = true
  try {
    overview.value = await api.tableMonitor.sample()
    ensureSelectedTable()
    await loadHistoryForCurrent()
    message.success('表监控采样完成')
  } catch (error) {
    console.error(error)
    message.error('表监控采样失败')
  } finally {
    sampling.value = false
  }
}

function handleFilterChange() {
  ensureSelectedTable(true)
  void loadHistoryForCurrent()
}

async function loadHistoryForCurrent() {
  const table = selectedTable.value
  if (!table) {
    historyRows.value = []
    renderHistoryChart()
    return
  }
  selectedTableKey.value = tableKey(table)
  try {
    historyRows.value = await api.tableMonitor.history({
      databaseRole: table.databaseRole,
      tableName: table.tableName,
      limit: historyLimit.value
    })
  } catch (error) {
    console.error(error)
    message.error('增长历史加载失败')
    historyRows.value = []
  } finally {
    renderHistoryChart()
  }
}

function ensureSelectedTable(reset = false) {
  if (reset || !selectedTable.value) {
    selectedTableKey.value = filteredTables.value[0] ? tableKey(filteredTables.value[0]) : undefined
  }
}

function customTableRow(record: TableStorageSnapshotSummary) {
  return {
    onClick: () => {
      selectedTableKey.value = tableKey(record)
      void loadHistoryForCurrent()
    }
  }
}

function rowClassName(record: TableStorageSnapshotSummary) {
  return tableKey(record) === selectedTableKey.value ? 'selected-monitor-row' : ''
}

function renderHistoryChart() {
  void nextTick(() => {
    if (!historyRows.value.length) {
      disposeChart(historyChart)
      return
    }
    const chart = ensureChart(historyChartRef, historyChart)
    if (!chart) return
    chart.setOption({
      tooltip: { trigger: 'axis' },
      legend: { top: 4, data: ['总大小', '行数'] },
      grid: { left: 56, right: 56, top: 44, bottom: 42 },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: historyRows.value.map((row) => formatSampleTime(row.sampledAt))
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
          data: historyRows.value.map((row) => row.totalBytes)
        },
        {
          name: '行数',
          type: 'line',
          yAxisIndex: 1,
          smooth: true,
          showSymbol: false,
          data: historyRows.value.map((row) => row.rowCount ?? null)
        }
      ]
    }, { notMerge: true })
  })
}

function ensureChart(elementRef: Ref<HTMLDivElement | undefined>, chartRef: ShallowRef<ECharts | undefined>) {
  const element = elementRef.value
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

function totalDatabaseBytes(database: DatabaseStorageSnapshotSummary): number | undefined {
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
  disposeChart(historyChart)
})

function resizeHistoryChart() {
  if (historyChart.value && !historyChart.value.isDisposed()) {
    historyChart.value.resize()
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

.history-chart {
  width: 100%;
  height: 320px;
}

@media (max-width: 768px) {
  .table-monitor-filters,
  .table-search {
    width: 100%;
  }

  .history-chart {
    height: 280px;
  }
}
</style>
