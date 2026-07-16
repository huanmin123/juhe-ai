<template>
  <StatsChartCard
    title="后台队列运行状态"
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
      table-class="stats-background-queues-table"
      :columns="backgroundQueueColumns"
      :data-source="rows"
      :mobile-data-source="rows"
      :pagination="pagination"
      row-key="key"
      size="small"
      :scroll-x="1280"
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
          <span class="background-queue-name">{{ record.name }}</span>
        </template>
        <template v-else-if="column.key === 'queueType'">
          <a-tag>{{ queueTypeLabel(record.queueType) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'workerRole'">
          <a-tag>{{ processRoleLabel(record.workerRole || 'worker') }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="backgroundQueueStatusColor(record)">
            {{ backgroundQueueStatusText(record) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'backlog'">
          {{ formatQueueNumber(backgroundQueueBacklog(record)) }}
        </template>
        <template v-else-if="column.key === 'runningCount'">
          {{ formatQueueRunningCount(record) }}
        </template>
        <template v-else-if="column.key === 'processingMetrics'">
          {{ formatProcessingMetrics(record) }}
        </template>
        <template v-else-if="column.key === 'problemMetrics'">
          {{ formatProblemMetrics(record) }}
        </template>
        <template v-else-if="column.key === 'oldestQueuedMs'">
          {{ formatQueueWait(record) }}
        </template>
        <template v-else-if="column.key === 'nextOrSuccessAt'">
          <span class="background-queue-time">
            <span>{{ queueTimeLabel(record) }}</span>
            <strong>{{ queueTimeText(record) }}</strong>
          </span>
        </template>
        <template v-else-if="column.key === 'lastError'">
          <a-tooltip v-if="record.lastError" :title="record.lastError">
            <span class="stats-queue-error">{{ record.lastError }}</span>
          </a-tooltip>
          <span v-else>-</span>
        </template>
      </template>
      <template #card="{ record }">
        <article class="background-queue-card">
          <div class="background-queue-card-head">
            <strong>{{ record.name }}</strong>
            <a-tag :color="backgroundQueueStatusColor(record)">
              {{ backgroundQueueStatusText(record) }}
            </a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>队列类型</span>
              <strong>{{ queueTypeLabel(record.queueType) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>所属 worker</span>
              <strong>{{ processRoleLabel(record.workerRole || 'worker') }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>积压</span>
              <strong>{{ formatQueueNumber(backgroundQueueBacklog(record)) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>活跃</span>
              <strong>{{ formatQueueRunningCount(record) }}</strong>
            </div>
            <div v-if="formatProcessingMetrics(record) !== '-'" class="mobile-list-meta-item">
              <span>容量 / 处理</span>
              <strong>{{ formatProcessingMetrics(record) }}</strong>
            </div>
            <div v-if="formatProblemMetrics(record) !== '-'" class="mobile-list-meta-item">
              <span>异常累计</span>
              <strong>{{ formatProblemMetrics(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最老等待</span>
              <strong>{{ formatQueueWait(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>{{ queueTimeLabel(record) }}</span>
              <strong>{{ queueTimeText(record) }}</strong>
            </div>
            <div v-if="record.lastError" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>最近错误</span>
              <strong>{{ record.lastError }}</strong>
            </div>
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </StatsChartCard>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'
import { formatDateTime } from '@/shared/formatters'
import StatsChartCard from './StatsChartCard.vue'
import { processRoleLabel } from './statsChartOptions'
import { formatBytesMiB, formatDuration, formatInteger } from './statsFormatters'
import { backgroundQueueActiveCount, backgroundQueueBacklog, backgroundQueueHistoricalProblemCount, backgroundQueueStatusColor, backgroundQueueStatusText, type BackgroundQueueRow } from './statsBackgroundQueues'

defineProps<{
  emptyDescription: string
  hasData: boolean
  loading: boolean
  pagination: Record<string, any>
  rows: BackgroundQueueRow[]
  runtimeAlertDescription: string
  runtimeAlertVisible: boolean
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
}>()

const backgroundQueueColumns = [
  { title: '队列', dataIndex: 'name', key: 'name', width: 240 },
  { title: '类型', key: 'queueType', width: 96 },
  { title: '所属 worker', key: 'workerRole', width: 112 },
  { title: '状态', key: 'status', width: 86 },
  { title: '积压', key: 'backlog', width: 86, align: 'right', sorter: (left: BackgroundQueueRow, right: BackgroundQueueRow) => backgroundQueueBacklog(left) - backgroundQueueBacklog(right), defaultSortOrder: 'descend' },
  { title: '活跃', key: 'runningCount', width: 86, align: 'right', sorter: (left: BackgroundQueueRow, right: BackgroundQueueRow) => numberValue(queueRunningDisplayCount(left)) - numberValue(queueRunningDisplayCount(right)) },
  { title: '容量 / 处理', key: 'processingMetrics', width: 220 },
  { title: '异常累计', key: 'problemMetrics', width: 260, sorter: sortBackgroundQueueProblemCount },
  { title: '最老等待', key: 'oldestQueuedMs', width: 110, align: 'right', sorter: sortBackgroundQueueWait },
  { title: '调度 / 写入', key: 'nextOrSuccessAt', width: 196 },
  { title: '最近错误', key: 'lastError', ellipsis: true }
]

function handleTableChange(...args: unknown[]): void {
  emit('change', ...args)
}

function queueTypeLabel(type: BackgroundQueueRow['queueType']): string {
  if (type === 'retry') return '重试队列'
  if (type === 'ipc') return 'IPC 队列'
  if (type === 'request') return '请求队列'
  if (type === 'gateway') return '网关队列'
  if (type === 'concurrency') return '并发短队列'
  if (type === 'redis') return 'Redis Stream'
  if (type === 'writer') return '写入池'
  return '本地队列'
}

function formatQueueNumber(value: number): string {
  return formatInteger(value)
}

function formatQueueBytes(row: BackgroundQueueRow): string {
  return row.queueBytes === undefined ? '-' : formatBytesMiB(row.queueBytes)
}

function formatQueueRunningCount(row: BackgroundQueueRow): string {
  const count = queueRunningDisplayCount(row)
  return count === undefined ? '-' : formatInteger(count)
}

function formatProcessingMetrics(row: BackgroundQueueRow): string {
  const parts: string[] = []
  if (row.queueBytes !== undefined) parts.push(`大小 ${formatQueueBytes(row)}`)
  if (row.completedCount !== undefined) parts.push(`完成 ${formatInteger(row.completedCount)}`)
  return parts.join('；') || '-'
}

function formatProblemMetrics(row: BackgroundQueueRow): string {
  const parts: string[] = []
  if (row.droppedCount !== undefined) parts.push(`丢弃 ${formatInteger(row.droppedCount)}`)
  if (row.expiredCount !== undefined) parts.push(`过期 ${formatInteger(row.expiredCount)}`)
  if (row.rejectedCount !== undefined) parts.push(`拒绝 ${formatInteger(row.rejectedCount)}`)
  if (row.timedOutCount !== undefined) parts.push(`超时 ${formatInteger(row.timedOutCount)}`)
  if (row.failedCount !== undefined) parts.push(`失败 ${formatInteger(row.failedCount)}`)
  if (row.flushFailureCount !== undefined) parts.push(`写入失败 ${formatInteger(row.flushFailureCount)}`)
  return parts.join('；') || '-'
}

function formatQueueWait(row: BackgroundQueueRow): string {
  const value = Math.max(
    numberValue(row.oldestQueuedMs),
    numberValue(row.writerPoolOldestQueuedMs),
    numberValue(row.pendingWriteOldestQueuedMs)
  )
  return value > 0 ? formatDuration(value) : '-'
}

function sortBackgroundQueueProblemCount(left: BackgroundQueueRow, right: BackgroundQueueRow): number {
  return backgroundQueueHistoricalProblemCount(left) - backgroundQueueHistoricalProblemCount(right)
}

function queueTimeLabel(row: BackgroundQueueRow): string {
  if (row.nextRunAt) return '下次运行'
  if (row.flushLastSuccessAt) return '最近写入成功'
  return '运行时间'
}

function queueTimeText(row: BackgroundQueueRow): string {
  return formatDateTime(row.nextRunAt || row.flushLastSuccessAt)
}

function sortBackgroundQueueWait(left: BackgroundQueueRow, right: BackgroundQueueRow): number {
  return queueWaitMs(left) - queueWaitMs(right)
}

function queueWaitMs(row: BackgroundQueueRow): number {
  return Math.max(
    numberValue(row.oldestQueuedMs),
    numberValue(row.writerPoolOldestQueuedMs),
    numberValue(row.pendingWriteOldestQueuedMs)
  )
}

function queueRunningDisplayCount(row: BackgroundQueueRow): number | undefined {
  return backgroundQueueActiveCount(row)
}

function numberValue(value: unknown): number {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : 0
}
</script>

<style scoped>
.stats-background-queues-table {
  min-height: 0;
}

.background-queue-name {
  min-width: 0;
  overflow-wrap: anywhere;
}

.background-queue-time {
  display: grid;
  gap: 2px;
}

.background-queue-time span {
  color: #64748b;
  font-size: 12px;
}

.background-queue-time strong {
  font-weight: 400;
}

.stats-queue-error {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  color: #cf1322;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
}

.background-queue-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.background-queue-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.background-queue-card-head strong {
  min-width: 0;
  color: #0f172a;
  font-weight: 400;
  overflow-wrap: anywhere;
}
</style>
