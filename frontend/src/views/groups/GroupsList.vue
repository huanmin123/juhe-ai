<template>
  <ResponsiveDataList
    table-class="page-table groups-table"
    :columns="columns"
    :data-source="groups"
    row-key="id"
    :loading="loading"
    :loading-more="mobileLoadingMore"
    :mobile-has-more="mobileHasMore"
    :pagination="tablePagination"
    :scroll-x="isManagementView ? 1610 : 1430"
    mobile-pagination
    pull-refresh-enabled
    :refreshing="loading"
    @change="forwardTableChange"
    @mobile-load-more="emit('mobile-load-more')"
    @mobile-refresh="emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="先创建一个分组，再到账户页把账户加入对应分组。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'name'">
        <div class="group-name-cell">
          <span class="group-name-line">
            <span class="group-name-text">{{ record.name }}</span>
            <a-tooltip v-if="groupInfoTooltip(record)">
              <template #title>
                <span class="authorized-tooltip-text">{{ groupInfoTooltip(record) }}</span>
              </template>
              <InfoCircleOutlined class="authorized-group-icon" :class="groupInfoIconClass(record)" />
            </a-tooltip>
          </span>
        </div>
      </template>
      <template v-else-if="column.key === 'providerCode'">
        <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'groupType'">
        <a-tooltip :title="groupPolicySummary(record)">
          <a-tag :color="groupTypeColor(record.groupType)">{{ groupTypeText(record.groupType) }}</a-tag>
        </a-tooltip>
      </template>
      <template v-else-if="column.key === 'systemAccount'">
        <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ groupSystemAccountText(record) }}</span>
      </template>
      <template v-else-if="column.key === 'description'">
        <span class="group-description-column-text">{{ groupDisplayDescription(record) || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'accountCount'">
        <a-tooltip :title="groupAccountStatsTooltip(record)">
          <div class="account-count-cell">
            <span class="account-count-row">
              <span class="account-count-label">可用:</span>
              <span class="account-count-value available">{{ groupStats(record).available }}</span>
              <span class="account-count-unit">个账号</span>
            </span>
            <span class="account-count-row">
              <span class="account-count-label">总量:</span>
              <span class="account-count-value">{{ groupStats(record).total }}</span>
              <span class="account-count-unit">个账号</span>
            </span>
          </div>
        </a-tooltip>
      </template>
      <template v-else-if="column.key === 'concurrency'">
        <a-tooltip :title="groupConcurrencyTooltip(record)">
          <a-tag :color="groupConcurrencyAvailable(record) ? 'blue' : 'default'">{{ groupConcurrencyText(record) }}</a-tag>
        </a-tooltip>
      </template>
      <template v-else-if="column.key === 'usage'">
        <UsageSummaryTags :usage="groupStats(record).todayUsage" />
      </template>
      <template v-else-if="column.key === 'status'">
        <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions
          v-if="groupRowActions(record).length || groupMoreActions(record).length"
          :actions="groupRowActions(record)"
          :more-actions="groupMoreActions(record)"
          @action-click="emit('action', $event, record)"
        />
      </template>
    </template>
    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">
            <div class="mobile-list-card-name-row">
              <span>{{ record.name }}</span>
              <a-tooltip v-if="groupInfoTooltip(record)">
                <template #title>
                  <span class="authorized-tooltip-text">{{ groupInfoTooltip(record) }}</span>
                </template>
                <InfoCircleOutlined class="authorized-group-icon" :class="groupInfoIconClass(record)" />
              </a-tooltip>
            </div>
          </div>
          <div class="mobile-list-card-tags">
            <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
            <a-tag :color="groupTypeColor(record.groupType)">{{ groupTypeText(record.groupType) }}</a-tag>
            <StatusTag class="status-tag" :color="groupStatusColor(record)" :label="groupStatusText(record)" />
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
            <span>系统账户</span>
            <strong>{{ groupSystemAccountText(record) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>说明</span>
            <strong>{{ groupDisplayDescription(record) || '-' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>可用账号</span>
            <strong>{{ groupStats(record).available }} / {{ groupStats(record).total }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>并发</span>
            <a-tooltip :title="groupConcurrencyTooltip(record)">
              <strong>{{ groupConcurrencyText(record) }}</strong>
            </a-tooltip>
          </div>
          <div class="mobile-list-meta-item">
            <span>用量(日)</span>
            <strong>{{ formatUsageSummary(groupStats(record).todayUsage) }}</strong>
          </div>
        </div>
        <div v-if="groupRowActions(record).length || groupMoreActions(record).length" class="mobile-list-card-actions">
          <RowActions
            variant="button"
            :actions="groupRowActions(record)"
            :more-actions="groupMoreActions(record)"
            @action-click="emit('action', $event, record)"
          />
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import StatusTag from '@/components/StatusTag.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { GroupSummary } from '@/types/domain'
import {
  formatUsageSummary,
  groupAccountStatsTooltip,
  groupConcurrencyAvailable,
  groupConcurrencyText,
  groupConcurrencyTooltip,
  groupDisplayDescription,
  groupInfoIconClass,
  groupInfoTooltip,
  groupStats,
  groupStatusColor,
  groupStatusText,
  groupSystemAccountText
} from './groupDisplay'
import {
  groupPolicySummary,
  groupTypeColor,
  groupTypeText
} from './groupSchedulingPolicy'
import {
  groupMoreActions,
  groupRowActions
} from './groupRowActions'

defineProps<{
  columns: Array<Record<string, unknown>>
  groups: GroupSummary[]
  isManagementView: boolean
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  providerName: (providerCode?: string) => string
  tablePagination?: false | Record<string, unknown>
}>()

const emit = defineEmits<{
  (event: 'action', key: string, group: GroupSummary): void
  (event: 'change', ...args: unknown[]): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

function forwardTableChange(...args: unknown[]) {
  emit('change', ...args)
}
</script>

<style scoped>
:deep(.groups-table .ant-table-cell) {
  white-space: nowrap;
}

:deep(.groups-table .ant-empty) {
  margin: 12px 0;
}

:deep(.groups-table .group-name-header-cell),
:deep(.groups-table .group-name-header-cell .ant-table-column-title) {
  font-weight: 400;
}

.group-name-cell {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.group-name-line,
.mobile-list-card-name-row,
.account-count-cell,
.account-count-row {
  display: flex;
  align-items: center;
  gap: 4px;
  color: #475569;
}

.group-name-text,
.group-description-column-text,
.mobile-list-card-name-row span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-name-text {
  color: #0f172a;
  font-weight: 400;
}

.mobile-list-card-title {
  font-weight: 400;
}

.group-description-column-text {
  color: #64748b;
  font-size: 12px;
}

.account-count-cell {
  flex-direction: column;
  align-items: flex-start;
}

.account-count-label {
  min-width: 38px;
  text-align: right;
}

.account-count-value {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 400;
}

.account-count-value.available {
  color: #0891b2;
}

.account-count-value.limited {
  color: #f59e0b;
}

.account-count-unit {
  padding: 1px 6px;
  color: #334155;
  background: #f1f5f9;
  border-radius: 4px;
}

.account-count-row,
.account-count-unit {
  color: #64748b;
  font-size: 12px;
}

.status-tag {
  width: fit-content;
}

.authorized-group-icon {
  flex: none;
  color: #08979c;
  cursor: help;
  font-size: 14px;
}

.authorized-group-icon.source-danger {
  color: #cf1322;
}

.authorized-group-icon.source-warning {
  color: #d48806;
}

.authorized-tooltip-text {
  white-space: pre-line;
}
</style>
