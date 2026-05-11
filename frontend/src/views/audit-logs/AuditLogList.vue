<template>
  <ResponsiveDataList
    table-class="page-table audit-table"
    :columns="columns"
    :data-source="records"
    row-key="id"
    :loading="loading"
    :scroll-x="1780"
    :pagination="pagination"
    mobile-pagination
    :mobile-has-more="mobileHasMore"
    :loading-more="loadingMore"
    pull-refresh-enabled
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="暂无审计日志。失败请求会全量记录，成功请求默认按 10% 采样。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'traceId'">
        <button class="link-button trace-cell" type="button" @click="$emit('detail', record)">{{ record.traceId }}</button>
      </template>
      <template v-else-if="column.key === 'outcome'">
        <a-tag :color="outcomeColor(record.auditOutcome)">{{ outcomeText(record.auditOutcome) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="statusColor(record.finalStatusCode, record.success)">{{ record.finalStatusCode ?? '-' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'endpoint'">
        <span class="endpoint-cell">{{ record.method }} {{ record.path }}</span>
      </template>
      <template v-else-if="column.key === 'model'">
        <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'stream'">
        <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'account'">
        <span>{{ displayName(record.accountName, record.accountId) }}</span>
      </template>
      <template v-else-if="column.key === 'apiKey'">
        <span>{{ displayName(record.apiKeyName, record.apiKeyId) }}</span>
      </template>
      <template v-else-if="column.key === 'group'">
        <span>{{ displayName(record.groupName, record.groupId) }}</span>
      </template>
      <template v-else-if="column.key === 'systemAccount'">
        <span>{{ displayName(record.systemAccountName, record.systemAccountId) }}</span>
      </template>
      <template v-else-if="column.key === 'payload'">
        <span>{{ formatBytes(record.rawPayloadBytes || record.payloadBytes) }}</span>
      </template>
      <template v-else-if="column.key === 'compression'">
        <span>{{ compressionText(record.rawPayloadBytes || record.payloadBytes, record.compressedPayloadBytes || record.payloadBytes) }}</span>
      </template>
      <template v-else-if="column.key === 'duration'">
        <span>{{ formatDuration(record.durationMs) }}</span>
      </template>
      <template v-else-if="column.key === 'createdAt'">
        <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="detailActions" @action-click="handleActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card" @click="$emit('detail', record)">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ record.method }} {{ record.path }}</div>
          <div class="mobile-list-card-tags">
            <a-tag :color="outcomeColor(record.auditOutcome)">{{ outcomeText(record.auditOutcome) }}</a-tag>
            <a-tag :color="statusColor(record.finalStatusCode, record.success)">{{ record.finalStatusCode ?? '-' }}</a-tag>
            <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>traceId</span>
            <strong class="mono-cell">{{ record.traceId }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>账号</span>
            <strong>{{ displayName(record.accountName, record.accountId) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>耗时</span>
            <strong>{{ formatDuration(record.durationMs) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>Payload</span>
            <strong>{{ formatBytes(record.rawPayloadBytes || record.payloadBytes) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>时间</span>
            <strong>{{ formatDateTime(record.createdAt) }}</strong>
          </div>
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { AuditLogSummary } from '@/types/domain'
import {
  displayName,
  compressionText,
  formatBytes,
  formatDateTime,
  formatDuration,
  outcomeColor,
  outcomeText,
  statusColor
} from './auditLogFormatters'
import { auditLogColumns } from './auditLogTableColumns'

defineProps<{
  loading: boolean
  loadingMore: boolean
  mobileHasMore: boolean
  pagination: Record<string, unknown>
  records: AuditLogSummary[]
}>()

const emit = defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'detail', record: AuditLogSummary): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

const columns = auditLogColumns
const detailActions: RowActionItem[] = [
  { key: 'detail', label: '详情', icon: 'detail', tone: 'info' }
]

function handleActionClick(key: string, record: AuditLogSummary) {
  if (key === 'detail') {
    emit('detail', record)
  }
}
</script>

<style scoped>
.audit-table :deep(.ant-table-cell) {
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
.endpoint-cell,
.mono-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.trace-cell {
  max-width: 230px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.endpoint-cell {
  display: inline-block;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}
</style>
