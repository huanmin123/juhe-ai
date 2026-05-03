<template>
  <div class="stats-page">
    <a-row :gutter="[16, 16]">
      <a-col v-for="item in summaryCards" :key="item.key" :xs="24" :sm="12" :lg="6">
        <a-card class="metric-card" :loading="loading">
          <div class="metric-label">{{ item.label }}</div>
          <div class="metric-value">{{ item.value }}</div>
          <div class="metric-extra">{{ item.extra }}</div>
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="12">
        <a-card title="Token 使用趋势（近 24 小时）" class="page-card" :loading="loading">
          <a-table size="small" :pagination="false" :columns="trendColumns" :data-source="usageOverview?.hourlyTrend ?? []" row-key="statHour" />
        </a-card>
      </a-col>
      <a-col :xs="24" :xl="12">
        <a-card title="模型分布（今日）" class="page-card" :loading="loading">
          <a-table size="small" :pagination="false" :columns="modelColumns" :data-source="usageOverview?.modelDistribution ?? []" row-key="model">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'model'">
                <a-tag color="geekblue">{{ record.model }}</a-tag>
              </template>
            </template>
          </a-table>
        </a-card>
      </a-col>
    </a-row>

    <a-row :gutter="[16, 16]" class="stats-section">
      <a-col :xs="24" :xl="12">
        <a-card title="错误情况（今日）" class="page-card" :loading="loading">
          <a-empty v-if="!usageOverview?.errors.length" description="今日暂无错误" />
          <a-table v-else size="small" :pagination="false" :columns="errorColumns" :data-source="usageOverview.errors" :row-key="errorRowKey" />
        </a-card>
      </a-col>
      <a-col :xs="24" :xl="12">
        <a-card title="系统监控" class="page-card" :loading="loading">
          <a-descriptions v-if="systemMetrics?.latest" size="small" :column="2" bordered>
            <a-descriptions-item label="CPU">{{ formatPercent(systemMetrics.latest.cpuPercent) }}</a-descriptions-item>
            <a-descriptions-item label="内存">{{ formatPercent(systemMetrics.latest.memoryUsedPercent) }}</a-descriptions-item>
            <a-descriptions-item label="RSS">{{ formatBytes(systemMetrics.latest.processRssBytes) }}</a-descriptions-item>
            <a-descriptions-item label="Heap">{{ formatBytes(systemMetrics.latest.processHeapUsedBytes) }}</a-descriptions-item>
            <a-descriptions-item label="事件循环延迟">{{ formatDuration(systemMetrics.latest.eventLoopLagMs) }}</a-descriptions-item>
            <a-descriptions-item label="统计滞后">{{ formatSeconds(systemMetrics.latest.statsLagSeconds ?? usageOverview?.statsLagSeconds) }}</a-descriptions-item>
            <a-descriptions-item label="数据库大小">{{ formatBytes(systemMetrics.latest.dbFileBytes) }}</a-descriptions-item>
            <a-descriptions-item label="采样时间">{{ formatDateTime(systemMetrics.latest.sampledAt) }}</a-descriptions-item>
          </a-descriptions>
          <a-empty v-else description="等待后台采样" />
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { SystemMetricsOverview, UsageStatsOverview } from '@/types/domain'

const loading = ref(false)
const usageOverview = ref<UsageStatsOverview>()
const systemMetrics = ref<SystemMetricsOverview>()
const isAdmin = authState.isAdmin

const summaryCards = computed(() => {
  const today = usageOverview.value?.today
  return [
    { key: 'requests', label: '今日请求', value: formatInteger(today?.requestCount), extra: `错误率 ${formatPercent((today?.errorRate ?? 0) * 100)}` },
    { key: 'duration', label: '平均响应', value: formatDuration(today?.averageDurationMs), extra: `首 Token ${formatDuration(today?.averageFirstTokenMs)}` },
    { key: 'tokens', label: '今日 Token', value: formatInteger(today?.totalTokens), extra: `输入 ${formatInteger(today?.inputTokens)} / 输出 ${formatInteger(today?.outputTokens)}` },
    { key: 'cost', label: '今日成本', value: formatCost(today?.totalCost), extra: `统计滞后 ${formatSeconds(usageOverview.value?.statsLagSeconds)}` }
  ]
})

const trendColumns = [
  { title: '小时', dataIndex: 'statHour', key: 'statHour' },
  { title: '请求', dataIndex: 'requestCount', key: 'requestCount' },
  { title: 'Token', key: 'totalTokens', customRender: ({ record }: { record: UsageStatsOverview['hourlyTrend'][number] }) => formatInteger(record.totalTokens) },
  { title: '平均响应', key: 'averageDurationMs', customRender: ({ record }: { record: UsageStatsOverview['hourlyTrend'][number] }) => formatDuration(record.averageDurationMs) },
  { title: '错误', dataIndex: 'errorCount', key: 'errorCount' }
]

const modelColumns = [
  { title: '模型', dataIndex: 'model', key: 'model' },
  { title: '请求', dataIndex: 'requestCount', key: 'requestCount' },
  { title: 'Token', key: 'totalTokens', customRender: ({ record }: { record: UsageStatsOverview['modelDistribution'][number] }) => formatInteger(record.totalTokens) },
  { title: '成本', key: 'totalCost', customRender: ({ record }: { record: UsageStatsOverview['modelDistribution'][number] }) => formatCost(record.totalCost) }
]

const errorColumns = [
  { title: '错误码', dataIndex: 'errorCode', key: 'errorCode' },
  { title: '状态码', dataIndex: 'statusCode', key: 'statusCode' },
  { title: '次数', dataIndex: 'errorCount', key: 'errorCount' },
  { title: '摘要', dataIndex: 'errorMessage', key: 'errorMessage', ellipsis: true }
]

async function loadData() {
  loading.value = true
  try {
    usageOverview.value = await api.stats.usageOverview()
    if (isAdmin.value) {
      systemMetrics.value = await api.stats.systemMetrics()
    }
  } catch (error) {
    console.error(error)
    message.error('统计数据加载失败')
  } finally {
    loading.value = false
  }
}

function errorRowKey(row: UsageStatsOverview['errors'][number]) {
  return `${row.providerCode}:${row.errorCode}:${row.statusCode ?? 0}`
}

function formatInteger(value?: number) {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

function formatCost(value?: number) {
  return `$${(value ?? 0).toFixed(4)}`
}

function formatPercent(value?: number) {
  if (value === undefined) return '-'
  return `${value.toFixed(1)}%`
}

function formatDuration(value?: number) {
  return value === undefined ? '-' : `${Math.round(value)} ms`
}

function formatSeconds(value?: number) {
  return value === undefined ? '-' : `${Math.round(value)} 秒`
}

function formatBytes(value?: number) {
  if (value === undefined) return '-'
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2)} GB`
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(2)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(2)} KB`
  return `${value} B`
}

function formatDateTime(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}

onMounted(loadData)
</script>

<style scoped>
.stats-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.metric-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.metric-label {
  color: #64748b;
  font-size: 13px;
}

.metric-value {
  margin-top: 8px;
  color: #0f172a;
  font-size: 26px;
  font-weight: 800;
}

.metric-extra {
  margin-top: 6px;
  color: #94a3b8;
  font-size: 12px;
}

.stats-section {
  margin-top: 0;
}
</style>