<template>
  <ResponsiveDataList
    table-class="page-table media-jobs-table"
    :columns="columns"
    :data-source="records"
    row-key="id"
    :loading="loading"
    :pagination="pagination"
    :scroll-x="mediaJobTableScrollX"
    mobile-pagination
    :mobile-has-more="mobileHasMore"
    :loading-more="mobileLoadingMore"
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="当前条件下没有媒体任务。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'id'">
        <div class="job-id-cell">
          <span class="job-id-text">{{ record.id }}</span>
          <span class="job-id-actions">
            <a-tooltip title="复制任务 ID">
              <a-button size="small" type="text" @click.stop="$emit('copy-job-id', record.id)">
                <template #icon><copy-outlined /></template>
              </a-button>
            </a-tooltip>
          </span>
        </div>
      </template>
      <template v-else-if="column.key === 'kind'">
        <a-tag color="geekblue">{{ formatMediaJobKind(record.kind) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="mediaJobStatusColor(record.status)">{{ formatMediaJobStatus(record.status) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'providerCode'">
        <span :class="record.providerCode ? 'mono-cell' : 'muted-cell'">{{ record.providerCode || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'model'">
        <a-tooltip v-if="record.model" :title="record.model" placement="topLeft">
          <a-tag class="model-tag" color="blue">{{ record.model }}</a-tag>
        </a-tooltip>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'seconds'">
        <span :class="record.seconds !== undefined ? 'mono-cell' : 'muted-cell'">{{ formatMediaJobSeconds(record.seconds) }}</span>
      </template>
      <template v-else-if="column.key === 'size'">
        <span :class="record.size !== undefined ? 'mono-cell' : 'muted-cell'">{{ formatMediaJobSize(record.size) }}</span>
      </template>
      <template v-else-if="column.key === 'apiKeyId'">
        <span :class="record.apiKeyId ? 'id-cell' : 'muted-cell'">{{ record.apiKeyId || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'accountId'">
        <span :class="record.accountId ? 'id-cell' : 'muted-cell'">{{ record.accountId || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'costUsd'">
        <span :class="record.costUsd !== undefined ? 'mono-cell' : 'muted-cell'">{{ formatMediaJobCost(record.costUsd) }}</span>
      </template>
      <template v-else-if="column.key === 'error'">
        <a-tooltip v-if="record.error" :title="mediaJobErrorDetail(record)" placement="topLeft">
          <a-tag class="error-tag" color="red">{{ mediaJobErrorSummary(record) }}</a-tag>
        </a-tooltip>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'createdAt'">
        <span class="mono-cell muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
      <template v-else-if="column.key === 'updatedAt'">
        <span class="mono-cell muted-cell">{{ formatDateTime(record.updatedAt) }}</span>
      </template>
    </template>
    <template #card="{ record }">
      <article class="media-job-mobile-card">
        <div class="media-job-mobile-card-head">
          <div>
            <strong>{{ formatMediaJobKind(record.kind) }}</strong>
            <span class="media-job-mobile-id">{{ record.id }}</span>
          </div>
          <a-tag :color="mediaJobStatusColor(record.status)">{{ formatMediaJobStatus(record.status) }}</a-tag>
        </div>
        <div class="media-job-mobile-grid">
          <span>模型</span>
          <strong>{{ record.model || '-' }}</strong>
          <span>供应商</span>
          <strong>{{ record.providerCode || '-' }}</strong>
          <span>时长 / 尺寸</span>
          <strong>{{ formatMediaJobSeconds(record.seconds) }} / {{ formatMediaJobSize(record.size) }}</strong>
          <span>成本</span>
          <strong>{{ formatMediaJobCost(record.costUsd) }}</strong>
          <span>API Key</span>
          <strong>{{ record.apiKeyId || '-' }}</strong>
          <span>AI 账户</span>
          <strong>{{ record.accountId || '-' }}</strong>
          <span v-if="record.error">错误</span>
          <strong v-if="record.error" class="media-job-mobile-error">{{ mediaJobErrorDetail(record) }}</strong>
          <span>创建时间</span>
          <strong>{{ formatDateTime(record.createdAt) }}</strong>
          <span>更新时间</span>
          <strong>{{ formatDateTime(record.updatedAt) }}</strong>
        </div>
        <a-button size="small" @click.stop="$emit('copy-job-id', record.id)">复制任务 ID</a-button>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import type { MediaJobListItem } from '@/types/domain'
import {
  formatMediaJobCost,
  formatMediaJobKind,
  formatMediaJobSeconds,
  formatMediaJobSize,
  formatMediaJobStatus,
  mediaJobErrorDetail,
  mediaJobErrorSummary,
  mediaJobStatusColor
} from './mediaJobsDisplay'
import { mediaJobTableScrollX } from './mediaJobsOptions'

defineProps<{
  columns: Array<Record<string, unknown>>
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  pagination: Record<string, unknown> | false
  records: MediaJobListItem[]
}>()

defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'copy-job-id', jobId: string): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()
</script>

<style scoped>
.media-jobs-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.media-jobs-table :deep(.ant-empty) {
  margin: 12px 0;
}

.mono-cell,
.job-id-text,
.id-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.muted-cell {
  color: var(--juhe-muted);
}

.job-id-cell {
  display: inline-flex;
  align-items: center;
  max-width: 200px;
  gap: 4px;
  vertical-align: bottom;
}

.job-id-actions {
  display: inline-flex;
  flex: none;
}

.job-id-text {
  min-width: 0;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.id-cell {
  display: inline-block;
  max-width: 140px;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  text-overflow: ellipsis;
  vertical-align: bottom;
}

.model-tag,
.error-tag {
  max-width: 100%;
  overflow-wrap: anywhere;
  white-space: normal;
}

.media-job-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface);
  justify-items: start;
}

.media-job-mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
  width: 100%;
}

.media-job-mobile-card-head div {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.media-job-mobile-card-head strong,
.media-job-mobile-card-head span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.media-job-mobile-id {
  color: var(--juhe-muted);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.media-job-mobile-grid {
  display: grid;
  grid-template-columns: minmax(86px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: var(--juhe-muted);
  font-size: 12px;
  width: 100%;
}

.media-job-mobile-grid strong {
  min-width: 0;
  overflow: hidden;
  color: var(--juhe-fg);
  overflow-wrap: anywhere;
  text-overflow: ellipsis;
}

.media-job-mobile-error {
  color: var(--juhe-danger);
}
</style>
