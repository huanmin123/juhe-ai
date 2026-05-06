<template>
  <a-card class="page-card responsive-page-card">
    <UsageRecordsFilterToolbar
      v-model:keyword="accountNameFilter"
      v-model:result="resultFilter"
      v-model:status-code="statusCodeFilter"
      v-model:system-account-id="systemAccountFilter"
      :active-filter-count="activeFilterCount"
      :is-admin="isAdmin"
      :refresh-loading="loading"
      :result-options="resultOptions"
      :status-code-options="statusCodeOptions"
      :system-accounts="systemAccounts"
      @reset="resetFilters"
      @refresh="loadData"
      @system-account-change="loadData"
    />

    <ResponsiveDataList
      table-class="page-table usage-table"
      :columns="columns"
      :data-source="filteredRecords"
      row-key="id"
      :loading="loading"
      :scroll-x="isAdmin ? 2050 : 1870"
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-refresh="loadData"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="中转网关接入后开始产生使用记录。" />
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
      </template>
      <template #card="{ record }">
        <UsageRecordMobileCard :is-admin="isAdmin" :record="record" />
      </template>
    </ResponsiveDataList>
  </a-card>

</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import type { UsageRecordListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { authState } from '@/composables/useAuth'
import type { SystemAccountSummary, UsageRecordSummary } from '@/types/domain'
import { allSystemAccountsValue, matchesSystemAccountFilter, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import UsageRecordCostCell from './UsageRecordCostCell.vue'
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

const loading = ref(false)
const records = ref<UsageRecordSummary[]>([])
const accountNameFilter = ref('')
const resultFilter = ref<'all' | 'success' | 'failed'>('all')
const statusCodeFilter = ref<string>('')
const systemAccountFilter = ref(allSystemAccountsValue)
const systemAccounts = ref<SystemAccountSummary[]>([])
const isAdmin = authState.isAdmin
type UsageRecordSortField = NonNullable<UsageRecordListParams['sortBy']>
type TableSortOrder = 'ascend' | 'descend' | null

const sortState = ref<{ field: UsageRecordSortField; order: TableSortOrder }>({ field: 'createdAt', order: 'descend' })

const resultOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
] satisfies Array<{ label: string; value: 'all' | 'success' | 'failed' }>

const statusCodeOptions = computed(() => {
  const uniqueCodes = Array.from(
    new Set(
      records.value
        .map((record) => record.statusCode)
        .filter((value): value is number => typeof value === 'number')
    )
  ).sort((left, right) => left - right)

  return uniqueCodes.map((code) => ({ label: String(code), value: String(code) }))
})

const activeFilterCount = computed(() => {
  let count = 0
  if (resultFilter.value !== 'all') count += 1
  if (statusCodeFilter.value) count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  return count
})

const filteredRecords = computed(() => {
  const nameTerm = accountNameFilter.value.trim().toLowerCase()
  return records.value.filter((record) => {
    if (!matchesSystemAccountFilter(record, systemAccountFilter.value, isAdmin.value)) return false
    if (nameTerm) {
      const accountText = `${record.accountName ?? ''} ${record.accountId ?? ''}`.toLowerCase()
      if (!accountText.includes(nameTerm)) return false
    }
    if (resultFilter.value === 'success' && !record.success) return false
    if (resultFilter.value === 'failed' && record.success) return false
    if (statusCodeFilter.value && String(record.statusCode ?? '') !== statusCodeFilter.value) return false
    return true
  })
})

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: 'AI账户名称', dataIndex: 'accountName', key: 'account', width: 170 }
  ]
  if (isAdmin.value) {
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
    { title: 'IP', dataIndex: 'clientIp', key: 'clientIp', width: 130 }
  )
  return baseColumns
})

function resetFilters(): void {
  accountNameFilter.value = ''
  resultFilter.value = 'all'
  statusCodeFilter.value = ''
  systemAccountFilter.value = allSystemAccountsValue
  void loadData()
}

async function handleTableChange(_pagination: unknown, _filters: unknown, sorter: unknown): Promise<void> {
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

async function loadData() {
  loading.value = true
  try {
    const systemAccountId = selectedSystemAccountId(systemAccountFilter.value, isAdmin.value)
    const sortOrder = sortState.value.order === 'ascend' ? 'asc' : 'desc'
    const [recordList, systemAccountList] = await Promise.all([
      api.usageRecords.list({ systemAccountId, sortBy: sortState.value.field, sortOrder }),
      isAdmin.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    records.value = recordList
    systemAccounts.value = systemAccountList
  } catch (error) {
    console.error(error)
    message.error('加载使用记录失败')
  } finally {
    loading.value = false
  }
}

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




