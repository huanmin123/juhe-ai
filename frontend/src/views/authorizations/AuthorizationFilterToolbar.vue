<template>
  <ResponsiveListToolbar
    :show-search="false"
    filter-title="筛选授权"
    :active-filter-count="activeFilterCount"
    :refresh-loading="loading"
    @reset="$emit('reset')"
    @refresh="$emit('refresh')"
  >
    <template #inline-filters>
      <a-select v-model:value="filters.resourceType" class="filter-select responsive-list-inline-filter" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
      <a-select
        v-model:value="filters.resourceId"
        show-search
        allow-clear
        option-filter-prop="label"
        class="filter-select filter-resource responsive-list-inline-filter"
        :options="resourceOptions"
        :disabled="filters.resourceType === 'all'"
        :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
        @change="$emit('refresh')"
      />
      <SystemPrincipalSelect
        v-model:value="filters.teamId"
        :teams="teams"
        :active-only="false"
        allow-clear
        class="filter-select responsive-list-inline-filter"
        placeholder="筛选授权来源"
        scope="team"
        @change="$emit('refresh')"
      />
      <SystemPrincipalSelect
        v-model:value="filters.granteeSystemAccountId"
        :accounts="users"
        :active-only="false"
        allow-clear
        class="filter-select filter-user responsive-list-inline-filter"
        placeholder="筛选被授权用户"
        @change="$emit('refresh')"
      />
    </template>
    <template #actions>
      <a-button @click="$emit('help')">
        <template #icon><question-circle-outlined /></template>
        授权帮助
      </a-button>
      <a-button type="primary" @click="$emit('create')">新增授权</a-button>
    </template>
    <template #filters>
      <label class="mobile-filter-field">
        <span>资源类型</span>
        <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="$emit('resource-type-change')" />
      </label>
      <label class="mobile-filter-field">
        <span>资源</span>
        <a-select
          v-model:value="filters.resourceId"
          show-search
          allow-clear
          option-filter-prop="label"
          :options="resourceOptions"
          :disabled="filters.resourceType === 'all'"
          :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
          @change="$emit('refresh')"
        />
      </label>
      <label class="mobile-filter-field">
        <span>授权来源</span>
        <SystemPrincipalSelect v-model:value="filters.teamId" :teams="teams" :active-only="false" allow-clear scope="team" placeholder="筛选授权来源" @change="$emit('refresh')" />
      </label>
      <label class="mobile-filter-field">
        <span>被授权用户</span>
        <SystemPrincipalSelect v-model:value="filters.granteeSystemAccountId" :accounts="users" :active-only="false" allow-clear placeholder="筛选被授权用户" @change="$emit('refresh')" />
      </label>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { SystemAccountSummary, SystemTeamSummary } from '@/types/domain'
import type { AuthorizationFilterResourceType } from './authorizationTableColumns'

defineProps<{
  filters: {
    resourceType: AuthorizationFilterResourceType
    resourceId?: string
    teamId?: string
    granteeSystemAccountId?: string
  }
  resourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }>
  resourceOptions: Array<{ label: string; value: string }>
  teams: SystemTeamSummary[]
  users: SystemAccountSummary[]
  activeFilterCount: number
  loading: boolean
}>()

defineEmits<{
  (event: 'create'): void
  (event: 'help'): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'resource-type-change'): void
}>()
</script>

<style scoped>
.filter-select {
  min-width: 140px;
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
</style>
