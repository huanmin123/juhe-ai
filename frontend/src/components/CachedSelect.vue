<template>
  <a-select
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :option-filter-prop="optionFilterProp"
    :options="selectOptions"
    :placeholder="placeholder"
    show-search
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import { mergeSelectedSelectOptions, rememberSelectOptions, type SelectOption } from '@/shared/selectLabelCache'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  options?: SelectOption[]
  selectedIds?: Array<string | undefined>
  selectedOptions?: Array<SelectOption | undefined>
  hiddenOptionValues?: Array<string | undefined>
  cacheKey?: string
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  mode?: SelectMode
  optionFilterProp?: string
  placeholder?: string
}>(), {
  options: () => [],
  selectedIds: () => [],
  selectedOptions: () => [],
  hiddenOptionValues: () => [],
  cacheKey: 'default',
  allowClear: false,
  disabled: false,
  loading: false,
  mode: undefined,
  optionFilterProp: 'label',
  placeholder: undefined
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const hiddenValueSet = computed(() => new Set(props.hiddenOptionValues.map((value) => value?.trim()).filter(Boolean)))
const mergedOptions = computed(() => mergeSelectedSelectOptions(
  props.cacheKey,
  props.options,
  normalizedSelectedIds.value,
  props.selectedOptions
))
const selectOptions = computed(() => mergedOptions.value.map((option) => (
  hiddenValueSet.value.has(option.value)
    ? { ...option, style: { ...option.style, display: 'none' } }
    : option
)))
const displayValue = computed(() => {
  const knownValues = new Set(mergedOptions.value.map((option) => option.value))
  if (Array.isArray(props.value)) {
    return props.value.filter((item) => knownValues.has(item))
  }
  return props.value && knownValues.has(props.value) ? props.value : undefined
})

watch(
  () => props.options,
  (options) => rememberSelectOptions(props.cacheKey, options),
  { immediate: true }
)
watch(
  () => props.selectedOptions,
  (options) => rememberSelectOptions(props.cacheKey, options.filter((option): option is SelectOption => Boolean(option))),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emit('change', value, option)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}
</script>
