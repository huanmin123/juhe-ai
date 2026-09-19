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
    :filter-option="filterOption"
    show-search
    v-bind="$attrs"
    @change="handleChange"
    @dropdown-visible-change="handleDropdownVisibleChange"
    @search="handleSearch"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'

import { mergeSelectedSelectOptions, rememberSelectOptions, type SelectOption } from '@/shared/selectLabelCache'
import { recordLocalSelectChoices, refreshLocalSelectPreferenceSnapshot, sortSelectOptionsByLocalPreference } from '@/shared/selectLocalPreferenceCache'

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
  filterOption?: boolean | ((input: string, option: SelectOption) => boolean)
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
  filterOption: undefined,
  placeholder: undefined
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'change', value: SelectValue, option: unknown): void
  (event: 'dropdownVisibleChange', open: boolean): void
  (event: 'search', value: string): void
}>()

const lastCommittedValues = ref<Array<string | undefined>>(selectedValues(props.value))
const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const localPreferenceKey = computed(() => props.preferenceKey ?? props.cacheKey)
// 排序快照版本：挂载和下拉打开时自增，驱动 sortedOptions 重新应用最新偏好排序；
// 选中动作只写偏好记录、不动该版本，当前视图不重排，下次打开下拉才生效。
const preferenceVersion = ref(0)
const hiddenValueSet = computed(() => new Set(props.hiddenOptionValues.map((value) => value?.trim()).filter(Boolean)))
const mergedOptions = computed(() => mergeSelectedSelectOptions(
  props.cacheKey,
  props.options,
  normalizedSelectedIds.value,
  props.selectedOptions
))
const sortedOptions = computed(() => {
  void preferenceVersion.value
  return sortSelectOptionsByLocalPreference(
    localPreferenceKey.value,
    mergedOptions.value,
    selectedValues(props.value),
    props.ignoredPreferenceValues
  )
})
const currentValueSet = computed(() => new Set(selectedValues(props.value).map((value) => value?.trim()).filter((value): value is string => Boolean(value))))
const selectOptions = computed(() => {
  const options = sortedOptions.value.map((option) => (
    hiddenValueSet.value.has(option.value) && currentValueSet.value.has(option.value)
      ? { ...option, style: { ...option.style, display: 'none' } }
      : option
  )).filter((option) => !hiddenValueSet.value.has(option.value) || currentValueSet.value.has(option.value))
  const knownValues = new Set(options.map((option) => option.value))
  // 候选未就绪时为已选值注入隐藏占位（display:none 不出现在下拉列表，但输入框/tag 能显示该值），
  // 避免已选值短暂闪空；真实候选到位后 merge 逻辑自然用真实 label 替换占位。
  const placeholderOptions = [...currentValueSet.value]
    .filter((value) => !knownValues.has(value))
    .map((value) => ({ label: value, value, style: { display: 'none' } }))
  return [...placeholderOptions, ...options].slice(0, 50)
})
// 不再过滤未知值：候选未就绪的短暂窗口内允许以原始值占位显示，不闪空。
const displayValue = computed(() => props.value)

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
watch(
  () => props.value,
  (value) => {
    lastCommittedValues.value = selectedValues(value)
  },
  { immediate: true }
)

onMounted(() => {
  refreshLocalSelectPreferenceSnapshot(localPreferenceKey.value)
  preferenceVersion.value += 1
})

function handleUpdateValue(value: SelectValue) {
  rememberLocalPreference(value, lastCommittedValues.value)
  lastCommittedValues.value = selectedValues(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emit('change', value, option)
}

function handleDropdownVisibleChange(open: boolean) {
  if (open) {
    refreshLocalSelectPreferenceSnapshot(localPreferenceKey.value)
    preferenceVersion.value += 1
  }
  emit('dropdownVisibleChange', open)
}

function handleSearch(value: string) {
  emit('search', value)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function rememberLocalPreference(value: SelectValue, previousValues: Array<string | undefined>): void {
  if (!props.recordPreference) return
  const nextValues = selectedValues(value)
  const previousValueSet = new Set(previousValues.map(normalizeValue).filter(Boolean))
  const valuesToRecord = Array.isArray(value)
    ? nextValues.filter((item) => !previousValueSet.has(normalizeValue(item)))
    : nextValues
  recordLocalSelectChoices(localPreferenceKey.value, valuesToRecord, mergedOptions.value, props.ignoredPreferenceValues)
}

function normalizeValue(value: string | undefined): string {
  return value?.trim() ?? ''
}
</script>
