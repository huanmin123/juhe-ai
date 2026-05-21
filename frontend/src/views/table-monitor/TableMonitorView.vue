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
            清理使用记录
          </a-button>
        </template>
      </ResponsiveListToolbar>
    </a-card>

    <a-modal
      v-model:open="cleanupModalOpen"
      title="清理使用记录"
      width="560px"
      ok-text="提交清理"
      cancel-text="取消"
      :confirm-loading="cleanupSubmitting"
      :ok-button-props="{ danger: true, disabled: cleanupSubmitting || !cleanupCutoffAt }"
      @ok="submitUsageRecordsCleanup"
    >
      <a-alert
        show-icon
        type="warning"
        message="清理最近 1 天之前的使用记录"
        description="系统会提交后台任务分批清理所选截止时间之前的 usage_records，最近 1 天强制保留。删除后 SQLite 文件大小不会立即变小，释放出的空闲页会留在库内供后续新增数据复用；只有需要归还磁盘时，才需要停服执行 VACUUM。"
      />
      <a-form class="cleanup-form" layout="vertical">
        <a-form-item label="清理这个时间之前的 usage_records" required>
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

    <a-drawer v-model:open="apiKeyCleanupDrawerOpen" width="min(960px, 96vw)" title="API Key 删除清理队列" :body-style="{ padding: '18px' }">
      <div class="cleanup-target-drawer">
        <a-alert
          show-icon
          type="info"
          message="仅展示待清理目标队列"
          description="失败和阻塞目标会优先展示；清理仍由后台 worker 分批执行，这里不扫描使用记录或审计明细。"
        />
        <div class="cleanup-target-toolbar">
          <span>最多展示 100 个待处理目标</span>
          <a-button size="small" :loading="apiKeyCleanupTargetsLoading" @click="loadApiKeyCleanupTargets">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
        <ResponsiveDataList
          size="small"
          table-class="cleanup-target-table"
          :columns="apiKeyCleanupTargetColumns"
          :data-source="apiKeyCleanupTargets"
          row-key="apiKeyId"
          :loading="apiKeyCleanupTargetsLoading"
          :pagination="false"
          :scroll-x="1100"
          :mobile-breakpoint="820"
          :lock-body-scroll="false"
        >
          <template #emptyText>
            <a-empty description="当前没有待清理目标。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'state'">
              <a-tag :color="apiKeyCleanupTargetStateColor(record)">{{ apiKeyCleanupTargetStateText(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'apiKeyId'">
              <span class="mono-cell">{{ record.apiKeyId }}</span>
            </template>
            <template v-else-if="column.key === 'systemAccountId'">
              <span class="mono-cell">{{ record.systemAccountId }}</span>
            </template>
            <template v-else-if="column.key === 'attemptCount'">
              {{ formatInteger(record.attemptCount) }}
            </template>
            <template v-else-if="column.key === 'createdAt'">
              {{ formatDateTime(record.createdAt) }}
            </template>
            <template v-else-if="column.key === 'lastAttemptAt'">
              {{ formatDateTime(record.lastAttemptAt) }}
            </template>
            <template v-else-if="column.key === 'reason'">
              <span class="cleanup-target-reason">{{ apiKeyCleanupTargetReason(record) }}</span>
            </template>
          </template>
          <template #card="{ record }">
            <article class="cleanup-target-mobile-card">
              <div class="cleanup-target-mobile-head">
                <a-tag :color="apiKeyCleanupTargetStateColor(record)">{{ apiKeyCleanupTargetStateText(record) }}</a-tag>
                <span>尝试 {{ formatInteger(record.attemptCount) }} 次</span>
              </div>
              <span class="mono-cell">API Key：{{ record.apiKeyId }}</span>
              <span class="mono-cell">系统账户：{{ record.systemAccountId }}</span>
              <span>最早登记：{{ formatDateTime(record.createdAt) }}</span>
              <span>最近尝试：{{ formatDateTime(record.lastAttemptAt) }}</span>
              <span>原因：{{ apiKeyCleanupTargetReason(record) }}</span>
            </article>
          </template>
        </ResponsiveDataList>
      </div>
    </a-drawer>

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

    <a-card class="page-card maintenance-summary-card">
      <div class="maintenance-summary-head">
        <div>
          <span class="maintenance-summary-title">API Key 删除清理</span>
          <span class="maintenance-summary-description">删除后关联记录和统计由 worker 分批处理</span>
        </div>
        <div class="maintenance-summary-actions">
          <a-tag :color="apiKeyCleanupStateColor">{{ apiKeyCleanupStateText }}</a-tag>
          <a-button size="small" :disabled="!apiKeyCleanupSummary || apiKeyCleanupSummary.pendingTargets <= 0" @click="openApiKeyCleanupTargets">
            <template #icon>
              <UnorderedListOutlined />
            </template>
            查看队列
          </a-button>
        </div>
      </div>
      <div class="maintenance-summary-body">
        <div class="maintenance-summary-count">
          <span>待清理目标</span>
          <strong>{{ formatInteger(apiKeyCleanupSummary?.pendingTargets ?? 0) }}</strong>
        </div>
        <div class="maintenance-summary-meta">
          <span>阻塞 {{ formatInteger(apiKeyCleanupSummary?.blockedTargets ?? 0) }}</span>
          <span>失败 {{ formatInteger(apiKeyCleanupSummary?.failedTargets ?? 0) }}</span>
          <span>最早登记 {{ formatDateTime(apiKeyCleanupSummary?.oldestCreatedAt) }}</span>
          <span>最近尝试 {{ formatDateTime(apiKeyCleanupSummary?.lastAttemptAt) }}</span>
        </div>
      </div>
    </a-card>

    <a-row :gutter="[16, 16]" class="history-grid">
      <a-col v-for="item in historyCards" :key="item.role" :xs="24" :xl="12">
        <a-card class="page-card history-card" :title="item.title">
          <DeferredRender
            v-if="item.rows.length > 0"
            :active="pageActive"
            :delay-frames="2"
            :min-height="320"
            :reset-key="`${item.role}:${item.rows.length}:${selectedTableKeys[item.role] || ''}`"
            reset-on-deactivate
            @ready="renderHistoryChart(item.role)"
          >
            <div :ref="item.setChartRef" class="history-chart" />
          </DeferredRender>
          <div v-else class="page-empty-card">
            <a-empty :description="`${databaseRoleLabel(item.role)}暂无增长历史`" />
          </div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="page-card">
      <DeferredRender :active="pageActive" :delay-frames="1" :min-height="360" reset-on-deactivate>
        <ResponsiveDataList
          :columns="columns"
          :data-source="filteredTables"
          :loading="loading"
          :pagination="tablePagination"
          :row-key="tableKey"
          :scroll-x="1180"
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
              <button class="table-name-button" type="button" @click="selectTable(record)">
                <span class="mono-cell">{{ record.tableName }}</span>
                <a-tag v-if="isSelectedTable(record)" color="blue">趋势</a-tag>
              </button>
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
            <button class="table-monitor-mobile-card" type="button" @click="selectTable(record)">
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
              <a-tag v-if="isSelectedTable(record)" color="blue">当前趋势表</a-tag>
            </button>
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
import type { ShallowRef } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { DeleteOutlined, ReloadOutlined, UnorderedListOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import DeferredRender from '@/components/DeferredRender.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { disposeChart, ensureChartFromElement, resizeEcharts, useEchartsPageLifecycle, type ECharts } from '@/composables/useEcharts'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatServerDateTimeInput } from '@/shared/formatters'
import type { ApiKeyRecordCleanupQueueTarget, DatabaseStorageSnapshotSummary, MonitoredDatabaseRole, TableStorageOverview, TableStorageSnapshotSummary, UsageRecordsCleanupResult } from '@/types/domain'

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

const apiKeyCleanupTargetColumns = [
  { title: '状态', key: 'state', width: 96, fixed: 'left' },
  { title: 'API Key ID', key: 'apiKeyId', width: 220 },
  { title: '系统账户 ID', key: 'systemAccountId', width: 220 },
  { title: '尝试', key: 'attemptCount', align: 'right', width: 90 },
  { title: '最早登记', key: 'createdAt', width: 180 },
  { title: '最近尝试', key: 'lastAttemptAt', width: 180 },
  { title: '原因', key: 'reason', width: 320 }
]

const loading = ref(false)
const keyword = ref('')
const historyRange = ref<[Dayjs, Dayjs] | undefined>(defaultHistoryRange())
const overview = ref<TableStorageOverview>()
const cleanupModalOpen = ref(false)
const cleanupSubmitting = ref(false)
const cleanupCutoffAt = ref<Dayjs | undefined>(defaultCleanupCutoffAt())
const cleanupResult = ref<UsageRecordsCleanupResult>()
const apiKeyCleanupDrawerOpen = ref(false)
const apiKeyCleanupTargetsLoading = ref(false)
const apiKeyCleanupTargets = ref<ApiKeyRecordCleanupQueueTarget[]>([])
const databaseSummaryRoles: MonitoredDatabaseRole[] = ['business', 'records']
const historyChartPointLimit = 720
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
const { pageActive } = useEchartsPageLifecycle({
  renderCharts: renderHistoryCharts,
  resizeCharts: resizeHistoryChart,
  disposeCharts: disposeHistoryCharts,
  onMounted: loadData,
  renderOnActivated: 'always'
})

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
const apiKeyCleanupSummary = computed(() => overview.value?.recordMaintenance?.apiKeyRecordCleanup)
const apiKeyCleanupStateText = computed(() => {
  const summary = apiKeyCleanupSummary.value
  if (!summary || summary.pendingTargets <= 0) return '无待清理'
  if (summary.failedTargets > 0) return '有失败'
  if (summary.blockedTargets > 0) return '等待重试'
  return '清理中'
})
const apiKeyCleanupStateColor = computed(() => {
  const summary = apiKeyCleanupSummary.value
  if (!summary || summary.pendingTargets <= 0) return 'green'
  if (summary.failedTargets > 0) return 'red'
  if (summary.blockedTargets > 0) return 'orange'
  return 'blue'
})

async function openApiKeyCleanupTargets() {
  apiKeyCleanupDrawerOpen.value = true
  await loadApiKeyCleanupTargets()
}

async function loadApiKeyCleanupTargets() {
  apiKeyCleanupTargetsLoading.value = true
  try {
    apiKeyCleanupTargets.value = await api.tableMonitor.apiKeyCleanupTargets({ limit: 100 })
  } catch (error) {
    console.error(error)
    message.error('API Key 删除清理队列加载失败')
  } finally {
    apiKeyCleanupTargetsLoading.value = false
  }
}

function apiKeyCleanupTargetStateText(record: ApiKeyRecordCleanupQueueTarget) {
  if (record.lastErrorMessage) return '失败'
  if (record.lastBlockedReason) return '阻塞'
  return '等待'
}

function apiKeyCleanupTargetStateColor(record: ApiKeyRecordCleanupQueueTarget) {
  if (record.lastErrorMessage) return 'red'
  if (record.lastBlockedReason) return 'orange'
  return 'blue'
}

function apiKeyCleanupTargetReason(record: ApiKeyRecordCleanupQueueTarget) {
  return record.lastErrorMessage || record.lastBlockedReason || '等待 worker 执行清理'
}

const filteredTables = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  return (overview.value?.tables ?? [])
    .filter((row) => matchesTableNameKeyword(row.tableName, text))
})
const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (historyRange.value && !isDefaultHistoryRange(historyRange.value)) count += 1
  return count
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
    return result.eligibleRows && result.eligibleRows > 0
      ? `后台清理任务已提交，首批预计清理 ${formatInteger(result.eligibleRows)} 条`
      : '后台清理任务已提交'
  }
  if (result.blockedReason) return '本次未清理'
  return result.deletedRows > 0
    ? `已清理 ${formatInteger(result.deletedRows)} 条使用记录`
    : '没有可清理的使用记录'
})

