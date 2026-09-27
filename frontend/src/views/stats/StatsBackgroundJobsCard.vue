<template>
  <StatsChartCard
    title="后台任务运行状态"
    :loading="loading"
    :has-data="hasData"
    :empty-description="emptyDescription"
    :error="error"
    :on-retry="onRetry"
  >
    <div class="background-jobs-toolbar">
      <a-select
        v-model:value="statusModel"
        class="background-job-status-select"
        :options="backgroundJobStatusOptions"
        size="small"
      />
      <a-button size="small" @click="emit('refresh')">刷新</a-button>
    </div>
    <ResponsiveDataList
      table-class="stats-background-jobs-table"
      :columns="backgroundJobColumns"
      :data-source="rows"
      :mobile-data-source="rows"
      :pagination="pagination"
      :row-class-name="backgroundJobRowClassName"
      row-key="runId"
      size="small"
      :scroll-x="1060"
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
        <template v-if="column.key === 'jobName'">
          <span class="background-job-name">{{ backgroundJobNameText(record.jobName) }}</span>
        </template>
        <template v-else-if="column.key === 'workerRole'">
          <a-tag>{{ workerRoleText(record.workerRole) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="backgroundJobStatusColor(record.status)">
            {{ backgroundJobStatusText(record.status) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'startedAt'">
          {{ formatDateTime(record.startedAt ?? undefined) }}
        </template>
        <template v-else-if="column.key === 'finishedAt'">
          {{ formatDateTime(record.finishedAt ?? undefined) }}
        </template>
        <template v-else-if="column.key === 'durationMs'">
          {{ formatDuration(record.durationMs) }}
        </template>
        <template v-else-if="column.key === 'errorMessage'">
          <a-tooltip v-if="record.errorMessage" :title="record.errorMessage">
            <span class="stats-job-error">{{ record.errorMessage }}</span>
          </a-tooltip>
          <span v-else>-</span>
        </template>
      </template>
      <template #card="{ record }">
        <article
          class="background-job-card"
          :class="{ 'background-job-card-failed': record.status === 'failed' }"
        >
          <div class="background-job-card-head">
            <strong class="background-job-name">{{ backgroundJobNameText(record.jobName) }}</strong>
            <a-tag :color="backgroundJobStatusColor(record.status)">
              {{ backgroundJobStatusText(record.status) }}
            </a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>角色</span>
              <strong>{{ workerRoleText(record.workerRole) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>开始</span>
              <strong>{{ formatDateTime(record.startedAt ?? undefined) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>结束</span>
              <strong>{{ formatDateTime(record.finishedAt ?? undefined) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>耗时</span>
              <strong>{{ formatDuration(record.durationMs) }}</strong>
            </div>
            <div v-if="record.errorMessage" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>错误</span>
              <strong>{{ record.errorMessage }}</strong>
            </div>
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </StatsChartCard>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import StatsChartCard from './StatsChartCard.vue'
import {
  backgroundJobNameText,
  backgroundJobRowClassName,
  backgroundJobStatusColor,
  backgroundJobStatusText,
  type BackgroundJobRow,
  workerRoleText
} from './statsBackgroundJobs'
import { formatDuration } from './statsFormatters'

const props = defineProps<{
  emptyDescription: string
  hasData: boolean
  loading: boolean
  pagination: Record<string, any>
  rows: BackgroundJobRow[]
  status: string
  error?: string
  onRetry?: () => void
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
  (event: 'status-change', status: string): void
  (event: 'refresh'): void
}>()

const backgroundJobStatusOptions = [
  { label: '全部', value: '' },
  { label: '成功', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '运行中', value: 'running' },
  { label: '排队中', value: 'queued' },
  { label: '已跳过', value: 'skipped' }
]

const statusModel = computed({
  get: () => props.status,
  set: (value: string) => emit('status-change', value)
})

const backgroundJobColumns = [
  { title: '任务名', dataIndex: 'jobName', key: 'jobName', width: 200 },
  { title: '角色', key: 'workerRole', width: 96 },
  { title: '状态', key: 'status', width: 96 },
  { title: '开始', key: 'startedAt', width: 168 },
  { title: '结束', key: 'finishedAt', width: 168 },
  { title: '耗时', key: 'durationMs', width: 96 },
  { title: '错误', key: 'errorMessage', ellipsis: true }
]

function handleTableChange(...args: unknown[]): void {
  emit('change', ...args)
}
</script>

<style scoped>
.stats-background-jobs-table {
  min-height: 0;
}

.background-jobs-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.background-job-status-select {
  width: 132px;
}

.background-job-name {
  display: inline-block;
  max-width: 100%;
  overflow-wrap: anywhere;
  word-break: break-word;
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

:deep(.ant-table-tbody > tr.stats-background-job-row-failed > td) {
  background: #fff1f0;
}

:deep(.ant-table-tbody > tr.stats-background-job-row-failed:hover > td) {
  background: #ffe1e0;
}

.background-job-card-failed {
  border-color: #ffa39e;
  background: #fff1f0;
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
