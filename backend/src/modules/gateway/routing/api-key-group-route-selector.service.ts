import { normalizeApiKeyGroupBindingWeight } from '../../../domain/api-key-routing.js'
import type { GatewayApiKeyGroupBindingRow, GatewayApiKeyRow } from '../../../storage/gateway-api-key.repository.js'

interface RoundRobinState {
  nextIndex: number
}

interface WeightedRouteState {
  currentWeights: Map<string, number>
}

const roundRobinStates = new Map<string, RoundRobinState>()
const weightedRouteStates = new Map<string, WeightedRouteState>()
const apiKeyGroupRouteStateMaxEntries = 10000

export function orderGatewayApiKeyGroupBindingsForDispatch(apiKey: GatewayApiKeyRow): GatewayApiKeyGroupBindingRow[] {
  const bindings = normalizeGatewayApiKeyGroupBindings(apiKey.group_bindings)
  if (bindings.length <= 1) {
    return bindings
  }
  if (apiKey.route_strategy_mode === 'round_robin') {
    return rotateBindings(bindings, nextRoundRobinIndex(apiKeyRouteStateKey(apiKey), bindings.length))
  }
  if (apiKey.route_strategy_mode === 'weighted') {
    return orderWeightedBindings(apiKeyRouteStateKey(apiKey), bindings)
  }
  return bindings
}

function apiKeyRouteStateKey(apiKey: GatewayApiKeyRow): string {
  return apiKey.route_strategy_id || apiKey.id
}

function normalizeGatewayApiKeyGroupBindings(bindings: GatewayApiKeyGroupBindingRow[] | undefined): GatewayApiKeyGroupBindingRow[] {
  return [...(bindings ?? [])]
    .filter((binding) => binding.status === 'active' && binding.group_enabled !== 0)
    .map((binding) => ({
      ...binding,
      weight: normalizeApiKeyGroupBindingWeight(binding.weight)
    }))
    .sort((left, right) => left.priority - right.priority || left.group_id.localeCompare(right.group_id))
}

function nextRoundRobinIndex(apiKeyId: string, bindingCount: number): number {
  const state = roundRobinStates.get(apiKeyId) ?? { nextIndex: 0 }
  const index = state.nextIndex % bindingCount
  roundRobinStates.set(apiKeyId, { nextIndex: (index + 1) % bindingCount })
  trimApiKeyRouteStateMap(roundRobinStates)
  return index
}

function orderWeightedBindings(apiKeyId: string, bindings: GatewayApiKeyGroupBindingRow[]): GatewayApiKeyGroupBindingRow[] {
  const state = weightedRouteStates.get(apiKeyId) ?? { currentWeights: new Map<string, number>() }
  cleanupWeightedState(state, bindings)
  const totalWeight = bindings.reduce((sum, binding) => sum + normalizeApiKeyGroupBindingWeight(binding.weight), 0)
  let selected = bindings[0]
  let selectedCurrentWeight = Number.NEGATIVE_INFINITY
  for (const binding of bindings) {
    const weight = normalizeApiKeyGroupBindingWeight(binding.weight)
    const current = (state.currentWeights.get(binding.id) ?? 0) + weight
    state.currentWeights.set(binding.id, current)
    if (current > selectedCurrentWeight || (current === selectedCurrentWeight && compareBindingOrder(binding, selected) < 0)) {
      selected = binding
      selectedCurrentWeight = current
    }
  }
  if (selected) {
    state.currentWeights.set(selected.id, (state.currentWeights.get(selected.id) ?? 0) - totalWeight)
  }
  weightedRouteStates.set(apiKeyId, state)
  trimApiKeyRouteStateMap(weightedRouteStates)
  const selectedIndex = bindings.findIndex((binding) => binding.id === selected?.id)
  const orderedByWeightDebt = [...bindings]
    .filter((binding) => binding.id !== selected?.id)
    .sort((left, right) => {
      const currentDelta = (state.currentWeights.get(right.id) ?? 0) - (state.currentWeights.get(left.id) ?? 0)
      if (currentDelta !== 0) return currentDelta
      const weightDelta = normalizeApiKeyGroupBindingWeight(right.weight) - normalizeApiKeyGroupBindingWeight(left.weight)
      if (weightDelta !== 0) return weightDelta
      return compareBindingOrder(left, right)
    })
  return selectedIndex >= 0 ? [bindings[selectedIndex], ...orderedByWeightDebt] : bindings
}

function trimApiKeyRouteStateMap<TValue>(map: Map<string, TValue>): void {
  while (map.size > apiKeyGroupRouteStateMaxEntries) {
    const oldestKey = map.keys().next().value
    if (typeof oldestKey !== 'string') {
      return
    }
    map.delete(oldestKey)
  }
}

function cleanupWeightedState(state: WeightedRouteState, bindings: GatewayApiKeyGroupBindingRow[]): void {
  const activeIds = new Set(bindings.map((binding) => binding.id))
  for (const id of state.currentWeights.keys()) {
    if (!activeIds.has(id)) {
      state.currentWeights.delete(id)
    }
  }
}

function rotateBindings(bindings: GatewayApiKeyGroupBindingRow[], startIndex: number): GatewayApiKeyGroupBindingRow[] {
  const normalizedStart = Math.max(0, Math.min(bindings.length - 1, startIndex))
  return [...bindings.slice(normalizedStart), ...bindings.slice(0, normalizedStart)]
}

function compareBindingOrder(left: GatewayApiKeyGroupBindingRow, right: GatewayApiKeyGroupBindingRow | undefined): number {
  if (!right) return -1
  if (left.priority !== right.priority) return left.priority - right.priority
  return left.group_id.localeCompare(right.group_id)
}
