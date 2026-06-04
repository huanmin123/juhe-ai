<template>
  <ResponsiveListToolbar
    :keyword="keyword"
    search-placeholder="搜索资源 / 授权对象"
    filter-title="筛选授权"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="loading"
    @update:keyword="$emit('update:keyword', $event)"
    @reset="$emit('reset')"
    @refresh="$emit('refresh')"
    @search="$emit('refresh')"
    >
    <template #inline-filters>
      <a-segmented
        v-if="!isManagementView"
        v-model:value="filters.direction"
        :options="directionOptions"
        class="direction-filter responsive-list-inline-filter"
        @change="$emit('refresh')"
      />
    </template>
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item v-if="!isManagementView" label="授权方式">
          <a-select v-model:value="filters.sourceType" :options="sourceOptions" @change="$emit('refresh')" />
        </a-form-item>
        <a-form-item label="授权状态">
          <a-select v-model:value="filters.status" :options="statusOptions" @change="$emit('refresh')" />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="授权内容">
          <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="资源归属用户">
          <SystemPrincipalSelect
            v-model:value="filters.resourceOwnerSystemAccountId"
            v-model:selected-principal="filters.resourceOwnerSystemAccount"
            :accounts="ownerUsers"
            :active-only="false"
            :filter-option="false"
            :loading="ownerLoading"
            include-all
            all-label="全部资源归属用户"
            placeholder="筛选资源归属用户"
            @dropdown-visible-change="$emit('owner-dropdown', $event)"
            @search="$emit('owner-search', $event)"
            @change="$emit('owner-change')"
          />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="授权资源">
          <GroupSelect
            v-if="filters.resourceType === 'group'"
            v-model:value="filters.resourceId"
            v-model:selected-group="filters.resourceGroup"
            allow-clear
            :disabled="resourceDisabled"
            :filter-option="false"
            :loading="resourceLoading"
            :options="resourceOptions"
            :placeholder="resourcePlaceholder"
            @dropdown-visible-change="$emit('resource-dropdown', $event)"
            @search="$emit('resource-search', $event)"
            @change="$emit('refresh')"
          />
          <AccountSelect
            v-else
            v-model:value="filters.resourceId"
            v-model:selected-account="filters.resourceAccount"
            allow-clear
            cache-key="accounts"
            :filter-option="false"
            :loading="resourceLoading"
            :options="resourceOptions"
            :disabled="filters.resourceType === 'all'"
            :placeholder="filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'"
            @dropdown-visible-change="$emit('resource-dropdown', $event)"
            @search="$emit('resource-search', $event)"
            @change="$emit('refresh')"
          />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="被授权团队">
          <SystemPrincipalSelect
            v-model:value="filters.teamId"
            v-model:selected-principal="filters.team"
            :teams="teams"
            :active-only="false"
            allow-clear
            :filter-option="false"
            :loading="teamLoading"
            scope="team"
            placeholder="筛选被授权团队"
            @dropdown-visible-change="$emit('team-dropdown', $event)"
            @search="$emit('team-search', $event)"
            @change="$emit('refresh')"
          />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="被授权用户">
          <SystemPrincipalSelect
            v-model:value="filters.granteeSystemAccountId"
            v-model:selected-principal="filters.granteeSystemAccount"
            :accounts="users"
            :active-only="false"
            allow-clear
            :filter-option="false"
            :loading="userLoading"
            placeholder="筛选被授权用户"
            @dropdown-visible-change="$emit('user-dropdown', $event)"
            @search="$emit('user-search', $event)"
            @change="$emit('refresh')"
          />
        </a-form-item>
      </a-form>
    </template>
    <template #filters>
      <label v-if="!isManagementView" class="mobile-filter-field">
        <span>授权方向</span>
        <a-segmented v-model:value="filters.direction" :options="directionOptions" @change="$emit('refresh')" />
      </label>
      <label v-if="!isManagementView" class="mobile-filter-field">
        <span>授权方式</span>
        <a-select v-model:value="filters.sourceType" :options="sourceOptions" @change="$emit('refresh')" />
      </label>
      <label class="mobile-filter-field">
        <span>授权状态</span>
        <a-select v-model:value="filters.status" :options="statusOptions" @change="$emit('refresh')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>授权内容</span>
        <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>资源归属用户</span>
        <SystemPrincipalSelect
          v-model:value="filters.resourceOwnerSystemAccountId"
          v-model:selected-principal="filters.resourceOwnerSystemAccount"
          :accounts="ownerUsers"
          :active-only="false"
          :filter-option="false"
          :loading="ownerLoading"
          include-all
          all-label="全部资源归属用户"
          placeholder="筛选资源归属用户"
          @dropdown-visible-change="$emit('owner-dropdown', $event)"
          @search="$emit('owner-search', $event)"
          @change="$emit('owner-change')"
        />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>授权资源</span>
        <GroupSelect
          v-if="filters.resourceType === 'group'"
          v-model:value="filters.resourceId"
          v-model:selected-group="filters.resourceGroup"
          allow-clear
          :disabled="resourceDisabled"
          :filter-option="false"
          :loading="resourceLoading"
          :options="resourceOptions"
          :placeholder="resourcePlaceholder"
          @dropdown-visible-change="$emit('resource-dropdown', $event)"
          @search="$emit('resource-search', $event)"
          @change="$emit('refresh')"
        />
        <AccountSelect
          v-else
          v-model:value="filters.resourceId"
          v-model:selected-account="filters.resourceAccount"
          allow-clear
          cache-key="accounts"
          :filter-option="false"
          :loading="resourceLoading"
          :options="resourceOptions"
          :disabled="filters.resourceType === 'all'"
          :placeholder="filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'"
          @dropdown-visible-change="$emit('resource-dropdown', $event)"
          @search="$emit('resource-search', $event)"
          @change="$emit('refresh')"
        />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>被授权团队</span>
        <SystemPrincipalSelect
          v-model:value="filters.teamId"
          v-model:selected-principal="filters.team"
          :teams="teams"
          :active-only="false"
          allow-clear
          :filter-option="false"
          :loading="teamLoading"
          scope="team"
          placeholder="筛选被授权团队"
          @dropdown-visible-change="$emit('team-dropdown', $event)"
          @search="$emit('team-search', $event)"
          @change="$emit('refresh')"
        />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>被授权用户</span>
        <SystemPrincipalSelect
          v-model:value="filters.granteeSystemAccountId"
          v-model:selected-principal="filters.granteeSystemAccount"
          :accounts="users"
          :active-only="false"
          allow-clear
          :filter-option="false"
          :loading="userLoading"
          placeholder="筛选被授权用户"
          @dropdown-visible-change="$emit('user-dropdown', $event)"
          @search="$emit('user-search', $event)"
          @change="$emit('refresh')"
        />
      </label>
    </template>
    <template #actions>
      <slot name="actions" />
      <a-button @click="$emit('help')">
        <template #icon><question-circle-outlined /></template>
        授权帮助
      </a-button>
      <a-button type="primary" @click="$emit('create')">新增授权</a-button>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'

