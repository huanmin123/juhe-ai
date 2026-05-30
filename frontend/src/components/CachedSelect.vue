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
import { recordLocalSelectChoices, sortSelectOptionsByLocalPreference } from '@/shared/selectLocalPreferenceCache'

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
  ignoredPreferenceValues?: Array<string | undefined>
  cacheKey?: string
  preferenceKey?: string
  recordPreference?: boolean
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
  ignoredPreferenceValues: () => [],
  cacheKey: 'default',
  preferenceKey: undefined,
  recordPreference: true,
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
const localPreferenceKey = computed(() => props.preferenceKey ?? props.cacheKey)
const hiddenValueSet = computed(() => new Set(props.hiddenOptionValues.map((value) => value?.trim()).filter(Boolean)))
const mergedOptions = computed(() => mergeSelectedSelectOptions(
  props.cacheKey,
  props.options,
  normalizedSelectedIds.value,
  props.selectedOptions
))
const sortedOptions = computed(() => sortSelectOptionsByLocalPreference(
  localPreferenceKey.value,
  mergedOptions.value,
  selectedValues(props.value),
  props.ignoredPreferenceValues
))
const currentValueSet = computed(() => new Set(selectedValues(props.value).map((value) => value?.trim()).filter(Boolean)))
const selectOptions = computed(() => sortedOptions.value.map((option) => (
  hiddenValueSet.value.has(option.value) && currentValueSet.value.has(option.value)
    ? { ...option, style: { ...option.style, display: 'none' } }
    : option
)).filter((option) => !hiddenValueSet.value.has(option.value) || currentValueSet.value.has(option.value)).slice(0, 50))
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
  rememberLocalPreference(value)
  emit('change', value, option)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function rememberLocalPreference(value: SelectValue): void {
  if (!props.recordPreference) return
  recordLocalSelectChoices(localPreferenceKey.value, selectedValues(value), mergedOptions.value, props.ignoredPreferenceValues)
}
</script>