const cleanupResultDescription = computed(() => {
  const result = cleanupResult.value
  if (!result) return ''
  if (result.blockedReason) return result.blockedReason
  if (result.queued) {
    const details = [
      `截止时间：${formatDateTime(result.cutoffAt)}`,
      result.eligibleRows !== undefined ? `首批可清理：${formatInteger(result.eligibleRows)} 条` : undefined,
      result.submittedAt ? `提交时间：${formatDateTime(result.submittedAt)}` : undefined,
      result.jobId ? `任务：${result.jobId}` : undefined,
      'worker 会在后台分批清理，稍后刷新表监控可查看记录库变化。'
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

function openCleanupModal() {
  cleanupResult.value = undefined
  cleanupCutoffAt.value = cleanupCutoffAt.value ?? defaultCleanupCutoffAt()
  cleanupModalOpen.value = true
}

async function submitUsageRecordsCleanup() {
  const cutoffAt = formatServerDateTimeInput(cleanupCutoffAt.value)
  if (!cutoffAt) {
    message.warning('请选择清理截止时间')
    return
  }
  if (cleanupCutoffAt.value?.isAfter(latestAllowedCleanupCutoff())) {
    message.warning('不能清理最近 1 天内的使用记录')
    return
  }
  cleanupSubmitting.value = true
  try {
    const result = await api.tableMonitor.cleanupUsageRecords({
      cutoffAt,
      batchSize: 10000,
      maxBatches: 100
    })
    cleanupResult.value = result
    if (result.queued) {
      message.success(result.eligibleRows && result.eligibleRows > 0 ? `使用记录清理任务已提交后台，首批预计 ${formatInteger(result.eligibleRows)} 条` : '使用记录清理任务已提交后台')
    } else if (result.deletedRows > 0) {
      message.success(`已清理 ${formatInteger(result.deletedRows)} 条使用记录`)
      await loadData()
    } else if (result.blockedReason) {
      message.warning(result.blockedReason)
    } else {
      message.info('没有可清理的使用记录')
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '清理使用记录失败'))
  } finally {
    cleanupSubmitting.value = false
  }
}

function handleFilterChange() {
  const changedRoles = ensureSelectedTable()
  if (changedRoles.length > 0) {
    void loadHistoryForRoles(changedRoles)
  }
}

function resetFilters() {
  keyword.value = ''
  historyRange.value = defaultHistoryRange()
  selectedTableKeys.value = {
    business: undefined,
    records: undefined
  }
  void loadData()
}

async function loadAllHistory() {
  await Promise.all(databaseSummaryRoles.map((role) => loadHistoryForRole(role)))
}

async function loadHistoryForRoles(roles: MonitoredDatabaseRole[]) {
  await Promise.all(roles.map((role) => loadHistoryForRole(role)))
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
      limit: historyChartPointLimit
    })
  } catch (error) {
    console.error(error)
    message.error(`${databaseRoleLabel(role)}增长历史加载失败`)
    historyRowsByRole.value[role] = []
  } finally {
    renderHistoryChart(role)
  }
}

