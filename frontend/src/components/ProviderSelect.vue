<template>
  <CachedSelect
    :value="value"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :options="selectOptions"
    :placeholder="placeholderText"
    :cache-key="cacheKey"
    :preference-key="preferenceKey"
    :ignored-preference-values="ignoredPreferenceValues"
    v-bind="$attrs"
    @change="handleChange"
    @dropdown-visible-change="handleDropdownVisibleChange"
    @search="handleSearch"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { ProviderDefinition } from '@/types/domain'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: string
  providers?: ProviderDefinition[]
  includeAll?: boolean
  allLabel?: string
  allValue?: string
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  placeholder?: string
  cacheKey?: string
  preferenceKey?: string
}>(), {
  providers: () => [],
  includeAll: false,
  allLabel: '全部供应商',
  allValue: 'all',
  allowClear: false,
  disabled: false,
  loading: false,
  placeholder: undefined,
  cacheKey: 'providers',
  preferenceKey: undefined
})

const emit = defineEmits<{
  (event: 'update:value', value: string | undefined): void
  (event: 'change', value: string | undefined, option: unknown): void
  (event: 'dropdownVisibleChange', open: boolean): void
  (event: 'search', value: string): void
}>()

const selectOptions = computed<SelectOption[]>(() => {
  const providerOptions = props.providers.map((provider) => ({
    label: provider.enabled === false ? `${provider.name}（停用）` : provider.name,
    value: provider.code,
    disabled: provider.enabled === false
  }))
  return props.includeAll
    ? [{ label: props.allLabel, value: props.allValue }, ...providerOptions]
    : providerOptions
})
const ignoredPreferenceValues = computed(() => props.includeAll ? [props.allValue] : [])
const placeholderText = computed(() => props.placeholder ?? (props.includeAll ? '全部供应商' : '选择供应商'))

function handleUpdateValue(value: string | string[] | undefined) {
  emit('update:value', normalizeValue(value))
}

function handleChange(value: string | string[] | undefined, option: unknown) {
  emit('change', normalizeValue(value), option)
}

function handleDropdownVisibleChange(open: boolean) {
  emit('dropdownVisibleChange', open)
}

function handleSearch(value: string) {
  emit('search', value)
}

function normalizeValue(value: string | string[] | undefined): string | undefined {
  return typeof value === 'string' ? value : undefined
}
</script>
