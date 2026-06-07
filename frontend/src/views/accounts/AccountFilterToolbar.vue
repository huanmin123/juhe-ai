<template>
  <ResponsiveListToolbar
    :keyword="filters.keyword"
    search-placeholder="账户名称"
    filter-title="筛选账户"
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :refresh-loading="refreshLoading"
    @update:keyword="emit('update:keyword', $event)"
    @search="emit('search')"
    @reset="emit('reset')"
    @refresh="emit('refresh')"
  >
    <template #advanced-filters>
      <a-form layout="vertical" class="advanced-filter-form">
        <a-form-item v-if="isManagementView" label="系统账户">
          <SystemPrincipalSelect
            :value="filters.systemAccountId"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountsLoading"
            :selected-principal="filters.systemAccount"
            include-all
            @update:value="handleSystemAccountUpdate"
            @update:selected-principal="emit('update:systemAccountSelection', $event)"
            @change="emit('system-account-change')"
            @dropdown-visible-change="emit('system-account-dropdown', $event)"
            @search="emit('system-account-search', $event)"
          />
        </a-form-item>
        <a-form-item label="供应商">
          <a-select
            :value="filters.providerCode || 'all'"
            :options="providerOptions"
            placeholder="全部供应商"
            @change="handleProviderUpdate"
          />
        </a-form-item>
        <a-form-item label="账户类型">
          <a-select
            :value="filters.type || 'all'"
            :options="accountTypeOptions"
            placeholder="全部类型"
            @change="handleTypeUpdate"
          />
        </a-form-item>
        <a-form-item label="分组">
          <GroupSelect
            :value="filters.groupId || undefined"
            :selected-group="filters.group"
            allow-clear
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groupOptions"
            :loading="groupOptionsLoading"
            :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
            @dropdown-visible-change="emit('group-dropdown', $event)"
            @search="emit('group-search', $event)"
            @update:selected-group="handleGroupSelectionUpdate"
            @update:value="handleGroupUpdate"
          />
        </a-form-item>
        <a-form-item label="账户状态">
          <a-select
            :value="filters.status"
            allow-clear
            :max-tag-count="1"
            mode="multiple"
            :options="statusOptions"
            placeholder="全部状态"
            @change="handleStatusChange"
          />
        </a-form-item>
      </a-form>
    </template>
    <template #actions>
      <slot name="actions" />
      <a-tooltip :title="exportTooltip">
        <a-button :loading="exportLoading" @click="emit('export')">
          <template #icon>
            <DownloadOutlined />
          </template>
          导出 JSON
        </a-button>
      </a-tooltip>
      <a-button @click="emit('import')">
        <template #icon>
          <UploadOutlined />
        </template>
        导入账户
      </a-button>
      <a-button type="primary" @click="emit('create')">添加账户</a-button>
    </template>
    <template #filters>
      <label class="mobile-filter-field">
        <span>供应商</span>
        <a-select
          :value="filters.providerCode || 'all'"
          :options="providerOptions"
          placeholder="全部供应商"
          @change="handleProviderUpdate"
        />
      </label>
      <label class="mobile-filter-field">
        <span>账户类型</span>
        <a-select
          :value="filters.type || 'all'"
          :options="accountTypeOptions"
          placeholder="全部类型"
          @change="handleTypeUpdate"
        />
      </label>
      <label class="mobile-filter-field">
        <span>分组</span>
        <GroupSelect
          :value="filters.groupId || undefined"
          :selected-group="filters.group"
          allow-clear
          :disabled="groupFilterDisabled"
          :filter-option="false"
          :groups="groupOptions"
          :loading="groupOptionsLoading"
          :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
          @dropdown-visible-change="emit('group-dropdown', $event)"
          @search="emit('group-search', $event)"
          @update:selected-group="handleGroupSelectionUpdate"
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
          :selected-principal="filters.systemAccount"
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
import { DownloadOutlined, UploadOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountStatus, GroupOptionSummary, ProviderDefinition, SystemAccountPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { accountTypeText } from './accountFormatters'
import type { AccountFilters } from './accountFormTypes'
import { FALLBACK_PROVIDERS } from './accountOptions'

type FilterOption<T extends string> = {
  label: string
  value: T
}
type SelectValue = string | string[] | undefined

const props = defineProps<{
  activeFilterCount: number
  exportLoading?: boolean
  filters: AccountFilters
  groupFilterDisabled?: boolean
  groupOptions: GroupOptionSummary[]
  groupOptionsLoading?: boolean
  isManagementView: boolean
  providers: ProviderDefinition[]
  refreshLoading: boolean
  selectedCount?: number
  statusOptions: Array<FilterOption<AccountStatus>>
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountsLoading?: boolean
}>()

const emit = defineEmits<{
  (event: 'create'): void
  (event: 'export'): void
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
  (event: 'update:groupSelection', value?: GroupSelection): void
  (event: 'update:keyword', value: string): void
  (event: 'update:providerCode', value: string): void
  (event: 'update:status', value: AccountStatus[]): void
  (event: 'update:systemAccountId', value: string): void
  (event: 'update:systemAccountSelection', value?: PrincipalSelection): void
  (event: 'update:type', value: string): void
}>()

const accountStatusValues = new Set<AccountStatus>(['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable'])
const resolvedProviders = computed(() => props.providers.length ? props.providers : FALLBACK_PROVIDERS)
const exportTooltip = computed(() => props.selectedCount
  ? `已选择 ${props.selectedCount} 个账户，将优先导出已选自有账户`
  : '未选择账户时按当前筛选导出自有账户')
const providerOptions = computed(() => [
  { label: '全部供应商', value: 'all' },
  ...resolvedProviders.value.map((provider) => ({ label: provider.name, value: provider.code }))
])
const accountTypeOptions = computed(() => {
  const providerCode = props.filters.providerCode || 'all'
  const selectedProvider = providerCode !== 'all'
    ? resolvedProviders.value.find((provider) => provider.code === providerCode)
    : undefined
  const providers = selectedProvider ? [selectedProvider] : resolvedProviders.value
  const seenTypes = new Set<string>()
  const types = providers
    .flatMap((provider) => provider.protocolProfiles.length
      ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
      : provider.accountTypes)
    .filter((type) => {
      if (seenTypes.has(type)) return false
      seenTypes.add(type)
      return true
    })
  return [
    { label: '全部类型', value: 'all' },
    ...types.map((type) => ({ label: accountTypeText(type), value: type }))
  ]
})
const advancedFilterCount = computed(() => [
  Boolean(props.filters.providerCode && props.filters.providerCode !== 'all'),
  Boolean(props.filters.type && props.filters.type !== 'all'),
  props.filters.status.length > 0,
  Boolean(props.filters.groupId),
  props.isManagementView && props.filters.systemAccountId !== allSystemAccountsValue
].filter(Boolean).length)

function handleProviderUpdate(value: SelectValue) {
  emit('update:providerCode', typeof value === 'string' ? value : 'all')
}

function handleTypeUpdate(value: SelectValue) {
  emit('update:type', typeof value === 'string' ? value : 'all')
}

function handleGroupUpdate(value: SelectValue) {
  emit('update:groupId', typeof value === 'string' ? value : '')
  emit('search')
}

function handleGroupSelectionUpdate(value?: GroupSelection) {
  emit('update:groupSelection', value)
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

.advanced-filter-form :deep(.ant-select) {
  width: 100%;
}
</style>
