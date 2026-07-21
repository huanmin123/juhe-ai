<template>
  <ResponsiveDataList
    table-class="page-table external-source-table"
    :columns="columns"
    :data-source="dataSource"
    row-key="id"
    :loading="loading"
    :pagination="pagination"
    :scroll-x="1620"
    @change="(...args: unknown[]) => emit('change', ...args)"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="暂无公开接口来源授权。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'source'">
        <div class="source-name-cell">
          <div class="source-name-line">
            <strong>{{ record.name }}</strong>
            <a-tag v-if="record.isBuiltIn" color="orange">Mock 数据</a-tag>
          </div>
        </div>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'tokens'">
        <div class="token-preview-cell">
          <span class="token-preview" :title="tokenDisplayTitle(primaryToken(record))">{{ formatTokenPreview(primaryToken(record)) }}</span>
          <a-tooltip title="复制完整 Token">
            <span class="token-copy-button-wrap">
              <a-button
                class="token-copy-button"
                type="text"
                size="small"
                :loading="Boolean(tokenCopyingKey) && tokenCopyingKey === tokenCopyKey(record)"
                :disabled="!primaryToken(record) || (Boolean(tokenCopyingKey) && tokenCopyingKey !== tokenCopyKey(record))"
                @click="emit('copy-token', record)"
              >
                <template #icon><copy-outlined /></template>
              </a-button>
            </span>
          </a-tooltip>
        </div>
      </template>
      <template v-else-if="column.key === 'scopes'">
        <div class="scope-tag-line">
          <a-tag v-if="record.isBuiltIn" color="blue">全部</a-tag>
          <template v-else>
            <a-tag v-for="scope in record.scopes" :key="scope">{{ scopeLabel(scope) }}</a-tag>
          </template>
          <span v-if="!record.isBuiltIn && !record.scopes.length" class="muted-cell">未授权</span>
        </div>
      </template>
      <template v-else-if="column.key === 'rateLimits'">
        <span>{{ formatRateLimits(record.rateLimits) }}</span>
      </template>
      <template v-else-if="column.key === 'notes'">
        <span class="source-note-cell" :title="sourceNotes(record)">{{ sourceNotes(record) }}</span>
      </template>
      <template v-else-if="column.key === 'expiresAt'">
        <span :class="record.expiresAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.expiresAt) }}</span>
      </template>
      <template v-else-if="column.key === 'lastUsedAt'">
        <span :class="record.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.lastUsedAt) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="actions(record)" @action-click="emitActionClick($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div>
            <div class="mobile-list-card-title">
              {{ record.name }}
              <a-tag v-if="record.isBuiltIn" color="orange">Mock 数据</a-tag>
            </div>
          </div>
          <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>Token</span>
            <strong>{{ formatTokenPreview(primaryToken(record)) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>限频</span>
            <strong>{{ formatRateLimits(record.rateLimits) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>到期</span>
            <strong>{{ formatDateTime(record.expiresAt) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>最近调用</span>
            <strong>{{ formatDateTime(record.lastUsedAt) }}</strong>
          </div>
        </div>
        <div class="mobile-list-note">
          <span>备注</span>
          <strong>{{ sourceNotes(record) }}</strong>
        </div>
        <div class="mobile-list-card-actions">
          <RowActions variant="button" :actions="actions(record)" @action-click="emitActionClick($event, record)" />
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResponsiveDataListTablePagination } from '@/components/responsiveDataListTableLayout'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { formatDateTime } from '@/shared/formatters'
import type {
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourcePrimaryTokenSummary
} from '@/types/domain'
import { formatRateLimits } from './externalSourceFormModel'

export type ExternalSourceRowActionKey =
  | 'edit'
  | 'enable'
  | 'disable'
  | 'delete'
  | 'generateToken'
  | 'resetToken'

defineProps<{
  actions: (record: ExternalIntegrationSourceListItem) => RowActionItem[]
  dataSource: ExternalIntegrationSourceListItem[]
  formatTokenPreview: (token: ExternalIntegrationSourcePrimaryTokenSummary | undefined) => string
  loading: boolean
  pagination: ResponsiveDataListTablePagination
  primaryToken: (record: ExternalIntegrationSourceListItem) => ExternalIntegrationSourcePrimaryTokenSummary | undefined
  scopeLabel: (scope: string) => string
  sourceNotes: (record: ExternalIntegrationSourceListItem) => string
  tokenCopyingKey: string
  tokenCopyKey: (record: ExternalIntegrationSourceListItem) => string
  tokenDisplayTitle: (token: ExternalIntegrationSourcePrimaryTokenSummary | undefined) => string
}>()

const emit = defineEmits<{
  change: [...args: unknown[]]
  'action-click': [actionKey: ExternalSourceRowActionKey, record: ExternalIntegrationSourceListItem]
  'copy-token': [record: ExternalIntegrationSourceListItem]
}>()

const columns = [
  { title: '来源授权', key: 'source', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 100, align: 'left' },
  { title: 'Token', key: 'tokens', width: 220, align: 'left' },
  { title: '接口资源授权', key: 'scopes', width: 300, className: 'scope-column', align: 'left' },
  { title: '备注', key: 'notes', width: 260, align: 'left' },
  { title: '限频', key: 'rateLimits', width: 180, align: 'left' },
  { title: '到期时间', key: 'expiresAt', width: 180, align: 'left' },
  { title: '最近调用', key: 'lastUsedAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 120, fixed: 'right', align: 'left' }
]

function emitActionClick(actionKey: string, record: ExternalIntegrationSourceListItem): void {
  emit('action-click', actionKey as ExternalSourceRowActionKey, record)
}

function sourceStatusText(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? '启用' : '停用'
}

function sourceStatusColor(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? 'green' : 'red'
}
</script>

<style scoped>
.source-name-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.source-name-line {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.source-name-line strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.scope-tag-line {
  display: flex;
  width: 300px;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
  white-space: normal;
}

.scope-tag-line :deep(.ant-tag) {
  max-width: 100%;
  margin-inline-end: 0;
  overflow-wrap: anywhere;
  white-space: normal;
}

.source-note-cell {
  display: -webkit-box;
  max-width: 260px;
  overflow: hidden;
  color: #475569;
  line-height: 1.5;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.mobile-list-note {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  border-top: 1px solid #f1f5f9;
  padding-top: 10px;
}

.mobile-list-note span {
  color: #64748b;
  font-size: 12px;
}

.mobile-list-note strong {
  color: #334155;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.external-source-table :deep(.scope-column) {
  max-width: 300px;
  white-space: normal;
}

.token-preview-cell {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
  gap: 8px;
}

.token-preview {
  display: inline-flex;
  align-items: center;
  max-width: calc(100% - 32px);
  box-sizing: border-box;
  padding: 3px 8px;
  overflow: hidden;
  color: #008b8b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  background: #eefafa;
}

.token-copy-button {
  color: #64748b;
}

.token-copy-button-wrap {
  flex: none;
}

.token-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}
</style>
