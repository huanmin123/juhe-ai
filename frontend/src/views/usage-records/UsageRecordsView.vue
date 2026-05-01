<template>
  <a-card class="page-card" title="使用记录">
    <div class="page-toolbar">
      <span class="toolbar-note">记录网关请求、命中账户、token 用量、成本和错误状态。</span>
      <div class="page-toolbar-actions">
        <a-button :loading="loading" @click="loadData">刷新</a-button>
      </div>
    </div>
    <a-table class="page-table usage-table" size="middle" :columns="columns" :data-source="records" row-key="id" :loading="loading" :scroll="{ x: 1680 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="中转网关接入后开始产生使用记录。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'requestId'">
          <span class="mono-cell">{{ shortId(record.requestId) }}</span>
        </template>
        <template v-else-if="column.key === 'apiKeyId'">
          <span class="muted-cell">{{ shortId(record.apiKeyId) }}</span>
        </template>
        <template v-else-if="column.key === 'groupId'">
          <span class="muted-cell">{{ shortId(record.groupId) }}</span>
        </template>
        <template v-else-if="column.key === 'accountId'">
          <span class="muted-cell">{{ shortId(record.accountId) }}</span>
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
          <span class="cost-cell">{{ formatCost(record.costUsd) }}</span>
        </template>
        <template v-else-if="column.key === 'durationMs'">
          <a-tag>{{ record.durationMs ?? 0 }} ms</a-tag>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
      </template>
    </a-table>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { onMounted, ref } from 'vue'

import { api } from '@/api/client'
import type { UsageRecordSummary } from '@/types/domain'

const loading = ref(false)
const records = ref<UsageRecordSummary[]>([])

const columns = [
  { title: '请求 ID', dataIndex: 'requestId', key: 'requestId', width: 150 },
  { title: 'API Key', dataIndex: 'apiKeyId', key: 'apiKeyId', width: 130 },
  { title: '分组', dataIndex: 'groupId', key: 'groupId', width: 130 },
  { title: '账户', dataIndex: 'accountId', key: 'accountId', width: 130 },
  { title: '模型', dataIndex: 'model', key: 'model', width: 170 },
  { title: '类型', key: 'stream', width: 90 },
  { title: '状态码', dataIndex: 'statusCode', key: 'statusCode', width: 90 },
  { title: '结果', key: 'success', width: 90 },
  { title: 'Tokens', key: 'tokens', width: 150 },
  { title: '成本', key: 'cost', width: 110 },
  { title: '耗时', dataIndex: 'durationMs', key: 'durationMs', width: 100 },
  { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 180 }
]

function shortId(value?: string): string {
  if (!value) return '-'
  return value.length > 14 ? `${value.slice(0, 8)}...${value.slice(-4)}` : value
}

function formatTokens(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

function formatCost(value?: number): string {
  if (!value) return '$0.000000'
  return `$${value.toFixed(6)}`
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

.token-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  color: #475569;
  font-size: 12px;
  line-height: 1.3;
}

.cost-cell {
  color: #059669;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 700;
}
</style>
