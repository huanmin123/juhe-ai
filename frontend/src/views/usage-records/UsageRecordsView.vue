<template>
  <a-card class="page-card">
    <div class="usage-toolbar">
      <div class="usage-toolbar-filters">
        <a-input
          v-model:value="accountNameFilter"
          allow-clear
          class="filter-input"
          placeholder="按账户名称筛选"
        />
        <a-select
          v-model:value="resultFilter"
          class="filter-select"
          :options="resultOptions"
        />
        <a-select
          v-model:value="statusCodeFilter"
          allow-clear
          class="filter-select"
          :options="statusCodeOptions"
          placeholder="状态码"
        />
        <a-button @click="resetFilters">重置</a-button>
      </div>
      <div class="page-toolbar-actions">
        <a-button :loading="loading" @click="loadData">刷新</a-button>
      </div>
    </div>

    <a-table
      class="page-table usage-table"
      size="middle"
      :columns="columns"
      :data-source="filteredRecords"
      row-key="id"
      :loading="loading"
      :scroll="{ x: isAdmin ? 2030 : 1850 }"
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
          <span :class="record.accountName ? 'name-cell' : 'muted-cell'">{{ displayName(record.accountName, record.accountId) }}</span>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">
            {{ record.systemAccountName || record.systemAccountId || '-' }}
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
          <a-tag :color="statusCodeColor(record.statusCode)">{{ record.statusCode ?? '-' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'success'">
          <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'tokens'">
          <div class="token-cell">
            <span>输入 {{ formatTokens(record.inputTokens) }}</span>
            <span>输出 {{ formatTokens(record.outputTokens) }}</span>
            <span>缓存 {{ formatTokens(record.cacheReadTokens) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'cost'">
          <span class="cost-cell-wrap">
            <span class="cost-cell">{{ formatCost(record.costUsd) }}</span>
            <a-popover v-if="record.costBreakdown" trigger="hover" placement="right" overlay-class-name="cost-popover">
              <template #content>
                <div class="cost-detail-panel">
                  <div class="cost-detail-title">成本明细</div>
                  <div class="cost-detail-row">
                    <span>输入成本</span>
                    <span class="cost-detail-value">{{ formatCost(record.costBreakdown.inputCostUsd) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>输出成本</span>
                    <span class="cost-detail-value">{{ formatCost(record.costBreakdown.outputCostUsd) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>输入单价</span>
                    <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.inputUsdPer1M) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>输出单价</span>
                    <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.outputUsdPer1M) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>缓存读取成本</span>
                    <span class="cost-detail-value">{{ formatCost(record.costBreakdown.cacheReadCostUsd) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>账户计费</span>
                    <span class="cost-detail-value">{{ formatCost(record.costBreakdown.accountChargeUsd) }}</span>
                  </div>
                  <div class="cost-detail-row">
                    <span>倍率</span>
                    <span class="cost-detail-value">{{ record.costBreakdown.multiplier }}x</span>
                  </div>
                </div>
              </template>
              <InfoCircleOutlined class="cost-detail-icon" />
            </a-popover>
          </span>
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
    </a-table>
  </a-card>

</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { UsageRecordSummary } from '@/types/domain'

const loading = ref(false)
const records = ref<UsageRecordSummary[]>([])
const accountNameFilter = ref('')
const resultFilter = ref<'all' | 'success' | 'failed'>('all')
const statusCodeFilter = ref<string>('')
const isAdmin = authState.isAdmin

const resultOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
]

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

const filteredRecords = computed(() => {
  const nameTerm = accountNameFilter.value.trim().toLowerCase()
  return records.value.filter((record) => {
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
    { title: '账户', dataIndex: 'accountName', key: 'account', width: 170 }
  ]
  if (isAdmin.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '接口', dataIndex: 'endpoint', key: 'endpoint', width: 150 },
    { title: '模型', dataIndex: 'model', key: 'model', width: 170 },
    { title: '类型', key: 'stream', width: 90 },
    { title: '状态码', dataIndex: 'statusCode', key: 'statusCode', width: 90 },
    { title: '结果', key: 'success', width: 90 },
    { title: 'Tokens', key: 'tokens', width: 150 },
    { title: '成本', key: 'cost', width: 110 },
    { title: '首 token', dataIndex: 'firstTokenMs', key: 'firstTokenMs', width: 100 },
    { title: '总耗时', dataIndex: 'durationMs', key: 'durationMs', width: 100 },
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 180 },
    { title: 'API Key', dataIndex: 'apiKeyName', key: 'apiKey', width: 170 },
    { title: '分组', dataIndex: 'groupName', key: 'group', width: 150 },
    { title: 'IP', dataIndex: 'clientIp', key: 'clientIp', width: 130 }
  )
  return baseColumns
})

function displayName(name?: string, id?: string): string {
  if (name) return name
  return id ? '已删除或未知' : '-'
}

function formatTokens(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

function formatEndpoint(value?: string): string {
  return value ?? '-'
}

function formatCost(value?: number): string {
  if (!value) return '$0.000000'
  return `$${value.toFixed(6)}`
}

function formatUnitPrice(value?: number): string {
  return typeof value === 'number' ? `$${value.toFixed(4)} / 1M Token` : '-'
}

function formatDuration(value?: number): string {
  return typeof value === 'number' ? `${(value / 1000).toFixed(2)} s` : '-'
}

function statusCodeColor(value?: number): string {
  if (!value) return 'default'
  if (value >= 200 && value < 300) return 'green'
  if (value >= 400 && value < 500) return 'orange'
  if (value >= 500) return 'red'
  return 'blue'
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

function resetFilters(): void {
  accountNameFilter.value = ''
  resultFilter.value = 'all'
  statusCodeFilter.value = ''
}

async function loadData() {
  loading.value = true
  try {
    records.value = await api.usageRecords.list()
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

.usage-toolbar {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 10px;
  align-items: center;
  margin-bottom: 14px;
}

.usage-toolbar-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}

.filter-input {
  width: 220px;
}

.filter-select {
  width: 150px;
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

.cost-cell {
  color: #059669;
  font-family: Consolas, 'Courier New', monospace;
}

.cost-cell-wrap {
  display: inline-flex;
  gap: 6px;
  align-items: center;
}

.cost-detail-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 13px;
}

.cost-detail-icon:hover {
  color: #2563eb;
}

.cost-detail-panel {
  min-width: 190px;
  color: #e2e8f0;
  font-size: 12px;
}

.cost-detail-title {
  margin-bottom: 6px;
  color: #f8fafc;
}

.cost-detail-row {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  line-height: 1.8;
}

.cost-detail-value {
  color: #60a5fa;
  font-family: Consolas, 'Courier New', monospace;
}

:global(.cost-popover .ant-popover-inner) {
  background: #0f172a;
  border-radius: 8px;
  box-shadow: 0 10px 24px rgb(15 23 42 / 24%);
}

:global(.cost-popover .ant-popover-arrow::before) {
  background: #0f172a;
}

.usage-toolbar .page-toolbar-actions {
  justify-content: flex-end;
}

@media (max-width: 768px) {
  .usage-toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .usage-toolbar-filters {
    width: 100%;
  }

  .usage-toolbar .page-toolbar-actions {
    justify-content: flex-start;
    width: 100%;
  }
}

</style>
