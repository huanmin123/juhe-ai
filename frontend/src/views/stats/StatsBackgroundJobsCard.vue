<template>
  <StatsChartCard
    title="后台任务运行状态"
    :loading="loading"
    :has-data="hasData"
    :empty-description="emptyDescription"
    :error="error"
    :on-retry="onRetry"
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
      :scroll-x="1160"
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
          </span>
        </template>
        <template v-else-if="column.key === 'running'">
          <a-tag :color="backgroundJobStatusColor(record)">
            {{ backgroundJobStatusText(record) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'workerRole'">
          <a-tag>{{ processRoleLabel(record.workerRole || 'worker') }}</a-tag>
        </template>
        <template v-else-if="column.key === 'runCount'">
          {{ formatInteger(record.runCount) }}
        </template>
        <template v-else-if="column.key === 'lastStartedAt'">
          {{ formatDateTime(record.lastStartedAt) }}
        </template>
        <template v-else-if="column.key === 'lastDurationMs'">
          {{ formatJobDuration(record.lastDurationMs) }}
        </template>
        <template v-else-if="column.key === 'maxDurationMs'">
          {{ formatJobDuration(record.maxDurationMs) }}
        </template>
        <template v-else-if="column.key === 'successCount'">
          {{ formatInteger(record.successCount) }}
        </template>
        <template v-else-if="column.key === 'failureCount'">
          {{ formatInteger(record.failureCount) }}
        </template>
        <template v-else-if="column.key === 'partialCount'">
          {{ formatInteger(record.partialCount) }}
        </template>
        <template v-else-if="column.key === 'skippedCount'">
          {{ formatInteger(record.skippedCount) }}
        </template>
        <template v-else-if="column.key === 'lastFinishedAt'">
          {{ formatDateTime(record.lastFinishedAt) }}
        </template>
        <template v-else-if="column.key === 'lastErrorAt'">
          {{ formatDateTime(record.lastErrorAt) }}
        </template>
        <template v-else-if="column.key === 'lastError'">
          <a-tooltip v-if="record.lastError" :title="record.lastError">
            <span class="stats-job-error">{{ record.lastError }}</span>
          </a-tooltip>
          <span v-else>-</span>
        </template>
        <template v-else-if="column.key === 'lastWarning'">
          <a-tooltip v-if="record.lastWarning" :title="record.lastWarning">
            <span class="stats-job-error">{{ record.lastWarning }}</span>
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
            <a-tag :color="backgroundJobStatusColor(record)">
              {{ backgroundJobStatusText(record) }}
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
              <span>本进程运行</span>
              <strong>{{ formatInteger(record.runCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近开始</span>
              <strong>{{ formatDateTime(record.lastStartedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最长耗时</span>
              <strong>{{ formatJobDuration(record.maxDurationMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>成功</span>
              <strong>{{ formatInteger(record.successCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>失败</span>
              <strong>{{ formatInteger(record.failureCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>部分失败</span>
              <strong>{{ formatInteger(record.partialCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>跳过</span>
              <strong>{{ formatInteger(record.skippedCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近完成</span>
              <strong>{{ formatDateTime(record.lastFinishedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近失败</span>
              <strong>{{ formatDateTime(record.lastErrorAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近部分失败</span>
              <strong>{{ formatDateTime(record.lastWarningAt) }}</strong>
            </div>
            <div v-if="record.lastError" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>最近错误</span>
              <strong>{{ record.lastError }}</strong>
            </div>
            <div v-if="record.lastWarning" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>部分失败原因</span>
              <strong>{{ record.lastWarning }}</strong>
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
import { formatDateTime } from '@/shared/formatters'
import StatsChartCard from './StatsChartCard.vue'
import { processRoleLabel } from './statsChartOptions'
import {
  backgroundJobStatusColor,
  backgroundJobStatusText,
  type BackgroundJobRow
} from './statsBackgroundJobs'
import { formatDuration, formatInteger } from './statsFormatters'

defineProps<{
  emptyDescription: string
  hasData: boolean
  loading: boolean
  pagination: Record<string, any>
  rows: BackgroundJobRow[]
  runtimeAlertDescription: string
  runtimeAlertVisible: boolean
  error?: string
  onRetry?: () => void
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
}>()

const backgroundJobColumns = [
  { title: '任务', dataIndex: 'name', key: 'name', width: 220 },
  { title: '所属 worker', key: 'workerRole', width: 112 },
  { title: '状态', key: 'running', width: 86 },
  { title: '本进程运行', key: 'runCount', width: 112, align: 'right', sorter: sortBackgroundJobNumber('runCount') },
  { title: '最近开始', key: 'lastStartedAt', width: 168 },
  { title: '最近耗时', key: 'lastDurationMs', width: 96 },
  { title: '最长耗时', key: 'maxDurationMs', width: 96 },
  { title: '成功', key: 'successCount', width: 84, align: 'right', sorter: sortBackgroundJobNumber('successCount') },
  { title: '累计失败（本进程）', key: 'failureCount', width: 148, align: 'right', sorter: sortBackgroundJobNumber('failureCount'), defaultSortOrder: 'descend' },
  { title: '部分失败（本进程）', key: 'partialCount', width: 148, align: 'right', sorter: sortBackgroundJobNumber('partialCount') },
  { title: '跳过', key: 'skippedCount', width: 84, align: 'right', sorter: sortBackgroundJobNumber('skippedCount') },
  { title: '最近完成', key: 'lastFinishedAt', width: 168 },
  { title: '最近失败', key: 'lastErrorAt', width: 168 },
  { title: '最近错误', key: 'lastError', ellipsis: true },
  { title: '部分失败原因', key: 'lastWarning', ellipsis: true }
]

function handleTableChange(...args: unknown[]): void {
  emit('change', ...args)
}

function formatJobDuration(value?: number): string {
  return value === undefined ? '-' : formatDuration(value)
}

function sortBackgroundJobNumber(field: 'runCount' | 'successCount' | 'failureCount' | 'partialCount' | 'skippedCount') {
  return (left: BackgroundJobRow, right: BackgroundJobRow) => numberValue(left[field]) - numberValue(right[field])
}

function backgroundJobDurationNote(row: BackgroundJobRow): string | undefined {
  if (row.name === 'cooldown-account-retest') {
    return '该任务会在冷却到期后按真实网关链路复测账号；失败后由 cooldown_until 推进下一次复测，先 3 秒起步并翻倍，达到最大暂停时间后进入慢速恢复。'
  }
  if (row.name === 'account-api-key-cooldown-retest') {
    return '该任务会在冷却到期后按真实网关链路复测账户内 API Key，并按复测结果恢复或延长冷却状态。'
  }
  return undefined
}

function numberValue(value: unknown): number {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : 0
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
