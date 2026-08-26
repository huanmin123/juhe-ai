<template>
  <CachedSelect
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :options="selectOptions"
    :placeholder="placeholder"
    :cache-key="selectCacheKey"
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
  mergeSelectedRouteStrategyOptions,
  rememberRouteStrategyLabels,
  rememberRouteStrategySelections,
  routeStrategySelectOptionLabel,
  routeStrategySelectionForId,
  type RouteStrategyOptionLike,
  type RouteStrategySelection,
  type SelectOption
} from '@/shared/routeStrategyLabelCache'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  routeStrategies?: RouteStrategyOptionLike[]
  options?: SelectOption[]
  selectedStrategy?: RouteStrategySelection
  selectedStrategies?: Array<RouteStrategySelection | undefined>
  selectedIds?: Array<string | undefined>
  hiddenOptionValues?: Array<string | undefined>
  cacheKey?: string
  preferenceKey?: string
  allowClear?: boolean
  disabled?: boolean
  disableInactive?: boolean
  loading?: boolean
  mode?: SelectMode
  placeholder?: string
  showSystemAccountLabel?: boolean
}>(), {
  routeStrategies: () => [],
  options: () => [],
  selectedStrategy: undefined,
  selectedStrategies: () => [],
  selectedIds: () => [],
  hiddenOptionValues: () => [],
  cacheKey: 'route-strategies',
  preferenceKey: undefined,
  allowClear: false,
  disabled: false,
  disableInactive: false,
  loading: false,
  mode: undefined,
  placeholder: '输入策略路由名称搜索',
  showSystemAccountLabel: false
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedStrategy', value: RouteStrategySelection | undefined): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const selectCacheKey = computed(() => props.showSystemAccountLabel ? `${props.cacheKey}:system-account-label` : props.cacheKey)
const baseOptions = computed<SelectOption[]>(() => [
  ...props.options,
  ...props.routeStrategies.map((strategy) => ({
    label: routeStrategySelectOptionLabel(strategy, { showSystemAccountLabel: props.showSystemAccountLabel }),
    value: strategy.id,
    disabled: props.disableInactive && strategy.status !== 'active'
  }))
])
const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const normalizedSelectedStrategies = computed(() => [
  props.selectedStrategy,
  ...props.selectedStrategies
])
const selectOptions = computed(() => mergeSelectedRouteStrategyOptions(
  selectCacheKey.value,
  baseOptions.value,
  normalizedSelectedIds.value,
  normalizedSelectedStrategies.value,
  { showSystemAccountLabel: props.showSystemAccountLabel }
))
const displayValue = computed(() => props.value)

watch(
  () => [props.routeStrategies, selectCacheKey.value, props.showSystemAccountLabel] as const,
  ([strategies]) => rememberRouteStrategyLabels(strategies, selectCacheKey.value, { showSystemAccountLabel: props.showSystemAccountLabel }),
  { immediate: true }
)
watch(
  normalizedSelectedStrategies,
  (strategies) => rememberRouteStrategySelections(strategies, selectCacheKey.value),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emitSelectedStrategy(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedStrategy(value)
  emit('change', value, option)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function emitSelectedStrategy(value: SelectValue): void {
  emit('update:selectedStrategy', typeof value === 'string' ? selectedStrategyForId(value) : undefined)
}

function selectedStrategyForId(id: string | undefined): RouteStrategySelection | undefined {
  return routeStrategySelectionForId(id, props.routeStrategies, selectOptions.value, selectCacheKey.value)
}
</script>
