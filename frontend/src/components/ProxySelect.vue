<template>
  <CachedSelect
    :value="displayValue"
    :allow-clear="allowClear"
    :disabled="disabled"
    :loading="loading"
    :mode="mode"
    :options="selectOptions"
    :placeholder="placeholder"
    :cache-key="proxyCacheKey"
    :preference-key="preferenceKey"
    :filter-option="filterOption"
    v-bind="$attrs"
    @change="handleChange"
    @dropdown-visible-change="handleDropdownVisibleChange"
    @search="handleSearch"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import {
  parseProxyCompositeValue,
  proxyCompositeValue,
  proxyLabelForId,
  proxyPoolLabelForId,
  proxyPoolOptionLabel,
  proxySelectOptionLabel,
  proxyTargetSelectionForValue,
  rememberProxyLabels,
  rememberProxyPoolLabels,
  rememberProxyPoolSelection,
  rememberProxySelections,
  type ProxyOptionKind,
  type ProxyOptionLike,
  type ProxyPoolOptionLike,
  type ProxySelection,
  type ProxyTargetSelection,
  type SelectOption
} from '@/shared/proxyLabelCache'

type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

// kind-aware 代理选择（代理池设计 §9.2/§9.5）：
// - 内部值为复合值 `profile:<id>` / `pool:<id>`，选项分「代理」「代理池」两组；
// - 提交前由表单/保存 payload 拆成裸 proxyProfileId / proxyPoolId，复合值绝不发给 API；
// - 标签缓存按 kind 分派（proxies / proxyPools 独立 key），CachedSelect 不理解复合值，
//   拆分/组装都在本包装层完成（参考 SystemPrincipalSelect 的 kind:id 模式）；
// - 编辑回填：候选未加载时用池/代理标签缓存注入组内选中项，不把复合值原样显示。
const props = withDefaults(defineProps<{
  value?: SelectValue
  proxies?: ProxyOptionLike[]
  pools?: ProxyPoolOptionLike[]
  options?: SelectOption[]
  selectedProxy?: ProxySelection
  selectedProxies?: Array<ProxySelection | undefined>
  selectedIds?: Array<string | undefined>
  cacheKey?: string
  preferenceKey?: string
  allowClear?: boolean
  disabled?: boolean
  loading?: boolean
  mode?: SelectMode
  filterOption?: boolean | ((input: string, option: SelectOption) => boolean)
  placeholder?: string
}>(), {
  proxies: () => [],
  pools: () => [],
  options: () => [],
  selectedProxy: undefined,
  selectedProxies: () => [],
  selectedIds: () => [],
  cacheKey: 'proxies',
  preferenceKey: undefined,
  allowClear: false,
  disabled: false,
  loading: false,
  mode: undefined,
  filterOption: false,
  placeholder: '输入代理名称搜索'
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedProxy', value: ProxySelection | undefined): void
  (event: 'update:selectedTarget', value: ProxyTargetSelection | undefined): void
  (event: 'change', value: SelectValue, option: unknown): void
  (event: 'dropdownVisibleChange', open: boolean): void
  (event: 'search', value: string): void
}>()

const proxyCacheKey = computed(() => props.cacheKey || 'proxies')

// 已选复合值（kind-aware 拆分），用于组内注入候选未覆盖的选中项。
const selectedTargets = computed<Array<ProxyTargetSelection | undefined>>(() =>
  selectedValues(props.value).map((value) => {
    const target = parseProxyCompositeValue(value)
    if (!target) return undefined
    return resolveSelectedTarget(target.kind, target.id)
  }))
const selectedTargetIds = computed(() => selectedTargets.value
  .filter((target): target is ProxyTargetSelection => Boolean(target))
  .map((target) => `${target.kind}:${target.id}`))

const profileOptions = computed<SelectOption[]>(() => {
  const base: SelectOption[] = props.options.length
    ? props.options
    : props.proxies.map((proxy) => ({
      label: proxySelectOptionLabel(proxy),
      value: compositeOptionValue('profile', proxy.id),
      disabled: proxy.enabled === false
    }))
  return withSelectedBackfill(base, 'profile')
})
const poolOptions = computed<SelectOption[]>(() => withSelectedBackfill(
  props.pools.map((pool) => ({
    label: proxyPoolOptionLabel(pool),
    value: compositeOptionValue('pool', pool.id),
    disabled: pool.enabled === false
  })),
  'pool'
))
// 分组选项（§9.2）：空组不渲染，避免出现空「代理池」标题。
const selectOptions = computed<SelectOption[]>(() => {
  const groups: SelectOption[] = []
  if (profileOptions.value.length) groups.push({ label: '代理', value: 'group:profile', options: profileOptions.value })
  if (poolOptions.value.length) groups.push({ label: '代理池', value: 'group:pool', options: poolOptions.value })
  return groups
})
const normalizedSelectedIds = computed(() => [
  ...selectedTargetIds.value,
  ...props.selectedIds.map((id) => compositeValue(id)).filter((value): value is string => Boolean(value))
])
const normalizedSelectedProxies = computed(() => [
  props.selectedProxy,
  ...props.selectedProxies
])
const displayValue = computed(() => props.value)

watch(
  () => props.proxies,
  (proxies) => rememberProxyLabels(proxies),
  { immediate: true }
)
watch(
  () => props.pools,
  (pools) => rememberProxyPoolLabels(pools),
  { immediate: true }
)
watch(
  normalizedSelectedProxies,
  (proxies) => rememberProxySelections(proxies),
  { immediate: true }
)

function handleUpdateValue(value: SelectValue) {
  emitSelectedTarget(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedTarget(value)
  emit('change', value, option)
}

function handleDropdownVisibleChange(open: boolean) {
  emit('dropdownVisibleChange', open)
}

function handleSearch(value: string) {
  emit('search', value)
}

function emitSelectedTarget(value: SelectValue): void {
  const selection = typeof value === 'string'
    ? proxyTargetSelectionForValue(value, props.proxies, props.pools, flatOptions())
    : undefined
  emit('update:selectedTarget', selection)
  emit('update:selectedProxy', selection && selection.kind === 'profile' ? selection : undefined)
  if (selection?.kind === 'pool') {
    rememberProxyPoolSelection(selection)
  }
}

function flatOptions(): SelectOption[] {
  return [...profileOptions.value, ...poolOptions.value]
}

function resolveSelectedTarget(kind: ProxyOptionKind, id: string): ProxyTargetSelection | undefined {
  if (kind === 'pool') {
    const pool = props.pools.find((item) => item.id === id)
    if (pool?.name?.trim()) return { id, name: proxyPoolOptionLabel(pool), kind }
    const cached = proxyPoolLabelForId(id)
    return cached ? { id, name: cached, kind } : undefined
  }
  const proxy = props.proxies.find((item) => item.id === id)
  if (proxy?.name?.trim()) return { id, name: proxySelectOptionLabel(proxy), kind }
  const option = props.options.find((item) => item.value === id || item.value === compositeOptionValue('profile', id))
  if (option?.label?.trim()) return { id, name: option.label.trim(), kind }
  const cached = proxyLabelForId(id)
  return cached ? { id, name: cached, kind } : undefined
}

// 组内回填（§9.2）：候选未就绪时按 kind 缓存注入已选复合值的选中项，
// 避免下拉/输入框把 `pool:<id>` 原样显示；候选到位后由真实候选替换。
function withSelectedBackfill(base: SelectOption[], kind: ProxyOptionKind): SelectOption[] {
  const known = new Set(base.map((option) => option.value))
  const backfilled = selectedTargets.value
    .filter((target): target is ProxyTargetSelection => target?.kind === kind)
    .filter((target) => !known.has(`${kind}:${target.id}`) && target.name.trim())
    .map((target) => ({ label: target.name, value: `${kind}:${target.id}` }))
  return backfilled.length ? [...base, ...backfilled] : base
}

function compositeValue(value: string | undefined): string | undefined {
  const target = parseProxyCompositeValue(value)
  if (!target) return undefined
  return `${target.kind}:${target.id}`
}

function compositeOptionValue(kind: ProxyOptionKind, id: string): string {
  return proxyCompositeValue(kind, id) ?? id
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}
</script>
