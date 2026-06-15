<template>
  <ResponsiveDataList
    table-class="page-table public-api-log-table"
    :columns="columns"
    :data-source="records"
    row-key="id"
    :loading="loading"
    :pagination="pagination"
    mobile-pagination
    :mobile-has-more="mobileHasMore"
    :loading-more="mobileLoadingMore"
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="暂无公开接口日志" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'createdAt'">
        <span class="mono-cell muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
      <template v-else-if="column.key === 'source'">
        <div class="source-cell">
          <span class="source-name-text">{{ record.sourceName || '未认证来源' }}</span>
        </div>
      </template>
      <template v-else-if="column.key === 'path'">
        <span class="path-cell">{{ record.method }} {{ record.path }}</span>
      </template>
      <template v-else-if="column.key === 'result'">
        <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'statusCode'">
        <a-tag :color="getPublicApiLogStatusColor(record.statusCode)">{{ record.statusCode ?? '-' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'duration'">
        {{ formatPublicApiLogDuration(record.durationMs) }}
      </template>
      <template v-else-if="column.key === 'traceId'">
        <span class="trace-id-full">{{ record.traceId || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="publicApiLogDetailActions" @action-click="handleActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="log-mobile-card">
        <div class="log-mobile-card-head">
          <div>
            <strong>{{ record.sourceName || '未认证来源' }}</strong>
            <span>{{ record.method }} {{ record.path }}</span>
          </div>
          <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
        </div>
        <div class="log-mobile-card-grid">
          <span>时间</span>
          <strong>{{ formatDateTime(record.createdAt) }}</strong>
          <span>状态码</span>
          <strong>{{ record.statusCode ?? '-' }}</strong>
          <span>耗时</span>
          <strong>{{ formatPublicApiLogDuration(record.durationMs) }}</strong>
          <span>客户端 IP</span>
          <strong>{{ record.clientIp || '-' }}</strong>
          <span>traceId</span>
          <strong class="trace-id-full">{{ record.traceId || '-' }}</strong>
        </div>
        <RowActions :actions="publicApiLogDetailActions" variant="button" @action-click="handleActionClick($event, record)" />
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import { formatDateTime } from '@/shared/formatters'
import type { PublicApiLogSummary } from '@/types/domain'
import {
  formatPublicApiLogDuration,
  getPublicApiLogStatusColor
} from './publicApiLogFormatters'
import { publicApiLogDetailActions } from './publicApiLogOptions'

defineProps<{
  columns: Array<Record<string, unknown>>
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  pagination: Record<string, unknown> | false
  records: PublicApiLogSummary[]
}>()

const emit = defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'detail', record: PublicApiLogSummary): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

function handleActionClick(key: string, record: PublicApiLogSummary): void {
  if (key === 'detail') {
    emit('detail', record)
  }
}
</script>

<style scoped>
.source-cell {
  display: grid;
  gap: 2px;
}

.source-name-text {
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
}

.mono-cell,
.trace-id-full,
.path-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.path-cell {
  display: block;
  overflow-wrap: anywhere;
}

.trace-id-full {
  display: inline-block;
  max-width: 100%;
  overflow-wrap: anywhere;
  white-space: normal;
}

.log-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.log-mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.log-mobile-card-head div {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.log-mobile-card-head strong,
.log-mobile-card-head span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.log-mobile-card-head span {
  color: #64748b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.log-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(86px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.log-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}
</style>
