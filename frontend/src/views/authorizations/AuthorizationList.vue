<template>
  <ResponsiveDataList
    table-class="page-table authorizations-table"
    :columns="columns"
    :data-source="authorizations"
    row-key="id"
    :loading="loading"
    :loading-more="loadingMore"
    :mobile-has-more="mobileHasMore"
    :pagination="pagination"
    :scroll-x="tableScrollX"
    mobile-pagination
    pull-refresh-enabled
    :refreshing="loading"
    @change="$emit('change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'resource'">
        <div class="resource-cell">
          <span class="resource-name">{{ record.resourceName || '-' }}</span>
          <a-tag :color="resourceTypeTag(record.resourceType).color">{{ resourceTypeTag(record.resourceType).text }}</a-tag>
        </div>
      </template>
      <template v-else-if="column.key === 'direction'">
        <a-tag :color="authorizationDirectionColor(record, currentSystemAccountId)">
          {{ authorizationDirectionText(record, currentSystemAccountId) }}
        </a-tag>
      </template>
      <template v-else-if="column.key === 'owner'">
        {{ record.resourceOwnerSystemAccountName || '-' }}
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
        <AuthorizationActions v-if="showActions" :authorization="record" :direction="direction" :is-management-view="isManagementView" compact @menu-click="$emit('menu-click', $event, record)" />
      </template>
    </template>

    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ record.resourceName || '-' }}</div>
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
            <strong>{{ record.resourceOwnerSystemAccountName || '-' }}</strong>
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
        <AuthorizationActions v-if="showActions" :authorization="record" :direction="direction" :is-management-view="isManagementView" @menu-click="$emit('menu-click', $event, record)" />
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResourceAuthorizationListItem } from '@/types/domain'
import AuthorizationActions from './AuthorizationActions.vue'
import AuthorizationSourceTag from './AuthorizationSourceTag.vue'
import AuthorizationStatusTag from './AuthorizationStatusTag.vue'
import { authorizationColumns, type AuthorizationDirectionFilter } from './authorizationTableColumns'
import { authorizationDirectionColor, authorizationDirectionText, formatDateTime, granteeTargetName, hasManualSource } from './authorizationFormatters'
import type { AuthorizationResourceType } from '@/types/domain'

const props = defineProps<{
  authorizations: ResourceAuthorizationListItem[]
  columns?: Array<Record<string, unknown>>
  currentSystemAccountId?: string
  emptyDescription: string
  direction: AuthorizationDirectionFilter
  isManagementView: boolean
  loading: boolean
  loadingMore?: boolean
  mobileHasMore?: boolean
  pagination?: false | Record<string, any>
}>()

defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'menu-click', menuEvent: { key: string | number }, authorization: ResourceAuthorizationListItem): void
  (event: 'mobile-load-more'): void
  (event: 'refresh'): void
}>()

const showActions = computed(() => props.isManagementView || props.direction === 'outbound' || hasReturnableInboundAuthorization.value)
const hasReturnableInboundAuthorization = computed(() => {
  if (props.isManagementView || props.direction !== 'inbound') return false
  return props.authorizations.some((authorization) => canReturnAuthorization(authorization))
})
const defaultColumns = computed(() => authorizationColumns.filter((column) => {
  if (props.isManagementView && column.key === 'direction') return false
  if (['usageTotal', 'lastUsedAt', 'limits'].includes(String(column.key))) return false
  if (!showActions.value && column.key === 'actions') return false
  return true
}))
const columns = computed(() => props.columns ?? defaultColumns.value)
const tableScrollX = computed(() => props.isManagementView ? 1240 : 1320)

function canReturnAuthorization(authorization: ResourceAuthorizationListItem): boolean {
  if (props.isManagementView || props.direction !== 'inbound') return false
  if (authorization.granteeType !== 'system_account') return false
  if (!hasManualSource(authorization)) return false
  return authorization.status !== 'revoked' && authorization.status !== 'returned'
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
