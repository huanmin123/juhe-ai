<template>
  <CachedSelect
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :options="selectOptions"
    :placeholder="placeholder"
    cache-key="proxies"
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import {
  mergeSelectedProxyOptions,
  proxySelectionForId,
  proxySelectOptionLabel,
  rememberProxyLabels,
  rememberProxySelections,
  type ProxyOptionLike,
  type ProxySelection,
  type SelectOption
} from '@/shared/proxyLabelCache'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  proxies?: ProxyOptionLike[]
  options?: SelectOption[]
  selectedProxy?: ProxySelection
  selectedProxies?: Array<ProxySelection | undefined>
  selectedIds?: Array<string | undefined>
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  mode?: SelectMode
  placeholder?: string
}>(), {
  proxies: () => [],
  options: () => [],
  selectedProxy: undefined,
  selectedProxies: () => [],
  selectedIds: () => [],
  allowClear: false,
  disabled: false,
  loading: false,
  mode: undefined,
  placeholder: '输入代理名称搜索'
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedProxy', value: ProxySelection | undefined): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const baseOptions = computed(() => [
  ...props.options,
  ...props.proxies.map((proxy) => ({
    label: proxySelectOptionLabel(proxy),
    value: proxy.id,
    disabled: proxy.enabled === false
  }))
])
const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds
])
const normalizedSelectedProxies = computed(() => [
  props.selectedProxy,
  ...props.selectedProxies
])
const selectOptions = computed(() => mergeSelectedProxyOptions(
  baseOptions.value,
  normalizedSelectedIds.value,
  normalizedSelectedProxies.value
))
const displayValue = computed(() => props.value)

watch(
  () => props.proxies,
  (proxies) => rememberProxyLabels(proxies),
  { immediate: true }
)
watch(
  normalizedSelectedProxies,
  (proxies) => rememberProxySelections(proxies),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emitSelectedProxy(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedProxy(value)
  emit('change', value, option)
}

function emitSelectedProxy(value: SelectValue): void {
  emit('update:selectedProxy', typeof value === 'string' ? selectedProxyForId(value) : undefined)
}

function selectedProxyForId(id: string | undefined): ProxySelection | undefined {
  return proxySelectionForId(id, props.proxies, selectOptions.value)
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}
</script>
