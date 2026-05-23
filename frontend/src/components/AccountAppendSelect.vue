<template>
  <AccountSelect
    :value="value"
    :accounts="accounts"
    :selected-accounts="selectedAccounts"
    :hidden-option-values="hiddenAccountIds"
    allow-clear
    cache-key="accounts"
    :disabled="disabled"
    :loading="loading"
    :max-tag-count="maxTagCount"
    mode="multiple"
    :placeholder="placeholder"
    show-search
    v-bind="$attrs"
    @change="handleChange"
    @dropdown-visible-change="emit('dropdown-visible-change', $event)"
    @search="emit('search', $event)"
  />
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'

import AccountSelect from '@/components/AccountSelect.vue'
import type { AccountOptionLike, AccountSelection } from '@/shared/accountLabelCache'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: string[]
  accounts?: AccountOptionLike[]
  selectedAccounts?: Array<AccountSelection | undefined>
  hiddenAccountIds?: Array<string | undefined>
  max?: number
  maxTagCount?: number
  disabled?: boolean
  loading?: boolean
  placeholder?: string
}>(), {
  value: () => [],
  accounts: () => [],
  selectedAccounts: () => [],
  hiddenAccountIds: () => [],
  max: 20,
  maxTagCount: 1,
  disabled: false,
  loading: false,
  placeholder: '输入账户名称添加账户'
})

const emit = defineEmits<{
  (event: 'update:value', value: string[]): void
  (event: 'change', value: string[], previousValue: string[]): void
  (event: 'dropdown-visible-change', open: boolean): void
  (event: 'search', value: string): void
}>()

function handleChange(value: string | string[] | undefined) {
  const previousValue = [...props.value]
  const nextValue = normalizedIds(value)
  const acceptedValue = nextValue.slice(0, props.max)
  if (nextValue.length > props.max) {
    message.warning(`最多添加 ${props.max} 个账户`)
  }
  emit('update:value', acceptedValue)
  emit('change', acceptedValue, previousValue)
}

function normalizedIds(value: string | string[] | undefined): string[] {
  const values = Array.isArray(value) ? value : [value]
  return [...new Set(values
    .map((item) => item?.trim() ?? '')
    .filter(Boolean))]
}
</script>
