<template>
  <StatsChartCard
    title="后台任务运行状态"
    :loading="loading"
    :has-data="hasData"
    :empty-description="emptyDescription"
    :error="error"
    :on-retry="onRetry"
  >
    <ResponsiveDataList
      table-class="stats-background-jobs-table"
      :columns="backgroundJobColumns"
      :data-source="rows"
      :mobile-data-source="rows"
      :pagination="pagination"
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
          <span class="background-job-name">{{ record.jobName }}</span>
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
        <article class="background-job-card">
          <div class="background-job-card-head">
            <strong class="background-job-name">{{ record.jobName }}</strong>
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
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import StatsChartCard from './StatsChartCard.vue'
import {
  backgroundJobStatusColor,
  backgroundJobStatusText,
  type BackgroundJobRow,
  workerRoleText
} from './statsBackgroundJobs'
import { formatDuration } from './statsFormatters'

defineProps<{
  emptyDescription: string
  hasData: boolean
  loading: boolean
  pagination: Record<string, any>
  rows: BackgroundJobRow[]
  error?: string
  onRetry?: () => void
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
}>()

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
