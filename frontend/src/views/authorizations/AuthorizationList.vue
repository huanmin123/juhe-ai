<template>
  <ResponsiveDataList
    table-class="page-table authorizations-table"
    :columns="columns"
    :data-source="authorizations"
    row-key="id"
    :loading="loading"
    :scroll-x="1540"
    pull-refresh-enabled
    :refreshing="loading"
    @mobile-refresh="$emit('refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="暂无授权记录，请先新增授权。" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'resource'">
        <div class="resource-cell">
          <span class="resource-name">{{ record.resourceName || record.resourceId }}</span>
        </div>
      </template>
      <template v-else-if="column.key === 'owner'">
        {{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}
      </template>
      <template v-else-if="column.key === 'grantee'">
        <div class="grantee-cell">
          <span>{{ record.granteeSystemAccountName || record.granteeSystemAccountId }}</span>
          <AuthorizationSourceTag :authorization="record" />
        </div>
      </template>
      <template v-else-if="column.key === 'usageTotal'">
        <div class="usage-total-cell">
          <span>{{ usageSummaryText(record.usage) }}</span>
        </div>
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
      <template v-else-if="column.key === 'remark'">
        <span>{{ record.remark || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <AuthorizationActions :authorization="record" compact @usage-detail="$emit('usage-detail', record)" @menu-click="$emit('menu-click', $event, record)" />
      </template>
    </template>

    <template #card="{ record }">
      <article class="mobile-list-card">
        <div class="mobile-list-card-head">
          <div class="mobile-list-card-title">{{ record.resourceName || record.resourceId }}</div>
          <div class="mobile-list-card-tags">
            <AuthorizationStatusTag :status="record.status" />
          </div>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>归属人</span>
            <strong>{{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>被授权用户</span>
            <strong class="mobile-user-tag-line">
              <span>{{ record.granteeSystemAccountName || record.granteeSystemAccountId }}</span>
              <AuthorizationSourceTag :authorization="record" />
            </strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>用量(日)</span>
            <strong>{{ usageSummaryText(record.usage) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>额度限制</span>
            <strong>{{ quotaLimitSummaryText(record.limits) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>说明</span>
            <strong>{{ record.remark || '-' }}</strong>
          </div>
        </div>
        <AuthorizationActions :authorization="record" @usage-detail="$emit('usage-detail', record)" @menu-click="$emit('menu-click', $event, record)" />
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResourceAuthorizationSummary } from '@/types/domain'
import AuthorizationActions from './AuthorizationActions.vue'
import AuthorizationSourceTag from './AuthorizationSourceTag.vue'
import AuthorizationStatusTag from './AuthorizationStatusTag.vue'
import { authorizationColumns } from './authorizationTableColumns'
import { formatDateTime, quotaLimitSummaryText, usageSummaryText } from './authorizationFormatters'

defineProps<{
  authorizations: ResourceAuthorizationSummary[]
  loading: boolean
}>()

defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }, authorization: ResourceAuthorizationSummary): void
  (event: 'refresh'): void
  (event: 'usage-detail', authorization: ResourceAuthorizationSummary): void
}>()

const columns = authorizationColumns
</script>

<style scoped>
.resource-cell,
.grantee-cell,
.usage-total-cell {
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
