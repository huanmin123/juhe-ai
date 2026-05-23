import {
  mergeSelectedSelectOptions,
  rememberSelectOption,
  selectLabelForValue,
  type SelectOption
} from './selectLabelCache'
import type { ProxyProfileOptionSummary } from '@/types/domain'

export type { SelectOption } from './selectLabelCache'

export interface ProxySelection {
  id: string
  name: string
}

export interface ProxyOptionLike {
  id: string
  name: string
  type?: string
  enabled?: boolean
}

const proxyCacheKey = 'proxies'

export function rememberProxyLabels(proxies: ProxyOptionLike[]): void {
  for (const proxy of proxies) {
    rememberProxyLabel(proxy.id, proxySelectOptionLabel(proxy))
  }
}

export function rememberProxyLabel(id: string | undefined, name: string | undefined): void {
  rememberSelectOption(proxyCacheKey, id, name)
}

export function rememberProxySelection(selection: ProxySelection | undefined): void {
  rememberProxyLabel(selection?.id, selection?.name)
}

export function rememberProxySelections(selections: Array<ProxySelection | undefined>): void {
  for (const selection of selections) {
    rememberProxySelection(selection)
  }
}

export function proxyLabelForId(id: string | undefined): string | undefined {
  return selectLabelForValue(proxyCacheKey, id)
}

export function proxySelectionForId(
  id: string | undefined,
  proxies: ProxyOptionLike[] = [],
  options: SelectOption[] = []
): ProxySelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const proxy = proxies.find((item) => item.id === normalizedId)
  if (proxy?.name?.trim()) return { id: normalizedId, name: proxySelectOptionLabel(proxy) }
  const option = options.find((item) => item.value === normalizedId)
  if (option?.label?.trim()) return { id: normalizedId, name: option.label.trim() }
  const cached = proxyLabelForId(normalizedId)
  return cached ? { id: normalizedId, name: cached } : undefined
}

export function proxySelectOptionLabel(proxy: ProxyOptionLike | ProxyProfileOptionSummary): string {
  const typeText = proxy.type?.trim()
  const disabledText = proxy.enabled === false ? '，已停用' : ''
  return typeText ? `${proxy.name}（${typeText}${disabledText}）` : proxy.name
}

export function mergeSelectedProxyOptions(
  options: SelectOption[],
  selectedIds: Array<string | undefined>,
  selectedProxies: Array<ProxySelection | undefined> = []
): SelectOption[] {
  return mergeSelectedSelectOptions(
    proxyCacheKey,
    options,
    selectedIds,
    selectedProxies.map((proxy) => proxy ? { label: proxy.name, value: proxy.id } : undefined)
  )
}
