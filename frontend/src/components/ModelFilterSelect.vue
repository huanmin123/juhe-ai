<template>
  <a-select
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :filter-option="filterModelOption"
    :loading="loading"
    :options="selectOptions"
    :placeholder="placeholder"
    option-filter-prop="label"
    show-search
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { ProviderModelOption } from '@/types/domain'

type SelectValue = string | number | string[] | undefined
type ModelSelectOption = {
  label: string
  value: string
  providerCode: string
}

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: string
  models?: ProviderModelOption[]
  options?: ModelSelectOption[]
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  placeholder?: string
}>(), {
  models: () => [],
  options: () => [],
  allowClear: true,
  disabled: false,
  loading: false,
  placeholder: '输入模型名称搜索'
})

const emit = defineEmits<{
  (event: 'update:value', value: string): void
  (event: 'change', value: string, option: unknown): void
}>()

const selectOptions = computed<ModelSelectOption[]>(() => (
  props.options.length
    ? props.options
    : props.models.map((item) => ({
      label: item.model,
      value: item.model,
      providerCode: item.providerCode
    }))
))
const displayValue = computed(() => props.value?.trim() ? props.value : undefined)

function handleUpdateValue(value: SelectValue): void {
  emit('update:value', normalizedValue(value))
}

function handleChange(value: SelectValue, option: unknown): void {
  emit('change', normalizedValue(value), option)
}

function normalizedValue(value: SelectValue): string {
  if (Array.isArray(value)) return value[0] ?? ''
  if (typeof value === 'number') return String(value)
  return typeof value === 'string' ? value : ''
}

function filterModelOption(input: string, option?: unknown): boolean {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  const record = option as Partial<ModelSelectOption> | undefined
  return includesKeyword(record?.label, keyword)
    || includesKeyword(record?.value, keyword)
    || includesKeyword(record?.providerCode, keyword)
}

function includesKeyword(value: unknown, keyword: string): boolean {
  return typeof value === 'string' && value.toLowerCase().includes(keyword)
}
</script>
