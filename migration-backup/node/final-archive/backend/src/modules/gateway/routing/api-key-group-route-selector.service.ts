import { normalizeApiKeyGroupBindingWeight } from '../../../domain/api-key-routing.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { runRedisOperationWithDeadline } from '../../../shared/redis-client.js'
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
const redisRouteStateTtlMs = 30 * 24 * 60 * 60 * 1000
const redisRouteStateOperationTimeoutMs = 3_000

export function orderGatewayApiKeyGroupBindingsForDispatch(apiKey: GatewayApiKeyRow): GatewayApiKeyGroupBindingRow[] {
  const bindings = normalizeGatewayApiKeyGroupBindings(apiKey.group_bindings)
  if (bindings.length <= 1) {
    return bindings
  }
  assertSyncRouteStateAllowed(apiKey.route_strategy_mode)
  if (apiKey.route_strategy_mode === 'round_robin') {
    return rotateBindings(bindings, nextRoundRobinIndex(apiKeyRouteStateKey(apiKey), bindings.length))
  }
  if (apiKey.route_strategy_mode === 'weighted') {
    return orderWeightedBindings(apiKeyRouteStateKey(apiKey), bindings)
  }
  return bindings
}

export async function orderGatewayApiKeyGroupBindingsForDispatchAsync(apiKey: GatewayApiKeyRow): Promise<GatewayApiKeyGroupBindingRow[]> {
  const bindings = normalizeGatewayApiKeyGroupBindings(apiKey.group_bindings)
  if (bindings.length <= 1) {
    return bindings
  }
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return orderGatewayApiKeyGroupBindingsForDispatch(apiKey)
  }
  if (apiKey.route_strategy_mode === 'round_robin') {
    return rotateBindings(bindings, await nextRedisRouteCounterIndex(redisRouteStateKey(apiKey, 'round-robin'), bindings.length))
  }
  if (apiKey.route_strategy_mode === 'weighted') {
    return orderWeightedBindingsWithRedisCounter(apiKey, bindings)
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

async function orderWeightedBindingsWithRedisCounter(apiKey: GatewayApiKeyRow, bindings: GatewayApiKeyGroupBindingRow[]): Promise<GatewayApiKeyGroupBindingRow[]> {
  const totalWeight = bindings.reduce((sum, binding) => sum + normalizeApiKeyGroupBindingWeight(binding.weight), 0)
  const selectedWeightIndex = await nextRedisRouteCounterIndex(redisRouteStateKey(apiKey, 'weighted'), totalWeight)
  let selected = bindings[0]
  let cursor = 0
  for (const binding of bindings) {
    cursor += normalizeApiKeyGroupBindingWeight(binding.weight)
    if (selectedWeightIndex < cursor) {
      selected = binding
      break
    }
  }
  return [
    selected,
    ...bindings
      .filter((binding) => binding.id !== selected.id)
      .sort((left, right) => {
        const weightDelta = normalizeApiKeyGroupBindingWeight(right.weight) - normalizeApiKeyGroupBindingWeight(left.weight)
        if (weightDelta !== 0) return weightDelta
        return compareBindingOrder(left, right)
      })
  ]
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

async function nextRedisRouteCounterIndex(key: string, modulo: number): Promise<number> {
  if (modulo <= 0) return 0
  const runtimeStateUrl = runtimeConfig.redis.stateUrl
  if (!runtimeStateUrl) {
    throw new Error('高性能模式动态路由需要 JUHE_AI_REDIS_STATE_URL')
  }
  const result = await runRedisOperationWithDeadline(runtimeStateUrl, {
    operationName: 'Redis 动态路由计数器更新',
    timeoutMs: redisRouteStateOperationTimeoutMs
  }, (client) => client.eval(`
      local value = redis.call('INCR', KEYS[1])
      redis.call('PEXPIRE', KEYS[1], ARGV[2])
      return (value - 1) % tonumber(ARGV[1])
    `, {
      keys: [key],
      arguments: [String(Math.trunc(modulo)), String(redisRouteStateTtlMs)]
    }))
  const index = typeof result === 'number' ? result : Number(result)
  if (!Number.isFinite(index)) {
    throw new Error('Redis 动态路由计数器返回值无效')
  }
  return Math.max(0, Math.trunc(index))
}

function redisRouteStateKey(apiKey: GatewayApiKeyRow, mode: 'round-robin' | 'weighted'): string {
  return `juhe-ai:route-state:api-key-group:${mode}:${Buffer.from(apiKeyRouteStateKey(apiKey)).toString('base64url')}`
}

function assertSyncRouteStateAllowed(mode: GatewayApiKeyRow['route_strategy_mode']): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  if (mode !== 'round_robin' && mode !== 'weighted') return
  throw new Error('高性能模式动态路由禁止使用本机同步状态，请调用 orderGatewayApiKeyGroupBindingsForDispatchAsync')
}
