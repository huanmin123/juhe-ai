<template>
  <a-form layout="vertical" :class="{ 'advanced-filter-form': applyOnChange }">
    <a-form-item label="模块">
      <a-select :value="moduleFilter" :options="moduleOptions" @change="handleModuleChange" />
    </a-form-item>
    <a-form-item label="动作">
      <a-select :value="actionFilter" :options="actionOptions" @change="handleActionChange" />
    </a-form-item>
    <a-form-item label="资源类型">
      <a-select :value="resourceTypeFilter" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
    </a-form-item>
    <a-form-item label="资源 ID">
      <a-input :value="resourceIdFilter" allow-clear placeholder="输入资源 ID 精确筛选" @press-enter="emitSearch" @update:value="emit('update:resourceIdFilter', $event)" />
    </a-form-item>
    <a-form-item label="时间范围">
      <a-range-picker
        :value="createdAtRange"
        allow-clear
        class="drawer-range-picker"
        format="YYYY-MM-DD HH:mm"
        show-time
        :placeholder="['开始时间', '结束时间']"
        @change="handleCreatedAtRangeChange"
      />
    </a-form-item>
    <a-form-item label="traceId">
      <a-input :value="traceIdFilter" allow-clear placeholder="输入 traceId" @press-enter="emitSearch" @update:value="emit('update:traceIdFilter', $event)" />
    </a-form-item>
    <template v-if="isManagementView">
      <a-form-item label="用户操作人">
        <SystemPrincipalSelect
          :value="actorSystemAccountFilter"
          :selected-principal="actorSystemAccountSelection"
          :accounts="actorSystemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="actorSystemAccountOptionsLoading"
          include-all
          all-label="全部操作人"
          placeholder="筛选用户操作人"
          @change="handleActorSystemAccountChange"
          @dropdown-visible-change="emit('actorDropdownVisibleChange', $event)"
          @search="emit('actorSearch', $event)"
          @update:selected-principal="emit('update:actorSystemAccountSelection', $event)"
          @update:value="updateActorSystemAccountFilter"
        />
      </a-form-item>
      <a-form-item label="影响用户">
        <SystemPrincipalSelect
          :value="affectedSystemAccountFilter"
          :selected-principal="affectedSystemAccountSelection"
          :accounts="affectedSystemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="affectedSystemAccountOptionsLoading"
          include-all
          all-label="全部用户"
          placeholder="筛选影响用户"
          @change="handleAffectedSystemAccountChange"
          @dropdown-visible-change="emit('affectedDropdownVisibleChange', $event)"
          @search="emit('affectedSearch', $event)"
          @update:selected-principal="emit('update:affectedSystemAccountSelection', $event)"
          @update:value="updateAffectedSystemAccountFilter"
        />
      </a-form-item>
      <a-form-item label="业务归属">
        <SystemPrincipalSelect
          :value="operationScopeSystemAccountFilter"
          :selected-principal="operationScopeSystemAccountSelection"
          :accounts="operationScopeSystemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="operationScopeSystemAccountOptionsLoading"
          include-all
          all-label="全部用户"
          placeholder="筛选业务归属"
          @change="handleOperationScopeSystemAccountChange"
          @dropdown-visible-change="emit('operationScopeDropdownVisibleChange', $event)"
          @search="emit('operationScopeSearch', $event)"
          @update:selected-principal="emit('update:operationScopeSystemAccountSelection', $event)"
          @update:value="updateOperationScopeSystemAccountFilter"
        />
      </a-form-item>
    </template>
  </a-form>
</template>

<script setup lang="ts">
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { SystemAccountPrincipalSummary } from '@/types/domain'
import { actionOptions, moduleOptions, resourceTypeOptions } from './operationLogOptions'
import { normalizeCreatedAtRange, type CreatedAtRangeValue } from './operationLogPageState'

const props = withDefaults(defineProps<{
  actionFilter: string
  actorSystemAccountFilter: string
  actorSystemAccountOptionsLoading: boolean
  actorSystemAccountSelection?: PrincipalSelection
  actorSystemAccounts: SystemAccountPrincipalSummary[]
  affectedSystemAccountFilter: string
  affectedSystemAccountOptionsLoading: boolean
  affectedSystemAccountSelection?: PrincipalSelection
  affectedSystemAccounts: SystemAccountPrincipalSummary[]
  applyOnChange?: boolean
  createdAtRange: CreatedAtRangeValue
  isManagementView: boolean
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  operationScopeSystemAccountOptionsLoading: boolean
  operationScopeSystemAccountSelection?: PrincipalSelection
  operationScopeSystemAccounts: SystemAccountPrincipalSummary[]
  resourceIdFilter: string
  resourceTypeFilter: string
  traceIdFilter: string
}>(), {
  applyOnChange: false
})

const emit = defineEmits<{
  search: []
  actorDropdownVisibleChange: [value: boolean]
  actorSearch: [value: string]
  affectedDropdownVisibleChange: [value: boolean]
  affectedSearch: [value: string]
  operationScopeDropdownVisibleChange: [value: boolean]
  operationScopeSearch: [value: string]
  'update:actionFilter': [value: string]
  'update:actorSystemAccountFilter': [value: string]
  'update:actorSystemAccountSelection': [value?: PrincipalSelection]
  'update:affectedSystemAccountFilter': [value: string]
  'update:affectedSystemAccountSelection': [value?: PrincipalSelection]
  'update:createdAtRange': [value: CreatedAtRangeValue]
  'update:moduleFilter': [value: string]
  'update:operationScopeSystemAccountFilter': [value: string]
  'update:operationScopeSystemAccountSelection': [value?: PrincipalSelection]
  'update:resourceIdFilter': [value: string]
  'update:resourceTypeFilter': [value: string]
  'update:traceIdFilter': [value: string]
}>()

function handleModuleChange(value: unknown): void {
  emit('update:moduleFilter', typeof value === 'string' ? value : 'all')
  emitSearch()
}

function handleActionChange(value: unknown): void {
  emit('update:actionFilter', typeof value === 'string' ? value : 'all')
  emitSearch()
}

function handleResourceTypeChange(value: unknown): void {
  emit('update:resourceTypeFilter', typeof value === 'string' ? value : 'all')
  emitSearch()
}

function handleCreatedAtRangeChange(value: CreatedAtRangeValue): void {
  emit('update:createdAtRange', normalizeCreatedAtRange(value))
  emitSearch()
}

function handleActorSystemAccountChange(): void {
  emitSearch()
}

function updateActorSystemAccountFilter(value: unknown): void {
  emit('update:actorSystemAccountFilter', typeof value === 'string' ? value : '')
}

function handleAffectedSystemAccountChange(): void {
  emitSearch()
}

function updateAffectedSystemAccountFilter(value: unknown): void {
  emit('update:affectedSystemAccountFilter', typeof value === 'string' ? value : '')
}

function handleOperationScopeSystemAccountChange(): void {
  emitSearch()
}

function updateOperationScopeSystemAccountFilter(value: unknown): void {
  emit('update:operationScopeSystemAccountFilter', typeof value === 'string' ? value : '')
}

function emitSearch(): void {
  if (props.applyOnChange) {
    emit('search')
  }
}
</script>

<style scoped>
.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-select),
.advanced-filter-form :deep(.ant-input) {
  width: 100%;
}
</style>
