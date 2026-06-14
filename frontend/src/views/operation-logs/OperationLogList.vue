<template>
  <ResponsiveDataList
    table-class="page-table operation-log-table"
    :columns="columns"
    :data-source="records"
    :mobile-data-source="records"
    row-key="id"
    :loading="loading"
    :loading-more="loadingMore"
    :mobile-has-more="mobileHasMore"
    :pagination="pagination"
    :scroll-x="operationLogTableScrollX(isManagementView)"
    mobile-pagination
    pull-refresh-enabled
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="当前条件下没有操作日志。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'summary'">
        <div class="summary-cell">
          <span>{{ record.summary }}</span>
        </div>
      </template>
      <template v-else-if="column.key === 'module'">
        <a-tag>{{ moduleText(record.module) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'action'">
        <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'actor'">
        <span class="name-cell">{{ actorText(record) }}</span>
      </template>
      <template v-else-if="column.key === 'scope'">
        <span :class="record.operationScopeSystemAccountName ? 'name-cell' : 'muted-cell'">
          {{ displayName(record.operationScopeSystemAccountName) }}
        </span>
      </template>
      <template v-else-if="column.key === 'traceId'">
        <span :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId ?? '-' }}</span>
      </template>
      <template v-else-if="column.key === 'createdAt'">
        <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="detailActions" @action-click="handleActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="operation-log-mobile-card">
        <div class="mobile-card-head">
          <span>{{ moduleText(record.module) }}</span>
          <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
        </div>
        <div class="mobile-card-meta">
          <span>{{ actorText(record) }}</span>
          <span>{{ formatDateTime(record.createdAt) }}</span>
        </div>
        <div class="mobile-card-summary">{{ record.summary }}</div>
        <div class="mobile-card-actions">
          <RowActions variant="button" :actions="detailActions" @action-click="handleActionClick($event, record)" />
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import { formatDateTime } from '@/shared/formatters'
import type { OperationLogSummary } from '@/types/domain'
import { actorText, displayName } from './operationLogDisplay'
import { actionColor, actionText, moduleText } from './operationLogLabels'
import { detailActions } from './operationLogOptions'
import { operationLogTableScrollX } from './operationLogTableConfig'

defineProps<{
  columns: Array<Record<string, unknown>>
  isManagementView: boolean
  loading: boolean
  loadingMore: boolean
  mobileHasMore: boolean
  pagination: Record<string, unknown> | false
  records: OperationLogSummary[]
}>()

const emit = defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'detail', record: OperationLogSummary): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

function handleActionClick(key: string, record: OperationLogSummary): void {
  if (key === 'detail') {
    emit('detail', record)
  }
}
</script>

<style scoped>
.operation-log-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.summary-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.summary-cell span {
  max-width: 280px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}

.muted-cell {
  color: #0f172a;
  font-size: 12px;
}

.name-cell {
  display: inline-block;
  max-width: 190px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.mono-cell {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.operation-log-mobile-card {
  display: grid;
  width: 100%;
  gap: 10px;
  padding: 12px;
  text-align: left;
  background: #ffffff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(15 23 42 / 4%);
}

.mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.mobile-card-head > span {
  min-width: 0;
  color: #0f172a;
  line-height: 1.35;
}

.mobile-card-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  color: #0f172a;
  font-size: 12px;
}

.mobile-card-summary {
  color: #0f172a;
  font-size: 13px;
  line-height: 1.4;
}

.mobile-card-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
