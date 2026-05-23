export interface SelectOption {
  label: string
  value: string
  disabled?: boolean
  style?: Record<string, string>
}

const caches = new Map<string, Map<string, string>>()

export function rememberSelectOptions(cacheKey: string, options: SelectOption[]): void {
  for (const option of options) {
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
  for (const option of options) {
    rememberSelectOption(cacheKey, option.value, option.label)
    merged.set(option.value, option)
  }
  for (const option of selectedOptions) {
    const normalizedValue = option?.value.trim()
    const normalizedLabel = option?.label.trim()
    if (!normalizedValue || !normalizedLabel || merged.has(normalizedValue)) continue
    rememberSelectOption(cacheKey, normalizedValue, normalizedLabel)
    merged.set(normalizedValue, { ...option, label: normalizedLabel, value: normalizedValue })
  }
  for (const id of selectedIds) {
    const normalizedId = id?.trim()
    if (!normalizedId || merged.has(normalizedId)) continue
    const label = selectLabelForValue(cacheKey, normalizedId)
    if (label) {
      merged.set(normalizedId, { label, value: normalizedId })
    }
  }
  return [...merged.values()]
}

function cacheFor(cacheKey: string): Map<string, string> {
  let cache = caches.get(cacheKey)
  if (!cache) {
    cache = new Map()
    caches.set(cacheKey, cache)
  }
  return cache
}
