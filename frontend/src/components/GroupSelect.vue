<template>
  <CachedSelect
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :options="selectOptions"
    :placeholder="placeholder"
    :cache-key="cacheKey"
    :preference-key="preferenceKey"
    :hidden-option-values="hiddenOptionValues"
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import {
  groupSelectionForId,
  groupSelectOptionLabel,
  mergeSelectedGroupOptions,
  rememberGroupLabels,
  rememberGroupSelections,
  type GroupSelection,
  type SelectOption
} from '@/shared/groupLabelCache'
import type { GroupOptionSummary } from '@/types/domain'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  groups?: GroupOptionSummary[]
  options?: SelectOption[]
  selectedGroup?: GroupSelection
  selectedIds?: Array<string | undefined>
  selectedGroups?: Array<GroupSelection | undefined>
  hiddenOptionValues?: Array<string | undefined>
  cacheKey?: string
  preferenceKey?: string
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  mode?: SelectMode
  placeholder?: string
}>(), {
  groups: () => [],
  options: () => [],
  selectedGroup: undefined,
  selectedIds: () => [],
  selectedGroups: () => [],
  hiddenOptionValues: () => [],
  cacheKey: 'groups',
  preferenceKey: undefined,
  allowClear: false,
  disabled: false,
  loading: false,
  mode: undefined,
  placeholder: '输入分组名称搜索'
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedGroup', value: GroupSelection | undefined): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const selectOptions = computed(() => {
  const baseOptions = [
    ...props.options,
    ...props.groups.map((group) => ({
      label: groupSelectOptionLabel(group),
      value: group.id
    }))
  ]
  return mergeSelectedGroupOptions(baseOptions, normalizedSelectedIds.value, normalizedSelectedGroups.value)
})

const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const normalizedSelectedGroups = computed(() => [
  props.selectedGroup,
  ...props.selectedGroups
])
const displayValue = computed(() => props.value)

watch(
  () => props.groups,
  (groups) => rememberGroupLabels(groups),
  { immediate: true }
)
watch(
  normalizedSelectedGroups,
  (groups) => rememberGroupSelections(groups),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emitSelectedGroup(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedGroup(value)
  emit('change', value, option)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function emitSelectedGroup(value: SelectValue): void {
  emit('update:selectedGroup', typeof value === 'string' ? selectedGroupForId(value) : undefined)
}

function selectedGroupForId(id: string | undefined): GroupSelection | undefined {
  return groupSelectionForId(id, props.groups, selectOptions.value)
}
</script>
