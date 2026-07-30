<template>
  <a-form v-if="visible" layout="vertical" :class="{ 'advanced-filter-form': mode === 'advanced' }">
    <a-form-item label="结果">
      <a-select v-model:value="outcomeValue" :options="outcomeOptions" @change="handleAdvancedApply" />
    </a-form-item>
    <a-form-item label="来源">
      <a-select v-model:value="trafficSourceValue" :options="trafficSourceOptions" @change="handleAdvancedApply" />
    </a-form-item>
    <a-form-item label="用户">
      <SystemPrincipalSelect
        v-model:value="systemAccountValue"
        v-model:selected-principal="systemAccountSelectionValue"
        :accounts="systemAccounts"
        :active-only="false"
        :filter-option="false"
        include-all
        :loading="systemAccountOptionsLoading"
        placeholder="全部系统账户"
        @change="handleAdvancedApply"
        @dropdown-visible-change="$emit('system-account-dropdown-visible-change', $event)"
        @search="$emit('system-account-search', $event)"
      />
    </a-form-item>
    <a-form-item label="AI账户">
      <AccountSelect
        v-model:value="accountIdValue"
        v-model:selected-account="accountSelectionValue"
        :accounts="accountOptions"
        :filter-option="false"
        :loading="accountOptionsLoading"
        allow-clear
        placeholder="选择 AI账户"
        @change="$emit('apply')"
        @dropdown-visible-change="$emit('account-dropdown-visible-change', $event)"
        @search="$emit('account-search', $event)"
      />
    </a-form-item>
    <a-form-item label="接口路径">
      <a-input v-model:value="pathValue" allow-clear placeholder="/v1/responses" @press-enter="handleAdvancedApply" />
    </a-form-item>
    <a-form-item label="会话 ID">
      <a-input v-model:value="sessionIdValue" allow-clear placeholder="完整会话 ID（精确匹配）" @press-enter="handleAdvancedApply" />
    </a-form-item>
  </a-form>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import AccountSelect from '@/components/AccountSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type {
  AuditOutcome,
  AuditTrafficSource,
  AccountOptionSummary,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'

type SelectOption<T extends string> = {
  label: string
  value: T
}

const props = withDefaults(defineProps<{
  mode: 'advanced' | 'mobile'
  visible?: boolean
  outcome: AuditOutcome | 'all'
  outcomeOptions: Array<SelectOption<AuditOutcome | 'all'>>
  trafficSource: AuditTrafficSource | 'all'
  trafficSourceOptions: Array<SelectOption<AuditTrafficSource | 'all'>>
  systemAccount: string
  systemAccountSelection?: PrincipalSelection
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountOptionsLoading?: boolean
  accountId: string
  accountSelection?: AccountSelection
  accountOptions: AccountOptionSummary[]
  accountOptionsLoading?: boolean
  sessionId: string
  path: string
}>(), {
  visible: true,
  systemAccountSelection: undefined,
  systemAccountOptionsLoading: false,
  accountSelection: undefined,
  accountOptionsLoading: false
})

const emit = defineEmits<{
  (event: 'update:outcome', value: AuditOutcome | 'all'): void
  (event: 'update:trafficSource', value: AuditTrafficSource | 'all'): void
  (event: 'update:systemAccount', value: string): void
  (event: 'update:systemAccountSelection', value: PrincipalSelection | undefined): void
  (event: 'update:accountId', value: string): void
  (event: 'update:accountSelection', value: AccountSelection | undefined): void
  (event: 'update:sessionId', value: string): void
  (event: 'update:path', value: string): void
  (event: 'apply'): void
  (event: 'system-account-dropdown-visible-change', value: boolean): void
  (event: 'system-account-search', value: string): void
  (event: 'account-dropdown-visible-change', value: boolean): void
  (event: 'account-search', value: string): void
}>()

const outcomeValue = computed({
  get: () => props.outcome,
  set: (value) => emit('update:outcome', value)
})
const trafficSourceValue = computed({
  get: () => props.trafficSource,
  set: (value) => emit('update:trafficSource', value)
})
const systemAccountValue = computed({
  get: () => props.systemAccount,
  set: (value) => emit('update:systemAccount', value ?? '')
})
const systemAccountSelectionValue = computed({
  get: () => props.systemAccountSelection,
  set: (value) => emit('update:systemAccountSelection', value)
})
const accountIdValue = computed({
  get: () => props.accountId,
  set: (value) => emit('update:accountId', value ?? '')
})
const accountSelectionValue = computed({
  get: () => props.accountSelection,
  set: (value) => emit('update:accountSelection', value)
})
const sessionIdValue = computed({
  get: () => props.sessionId,
  set: (value) => emit('update:sessionId', value)
})
const pathValue = computed({
  get: () => props.path,
  set: (value) => emit('update:path', value)
})

function handleAdvancedApply(): void {
  if (props.mode === 'advanced') {
    emit('apply')
  }
}
</script>

<style scoped>
.advanced-filter-form :deep(.ant-input) {
  width: 100%;
}
</style>
