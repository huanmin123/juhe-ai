<template>
  <CachedSelect
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :options="selectOptions"
    :hidden-option-values="hiddenOptionValues"
    :placeholder="placeholder"
    :cache-key="cacheKey"
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import {
  accountSelectOptionLabel,
  accountSelectionForId,
  mergeSelectedAccountOptions,
  rememberAccountLabels,
  rememberAccountSelections,
  type AccountOptionLike,
  type AccountSelection,
  type SelectOption
} from '@/shared/accountLabelCache'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  accounts?: AccountOptionLike[]
  options?: SelectOption[]
  selectedAccount?: AccountSelection
  selectedAccounts?: Array<AccountSelection | undefined>
  selectedIds?: Array<string | undefined>
  hiddenOptionValues?: Array<string | undefined>
  cacheKey?: string
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  mode?: SelectMode
  placeholder?: string
}>(), {
  accounts: () => [],
  options: () => [],
  selectedAccount: undefined,
  selectedAccounts: () => [],
  selectedIds: () => [],
  hiddenOptionValues: () => [],
  cacheKey: 'accounts',
  allowClear: false,
  disabled: false,
  loading: false,
  mode: undefined,
  placeholder: '输入账户名称搜索'
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedAccount', value: AccountSelection | undefined): void
  (event: 'update:selectedAccounts', value: AccountSelection[]): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const baseOptions = computed(() => (
  props.options.length
    ? props.options
    : props.accounts.map((account) => ({ label: accountSelectOptionLabel(account), value: account.id }))
))
const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const normalizedSelectedAccounts = computed(() => [
  props.selectedAccount,
  ...props.selectedAccounts
])
const selectOptions = computed(() => mergeSelectedAccountOptions(
  props.cacheKey,
  baseOptions.value,
  normalizedSelectedIds.value,
  normalizedSelectedAccounts.value
))
const displayValue = computed(() => props.value)

watch(
  () => props.accounts,
  (accounts) => rememberAccountLabels(accounts, props.cacheKey),
  { immediate: true }
)
watch(
  normalizedSelectedAccounts,
  (accounts) => rememberAccountSelections(accounts, props.cacheKey),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emitSelectedAccounts(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedAccounts(value)
  emit('change', value, option)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function emitSelectedAccounts(value: SelectValue): void {
  const selections = selectedValues(value)
    .map((id) => selectedAccountForId(id))
    .filter((selection): selection is AccountSelection => Boolean(selection))
  emit('update:selectedAccounts', selections)
  emit('update:selectedAccount', typeof value === 'string' ? selections[0] : undefined)
}

function selectedAccountForId(id: string | undefined): AccountSelection | undefined {
  return accountSelectionForId(id, props.accounts, selectOptions.value, props.cacheKey)
}
</script>
