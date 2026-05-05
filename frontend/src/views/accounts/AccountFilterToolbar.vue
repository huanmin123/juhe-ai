<template>
  <ResponsiveListToolbar
    :keyword="filters.keyword"
    search-placeholder="搜索账号..."
    filter-title="筛选账户"
    :active-filter-count="activeFilterCount"
    :mobile-action-count="1"
    :refresh-loading="refreshLoading"
    @update:keyword="emit('update:keyword', $event)"
    @search="emit('search')"
    @reset="emit('reset')"
    @refresh="emit('refresh')"
  >
    <template #inline-filters>
      <a-select :value="filters.type" class="toolbar-select responsive-list-inline-filter" :options="typeOptions" @update:value="emit('update:type', $event)" />
      <a-select :value="filters.status" class="toolbar-select responsive-list-inline-filter" :options="statusOptions" @update:value="emit('update:status', $event)" />
      <a-select :value="filters.schedulable" class="toolbar-select responsive-list-inline-filter" :options="schedulableOptions" @update:value="emit('update:schedulable', $event)" />
      <SystemPrincipalSelect
        v-if="isAdmin"
        :value="filters.systemAccountId"
        :accounts="systemAccounts"
        :active-only="false"
        include-all
        class="toolbar-select responsive-list-inline-filter"
        @update:value="handleSystemAccountUpdate"
        @change="emit('system-account-change')"
      />
    </template>
    <template #actions>
      <a-button type="primary" @click="emit('create')">添加账户</a-button>
    </template>
    <template #filters>
      <label class="mobile-filter-field">
        <span>账户类型</span>
        <a-select :value="filters.type" :options="typeOptions" @update:value="emit('update:type', $event)" />
      </label>
      <label class="mobile-filter-field">
        <span>账户状态</span>
        <a-select :value="filters.status" :options="statusOptions" @update:value="emit('update:status', $event)" />
      </label>
      <label class="mobile-filter-field">
        <span>启停状态</span>
        <a-select :value="filters.schedulable" :options="schedulableOptions" @update:value="emit('update:schedulable', $event)" />
      </label>
      <label v-if="isAdmin" class="mobile-filter-field">
        <span>系统账户</span>
        <SystemPrincipalSelect
          :value="filters.systemAccountId"
          :accounts="systemAccounts"
          :active-only="false"
          include-all
          @update:value="handleSystemAccountUpdate"
          @change="emit('system-account-change')"
        />
      </label>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountStatus, AccountType, SystemAccountSummary } from '@/types/domain'
import type { AccountFilters } from './accountFormTypes'
import type { SchedulableFilter } from './accountFormatters'

type FilterOption<T extends string> = {
  label: string
  value: T
}
type SelectValue = string | string[] | undefined

defineProps<{
  activeFilterCount: number
  filters: AccountFilters
  isAdmin: boolean
  refreshLoading: boolean
  schedulableOptions: ReadonlyArray<FilterOption<SchedulableFilter>>
  statusOptions: Array<FilterOption<'all' | AccountStatus>>
  systemAccounts: SystemAccountSummary[]
  typeOptions: Array<FilterOption<'all' | AccountType>>
}>()

const emit = defineEmits<{
  (event: 'create'): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'system-account-change'): void
  (event: 'update:keyword', value: string): void
  (event: 'update:schedulable', value: SchedulableFilter): void
  (event: 'update:status', value: 'all' | AccountStatus): void
  (event: 'update:systemAccountId', value: string): void
  (event: 'update:type', value: 'all' | AccountType): void
}>()

function handleSystemAccountUpdate(value: SelectValue) {
  emit('update:systemAccountId', typeof value === 'string' ? value : '')
}
</script>

<style scoped>
.toolbar-select {
  min-width: 150px;
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