function ensureSelectedTable(reset = false): MonitoredDatabaseRole[] {
  const changedRoles: MonitoredDatabaseRole[] = []
  for (const role of databaseSummaryRoles) {
    const previousKey = selectedTableKeys.value[role]
    const previousVisible = !reset && previousKey
      ? filteredTables.value.some((row) => row.databaseRole === role && tableKey(row) === previousKey)
      : false
    const firstTable = firstTableForRole(role)
    const nextKey = previousVisible ? previousKey : firstTable ? tableKey(firstTable) : undefined
    if (previousKey !== nextKey) {
      changedRoles.push(role)
    }
    selectedTableKeys.value[role] = nextKey
  }
  return changedRoles
}

function selectedTableForRole(role: MonitoredDatabaseRole): TableStorageSnapshotSummary | undefined {
  const current = selectedTableKeys.value[role]
  return filteredTables.value.find((row) => row.databaseRole === role && tableKey(row) === current) ?? firstTableForRole(role)
}

function firstTableForRole(role: MonitoredDatabaseRole): TableStorageSnapshotSummary | undefined {
  return filteredTables.value.find((row) => row.databaseRole === role)
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
  return dayjs().subtract(1, 'day')
}

function range(start: number, end: number) {
  const output: number[] = []
  for (let value = Math.max(0, start); value < end; value += 1) {
    output.push(value)
  }
  return output
}

