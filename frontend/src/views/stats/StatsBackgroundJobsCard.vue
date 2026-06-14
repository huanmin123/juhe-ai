<template>
  <StatsChartCard
    title="后台任务运行状态"
    description="展示各后台 worker 内定时任务的最近耗时、失败、跳过和复测队列情况。"
    :loading="loading"
    :has-data="hasData"
    :empty-description="emptyDescription"
  >
    <RuntimeAvailabilityAlert
      :visible="runtimeAlertVisible"
      message="后台运行态暂时不可观测"
      :description="runtimeAlertDescription"
    />
    <ResponsiveDataList
      table-class="stats-background-jobs-table"
      :columns="backgroundJobColumns"
      :data-source="rows"
      :mobile-data-source="rows"
      :pagination="pagination"
      row-key="name"
      size="small"
      :scroll-x="860"
      :table-scroll-y="240"
      :table-scroll-enabled="false"
      :lock-body-scroll="false"
      :adaptive-column-width="false"
      @change="handleTableChange"
    >
      <template #emptyText>
        <a-empty :description="emptyDescription" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <span class="background-job-name-cell">
            <span class="background-job-name">
              <span>{{ record.name }}</span>
              <a-tooltip v-if="backgroundJobDurationNote(record)" :title="backgroundJobDurationNote(record)">
                <InfoCircleOutlined class="background-job-info-icon" />
              </a-tooltip>
            </span>
            <span v-if="backgroundJobRetryQueueSummary(record)" class="background-job-queue-summary">
              {{ backgroundJobRetryQueueSummary(record) }}
            </span>
          </span>
        </template>
        <template v-else-if="column.key === 'running'">
          <a-tag :color="record.running ? 'processing' : record.failureCount > 0 ? 'warning' : 'success'">
            {{ record.running ? '运行中' : '空闲' }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'workerRole'">
          <a-tag>{{ processRoleLabel(record.workerRole || 'worker') }}</a-tag>
        </template>
        <template v-else-if="column.key === 'lastDurationMs'">
          {{ formatJobDuration(record.lastDurationMs) }}
        </template>
        <template v-else-if="column.key === 'maxDurationMs'">
          {{ formatJobDuration(record.maxDurationMs) }}
        </template>
        <template v-else-if="column.key === 'counts'">
          {{ formatJobCounts(record) }}
        </template>
        <template v-else-if="column.key === 'lastFinishedAt'">
          {{ formatDateTime(record.lastFinishedAt) }}
        </template>
        <template v-else-if="column.key === 'lastError'">
          <a-tooltip v-if="record.lastError" :title="record.lastError">
            <span class="stats-job-error">{{ record.lastError }}</span>
          </a-tooltip>
          <span v-else>-</span>
        </template>
      </template>
      <template #card="{ record }">
        <article class="background-job-card">
          <div class="background-job-card-head">
            <strong class="background-job-name-cell">
              <span class="background-job-name">
                <span>{{ record.name }}</span>
                <a-tooltip v-if="backgroundJobDurationNote(record)" :title="backgroundJobDurationNote(record)">
                  <InfoCircleOutlined class="background-job-info-icon" />
                </a-tooltip>
              </span>
            </strong>
            <a-tag :color="record.running ? 'processing' : record.failureCount > 0 ? 'warning' : 'success'">
              {{ record.running ? '运行中' : '空闲' }}
            </a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>所属 worker</span>
              <strong>{{ processRoleLabel(record.workerRole || 'worker') }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近耗时</span>
              <strong>{{ formatJobDuration(record.lastDurationMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最长耗时</span>
              <strong>{{ formatJobDuration(record.maxDurationMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>成功 / 失败 / 跳过</span>
              <strong>{{ formatJobCounts(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近完成</span>
              <strong>{{ formatDateTime(record.lastFinishedAt) }}</strong>
            </div>
            <div v-if="record.lastError" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>最近错误</span>
              <strong>{{ record.lastError }}</strong>
            </div>
            <div v-if="backgroundJobRetryQueueSummary(record)" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>复活队列</span>
              <strong>{{ backgroundJobRetryQueueSummary(record) }}</strong>
            </div>
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </StatsChartCard>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'
import { formatDateTime, serverDateTimeTimestamp } from '@/shared/formatters'
import type { SystemMetricsOverview } from '@/types/domain'
import StatsChartCard from './StatsChartCard.vue'
import { processRoleLabel } from './statsChartOptions'
import { formatDuration, formatInteger } from './statsFormatters'

type BackgroundJobRow = NonNullable<SystemMetricsOverview['backgroundJobs']>[number]

defineProps<{
  emptyDescription: string
  hasData: boolean
  loading: boolean
  pagination: Record<string, any>
  rows: BackgroundJobRow[]
  runtimeAlertDescription: string
  runtimeAlertVisible: boolean
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
}>()

const backgroundJobColumns = [
  { title: '任务', dataIndex: 'name', key: 'name', width: 220 },
  { title: '所属 worker', key: 'workerRole', width: 112 },
  { title: '状态', key: 'running', width: 86 },
  { title: '最近耗时', key: 'lastDurationMs', width: 96 },
  { title: '最长耗时', key: 'maxDurationMs', width: 96 },
  { title: '成功 / 失败 / 跳过', key: 'counts', width: 138 },
  { title: '最近完成', key: 'lastFinishedAt', width: 168 },
  { title: '最近错误', key: 'lastError', ellipsis: true }
]

function handleTableChange(...args: unknown[]): void {
  emit('change', ...args)
}

function formatJobDuration(value?: number): string {
  return value === undefined ? '-' : formatDuration(value)
}

function formatJobCounts(row: BackgroundJobRow): string {
  return `${formatInteger(row.successCount)} / ${formatInteger(row.failureCount)} / ${formatInteger(row.skippedCount)}`
}

function backgroundJobRetryQueueSummary(row: BackgroundJobRow): string | undefined {
  const queue = row.retryQueue
  if (!queue) return undefined
  const nextRunAt = formatRetryQueueNextRunAt(queue.nextRunAt)
  return `队列：待执行 ${formatInteger(queue.pendingCount)} / 运行中 ${formatInteger(queue.runningCount)}${nextRunAt ? ` / 下次 ${nextRunAt}` : ''}`
}

function formatRetryQueueNextRunAt(value?: string): string | undefined {
  if (!value) return undefined
  const timestamp = serverDateTimeTimestamp(value)
  if (timestamp === undefined) return undefined
  return new Intl.DateTimeFormat('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  }).format(timestamp)
}

function backgroundJobDurationNote(row: BackgroundJobRow): string | undefined {
  if (row.name === 'cooldown-account-retest') {
    return '该任务会在冷却到期后按真实网关链路复测账号；失败后由 cooldown_until 推进下一次复测，先 3 秒起步并翻倍，达到最大暂停时间后进入慢速恢复。'
  }
  if (row.name === 'account-api-key-cooldown-retest') {
    return '该任务会在冷却到期后按真实网关链路复测账户内 API Key；队列堆积表示 Key 级恢复探测仍在排队。'
  }
  return undefined
}
</script>

<style scoped>
.stats-background-jobs-table {
  min-height: 0;
}

.background-job-name-cell {
  display: inline-grid;
  min-width: 0;
  max-width: 100%;
  gap: 3px;
}

.background-job-name {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  max-width: 100%;
}

.background-job-name span {
  min-width: 0;
  overflow-wrap: anywhere;
}

.background-job-queue-summary {
  min-width: 0;
  color: #64748b;
  font-size: 12px;
  font-weight: 400;
  line-height: 1.35;
  overflow-wrap: anywhere;
}

.background-job-info-icon {
  flex: none;
  color: #64748b;
  cursor: help;
  font-size: 14px;
}

.stats-job-error {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  color: #cf1322;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
}

.background-job-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.background-job-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.background-job-card-head strong {
  min-width: 0;
  color: #0f172a;
  font-weight: 400;
  overflow-wrap: anywhere;
}
</style>
