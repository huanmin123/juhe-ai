export interface SelectOption {
  label: string
  value: string
  disabled?: boolean
  style?: Record<string, string>
  /** a-select 分组节点（ant-design options 嵌套）：有 children 时本节点不承载可选项。 */
  options?: SelectOption[]
}

export function isSelectOptionGroup(option: SelectOption): boolean {
  return Array.isArray(option.options)
}

const caches = new Map<string, Map<string, string>>()

export function rememberSelectOptions(cacheKey: string, options: SelectOption[]): void {
  for (const option of options) {
    // 分组节点与组内子项不写入平铺标签缓存：子项标签由调用方的 kind-aware
    // 缓存维护（如 proxyLabelCache 的 proxies/proxyPools 分键），避免复合值或
    // 分组语义串入平铺缓存。
    if (isSelectOptionGroup(option)) continue
    rememberSelectOption(cacheKey, option.value, option.label)
  }
}

export function rememberSelectOption(cacheKey: string, value: string | undefined, label: string | undefined): void {
  const normalizedValue = value?.trim()
  const normalizedLabel = label?.trim()
  if (!normalizedValue || !normalizedLabel) return
  cacheFor(cacheKey).set(normalizedValue, normalizedLabel)
}

export function selectLabelForValue(cacheKey: string, value: string | undefined): string | undefined {
  return value ? cacheFor(cacheKey).get(value) : undefined
}

export function selectedOptionForValue(cacheKey: string, value: string | undefined, options: SelectOption[] = []): SelectOption | undefined {
  const normalizedValue = value?.trim()
  if (!normalizedValue) return undefined
  const option = options.find((item) => item.value === normalizedValue)
  if (option?.label?.trim()) return { label: option.label.trim(), value: normalizedValue, disabled: option.disabled }
  const cachedLabel = selectLabelForValue(cacheKey, normalizedValue)
  return cachedLabel ? { label: cachedLabel, value: normalizedValue } : undefined
}

export function mergeSelectedSelectOptions(
  cacheKey: string,
  options: SelectOption[],
  selectedIds: Array<string | undefined>,
  selectedOptions: Array<SelectOption | undefined> = []
): SelectOption[] {
  const merged = new Map<string, SelectOption>()
  const existingValues = new Set<string>()
  for (const option of options) {
    if (isSelectOptionGroup(option)) {
      for (const child of option.options ?? []) existingValues.add(child.value)
      merged.set(groupNodeKey(option), option)
      continue
    }
    rememberSelectOption(cacheKey, option.value, option.label)
    existingValues.add(option.value)
    merged.set(option.value, option)
  }
  for (const option of selectedOptions) {
    const normalizedValue = option?.value.trim()
    const normalizedLabel = option?.label.trim()
    if (!normalizedValue || !normalizedLabel || existingValues.has(normalizedValue)) continue
    rememberSelectOption(cacheKey, normalizedValue, normalizedLabel)
    existingValues.add(normalizedValue)
    merged.set(normalizedValue, { ...option, label: normalizedLabel, value: normalizedValue })
  }
  for (const id of selectedIds) {
    const normalizedId = id?.trim()
    if (!normalizedId || existingValues.has(normalizedId)) continue
    const label = selectLabelForValue(cacheKey, normalizedId)
    if (label) {
      existingValues.add(normalizedId)
      merged.set(normalizedId, { label, value: normalizedId })
    }
  }
  // 分组节点原位保留，组内子项做同样的选中合并（未知选中值按缓存标签注入组尾）。
  const output: SelectOption[] = []
  for (const entry of merged.values()) {
    if (!isSelectOptionGroup(entry)) {
      output.push(entry)
      continue
    }
    const children = mergeSelectedSelectOptions(cacheKey, entry.options ?? [], selectedIds, selectedOptions)
      .filter((child) => !isSelectOptionGroup(child))
    output.push({ ...entry, options: children })
  }
  return output
}

function groupNodeKey(option: SelectOption): string {
  return `__group__${option.label}`
}

function cacheFor(cacheKey: string): Map<string, string> {
  let cache = caches.get(cacheKey)
  if (!cache) {
    cache = new Map()
    caches.set(cacheKey, cache)
  }
  return cache
}