import AccountSelect from '@/components/AccountSelect.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import type { AuthorizationDirectionFilter, AuthorizationFilterResourceType, AuthorizationSourceFilter, AuthorizationStatusFilter } from './authorizationTableColumns'

const props = defineProps<{
  keyword: string
  filters: {
    direction: AuthorizationDirectionFilter
    sourceType: AuthorizationSourceFilter
    status: AuthorizationStatusFilter
    resourceType: AuthorizationFilterResourceType
    resourceOwnerSystemAccountId: string
    resourceOwnerSystemAccount?: PrincipalSelection
    resourceId?: string
    resourceAccount?: AccountSelection
    resourceGroup?: GroupSelection
    teamId?: string
    team?: PrincipalSelection
    granteeSystemAccountId?: string
    granteeSystemAccount?: PrincipalSelection
  }
  isManagementView: boolean
  directionOptions: Array<{ label: string; value: AuthorizationDirectionFilter }>
  sourceOptions: Array<{ label: string; value: AuthorizationSourceFilter }>
  statusOptions: Array<{ label: string; value: AuthorizationStatusFilter }>
  resourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }>
  resourceOptions: Array<{ label: string; value: string }>
  resourceDisabled?: boolean
  resourceLoading?: boolean
  resourcePlaceholder?: string
  ownerUsers: SystemAccountPrincipalSummary[]
  ownerLoading?: boolean
  teams: SystemTeamPrincipalSummary[]
  teamLoading?: boolean
  users: SystemAccountPrincipalSummary[]
  userLoading?: boolean
  activeFilterCount: number
  advancedFilterCount: number
  loading: boolean
}>()

defineEmits<{
  (event: 'create'): void
  (event: 'help'): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'owner-change'): void
  (event: 'owner-search', value: string): void
  (event: 'owner-dropdown', open: boolean): void
  (event: 'resource-type-change'): void
  (event: 'resource-search', value: string): void
  (event: 'resource-dropdown', open: boolean): void
  (event: 'team-search', value: string): void
  (event: 'team-dropdown', open: boolean): void
  (event: 'update:keyword', value: string): void
  (event: 'user-search', value: string): void
  (event: 'user-dropdown', open: boolean): void
}>()

</script>

<style scoped>
.filter-select {
  min-width: 140px;
}

.direction-filter {
  width: max-content;
}

.filter-resource,
.filter-user {
  min-width: 220px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.advanced-filter-form :deep(.ant-select) {
  width: 100%;
}
</style>
