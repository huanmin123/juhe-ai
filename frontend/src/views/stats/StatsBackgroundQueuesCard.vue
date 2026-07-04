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
          <a-tag :color="queueStatusColor(record)">
            {{ queueStatusText(record) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'backlog'">
          {{ formatQueueNumber(backgroundQueueBacklog(record)) }}
        </template>
        <template v-else-if="column.key === 'runningCount'">
          {{ formatQueueRunningCount(record) }}
        </template>
        <template v-else-if="column.key === 'queueBytes'">
          {{ formatQueueBytes(record) }}
        </template>
        <template v-else-if="column.key === 'completedCount'">
          {{ formatOptionalNumber(record.completedCount) }}
        </template>
        <template v-else-if="column.key === 'droppedCount'">
          {{ formatOptionalNumber(record.droppedCount) }}
        </template>
        <template v-else-if="column.key === 'flushFailureCount'">
          {{ formatOptionalNumber(record.flushFailureCount) }}
        </template>
        <template v-else-if="column.key === 'rejectedTimedOutCount'">
          {{ formatRejectedTimedOut(record) }}
        </template>
        <template v-else-if="column.key === 'oldestQueuedMs'">
          {{ formatQueueWait(record) }}
        </template>
        <template v-else-if="column.key === 'nextOrSuccessAt'">
          {{ formatDateTime(record.nextRunAt || record.flushLastSuccessAt) }}
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
            <a-tag :color="queueStatusColor(record)">
              {{ queueStatusText(record) }}
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
            <div class="mobile-list-meta-item">
              <span>队列大小</span>
              <strong>{{ formatQueueBytes(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>已完成</span>
              <strong>{{ formatOptionalNumber(record.completedCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>丢弃</span>
              <strong>{{ formatOptionalNumber(record.droppedCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>写入失败</span>
              <strong>{{ formatOptionalNumber(record.flushFailureCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>拒超</span>
              <strong>{{ formatRejectedTimedOut(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最老等待</span>
              <strong>{{ formatQueueWait(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>时间</span>
              <strong>{{ formatDateTime(record.nextRunAt || record.flushLastSuccessAt) }}</strong>
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
import { backgroundQueueBacklog, backgroundQueueProblemCount, type BackgroundQueueRow } from './statsBackgroundQueues'

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
  { title: '活跃', key: 'runningCount', width: 86, align: 'right', sorter: (left: BackgroundQueueRow, right: BackgroundQueueRow) => queueRunningDisplayCount(left) - queueRunningDisplayCount(right) },
  { title: '大小', key: 'queueBytes', width: 96, align: 'right', sorter: sortBackgroundQueueNumber('queueBytes') },
  { title: '已完成', key: 'completedCount', width: 96, align: 'right', sorter: sortBackgroundQueueNumber('completedCount') },
  { title: '丢弃', key: 'droppedCount', width: 84, align: 'right', sorter: sortBackgroundQueueNumber('droppedCount') },
  { title: '写入失败', key: 'flushFailureCount', width: 108, align: 'right', sorter: sortBackgroundQueueProblemCount },
  { title: '拒超', key: 'rejectedTimedOutCount', width: 86, align: 'right', sorter: sortBackgroundQueueProblemCount },
  { title: '最老等待', key: 'oldestQueuedMs', width: 110, align: 'right', sorter: sortBackgroundQueueWait },
  { title: '时间', key: 'nextOrSuccessAt', width: 168 },
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

function queueStatusText(row: BackgroundQueueRow): string {
  if (row.lastError || numberValue(row.flushFailureCount) > 0) return '异常'
  if (backgroundQueueProblemCount(row) > 0) return '异常'
  if (queueRunningCount(row) > 0) return '运行中'
  if (backgroundQueueBacklog(row) > 0) return row.queueType === 'retry' ? '待执行' : '积压'
  return '空闲'
}

function queueStatusColor(row: BackgroundQueueRow): string {
  if (row.lastError || backgroundQueueProblemCount(row) > 0) return 'error'
  if (queueRunningCount(row) > 0) return 'processing'
  if (backgroundQueueBacklog(row) > 0) return 'warning'
  return 'success'
}

function formatQueueNumber(value: number): string {
  return formatInteger(value)
}

function formatOptionalNumber(value?: number): string {
  return value === undefined ? '-' : formatInteger(value)
}

function formatQueueBytes(row: BackgroundQueueRow): string {
  return row.queueBytes === undefined ? '-' : formatBytesMiB(row.queueBytes)
}

function formatQueueRunningCount(row: BackgroundQueueRow): string {
  return formatInteger(queueRunningDisplayCount(row))
}

function formatRejectedTimedOut(row: BackgroundQueueRow): string {
  const rejected = numberValue(row.rejectedCount) + numberValue(row.expiredCount)
  const timedOut = numberValue(row.timedOutCount) + numberValue(row.failedCount)
  return `${formatInteger(rejected)} / ${formatInteger(timedOut)}`
}

function formatQueueWait(row: BackgroundQueueRow): string {
  const value = Math.max(
    numberValue(row.oldestQueuedMs),
    numberValue(row.writerPoolOldestQueuedMs),
    numberValue(row.pendingWriteOldestQueuedMs)
  )
  return value > 0 ? formatDuration(value) : '-'
}

function sortBackgroundQueueNumber(field: keyof Pick<BackgroundQueueRow, 'runningCount' | 'queueBytes' | 'completedCount' | 'droppedCount'>) {
  return (left: BackgroundQueueRow, right: BackgroundQueueRow) => numberValue(left[field]) - numberValue(right[field])
}

function sortBackgroundQueueProblemCount(left: BackgroundQueueRow, right: BackgroundQueueRow): number {
  return backgroundQueueProblemCount(left) - backgroundQueueProblemCount(right)
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

function queueRunningCount(row: BackgroundQueueRow): number {
  return numberValue(row.runningCount) + numberValue(row.writerPoolActiveJobs)
}

function queueRunningDisplayCount(row: BackgroundQueueRow): number {
  return row.consumers !== undefined ? numberValue(row.consumers) : queueRunningCount(row)
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
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
