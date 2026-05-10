<template>
  <ResponsiveDataList
    :table-class="tableClass"
    :columns="columns"
    :data-source="records"
    row-key="id"
    :loading="loading"
    :scroll-x="1710"
    :pagination="pagination"
    :mobile-pagination="mobilePagination"
    :mobile-has-more="mobileHasMore"
    :loading-more="loadingMore"
    pull-refresh-enabled
    :refreshing="refreshing"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'time'">
        <span class="muted-cell">{{ formatDateTime(record.time) }}</span>
      </template>
      <template v-else-if="column.key === 'level'">
        <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'traceId'">
        <button class="link-button trace-cell" type="button" @click="$emit('trace', record.traceId)">{{ record.traceId ?? '-' }}</button>
      </template>
      <template v-else-if="column.key === 'event'">
        <a-tooltip v-if="record.event" :title="record.event">
          <span class="compact-cell">{{ eventText(record.event) }}</span>
        </a-tooltip>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'message'">
        <span :class="record.errorMessage ? 'error-message-cell' : 'message-cell'">{{ messageText(record) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="detailActions" @action-click="handleActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card" @click="$emit('detail', record)">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ cardTitle(record) }}</div>
          <div class="mobile-list-card-tags">
            <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>traceId</span>
            <strong class="mono-cell">{{ record.traceId ?? '-' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>事件</span>
            <strong>{{ eventText(record.event) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>消息</span>
            <strong>{{ messageText(record) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>时间</span>
            <strong>{{ formatDateTime(record.time) }}</strong>
          </div>
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { formatDateTime } from '@/shared/formatters'
import type { RuntimeLogGrepItem, RuntimeLogSummary } from '@/types/domain'
import { eventText, levelColor, levelText } from './runtimeLogFormatters'
import { runtimeLogColumns } from './runtimeLogTableColumns'

type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem

const props = withDefaults(defineProps<{
  actionLabel?: string
  emptyDescription: string
  loading: boolean
  loadingMore?: boolean
  messageMode?: 'index' | 'grep'
  mobileHasMore?: boolean
  mobilePagination?: boolean
  pagination?: false | Record<string, unknown>
  records: RuntimeLogListRecord[]
  refreshing?: boolean
  tableClass: string
}>(), {
  actionLabel: '详情',
  loadingMore: false,
  messageMode: 'index',
  mobileHasMore: false,
  mobilePagination: false,
  pagination: undefined,
  refreshing: false
})

const emit = defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'detail', record: RuntimeLogListRecord): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
  (event: 'trace', traceId?: string): void
}>()

const columns = runtimeLogColumns
const detailActions = computed<RowActionItem[]>(() => [
  { key: 'detail', label: props.actionLabel, icon: 'detail', tone: 'info' }
])

function handleActionClick(key: string, record: RuntimeLogListRecord) {
  if (key === 'detail') {
    emit('detail', record)
  }
}

function messageText(record: RuntimeLogListRecord): string {
  if (props.messageMode === 'grep' && 'line' in record) {
    return record.errorMessage || record.message || record.line || '-'
  }
  return record.errorMessage || record.message || '-'
}

function cardTitle(record: RuntimeLogListRecord): string {
  if (props.messageMode === 'grep' && 'line' in record) {
    return (record.event ? eventText(record.event) : '') || record.message || record.errorMessage || record.line || record.id
  }
  return (record.event ? eventText(record.event) : '') || record.message || record.errorMessage || record.id
}
</script>

<style scoped>
.runtime-log-table :deep(.ant-table-cell),
.grep-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.link-button {
  padding: 0;
  color: #1677ff;
  background: transparent;
  border: 0;
  cursor: pointer;
}

.trace-cell,
.compact-cell,
.mono-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.trace-cell,
.compact-cell,
.message-cell,
.error-message-cell {
  display: inline-block;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
}

.trace-cell {
  max-width: 230px;
}

.compact-cell {
  max-width: 210px;
}

.message-cell,
.error-message-cell {
  max-width: 600px;
}

.error-message-cell {
  color: #dc2626;
}
</style>
