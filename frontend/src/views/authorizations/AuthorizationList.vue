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
          <a-tag :color="resourceTypeTag(record.resourceType).color">{{ resourceTypeTag(record.resourceType).text }}</a-tag>
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
      <template v-else-if="column.key === 'status'">
        <AuthorizationStatusTag :status="record.status" />
      </template>
      <template v-else-if="column.key === 'createdAt'">
        {{ formatDateTime(record.createdAt) }}
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
            <a-tag :color="resourceTypeTag(record.resourceType).color">{{ resourceTypeTag(record.resourceType).text }}</a-tag>
            <a-tag v-if="!isManagementView" :color="authorizationDirectionColor(record, currentSystemAccountId)">
              {{ authorizationDirectionText(record, currentSystemAccountId) }}
            </a-tag>
            <AuthorizationStatusTag :status="record.status" />
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>资源归属人</span>
            <strong>{{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>被授权的目标</span>
            <strong class="mobile-user-tag-line">
              <span>{{ granteeTargetName(record) }}</span>
              <AuthorizationSourceTag :authorization="record" />
            </strong>
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
import type { ResourceAuthorizationSummary } from '@/types/domain'
import AuthorizationActions from './AuthorizationActions.vue'
import AuthorizationSourceTag from './AuthorizationSourceTag.vue'
import AuthorizationStatusTag from './AuthorizationStatusTag.vue'
import { authorizationColumns, type AuthorizationDirectionFilter } from './authorizationTableColumns'
import { activeTeamSources, authorizationDirectionColor, authorizationDirectionText, formatDateTime, granteeTargetName, hasManualSource } from './authorizationFormatters'
import type { AuthorizationResourceType } from '@/types/domain'

const props = defineProps<{
  authorizations: ResourceAuthorizationSummary[]
  currentSystemAccountId?: string
  emptyDescription: string
  direction: AuthorizationDirectionFilter
  isManagementView: boolean
  loading: boolean
}>()

defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }, authorization: ResourceAuthorizationSummary): void
  (event: 'refresh'): void
}>()

const showActions = computed(() => props.isManagementView || props.direction === 'outbound')
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
  if (['usageTotal', 'lastUsedAt', 'limits'].includes(String(column.key))) return false
  if (!showActions.value && column.key === 'actions') return false
  return true
}).map((column) => {
  if (column.key === 'actions') return { ...column, width: actionColumnWidth.value }
  return column
}))
const tableScrollX = computed(() => props.isManagementView ? 1240 : 1320)

function canManageAuthorization(authorization: ResourceAuthorizationSummary): boolean {
  return props.isManagementView || authorization.permissions?.canEdit === true
}

function authorizationActionCount(authorization: ResourceAuthorizationSummary): number {
  let count = 1
  if (authorization.status === 'active' || authorization.status === 'paused') count += 1
  if (authorization.granteeType === 'team') return count + 1
  if (authorization.status === 'active' && hasManualSource(authorization)) count += 1
  count += activeTeamSources(authorization).length
  return count
}

function resourceTypeTag(resourceType: AuthorizationResourceType) {
  return resourceType === 'group'
    ? { text: '分组', color: 'purple' }
    : { text: 'AI账户', color: 'blue' }
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

.resource-cell :deep(.ant-tag) {
  flex: 0 0 auto;
  margin-inline-end: 0;
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
