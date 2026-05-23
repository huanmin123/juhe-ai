<template>
  <ResponsiveListToolbar
    :keyword="keyword"
    search-placeholder="AI 账户名称"
    filter-title="筛选使用记录"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="refreshLoading"
    @update:keyword="emit('update:keyword', $event)"
    @reset="emit('reset')"
    @refresh="emit('refresh')"
    @search="emit('search')"
  >
    <template #inline-filters>
      <a-range-picker
        :value="dateRange"
        allow-clear
        class="filter-select date-range-filter toolbar-select responsive-list-inline-filter"
        format="YYYY-MM-DD"
        :placeholder="['开始日期', '结束日期']"
        @change="handleDateRangeUpdate"
      />
      <a-select
        :value="result"
        class="filter-select toolbar-select responsive-list-inline-filter"
        :options="resultOptions"
        @update:value="handleResultUpdate"
      />
    </template>
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item label="分组">
          <GroupSelect
            :value="groupId"
            :selected-group="groupSelection"
            allow-clear
            :filter-option="false"
            :groups="groupOptions"
            :loading="groupOptionsLoading"
            placeholder="全部分组"
            @update:value="handleGroupUpdate"
            @update:selected-group="emit('update:groupSelection', $event)"
            @change="emit('group-change')"
            @dropdown-visible-change="emit('group-dropdown', $event)"
            @search="emit('group-search', $event)"
          />
        </a-form-item>
        <a-form-item label="状态码">
          <a-input :value="statusCode" allow-clear placeholder="状态码" @update:value="handleStatusCodeUpdate" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item v-if="isManagementView" label="系统账户">
          <SystemPrincipalSelect
            :value="systemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountsLoading"
            :selected-principal="systemAccountSelection"
            include-all
            @update:value="handleSystemAccountUpdate"
            @update:selected-principal="emit('update:systemAccountSelection', $event)"
            @change="emit('system-account-change')"
            @dropdown-visible-change="emit('system-account-dropdown', $event)"
            @search="emit('system-account-search', $event)"
          />
        </a-form-item>
      </a-form>
    </template>
    <template #filters>
      <label class="mobile-filter-field">
        <span>时间范围</span>
        <a-range-picker
          :value="dateRange"
          allow-clear
          class="mobile-date-range-filter"
          format="YYYY-MM-DD"
          :placeholder="['开始日期', '结束日期']"
          @change="handleDateRangeUpdate"
        />
      </label>
      <label class="mobile-filter-field">
        <span>请求结果</span>
        <a-select :value="result" :options="resultOptions" @update:value="handleResultUpdate" />
      </label>
      <label class="mobile-filter-field">
        <span>分组</span>
        <GroupSelect
          :value="groupId"
          :selected-group="groupSelection"
          allow-clear
          :filter-option="false"
          :groups="groupOptions"
          :loading="groupOptionsLoading"
          placeholder="全部分组"
          @update:value="handleGroupUpdate"
          @update:selected-group="emit('update:groupSelection', $event)"
          @change="emit('group-change')"
          @dropdown-visible-change="emit('group-dropdown', $event)"
          @search="emit('group-search', $event)"
        />
      </label>
      <label class="mobile-filter-field">
        <span>状态码</span>
        <a-input :value="statusCode" allow-clear placeholder="状态码" @update:value="handleStatusCodeUpdate" @press-enter="emit('search')" />
      </label>
      <label v-if="isManagementView" class="mobile-filter-field">
        <span>系统账户</span>
        <SystemPrincipalSelect
          :value="systemAccountId"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountsLoading"
          :selected-principal="systemAccountSelection"
          include-all
          @update:value="handleSystemAccountUpdate"
          @update:selected-principal="emit('update:systemAccountSelection', $event)"
          @change="emit('system-account-change')"
          @dropdown-visible-change="emit('system-account-dropdown', $event)"
          @search="emit('system-account-search', $event)"
        />
      </label>
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'

import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { normalizeDayjsDateRange } from '@/shared/dateRange'
import type { GroupOptionSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'

type ResultFilter = 'all' | 'success' | 'failed'
type FilterOption<T extends string> = {
  label: string
  value: T
}
type SelectValue = string | string[] | number | undefined
type DateRangeValue = Array<Dayjs | null | undefined> | null | undefined

defineProps<{
  activeFilterCount: number
  advancedFilterCount: number
  dateRange?: [Dayjs, Dayjs]
  groupId?: string
  groupOptions: GroupOptionSummary[]
  groupOptionsLoading?: boolean
  groupSelection?: GroupSelection
  isManagementView: boolean
  keyword: string
  refreshLoading: boolean
  result: ResultFilter
  resultOptions: Array<FilterOption<ResultFilter>>
  statusCode: string
  systemAccountId: string
  systemAccountSelection?: PrincipalSelection
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountsLoading?: boolean
}>()

const emit = defineEmits<{
  (event: 'group-change'): void
  (event: 'group-dropdown', open: boolean): void
  (event: 'group-search', value: string): void
  (event: 'refresh'): void
  (event: 'reset'): void
  (event: 'search'): void
  (event: 'system-account-change'): void
  (event: 'system-account-dropdown', open: boolean): void
  (event: 'system-account-search', value: string): void
  (event: 'update:dateRange', value?: [Dayjs, Dayjs]): void
  (event: 'update:groupId', value: string | undefined): void
  (event: 'update:groupSelection', value?: GroupSelection): void
  (event: 'update:keyword', value: string): void
  (event: 'update:result', value: ResultFilter): void
  (event: 'update:statusCode', value: string): void
  (event: 'update:systemAccountId', value: string): void
  (event: 'update:systemAccountSelection', value?: PrincipalSelection): void
}>()

function handleResultUpdate(value: ResultFilter) {
  emit('update:result', value)
  emit('search')
}

function handleStatusCodeUpdate(value: SelectValue) {
  const nextValue = typeof value === 'number'
    ? String(value)
    : typeof value === 'string'
      ? value
      : ''
  emit('update:statusCode', nextValue)
  if (!nextValue) {
    emit('search')
  }
}

function handleGroupUpdate(value: SelectValue) {
  emit('update:groupId', typeof value === 'string' ? value : undefined)
}

function handleDateRangeUpdate(value: DateRangeValue) {
  emit('update:dateRange', normalizeDayjsDateRange(dateRangeValue(value)))
  emit('search')
}

function handleSystemAccountUpdate(value: SelectValue) {
  emit('update:systemAccountId', typeof value === 'string' ? value : '')
}

function dateRangeValue(value: DateRangeValue): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  return start && end ? [start, end] : undefined
}
</script>

<style scoped>
.filter-select {
  width: 150px;
}

.system-account-filter {
  width: 180px;
}

.group-filter {
  width: 180px;
}

.date-range-filter {
  width: 240px;
}

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

.advanced-filter-form :deep(.ant-picker),
.advanced-filter-form :deep(.ant-select),
.advanced-filter-form :deep(.ant-input) {
  width: 100%;
}

.mobile-date-range-filter {
  width: 100%;
}
</style>
