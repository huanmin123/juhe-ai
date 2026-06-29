<template>
  <ResponsiveDataList
    table-class="page-table api-keys-table"
    :columns="columns"
    :data-source="dataSource"
    :mobile-data-source="mobileDataSource"
    row-key="id"
    :loading="loading"
    :loading-more="loadingMore"
    :mobile-has-more="mobileHasMore"
    :pagination="pagination"
    :scroll-x="isManagementView ? 2020 : 1840"
    mobile-pagination
    pull-refresh-enabled
    :refreshing="loading"
    @change="(...args: unknown[]) => emit('change', ...args)"
    @mobile-load-more="emit('mobile-load-more')"
    @mobile-refresh="emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定策略路由；接入说明可点击右上角帮助查看。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'name'">
        <div class="name-with-tag">
          <span>{{ record.name }}</span>
          <a-tag v-if="record.isDefault" color="blue">默认</a-tag>
        </div>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tooltip v-if="apiKeyStatusTooltipLines(record).length" placement="topLeft">
          <template #title>
            <div class="status-tooltip">
              <div v-for="line in apiKeyStatusTooltipLines(record)" :key="line">{{ line }}</div>
            </div>
          </template>
          <span>
            <StatusTag :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
          </span>
        </a-tooltip>
        <StatusTag v-else :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
      </template>
      <template v-else-if="column.key === 'availabilitySchedule'">
        <a-tag class="schedule-tag" :color="apiKeyScheduleTagColor(record)">
          {{ apiKeyScheduleSummary(record.availabilitySchedule) }}
        </a-tag>
      </template>
      <template v-else-if="column.key === 'usage'">
        <UsageSummaryTags :usage="record.usage" />
      </template>
      <template v-else-if="column.key === 'key'">
        <div class="key-preview-cell">
          <span class="key-preview" :title="keyDisplayTitle(record)">{{ formatKeyPreview(record) }}</span>
          <a-tooltip title="复制完整密钥">
            <span class="key-copy-button-wrap">
              <a-button
                class="key-copy-button"
                type="text"
                size="small"
                :loading="keyCopyingId === record.id"
                :disabled="Boolean(keyCopyingId) && keyCopyingId !== record.id"
                @click="emit('copy-key', record)"
              >
                <template #icon><copy-outlined /></template>
              </a-button>
            </span>
          </a-tooltip>
        </div>
      </template>
      <template v-else-if="column.key === 'routeStrategy'">
        <div class="route-strategy-cell">
          <span>{{ apiKeyRouteStrategyName(record) }}</span>
          <a-tag :color="apiKeyRouteStrategyTagColor(record)">{{ apiKeyRouteStrategyModeText(record.routeStrategyMode) }}</a-tag>
        </div>
      </template>
      <template v-else-if="column.key === 'systemAccount'">
        <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ apiKeySystemAccountText(record) }}</span>
      </template>
      <template v-else-if="column.key === 'quotaLimits'">
        <span>{{ quotaLimitSummaryText(record.quotaLimits) }}</span>
      </template>
      <template v-else-if="column.key === 'description'">
        <span>{{ record.description || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="primaryActions(record)" :more-actions="moreActions(record)" @action-click="emit('action-click', $event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">
            <span>{{ record.name }}</span>
            <a-tag v-if="record.isDefault" color="blue">默认</a-tag>
          </div>
          <div class="mobile-list-card-tags">
            <a-tooltip v-if="apiKeyStatusTooltipLines(record).length" placement="topLeft">
              <template #title>
                <div class="status-tooltip">
                  <div v-for="line in apiKeyStatusTooltipLines(record)" :key="line">{{ line }}</div>
                </div>
              </template>
              <span>
                <StatusTag :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
              </span>
            </a-tooltip>
            <StatusTag v-else :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
            <a-tag :color="apiKeyRouteStrategyTagColor(record)">{{ apiKeyRouteStrategyModeText(record.routeStrategyMode) }}</a-tag>
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>API Key</span>
            <strong>{{ formatKeyPreview(record) }}</strong>
          </div>
          <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
            <span>系统账户</span>
            <strong>{{ apiKeySystemAccountText(record) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>过期时间</span>
            <strong>{{ formatDateTime(record.expiresAt) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>时间计划</span>
            <strong>{{ apiKeyScheduleSummary(record.availabilitySchedule) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>累计用量</span>
            <strong>{{ formatUsageSummary(record.usage) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>美元额度</span>
            <strong>{{ quotaLimitSummaryText(record.quotaLimits) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>策略路由</span>
            <strong>{{ apiKeyRouteStrategyName(record) }} / {{ apiKeyRouteStrategyModeText(record.routeStrategyMode) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>说明</span>
            <strong>{{ record.description || '-' }}</strong>
          </div>
        </div>
        <div class="mobile-list-card-actions">
          <RowActions variant="button" :actions="primaryActions(record)" :more-actions="moreActions(record)" @action-click="emit('action-click', $event, record)" />
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
import StatusTag from '@/components/StatusTag.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { formatDateTime } from '@/shared/formatters'
import type { ApiKeySummary } from '@/types/domain'
import { quotaLimitSummaryText } from '@/views/shared/requestQuotaFormatters'
import {
  apiKeyRouteStrategyModeText,
  apiKeyRouteStrategyName,
  apiKeyRouteStrategyTagColor,
  apiKeyScheduleSummary,
  apiKeyScheduleTagColor,
  apiKeyStatusTagColor,
  apiKeyStatusTagLabel,
  apiKeyStatusTooltipLines,
  apiKeySystemAccountText,
  formatKeyPreview,
  formatUsageSummary,
  keyDisplayTitle
} from './apiKeyFormatters'

defineProps<{
  columns: Array<Record<string, any>>
  dataSource: ApiKeySummary[]
  keyCopyingId: string
  loading: boolean
  loadingMore: boolean
  mobileDataSource: ApiKeySummary[]
  mobileHasMore: boolean
  moreActions: (record: ApiKeySummary) => RowActionItem[]
  pagination: ResponsiveDataListTablePagination
  primaryActions: (record: ApiKeySummary) => RowActionItem[]
  isManagementView: boolean
}>()

const emit = defineEmits<{
  change: [...args: unknown[]]
  'action-click': [actionKey: string, record: ApiKeySummary]
  'copy-key': [record: ApiKeySummary]
  'mobile-load-more': []
  'mobile-refresh': []
}>()
</script>

<style scoped>
.api-keys-table :deep(.ant-empty) {
  margin: 12px 0;
}

.api-keys-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.route-strategy-cell {
  display: flex;
  align-items: center;
  gap: 4px;
  max-width: 320px;
}

.name-with-tag,
.mobile-list-card-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.name-with-tag span,
.mobile-list-card-title span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.route-strategy-cell span {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.schedule-tag {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-tooltip {
  max-width: 320px;
  line-height: 1.7;
  white-space: pre-wrap;
}

.route-tooltip {
  max-width: 360px;
  line-height: 1.7;
  white-space: pre-line;
}

.key-preview-cell {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
  gap: 8px;
}

.key-preview {
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

.key-copy-button {
  color: #94a3b8;
}

.key-copy-button-wrap {
  flex: none;
}

.key-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}
</style>
