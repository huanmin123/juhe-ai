<template>
  <ResponsiveDataList
    table-class="page-table authorizations-table"
    :columns="columns"
    :data-source="authorizations"
    row-key="id"
    :loading="loading"
    :scroll-x="tableScrollX"
    pull-refresh-enabled
    :refreshing="loading"
    @mobile-refresh="$emit('refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'resource'">
        <div class="resource-cell">
          <span class="resource-name">{{ record.resourceName || record.resourceId }}</span>
        </div>
      </template>
      <template v-else-if="column.key === 'direction'">
        <a-tag :color="authorizationDirectionColor(record, currentSystemAccountId)">
          {{ authorizationDirectionText(record, currentSystemAccountId) }}
        </a-tag>
      </template>
      <template v-else-if="column.key === 'owner'">
        {{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}
      </template>
      <template v-else-if="column.key === 'grantee'">
        <div class="grantee-cell">
          <span>{{ granteeTargetName(record) }}</span>
          <AuthorizationSourceTag :authorization="record" />
        </div>
      </template>
      <template v-else-if="column.key === 'usageTotal'">
        <UsageSummaryTags :usage="record.usage" />
      </template>
      <template v-else-if="column.key === 'limits'">
        <span>{{ quotaLimitSummaryText(record.limits) }}</span>
      </template>
      <template v-else-if="column.key === 'status'">
        <AuthorizationStatusTag :status="record.status" />
      </template>
      <template v-else-if="column.key === 'createdAt'">
        {{ formatDateTime(record.createdAt) }}
      </template>
      <template v-else-if="column.key === 'lastUsedAt'">
        {{ formatDateTime(record.lastUsedAt ?? record.usage?.lastUsedAt) }}
      </template>
      <template v-else-if="column.key === 'remark'">
        <span>{{ record.remark || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <AuthorizationActions v-if="showActions" :authorization="record" :is-management-view="isManagementView" compact @menu-click="$emit('menu-click', $event, record)" />
      </template>
    </template>

    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ record.resourceName || record.resourceId }}</div>
          <div class="mobile-list-card-tags">
            <a-tag v-if="!isManagementView" :color="authorizationDirectionColor(record, currentSystemAccountId)">
              {{ authorizationDirectionText(record, currentSystemAccountId) }}
            </a-tag>
            <AuthorizationStatusTag :status="record.status" />
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>归属人</span>
            <strong>{{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>被授权的目标</span>
            <strong class="mobile-user-tag-line">
              <span>{{ granteeTargetName(record) }}</span>
              <AuthorizationSourceTag :authorization="record" />
            </strong>
          </div>
          <div v-if="showUsageMetrics" class="mobile-list-meta-item mobile-list-meta-wide">
            <span>{{ usageColumnLabel }}</span>
            <strong><UsageSummaryTags :usage="record.usage" /></strong>
          </div>
          <div v-if="showLastUsedAt" class="mobile-list-meta-item mobile-list-meta-wide">
            <span>最后使用</span>
            <strong>{{ formatDateTime(record.lastUsedAt ?? record.usage?.lastUsedAt) }}</strong>
          </div>
          <div v-if="showQuotaLimits" class="mobile-list-meta-item mobile-list-meta-wide">
            <span>额度限制</span>
            <strong>{{ quotaLimitSummaryText(record.limits) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>说明</span>
            <strong>{{ record.remark || '-' }}</strong>
          </div>
        </div>
        <AuthorizationActions v-if="showActions" :authorization="record" :is-management-view="isManagementView" @menu-click="$emit('menu-click', $event, record)" />
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { ResourceAuthorizationSummary } from '@/types/domain'
import AuthorizationActions from './AuthorizationActions.vue'
import AuthorizationSourceTag from './AuthorizationSourceTag.vue'
import AuthorizationStatusTag from './AuthorizationStatusTag.vue'
import { authorizationColumns, type AuthorizationDirectionFilter } from './authorizationTableColumns'
import { activeTeamSources, authorizationDirectionColor, authorizationDirectionText, formatDateTime, granteeTargetName, hasManualSource, quotaLimitSummaryText } from './authorizationFormatters'

const props = defineProps<{
  authorizations: ResourceAuthorizationSummary[]
  currentSystemAccountId?: string
  emptyDescription: string
  direction: AuthorizationDirectionFilter
  isManagementView: boolean
  loading: boolean
  usageColumnLabel: string
}>()

defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }, authorization: ResourceAuthorizationSummary): void
  (event: 'refresh'): void
}>()

const showActions = computed(() => props.isManagementView || props.direction === 'outbound')
const showUsageMetrics = computed(() => !props.isManagementView)
const showLastUsedAt = computed(() => !props.isManagementView && props.direction === 'outbound')
const showQuotaLimits = computed(() => !props.isManagementView)
const actionColumnWidth = computed(() => {
  if (!showActions.value) return 0
  const maxActionCount = props.authorizations.reduce((maxCount, authorization) => {
    if (!canManageAuthorization(authorization)) return maxCount
    return Math.max(maxCount, authorizationActionCount(authorization))
  }, 0)
  return Math.max(84, 24 + maxActionCount * 30)
})
const columns = computed(() => authorizationColumns.filter((column) => {
  if (props.isManagementView && column.key === 'direction') return false
  if (props.isManagementView && ['usageTotal', 'lastUsedAt', 'limits'].includes(String(column.key))) return false
  if (!showLastUsedAt.value && column.key === 'lastUsedAt') return false
  if (!showActions.value && column.key === 'actions') return false
  return true
}).map((column) => {
  if (column.key === 'usageTotal') return { ...column, title: props.usageColumnLabel }
  if (column.key === 'actions') return { ...column, width: actionColumnWidth.value }
  return column
}))
const tableScrollX = computed(() => props.isManagementView ? 1240 : 1540)

function canManageAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  return props.isManagementView || authorization.permissions?.canEdit === true
}

function authorizationActionCount(authorization: ResourceAuthorizationSummary): number {
  let count = 1
  if (authorization.status === 'active' || authorization.status === 'paused') count += 1
  if (authorization.status === 'active' && hasManualSource(authorization)) count += 1
  count += activeTeamSources(authorization).length
  return count
}
</script>

<style scoped>
.resource-cell,
.grantee-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.resource-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorizations-table :deep(.ant-table-cell) {
  white-space: nowrap;
}
</style>
