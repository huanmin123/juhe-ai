import {
  mergeSelectedSelectOptions,
  rememberSelectOption,
  selectLabelForValue,
  type SelectOption
} from './selectLabelCache'
import type { ProxyOptionKind, ProxyPoolOptionSummary, ProxyProfileOptionSummary } from '@/types/domain'

export type { SelectOption } from './selectLabelCache'
export type { ProxyOptionKind } from '@/types/domain'

export interface ProxySelection {
  id: string
  name: string
}

/** kind-aware 选中项（§9.5）：ProxySelect 上抛给表单层，kind 决定拆分到哪个裸 id 字段。 */
export interface ProxyTargetSelection extends ProxySelection {
  kind: ProxyOptionKind
}

export interface ProxyOptionLike {
  id: string
  name: string
  type?: string
  enabled?: boolean
}

const proxyCacheKey = 'proxies'
// 池标签使用独立缓存键（代理池设计 §9.5）：避免 id 冲突和标签格式串味——
// 池标签不写入代理缓存，代理标签不覆盖池缓存。
const proxyPoolCacheKey = 'proxyPools'

// ---- 复合值编解码（§9.5 值编码）：仅存在于表单/组件层，API 只认裸 id。 ----

export type ProxyCompositeValue = string

export function proxyCompositeValue(kind: ProxyOptionKind, id: string | undefined | null): ProxyCompositeValue | undefined {
  const normalizedId = id?.trim()
  return normalizedId ? `${kind}:${normalizedId}` : undefined
}

export interface ProxyTargetRef {
  kind: ProxyOptionKind
  id: string
}

/**
 * 解析 ProxySelect 的内部复合值：`profile:<id>` / `pool:<id>`。
 * 兼容历史裸 id（无冒号前缀）：按代理档案处理，旧调用方回填不破。
 */
export function parseProxyCompositeValue(value: string | undefined | null): ProxyTargetRef | undefined {
  const normalizedValue = value?.trim()
  if (!normalizedValue) return undefined
  const separatorIndex = normalizedValue.indexOf(':')
  if (separatorIndex <= 0) return { kind: 'profile', id: normalizedValue }
  const kind = normalizedValue.slice(0, separatorIndex)
  const id = normalizedValue.slice(separatorIndex + 1).trim()
  if ((kind === 'profile' || kind === 'pool') && id) return { kind, id }
  return undefined
}

/** 表单互斥拆分：复合值 → 裸 proxyProfileId / proxyPoolId（两者恰有一个非空）。 */
export function splitProxyCompositeValue(value: string | undefined | null): { profileId?: string; poolId?: string } {
  const target = parseProxyCompositeValue(value)
  if (!target) return {}
  return target.kind === 'pool' ? { poolId: target.id } : { profileId: target.id }
}

/** 表单互斥组装：裸 proxyProfileId / proxyPoolId → 复合值（池优先，二者互斥由调用方保证）。 */
export function joinProxyCompositeValue(profileId: string | undefined | null, poolId: string | undefined | null): ProxyCompositeValue | undefined {
  const normalizedPoolId = poolId?.trim()
  if (normalizedPoolId) return proxyCompositeValue('pool', normalizedPoolId)
  return proxyCompositeValue('profile', profileId)
}

// ---- 代理档案标签缓存（沿用原 key，行为不变） ----

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

// ---- 代理池标签缓存（独立 key，kind-aware §9.5） ----

export interface ProxyPoolOptionLike {
  id: string
  name: string
  enabled?: boolean
  memberCount?: number
  assignableMemberCount?: number
}

export function proxyPoolOptionLabel(pool: ProxyPoolOptionLike | ProxyPoolOptionSummary): string {
  const memberCount = pool.memberCount ?? 0
  const assignableCount = pool.assignableMemberCount ?? 0
  const base = `${pool.name}（${memberCount} 个成员，${assignableCount} 个可分配）`
  if (pool.enabled === false) return `${base}，已停用`
  if (memberCount === 0) return `${base}，无成员`
  if (assignableCount === 0) return `${base}，无可分配成员`
  return base
}

export function rememberProxyPoolLabels(pools: ProxyPoolOptionLike[]): void {
  for (const pool of pools) {
    rememberProxyPoolLabel(pool.id, proxyPoolOptionLabel(pool))
  }
}

export function rememberProxyPoolLabel(id: string | undefined, name: string | undefined): void {
  rememberSelectOption(proxyPoolCacheKey, id, name)
}

export function rememberProxyPoolSelection(selection: ProxySelection | undefined): void {
  rememberProxyPoolLabel(selection?.id, selection?.name)
}

export function proxyPoolLabelForId(id: string | undefined): string | undefined {
  return selectLabelForValue(proxyPoolCacheKey, id)
}

export function proxyPoolSelectionForId(
  id: string | undefined,
  pools: ProxyPoolOptionLike[] = [],
  options: SelectOption[] = []
): ProxySelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const pool = pools.find((item) => item.id === normalizedId)
  if (pool?.name?.trim()) return { id: normalizedId, name: proxyPoolOptionLabel(pool) }
  const option = options.find((item) => item.value === normalizedId)
  if (option?.label?.trim()) return { id: normalizedId, name: option.label.trim() }
  const cached = proxyPoolLabelForId(normalizedId)
  return cached ? { id: normalizedId, name: cached } : undefined
}

/** 复合值 → 选中项（kind-aware）：按 kind 分派到代理/池的候选与缓存，互不串缓存。 */
export function proxyTargetSelectionForValue(
  value: string | undefined,
  proxies: ProxyOptionLike[] = [],
  pools: ProxyPoolOptionLike[] = [],
  options: SelectOption[] = []
): (ProxySelection & { kind: ProxyOptionKind }) | undefined {
  const target = parseProxyCompositeValue(value)
  if (!target) return undefined
  const selection = target.kind === 'pool'
    ? proxyPoolSelectionForId(target.id, pools, options)
    : proxySelectionForId(target.id, proxies, options)
  return selection ? { ...selection, kind: target.kind } : undefined
}
