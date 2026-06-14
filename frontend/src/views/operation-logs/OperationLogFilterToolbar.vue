<template>
  <ResponsiveListToolbar
    :keyword="summaryKeyword"
    search-placeholder="搜索操作摘要"
    filter-title="操作日志筛选"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="loading"
    @refresh="emit('refresh')"
    @reset="emit('reset')"
    @search="emit('search')"
    @update:keyword="emit('update:summaryKeyword', $event)"
  >
    <template #advanced-filters>
      <OperationLogFilterForm
        :action-filter="actionFilter"
        :actor-system-account-filter="actorSystemAccountFilter"
        :actor-system-account-options-loading="actorSystemAccountOptionsLoading"
        :actor-system-account-selection="actorSystemAccountSelection"
        :actor-system-accounts="actorSystemAccounts"
        :affected-system-account-filter="affectedSystemAccountFilter"
        :affected-system-account-options-loading="affectedSystemAccountOptionsLoading"
        :affected-system-account-selection="affectedSystemAccountSelection"
        :affected-system-accounts="affectedSystemAccounts"
        :apply-on-change="true"
        :created-at-range="createdAtRange"
        :is-management-view="isManagementView"
        :module-filter="moduleFilter"
        :operation-scope-system-account-filter="operationScopeSystemAccountFilter"
        :operation-scope-system-account-options-loading="operationScopeSystemAccountOptionsLoading"
        :operation-scope-system-account-selection="operationScopeSystemAccountSelection"
        :operation-scope-system-accounts="operationScopeSystemAccounts"
        :resource-id-filter="resourceIdFilter"
        :resource-type-filter="resourceTypeFilter"
        :trace-id-filter="traceIdFilter"
        @actor-dropdown-visible-change="emit('actorDropdownVisibleChange', $event)"
        @actor-search="emit('actorSearch', $event)"
        @affected-dropdown-visible-change="emit('affectedDropdownVisibleChange', $event)"
        @affected-search="emit('affectedSearch', $event)"
        @operation-scope-dropdown-visible-change="emit('operationScopeDropdownVisibleChange', $event)"
        @operation-scope-search="emit('operationScopeSearch', $event)"
        @search="emit('search')"
        @update:action-filter="emit('update:actionFilter', $event)"
        @update:actor-system-account-filter="emit('update:actorSystemAccountFilter', $event)"
        @update:actor-system-account-selection="emit('update:actorSystemAccountSelection', $event)"
        @update:affected-system-account-filter="emit('update:affectedSystemAccountFilter', $event)"
        @update:affected-system-account-selection="emit('update:affectedSystemAccountSelection', $event)"
        @update:created-at-range="emit('update:createdAtRange', $event)"
        @update:module-filter="emit('update:moduleFilter', $event)"
        @update:operation-scope-system-account-filter="emit('update:operationScopeSystemAccountFilter', $event)"
        @update:operation-scope-system-account-selection="emit('update:operationScopeSystemAccountSelection', $event)"
        @update:resource-id-filter="emit('update:resourceIdFilter', $event)"
        @update:resource-type-filter="emit('update:resourceTypeFilter', $event)"
        @update:trace-id-filter="emit('update:traceIdFilter', $event)"
      />
    </template>
    <template #actions>
      <slot name="actions" />
    </template>
    <template #filters>
      <OperationLogFilterForm
        :action-filter="actionFilter"
        :actor-system-account-filter="actorSystemAccountFilter"
        :actor-system-account-options-loading="actorSystemAccountOptionsLoading"
        :actor-system-account-selection="actorSystemAccountSelection"
        :actor-system-accounts="actorSystemAccounts"
        :affected-system-account-filter="affectedSystemAccountFilter"
        :affected-system-account-options-loading="affectedSystemAccountOptionsLoading"
        :affected-system-account-selection="affectedSystemAccountSelection"
        :affected-system-accounts="affectedSystemAccounts"
        :created-at-range="createdAtRange"
        :is-management-view="isManagementView"
        :module-filter="moduleFilter"
        :operation-scope-system-account-filter="operationScopeSystemAccountFilter"
        :operation-scope-system-account-options-loading="operationScopeSystemAccountOptionsLoading"
        :operation-scope-system-account-selection="operationScopeSystemAccountSelection"
        :operation-scope-system-accounts="operationScopeSystemAccounts"
        :resource-id-filter="resourceIdFilter"
        :resource-type-filter="resourceTypeFilter"
        :trace-id-filter="traceIdFilter"
        @actor-dropdown-visible-change="emit('actorDropdownVisibleChange', $event)"
        @actor-search="emit('actorSearch', $event)"
        @affected-dropdown-visible-change="emit('affectedDropdownVisibleChange', $event)"
        @affected-search="emit('affectedSearch', $event)"
        @operation-scope-dropdown-visible-change="emit('operationScopeDropdownVisibleChange', $event)"
        @operation-scope-search="emit('operationScopeSearch', $event)"
        @update:action-filter="emit('update:actionFilter', $event)"
        @update:actor-system-account-filter="emit('update:actorSystemAccountFilter', $event)"
        @update:actor-system-account-selection="emit('update:actorSystemAccountSelection', $event)"
        @update:affected-system-account-filter="emit('update:affectedSystemAccountFilter', $event)"
        @update:affected-system-account-selection="emit('update:affectedSystemAccountSelection', $event)"
        @update:created-at-range="emit('update:createdAtRange', $event)"
        @update:module-filter="emit('update:moduleFilter', $event)"
        @update:operation-scope-system-account-filter="emit('update:operationScopeSystemAccountFilter', $event)"
        @update:operation-scope-system-account-selection="emit('update:operationScopeSystemAccountSelection', $event)"
        @update:resource-id-filter="emit('update:resourceIdFilter', $event)"
        @update:resource-type-filter="emit('update:resourceTypeFilter', $event)"
        @update:trace-id-filter="emit('update:traceIdFilter', $event)"
      />
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { SystemAccountPrincipalSummary } from '@/types/domain'
import type { CreatedAtRangeValue } from './operationLogPageState'
import OperationLogFilterForm from './OperationLogFilterForm.vue'

