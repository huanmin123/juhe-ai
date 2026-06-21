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
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item label="时间范围">
          <a-range-picker
            :value="dateRange"
            allow-clear
            format="YYYY-MM-DD"
            :placeholder="['开始日期', '结束日期']"
            @change="handleDateRangeUpdate"
          />
        </a-form-item>
        <a-form-item label="请求结果">
          <a-select :value="result" :options="resultOptions" @update:value="handleResultUpdate" />
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
        <a-form-item label="分组">
          <GroupSelect
            :value="groupId"
            :selected-group="groupSelection"
            allow-clear
            :filter-option="false"
            :groups="groupOptions"
            :disabled="groupDisabled"
            :loading="groupOptionsLoading"
            :placeholder="groupDisabled ? '请先选择系统账户' : '全部分组'"
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
        <a-form-item label="traceId">
          <a-input :value="traceId" allow-clear placeholder="traceId 前缀" @update:value="handleTraceIdUpdate" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="模型">
          <ModelFilterSelect
            :value="model"
            :loading="modelsLoading"
            :models="modelOptions"
            placeholder="全部模型"
            @change="handleModelChange"
            @update:value="handleModelUpdate"
          />
        </a-form-item>
        <a-form-item label="IP">
          <a-input :value="clientIp" allow-clear placeholder="客户端 IP 前缀" @update:value="handleClientIpUpdate" @press-enter="emit('search')" />
        </a-form-item>
        <a-form-item label="请求来源">
          <a-select :value="trafficSource" :options="trafficSourceOptions" @update:value="handleTrafficSourceUpdate" />
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
        <span>请求来源</span>
        <a-select :value="trafficSource" :options="trafficSourceOptions" @update:value="handleTrafficSourceUpdate" />
      </label>
      <label class="mobile-filter-field">
        <span>分组</span>
        <GroupSelect
          :value="groupId"
          :selected-group="groupSelection"
          allow-clear
          :filter-option="false"
          :groups="groupOptions"
          :disabled="groupDisabled"
          :loading="groupOptionsLoading"
          :placeholder="groupDisabled ? '请先选择系统账户' : '全部分组'"
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
      <label class="mobile-filter-field">
        <span>traceId</span>
        <a-input :value="traceId" allow-clear placeholder="traceId 前缀" @update:value="handleTraceIdUpdate" @press-enter="emit('search')" />
      </label>
      <label class="mobile-filter-field">
        <span>模型</span>
        <ModelFilterSelect
          :value="model"
          :loading="modelsLoading"
          :models="modelOptions"
          placeholder="全部模型"
          @change="handleModelChange"
          @update:value="handleModelUpdate"
        />
      </label>
      <label class="mobile-filter-field">
        <span>IP</span>
        <a-input :value="clientIp" allow-clear placeholder="客户端 IP 前缀" @update:value="handleClientIpUpdate" @press-enter="emit('search')" />
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
    <template #actions>
      <slot name="actions" />
    </template>
  </ResponsiveListToolbar>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'

import GroupSelect from '@/components/GroupSelect.vue'
import ModelFilterSelect from '@/components/ModelFilterSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { normalizeDayjsDateRange } from '@/shared/dateRange'
import type { GroupOptionSummary, ProviderModelOption, SystemAccountPrincipalSummary } from '@/types/domain'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'

type ResultFilter = 'all' | 'success' | 'failed'
type TrafficSourceFilter = 'all' | 'gateway' | 'manual_account_test' | 'cooldown_retest' | 'hybrid_scoring'
type FilterOption<T extends string> = {
  label: string
  value: T
}
type SelectValue = string | string[] | number | undefined
type DateRangeValue = Array<Dayjs | null | undefined> | null | undefined

defineProps<{
  activeFilterCount: number
  advancedFilterCount: number
  clientIp: string
  dateRange?: [Dayjs, Dayjs]
  groupId?: string
  groupDisabled?: boolean
  groupOptions: GroupOptionSummary[]
  groupOptionsLoading?: boolean
  groupSelection?: GroupSelection
  isManagementView: boolean
  keyword: string
  model: string
  modelOptions: ProviderModelOption[]
  modelsLoading?: boolean
  refreshLoading: boolean
  result: ResultFilter
  resultOptions: Array<FilterOption<ResultFilter>>
  statusCode: string
  systemAccountId: string
  systemAccountSelection?: PrincipalSelection
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountsLoading?: boolean
  traceId: string
  trafficSource: TrafficSourceFilter
  trafficSourceOptions: Array<FilterOption<TrafficSourceFilter>>
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
  (event: 'update:clientIp', value: string): void
  (event: 'update:dateRange', value?: [Dayjs, Dayjs]): void
  (event: 'update:groupId', value: string | undefined): void
  (event: 'update:groupSelection', value?: GroupSelection): void
  (event: 'update:keyword', value: string): void
  (event: 'update:model', value: string): void
  (event: 'update:result', value: ResultFilter): void
  (event: 'update:statusCode', value: string): void
  (event: 'update:systemAccountId', value: string): void
  (event: 'update:systemAccountSelection', value?: PrincipalSelection): void
  (event: 'update:traceId', value: string): void
  (event: 'update:trafficSource', value: TrafficSourceFilter): void
}>()

function handleResultUpdate(value: ResultFilter) {
  emit('update:result', value)
  emit('search')
}

function handleTrafficSourceUpdate(value: TrafficSourceFilter) {
  emit('update:trafficSource', value)
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

function handleClientIpUpdate(value: SelectValue) {
  const nextValue = typeof value === 'number'
    ? String(value)
    : typeof value === 'string'
      ? value
      : ''
  emit('update:clientIp', nextValue)
  if (!nextValue) {
    emit('search')
  }
}

function handleTraceIdUpdate(value: SelectValue) {
  const nextValue = typeof value === 'number'
    ? String(value)
    : typeof value === 'string'
      ? value
      : ''
  emit('update:traceId', nextValue)
  if (!nextValue) {
    emit('search')
  }
}

function handleModelUpdate(value: SelectValue) {
  const nextValue = typeof value === 'number'
    ? String(value)
    : typeof value === 'string'
      ? value
      : ''
  emit('update:model', nextValue)
}

function handleModelChange() {
  emit('search')
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
