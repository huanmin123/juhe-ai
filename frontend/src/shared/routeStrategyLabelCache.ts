import type { RouteStrategyMode, RouteStrategyStatus } from '@/types/domain'
import {
  mergeSelectedSelectOptions,
  rememberSelectOption,
  selectLabelForValue,
  type SelectOption
} from './selectLabelCache'

export type { SelectOption } from './selectLabelCache'

export interface RouteStrategySelection {
  id: string
  name: string
  mode?: RouteStrategyMode
  status?: RouteStrategyStatus
  isDefault?: boolean
  systemAccountName?: string
}

export interface RouteStrategyOptionLike extends RouteStrategySelection {}

const routeStrategyCacheKey = 'route-strategies'

export function rememberRouteStrategyLabels(
  strategies: RouteStrategyOptionLike[],
  cacheKey = routeStrategyCacheKey,
  options: { showSystemAccountLabel?: boolean } = {}
): void {
  for (const strategy of strategies) {
    rememberRouteStrategyLabel(strategy.id, routeStrategySelectOptionLabel(strategy, options), cacheKey)
  }
}

export function rememberRouteStrategyLabel(id: string | undefined, label: string | undefined, cacheKey = routeStrategyCacheKey): void {
  rememberSelectOption(cacheKey, id, label)
}

export function rememberRouteStrategySelection(selection: RouteStrategySelection | undefined, cacheKey = routeStrategyCacheKey): void {
  rememberRouteStrategyLabel(selection?.id, selection?.name, cacheKey)
}

export function rememberRouteStrategySelections(selections: Array<RouteStrategySelection | undefined>, cacheKey = routeStrategyCacheKey): void {
  for (const selection of selections) {
    rememberRouteStrategySelection(selection, cacheKey)
  }
}

export function routeStrategyLabelForId(id: string | undefined, cacheKey = routeStrategyCacheKey): string | undefined {
  return selectLabelForValue(cacheKey, id)
}

export function routeStrategySelectionForId(
  id: string | undefined,
  strategies: RouteStrategyOptionLike[] = [],
  options: SelectOption[] = [],
  cacheKey = routeStrategyCacheKey
): RouteStrategySelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const strategy = strategies.find((item) => item.id === normalizedId)
  if (strategy?.name?.trim()) return routeStrategySelectionFromOption(strategy)
  const option = options.find((item) => item.value === normalizedId)
  if (option?.label?.trim()) return { id: normalizedId, name: option.label.trim() }
  const cached = routeStrategyLabelForId(normalizedId, cacheKey)
  return cached ? { id: normalizedId, name: cached } : undefined
}

export function routeStrategySelectionFromOption(strategy: RouteStrategyOptionLike): RouteStrategySelection {
  return {
    id: strategy.id,
    name: strategy.name,
    mode: strategy.mode,
    status: strategy.status,
    isDefault: strategy.isDefault,
    systemAccountName: strategy.systemAccountName
  }
}

export function routeStrategySelectOptionLabel(
  strategy: RouteStrategyOptionLike,
  options: { showSystemAccountLabel?: boolean } = {}
): string {
  const ownerPrefix = options.showSystemAccountLabel && strategy.systemAccountName ? `${strategy.systemAccountName} / ` : ''
  const suffix = strategy.isDefault ? '默认' : routeStrategyModeText(strategy.mode)
  return `${ownerPrefix}${strategy.name}（${suffix}）`
}

export function mergeSelectedRouteStrategyOptions(
  cacheKey: string,
  options: SelectOption[],
  selectedIds: Array<string | undefined>,
  selectedStrategies: Array<RouteStrategySelection | undefined> = [],
  labelOptions: { showSystemAccountLabel?: boolean } = {}
): SelectOption[] {
  return mergeSelectedSelectOptions(
    cacheKey,
    options,
    selectedIds,
    selectedStrategies.map((strategy) => strategy ? { label: routeStrategySelectOptionLabel(strategy, labelOptions), value: strategy.id } : undefined)
  )
}

export function routeStrategyModeText(mode: RouteStrategyMode | undefined): string {
  if (mode === 'hybrid_smart') return '混合智能路由'
  if (mode === 'weighted') return '权重调度路由'
  if (mode === 'round_robin') return '轮询路由'
  if (mode === 'failover') return '故障回退路由'
  return '普通路由'
}