defineProps<{
  actionFilter: string
  activeFilterCount: number
  actorSystemAccountFilter: string
  actorSystemAccountOptionsLoading: boolean
  actorSystemAccountSelection?: PrincipalSelection
  actorSystemAccounts: SystemAccountPrincipalSummary[]
  affectedSystemAccountFilter: string
  affectedSystemAccountOptionsLoading: boolean
  affectedSystemAccountSelection?: PrincipalSelection
  affectedSystemAccounts: SystemAccountPrincipalSummary[]
  advancedFilterCount: number
  createdAtRange: CreatedAtRangeValue
  isManagementView: boolean
  loading: boolean
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  operationScopeSystemAccountOptionsLoading: boolean
  operationScopeSystemAccountSelection?: PrincipalSelection
  operationScopeSystemAccounts: SystemAccountPrincipalSummary[]
  resourceIdFilter: string
  resourceTypeFilter: string
  summaryKeyword: string
  traceIdFilter: string
}>()

const emit = defineEmits<{
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'actorDropdownVisibleChange', value: boolean): void
  (event: 'actorSearch', value: string): void
  (event: 'affectedDropdownVisibleChange', value: boolean): void
  (event: 'affectedSearch', value: string): void
  (event: 'operationScopeDropdownVisibleChange', value: boolean): void
  (event: 'operationScopeSearch', value: string): void
  (event: 'update:actionFilter', value: string): void
  (event: 'update:actorSystemAccountFilter', value: string): void
  (event: 'update:actorSystemAccountSelection', value?: PrincipalSelection): void
  (event: 'update:affectedSystemAccountFilter', value: string): void
  (event: 'update:affectedSystemAccountSelection', value?: PrincipalSelection): void
  (event: 'update:createdAtRange', value: CreatedAtRangeValue): void
  (event: 'update:moduleFilter', value: string): void
  (event: 'update:operationScopeSystemAccountFilter', value: string): void
  (event: 'update:operationScopeSystemAccountSelection', value?: PrincipalSelection): void
  (event: 'update:resourceIdFilter', value: string): void
  (event: 'update:resourceTypeFilter', value: string): void
  (event: 'update:summaryKeyword', value: string): void
  (event: 'update:traceIdFilter', value: string): void
}>()
</script>
