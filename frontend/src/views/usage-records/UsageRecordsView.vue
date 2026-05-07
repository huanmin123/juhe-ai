<template>
  <a-card class="page-card responsive-page-card">
    <UsageRecordsFilterToolbar
      v-model:keyword="accountNameFilter"
      v-model:result="resultFilter"
      v-model:status-code="statusCodeFilter"
      v-model:system-account-id="systemAccountFilter"
      :active-filter-count="activeFilterCount"
      :is-management-view="isManagementView"
      :refresh-loading="loading"
      :result-options="resultOptions"
      :system-accounts="systemAccounts"
      @reset="resetFilters"
      @refresh="refreshRecords"
      @search="applyFilters"
      @system-account-change="applyFilters"
    />

    <ResponsiveDataList
      table-class="page-table usage-table"
      :columns="columns"
      :data-source="filteredRecords"
      :mobile-data-source="mobileRecords"
      row-key="id"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :scroll-x="isManagementView ? 2050 : 1870"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前条件下没有使用记录。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'apiKey'">
          <span :class="record.apiKeyName ? 'name-cell' : 'muted-cell'">{{ displayName(record.apiKeyName, record.apiKeyId) }}</span>
        </template>
        <template v-else-if="column.key === 'group'">
          <span :class="record.groupName ? 'name-cell' : 'muted-cell'">{{ displayName(record.groupName, record.groupId) }}</span>
        </template>
        <template v-else-if="column.key === 'account'">
          <span :class="record.accountName || record.accountId ? 'name-cell' : 'muted-cell'">{{ accountDisplayText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">
            {{ usageRecordSystemAccountText(record) }}
          </span>
        </template>
        <template v-else-if="column.key === 'clientIp'">
          <span :class="record.clientIp ? 'ip-cell' : 'muted-cell'">{{ record.clientIp ?? '-' }}</span>
        </template>
        <template v-else-if="column.key === 'endpoint'">
          <span :class="record.endpoint ? 'endpoint-cell' : 'muted-cell'">{{ formatEndpoint(record.endpoint) }}</span>
        </template>
        <template v-else-if="column.key === 'model'">
          <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
          <span v-else class="muted-cell">-</span>
        </template>
        <template v-else-if="column.key === 'stream'">
          <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'statusCode'">
          <a-tag :color="statusCodeColor(record)">{{ statusCodeText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'success'">
          <UsageRecordResultCell :record="record" />
        </template>
        <template v-else-if="column.key === 'tokens'">
          <div class="token-cell">
            <span>输入 {{ formatTokens(record.inputTokens) }}</span>
            <span>输出 {{ formatTokens(record.outputTokens) }}</span>
            <span>缓存 {{ formatTokens(record.cacheReadTokens) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'cost'">
          <UsageRecordCostCell :record="record" />
        </template>
        <template v-else-if="column.key === 'firstTokenMs'">
          <span>{{ formatDuration(record.firstTokenMs) }}</span>
        </template>
        <template v-else-if="column.key === 'durationMs'">
          <span>{{ formatDuration(record.durationMs) }}</span>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-button type="link" size="small" @click="openDetail(record)">详情</a-button>
        </template>
      </template>
      <template #card="{ record }">
        <UsageRecordMobileCard :is-management-view="isManagementView" :record="record" @detail="openDetail(record)" />
      </template>
    </ResponsiveDataList>
  </a-card>

  <UsageRecordDetailDrawer
    v-model:open="detailOpen"
    :is-management-view="isManagementView"
    :record="selectedRecord"
    @close="closeDetail"
  />

</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import type { UsageRecordListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { SystemAccountSummary, UsageRecordSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import UsageRecordCostCell from './UsageRecordCostCell.vue'
import UsageRecordDetailDrawer from './UsageRecordDetailDrawer.vue'
import UsageRecordMobileCard from './UsageRecordMobileCard.vue'
import UsageRecordResultCell from './UsageRecordResultCell.vue'
import UsageRecordsFilterToolbar from './UsageRecordsFilterToolbar.vue'
import {
  accountDisplayText,
  displayName,
  formatDateTime,
  formatDuration,
  formatEndpoint,
  formatTokens,
  statusCodeColor,
  statusCodeText,
  usageRecordSystemAccountText
} from './usageRecordFormatters'

type UsageRecordSortField = NonNullable<UsageRecordListParams['sortBy']>
type TableSortOrder = 'ascend' | 'descend' | null
type UsageRecordsPageState = {
  accountNameFilter: string
  pagination: { current: number; pageSize: number }
  resultFilter: 'all' | 'success' | 'failed'
  sortState: { field: UsageRecordSortField; order: TableSortOrder }
  statusCodeFilter: string
  systemAccountFilter: string
}

const pageSize = 20
const defaultUsageRecordsPageState = (): UsageRecordsPageState => ({
  accountNameFilter: '',
  pagination: { current: 1, pageSize },
  resultFilter: 'all',
  sortState: { field: 'createdAt', order: 'descend' },
  statusCodeFilter: '',
  systemAccountFilter: allSystemAccountsValue
})
const pageStateCache = usePageStateCache<UsageRecordsPageState>(undefined, defaultUsageRecordsPageState, { version: 2 })
const initialPageState = pageStateCache.read()

const loading = ref(false)
const mobileLoadingMore = ref(false)
const records = ref<UsageRecordSummary[]>([])
const detailOpen = ref(false)
const selectedRecord = ref<UsageRecordSummary>()
const accountNameFilter = ref(initialPageState.accountNameFilter)
const resultFilter = ref<'all' | 'success' | 'failed'>(initialPageState.resultFilter)
const statusCodeFilter = ref<string>(initialPageState.statusCodeFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccounts = ref<SystemAccountSummary[]>([])
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const sortState = ref<{ field: UsageRecordSortField; order: TableSortOrder }>(initialPageState.sortState)
const pagination = reactive({ current: initialPageState.pagination.current, pageSize: initialPageState.pagination.pageSize, total: 0 })

const resultOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
] satisfies Array<{ label: string; value: 'all' | 'success' | 'failed' }>

const activeFilterCount = computed(() => {
  let count = 0
  if (accountNameFilter.value.trim()) count += 1
  if (resultFilter.value !== 'all') count += 1
  if (statusCodeFilter.value) count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  return count
})

const filteredRecords = computed(() => records.value)
const mobileRecords = computed(() => records.value)
const mobileHasMore = computed(() => records.value.length < pagination.total)
const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 条使用记录`
}))

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: 'AI账户名称', dataIndex: 'accountName', key: 'account', width: 170 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '接口', dataIndex: 'endpoint', key: 'endpoint', width: 150 },
    { title: '模型', dataIndex: 'model', key: 'model', width: 170 },
    { title: '类型', key: 'stream', width: 90 },
    { title: '状态', dataIndex: 'statusCode', key: 'statusCode', width: 110 },
    { title: '结果', key: 'success', width: 90 },
    { title: 'Tokens', key: 'tokens', width: 150 },
    { title: '成本', key: 'cost', width: 110, sorter: true, sortOrder: columnSortOrder('costUsd') },
    { title: '首 token', dataIndex: 'firstTokenMs', key: 'firstTokenMs', width: 100, sorter: true, sortOrder: columnSortOrder('firstTokenMs') },
    { title: '总耗时', dataIndex: 'durationMs', key: 'durationMs', width: 100, sorter: true, sortOrder: columnSortOrder('durationMs') },
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 180, sorter: true, sortOrder: columnSortOrder('createdAt') },
    { title: 'API Key', dataIndex: 'apiKeyName', key: 'apiKey', width: 170 },
    { title: '分组', dataIndex: 'groupName', key: 'group', width: 150 },
    { title: 'IP', dataIndex: 'clientIp', key: 'clientIp', width: 130 },
    { title: '操作', key: 'actions', width: 90, fixed: 'right' }
  )
  return baseColumns
})

function openDetail(record: UsageRecordSummary): void {
  selectedRecord.value = record
  detailOpen.value = true
}

function closeDetail(): void {
  detailOpen.value = false
}

function resetFilters(): void {
  const defaults = defaultUsageRecordsPageState()
  accountNameFilter.value = defaults.accountNameFilter
  resultFilter.value = defaults.resultFilter
  statusCodeFilter.value = defaults.statusCodeFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  sortState.value = defaults.sortState
  pagination.current = defaults.pagination.current
  pagination.pageSize = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadData()
}

async function handleTableChange(paginationInfo: unknown, _filters: unknown, sorter: unknown): Promise<void> {
  updatePaginationFromTable(paginationInfo)
  const normalized = normalizeTableSorter(sorter)
  sortState.value = normalized ?? { field: 'createdAt', order: 'descend' }
  await loadData()
}

function columnSortOrder(field: UsageRecordSortField): TableSortOrder {
  return sortState.value.field === field ? sortState.value.order : null
}

function normalizeTableSorter(sorter: unknown): { field: UsageRecordSortField; order: TableSortOrder } | undefined {
  const item = Array.isArray(sorter) ? sorter[0] : sorter
  if (!item || typeof item !== 'object') return undefined
  const record = item as Record<string, unknown>
  const field = sortFieldFromColumn(record.columnKey ?? record.field)
  const order = record.order === 'ascend' || record.order === 'descend' ? record.order : null
  return field && order ? { field, order } : undefined
}

function sortFieldFromColumn(value: unknown): UsageRecordSortField | undefined {
  if (value === 'cost') return 'costUsd'
  if (value === 'costUsd' || value === 'firstTokenMs' || value === 'durationMs' || value === 'createdAt') return value
  return undefined
}

function updatePaginationFromTable(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
}

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function refreshRecords(): void {
  pagination.current = 1
  void loadData()
}

async function loadMoreMobileRecords(): Promise<void> {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await loadData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileRecords(): Promise<void> {
  pagination.current = 1
  await loadData()
}

async function loadData(options: { append?: boolean; quiet?: boolean } = {}): Promise<void> {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const [recordList, systemAccountList] = await fetchRecords()
    pagination.current = recordList.page
    pagination.pageSize = recordList.pageSize
    pagination.total = recordList.total
    records.value = options.append ? [...records.value, ...recordList.items] : recordList.items
    systemAccounts.value = systemAccountList
  } catch (error) {
    console.error(error)
    message.error('加载使用记录失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

async function fetchRecords() {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(systemAccountFilter.value) : undefined
  const sortOrder = sortState.value.order === 'ascend' ? 'asc' : 'desc'
  const params: UsageRecordListParams = {
    page: pagination.current,
    pageSize: pagination.pageSize,
    accountKeyword: accountNameFilter.value.trim() || undefined,
    result: resultFilter.value,
    statusCode: normalizedStatusCode(statusCodeFilter.value),
    systemAccountId,
    sortBy: sortState.value.field,
    sortOrder
  }
  return Promise.all([
    isManagementView.value
      ? api.usageRecords.list(params)
      : api.myUsageRecords.list(params),
    isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
  ])
}

function normalizedStatusCode(value: string): number | undefined {
  const text = value.trim()
  if (!text) return undefined
  const statusCode = Number(text)
  return Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 599 ? statusCode : undefined
}

function snapshotPageState(): UsageRecordsPageState {
  return {
    accountNameFilter: accountNameFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    resultFilter: resultFilter.value,
    sortState: sortState.value,
    statusCodeFilter: statusCodeFilter.value,
    systemAccountFilter: systemAccountFilter.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)
</script>

<style scoped>
.usage-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.usage-table :deep(.ant-empty) {
  margin: 12px 0;
}

.token-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  color: #475569;
  font-size: 12px;
  line-height: 1.3;
}

.name-cell {
  display: inline-block;
  max-width: 160px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.ip-cell {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.endpoint-cell {
  display: inline-block;
  max-width: 140px;
  overflow: hidden;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

</style>