function selectTable(record: TableStorageSnapshotSummary) {
  selectedTableKeys.value[record.databaseRole] = tableKey(record)
  void loadHistoryForRole(record.databaseRole)
}

function isSelectedTable(record: TableStorageSnapshotSummary) {
  return tableKey(record) === selectedTableKeys.value[record.databaseRole]
}

function renderHistoryChart(role: MonitoredDatabaseRole) {
  if (!pageActive.value) return
  void nextTick(() => {
    if (!pageActive.value) return
    const rows = historyRowsByRole.value[role]
    if (!rows.length) {
      disposeChart(historyCharts[role])
      return
    }
    const chart = ensureChartFromElement(historyChartElements[role], historyCharts[role])
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

function renderHistoryCharts() {
  for (const role of databaseSummaryRoles) {
    renderHistoryChart(role)
  }
}

function disposeHistoryCharts() {
  for (const role of databaseSummaryRoles) {
    disposeChart(historyCharts[role])
  }
}

function resizeHistoryChart() {
  resizeEcharts(databaseSummaryRoles.map((role) => historyCharts[role].value))
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

.maintenance-summary-card {
  border: 1px solid #e8edf5;
  border-radius: 14px;
}

.maintenance-summary-card :deep(.ant-card-body) {
  padding: 18px 20px;
}

.maintenance-summary-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.maintenance-summary-head > div {
  min-width: 0;
}

.maintenance-summary-actions {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.maintenance-summary-title {
  display: block;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
  line-height: 1.4;
}

.maintenance-summary-description {
  display: block;
  margin-top: 4px;
  color: #64748b;
  font-size: 13px;
  line-height: 1.5;
}

.maintenance-summary-body {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px 20px;
  margin-top: 18px;
}

.maintenance-summary-count {
  display: grid;
  gap: 4px;
  color: #64748b;
  font-size: 13px;
}

.maintenance-summary-count strong {
  color: #0f172a;
  font-size: 28px;
  font-weight: 800;
  line-height: 1.1;
}

.maintenance-summary-meta {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px 14px;
  min-width: 0;
  color: #64748b;
  font-size: 13px;
}

.cleanup-target-drawer {
  display: grid;
  gap: 14px;
}

.cleanup-target-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: #64748b;
  font-size: 13px;
}

.cleanup-target-reason {
  display: block;
  min-width: 0;
  max-width: 320px;
  overflow-wrap: anywhere;
  color: #334155;
}

.cleanup-target-mobile-card {
  display: grid;
  gap: 8px;
  padding: 12px;
  color: #334155;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
}

.cleanup-target-mobile-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: #64748b;
  font-size: 13px;
}

.table-monitor-table-placeholder {
  display: flex;
  min-height: 360px;
  align-items: center;
  justify-content: center;
}

.table-name-button {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 8px;
  padding: 0;
  color: #0f172a;
  text-align: left;
  background: transparent;
  border: 0;
  cursor: pointer;
}

.table-name-button:hover {
  color: #1677ff;
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
  height: 320px;
}

@media (max-width: 768px) {
  .table-history-range {
    width: 100%;
  }

  .history-chart {
    height: 280px;
  }

  .maintenance-summary-head {
    align-items: stretch;
    flex-direction: column;
  }

  .maintenance-summary-actions {
    justify-content: flex-start;
  }

  .maintenance-summary-body {
    align-items: flex-start;
  }

  .cleanup-target-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .maintenance-summary-meta {
    justify-content: flex-start;
  }
}
</style>
