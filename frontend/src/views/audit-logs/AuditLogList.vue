<template>
  <ResponsiveDataList
    table-class="page-table audit-table"
    :columns="tableColumns"
    :data-source="records"
    row-key="id"
    :loading="loading"
    :scroll-x="1840"
    :pagination="pagination"
    :mobile-pagination="mobilePagination"
    :mobile-has-more="mobileHasMore"
    :loading-more="loadingMore"
    pull-refresh-enabled
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'traceId'">
        <span class="trace-cell mono-cell">{{ record.traceId }}</span>
      </template>
      <template v-else-if="column.key === 'session'">
        <div v-if="record.conversationKey" class="session-cell">
          <a-tooltip :title="`完整会话 Key：${record.conversationKey}`">
            <a-button class="session-key-button mono-cell" type="link" @click="emit('filter-conversation', record.conversationKey)">
              {{ formatConversationKeyPreview(record.conversationKey) }}
            </a-button>
          </a-tooltip>
          <div class="session-cell-meta">
            <a-tag :color="sessionResolutionColor(record.sessionResolution)">{{ sessionResolutionText(record.sessionResolution) }}</a-tag>
            <span v-if="record.sessionSource">{{ record.sessionSource }}</span>
            <a-tag v-if="record.identityConflict" color="red">冲突</a-tag>
          </div>
        </div>
        <span v-else class="session-cell-meta">
          <a-tag :color="sessionResolutionColor(record.sessionResolution)">{{ sessionResolutionText(record.sessionResolution) }}</a-tag>
          <span v-if="record.sessionSource">{{ record.sessionSource }}</span>
        </span>
      </template>
      <template v-else-if="column.key === 'outcome'">
        <a-tag :color="outcomeColor(record.auditOutcome)">{{ outcomeText(record.auditOutcome) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="statusColor(record.finalStatusCode, record.success)">{{ record.finalStatusCode ?? '-' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'trafficSource'">
        <a-tag :color="trafficSourceColor(record.trafficSource)">{{ trafficSourceText(record.trafficSource) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'endpoint'">
        <a-tooltip :title="auditEndpointText(record)" placement="topLeft">
          <span class="endpoint-cell">{{ auditEndpointText(record) }}</span>
        </a-tooltip>
      </template>
      <template v-else-if="column.key === 'model'">
        <span v-if="record.model" class="model-cell">
          <a-tag color="blue">{{ record.model }}</a-tag>
          <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
        </span>
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
        <span>{{ displayAuditGroupName(record.groupName, record.groupId) }}</span>
      </template>
      <template v-else-if="column.key === 'systemAccount'">
        <span>{{ displayName(record.systemAccountName, record.systemAccountId) }}</span>
      </template>
      <template v-else-if="column.key === 'duration'">
        <a-tooltip :title="auditDurationLabel(record)">
          <span>{{ formatDuration(auditDurationMs(record)) }}</span>
        </a-tooltip>
      </template>
      <template v-else-if="column.key === 'createdAt'">
        <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="detailActions" @action-click="handleActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ record.method }} {{ record.path }}</div>
          <div class="mobile-list-card-tags">
            <a-tag :color="outcomeColor(record.auditOutcome)">{{ outcomeText(record.auditOutcome) }}</a-tag>
            <a-tag :color="statusColor(record.finalStatusCode, record.success)">{{ record.finalStatusCode ?? '-' }}</a-tag>
            <a-tag :color="trafficSourceColor(record.trafficSource)">{{ trafficSourceText(record.trafficSource) }}</a-tag>
            <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
            <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
            <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>traceId</span>
            <strong class="mono-cell">{{ record.traceId }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>会话</span>
            <a-button
              v-if="record.conversationKey"
              class="session-key-button mono-cell"
              type="link"
              @click="emit('filter-conversation', record.conversationKey)"
            >
              {{ formatConversationKeyPreview(record.conversationKey) }}
            </a-button>
            <strong v-else>{{ sessionResolutionText(record.sessionResolution) }}</strong>
            <small v-if="record.sessionSource">{{ record.sessionSource }} · {{ sessionResolutionText(record.sessionResolution) }}</small>
          </div>
          <div class="mobile-list-meta-item">
            <span>AI账户</span>
            <strong>{{ displayName(record.accountName, record.accountId) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>{{ auditDurationLabel(record) }}</span>
            <strong>{{ formatDuration(auditDurationMs(record)) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>时间</span>
            <strong>{{ formatDateTime(record.createdAt) }}</strong>
          </div>
        </div>
        <div class="mobile-list-card-actions">
          <RowActions variant="button" :actions="detailActions" @action-click="handleActionClick($event, record)" />
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
import type { AuditLogListItem } from '@/types/domain'
import {
  displayAuditGroupName,
  displayName,
  formatConversationKeyPreview,
  formatDateTime,
  formatDuration,
  outcomeColor,
  outcomeText,
  sessionResolutionColor,
  sessionResolutionText,
  statusColor,
  trafficSourceColor,
  trafficSourceText
} from './auditLogFormatters'
import { auditLogColumns } from './auditLogTableColumns'

const props = withDefaults(defineProps<{
  columns?: Array<Record<string, unknown>>
  loading: boolean
  loadingMore: boolean
  mobileHasMore: boolean
  mobilePagination?: boolean
  pagination: Record<string, unknown>
  records: AuditLogListItem[]
  emptyDescription?: string
}>(), {
  columns: () => auditLogColumns,
  mobilePagination: true,
  emptyDescription: '暂无审计日志。'
})

const emit = defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'detail', record: AuditLogListItem): void
  (event: 'filter-conversation', conversationKey: string): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

const tableColumns = computed(() => props.columns)
const detailActions: RowActionItem[] = [
  { key: 'detail', label: '详情', icon: 'detail', tone: 'info' }
]

function handleActionClick(key: string, record: AuditLogListItem) {
  if (key === 'detail') {
    emit('detail', record)
  }
}

function auditEndpointText(record: AuditLogListItem): string {
  return `${record.method} ${record.path}`
}

function auditDurationMs(record: AuditLogListItem): number | undefined {
  return record.httpDurationMs ?? record.durationMs
}

function auditDurationLabel(record: AuditLogListItem): string {
  return record.httpDurationMs === undefined ? '审计耗时' : '客户端耗时'
}
</script>

<style scoped>
.audit-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.trace-cell,
.endpoint-cell,
.mono-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.trace-cell {
  display: inline-block;
  max-width: 230px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-cell {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.session-key-button {
  display: inline-flex;
  width: fit-content;
  max-width: 100%;
  height: auto;
  padding: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-cell-meta {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  gap: 4px;
  color: #64748b;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.session-cell-meta .ant-tag {
  flex: 0 0 auto;
  margin-inline-end: 0;
}

.mobile-list-meta-item small {
  display: block;
  overflow-wrap: anywhere;
  color: #64748b;
}

.endpoint-cell {
  display: inline-block;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}

.model-cell {
  display: inline-flex;
  max-width: 260px;
  flex-wrap: wrap;
  gap: 4px;
  vertical-align: bottom;
}
</style>
