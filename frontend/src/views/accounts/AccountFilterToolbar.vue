<template>
  <ResponsiveListToolbar
    :keyword="filters.keyword"
    search-placeholder="账户名称"
    filter-title="筛选账户"
    :active-filter-count="activeFilterCount"
    :mobile-action-count="isManagementView ? 2 : 1"
    :refresh-loading="refreshLoading"
    @update:keyword="emit('update:keyword', $event)"
    @search="emit('search')"
    @reset="emit('reset')"
    @refresh="emit('refresh')"
  >
    <template #inline-filters>
      <a-select
        :value="filters.groupId || undefined"
        allow-clear
        class="toolbar-select account-group-filter responsive-list-inline-filter"
        :disabled="groupFilterDisabled"
        :filter-option="false"
        :loading="groupOptionsLoading"
        :options="groupSelectOptions"
        placeholder="全部分组"
        show-search
        @dropdown-visible-change="emit('group-dropdown', $event)"
        @search="emit('group-search', $event)"
        @update:value="handleGroupUpdate"
      />
      <a-select
        :value="filters.status"
        allow-clear
        class="toolbar-select account-status-filter responsive-list-inline-filter"
        :max-tag-count="1"
        mode="multiple"
        :options="statusOptions"
        placeholder="全部状态"
        @change="handleStatusChange"
      />
      <SystemPrincipalSelect
        v-if="isManagementView"
        :value="filters.systemAccountId"
        :accounts="systemAccounts"
        :active-only="false"
        :filter-option="false"
        :loading="systemAccountsLoading"
        include-all
        class="toolbar-select responsive-list-inline-filter"
        @update:value="handleSystemAccountUpdate"
        @change="emit('system-account-change')"
        @dropdown-visible-change="emit('system-account-dropdown', $event)"
        @search="emit('system-account-search', $event)"
      />
    </template>
    <template #actions>
      <a-button v-if="isManagementView" @click="emit('import')">
        <template #icon>
          <UploadOutlined />
        </template>
        导入账户
      </a-button>
      <a-button type="primary" @click="emit('create')">添加账户</a-button>
    </template>
    <template #filters>
      <label class="mobile-filter-field">
        <span>分组</span>
        <a-select
          :value="filters.groupId || undefined"
          allow-clear
          :disabled="groupFilterDisabled"
          :filter-option="false"
          :loading="groupOptionsLoading"
          :options="groupSelectOptions"
          placeholder="全部分组"
          show-search
          @dropdown-visible-change="emit('group-dropdown', $event)"
          @search="emit('group-search', $event)"
          @update:value="handleGroupUpdate"
        />
      </label>
      <label class="mobile-filter-field">
        <span>账户状态</span>
        <a-select
          :value="filters.status"
          allow-clear
          :max-tag-count="1"
          mode="multiple"
          :options="statusOptions"
          placeholder="全部状态"
          @change="handleStatusChange"
        />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>系统账户</span>
        <SystemPrincipalSelect
          :value="filters.systemAccountId"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountsLoading"
          include-all
          @update:value="handleSystemAccountUpdate"
          @change="emit('system-account-change')"
          @dropdown-visible-change="emit('system-account-dropdown', $event)"
          @search="emit('system-account-search', $event)"
        />
      </label>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import { UploadOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountStatus, GroupOptionSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import type { AccountFilters } from './accountFormTypes'

type FilterOption<T extends string> = {
  label: string
  value: T
}
type SelectValue = string | string[] | undefined

const props = defineProps<{
  activeFilterCount: number
  filters: AccountFilters
  groupFilterDisabled?: boolean
  groupOptions: GroupOptionSummary[]
  groupOptionsLoading?: boolean
  isManagementView: boolean
  refreshLoading: boolean
  statusOptions: Array<FilterOption<AccountStatus>>
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountsLoading?: boolean
}>()

const emit = defineEmits<{
  (event: 'create'): void
  (event: 'group-dropdown', open: boolean): void
  (event: 'group-search', value: string): void
  (event: 'import'): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'system-account-change'): void
  (event: 'system-account-dropdown', open: boolean): void
  (event: 'system-account-search', value: string): void
  (event: 'update:groupId', value: string): void
  (event: 'update:keyword', value: string): void
  (event: 'update:status', value: AccountStatus[]): void
  (event: 'update:systemAccountId', value: string): void
}>()

const accountStatusValues = new Set<AccountStatus>(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable'])

const groupSelectOptions = computed(() => props.groupOptions.map((group) => ({
  label: group.name,
  value: group.id
})))

function handleGroupUpdate(value: SelectValue) {
  emit('update:groupId', typeof value === 'string' ? value : '')
  emit('search')
}

function handleSystemAccountUpdate(value: SelectValue) {
  emit('update:systemAccountId', typeof value === 'string' ? value : '')
}

function handleStatusChange(value: SelectValue) {
  emit('update:status', Array.isArray(value) ? value.filter(isAccountStatus) : [])
  emit('search')
}

function isAccountStatus(value: string): value is AccountStatus {
  return accountStatusValues.has(value as AccountStatus)
}
</script>

<style scoped>
.toolbar-select {
  min-width: 150px;
}

.account-status-filter {
  min-width: 172px;
}

.account-group-filter {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.mobile-filter-field :deep(.ant-select) {
  width: 100%;
}

@media (max-width: 900px) {
  .toolbar-select {
    width: 100%;
    min-width: 0;
  }
}
</style>
