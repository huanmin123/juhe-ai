import { randomUUID } from 'node:crypto'

import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'
import type { RouteStrategySpeedFirstConfig } from '../../../domain/types.js'
import { gatewayAccountRuntimeKey, runtimeAccountIdFromKey, type SuppressibleGatewayAccount } from './account-runtime-keys.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'

export interface NormalRouteLatencyDegradationScope {
  systemAccountId: string
  routeStrategyId: string
  groupId: string
}

export interface NormalRouteLatencyDegradationOrderResult<T> {
  accounts: T[]
  applied: boolean
  degradedAccountIds: string[]
  bypassedAllDegraded: boolean
}

export interface NormalRouteLatencySlowResult {
  accountId: string
  slowCount: number
  degraded: boolean
  degradedUntil?: string
  recoverySuccessCount: number
  nextProbeAt?: string
}

export interface NormalRouteLatencySuccessResult {
  accountId: string
  cleared: boolean
  recoverySuccessCount: number
  requiredRecoverySuccessCount: number
}

export interface NormalRouteLatencyProbeCandidate {
  stateKey: string
  accountId: string
  accountName?: string
  runtimeKey: string
  scope: NormalRouteLatencyDegradationScope
  config: RouteStrategySpeedFirstConfig
  degradedUntil: string
  nextProbeAt: string
  recoverySuccessCount: number
}

interface NormalRouteLatencyState {
  accountId: string
  accountName?: string
  runtimeKey: string
  scope: NormalRouteLatencyDegradationScope
  config: RouteStrategySpeedFirstConfig
  firstSlowAtMs: number
  lastSlowAtMs: number
  slowCount: number
  degradedUntilMs?: number
  successCount: number
  nextProbeAtMs?: number
  reason: string
}

const latencyStateStore = createRuntimeStateStore('gateway-normal-route-latency-degradation')
const latencyStateVersion = 'v1'
const latencyStateAllIndexKey = `${latencyStateVersion}:all-index`
const latencyStateProbeIndexKey = `${latencyStateVersion}:probe-index`
const latencyStateAllIndexLockKey = `${latencyStateVersion}:all-index-lock`
const latencyStateProbeIndexLockKey = `${latencyStateVersion}:probe-index-lock`
const latencyStateIndexMaxKeys = 10_000
const latencyStateIndexTtlMs = 24 * 60 * 60 * 1000
const latencyStateIndexLockTtlMs = 2000
const latencyStateMutationLockTtlMs = 2000

export async function orderGatewayAccountsByNormalRouteLatencyDegradationAsync<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  scope: NormalRouteLatencyDegradationScope | undefined,
  config: RouteStrategySpeedFirstConfig | undefined,
  _modelPriority?: GatewayAccountModelPriority
): Promise<NormalRouteLatencyDegradationOrderResult<T>> {
  if (!scope || !config || accounts.length === 0) {
    return {
      accounts,
      applied: false,
      degradedAccountIds: [],
      bypassedAllDegraded: false
    }
  }

  const now = Date.now()
  const states = await Promise.all(accounts.map(async (account) => ({
    account,
    state: await loadLatencyState(accountLatencyStateKey(scope, account))
  })))
  const normalAccounts: T[] = []
  const degradedAccounts: T[] = []
  const degradedAccountIds: string[] = []
  for (const item of states) {
    if (item.state?.degradedUntilMs && item.state.degradedUntilMs > now) {
      degradedAccounts.push(item.account)
      degradedAccountIds.push(item.account.id)
    } else {
      normalAccounts.push(item.account)
    }
  }

  if (degradedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      degradedAccountIds: [],
      bypassedAllDegraded: false
    }
  }

  if (normalAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      degradedAccountIds,
      bypassedAllDegraded: true
    }
  }

  return {
    accounts: [...normalAccounts, ...degradedAccounts],
    applied: true,
    degradedAccountIds,
    bypassedAllDegraded: false
  }
}

export async function recordNormalRouteFirstByteSlowAsync(
  account: SuppressibleGatewayAccount,
  scope: NormalRouteLatencyDegradationScope | undefined,
  config: RouteStrategySpeedFirstConfig | undefined,
  reason = '普通路由速度优先首字等待超时'
): Promise<NormalRouteLatencySlowResult | undefined> {
  if (!scope || !config) return undefined
  const key = accountLatencyStateKey(scope, account)
  return withLatencyStateMutationLock(key, () => recordNormalRouteFirstByteSlowLockedAsync(account, scope, config, reason, key))
}

async function recordNormalRouteFirstByteSlowLockedAsync(
  account: SuppressibleGatewayAccount,
  scope: NormalRouteLatencyDegradationScope,
  config: RouteStrategySpeedFirstConfig,
  reason: string,
  key: string
): Promise<NormalRouteLatencySlowResult> {
  const now = Date.now()
  const current = await loadLatencyState(key)
  const slowWindowMs = Math.max(60, config.slowWindowSeconds) * 1000
  const withinWindow = current && now - current.firstSlowAtMs <= slowWindowMs
  const slowCount = withinWindow ? current.slowCount + 1 : 1
  const currentStillDegraded = Boolean(current?.degradedUntilMs && current.degradedUntilMs > now)
  const triggeredDegraded = slowCount >= config.slowTriggerCount
  const degraded = triggeredDegraded || currentStillDegraded
  const degradedUntilMs = triggeredDegraded
    ? Math.max(current?.degradedUntilMs ?? 0, now + Math.max(60, config.degradedTtlSeconds) * 1000)
    : current?.degradedUntilMs
  const nextProbeAtMs = triggeredDegraded
    ? now + nextProbeDelayMs(config, key)
    : current?.nextProbeAtMs
  const runtimeKey = gatewayAccountRuntimeKey(account)
  const state: NormalRouteLatencyState = {
    accountId: account.id,
    accountName: optionalAccountName(account),
    runtimeKey,
    scope,
    config,
    firstSlowAtMs: withinWindow ? current.firstSlowAtMs : now,
    lastSlowAtMs: now,
    slowCount,
    degradedUntilMs,
    successCount: 0,
    nextProbeAtMs,
    reason
  }
  await latencyStateStore.setJson(key, state, latencyStateTtlMs(config, degraded))
  await addLatencyStateAllIndexKey(key)
  if (degraded) {
    await addLatencyStateProbeIndexKey(key)
  }
  return {
    accountId: account.id,
    slowCount,
    degraded,
    degradedUntil: degradedUntilMs ? new Date(degradedUntilMs).toISOString() : undefined,
    recoverySuccessCount: 0,
    nextProbeAt: nextProbeAtMs ? new Date(nextProbeAtMs).toISOString() : undefined
  }
}

export async function recordNormalRouteFirstByteSuccessAsync(
  account: SuppressibleGatewayAccount,
  scope: NormalRouteLatencyDegradationScope | undefined,
  config: RouteStrategySpeedFirstConfig | undefined,
  firstByteMs: number | undefined
): Promise<NormalRouteLatencySuccessResult | undefined> {
  if (!scope || !config || firstByteMs === undefined || firstByteMs > config.firstByteThresholdMs) return undefined
  const key = accountLatencyStateKey(scope, account)
  return withLatencyStateMutationLock(key, async () => recordNormalRouteFirstByteSuccessLockedAsync(account, config, key))
}

async function recordNormalRouteFirstByteSuccessLockedAsync(
  account: SuppressibleGatewayAccount,
  config: RouteStrategySpeedFirstConfig,
  key: string
): Promise<NormalRouteLatencySuccessResult | undefined> {
  const current = await loadLatencyState(key)
  if (!current) return undefined
  const now = Date.now()
  if (!current.degradedUntilMs || current.degradedUntilMs <= now) {
    await latencyStateStore.delete(key)
    await removeLatencyStateIndexKeys([key])
    return {
      accountId: account.id,
      cleared: true,
      recoverySuccessCount: 0,
      requiredRecoverySuccessCount: config.recoverySuccessCount
    }
  }
  const successCount = current.successCount + 1
  if (successCount >= config.recoverySuccessCount) {
    await latencyStateStore.delete(key)
    await removeLatencyStateIndexKeys([key])
    return {
      accountId: account.id,
      cleared: true,
      recoverySuccessCount: successCount,
      requiredRecoverySuccessCount: config.recoverySuccessCount
    }
  }
  await latencyStateStore.setJson(key, {
    ...current,
    successCount,
    nextProbeAtMs: now + nextProbeDelayMs(config, key)
  }, latencyStateTtlMs(config, true))
  return {
    accountId: account.id,
    cleared: false,
    recoverySuccessCount: successCount,
    requiredRecoverySuccessCount: config.recoverySuccessCount
  }
}

export async function isNormalRouteAccountLatencyDegradedAsync(
  account: SuppressibleGatewayAccount,
  scope: NormalRouteLatencyDegradationScope | undefined
): Promise<boolean> {
  if (!scope) return false
  const current = await loadLatencyState(accountLatencyStateKey(scope, account))
  return Boolean(current?.degradedUntilMs && current.degradedUntilMs > Date.now())
}

export async function listNormalRouteLatencyProbeCandidatesAsync(
  limit = 20,
  now = Date.now()
): Promise<NormalRouteLatencyProbeCandidate[]> {
  const normalizedLimit = normalizePositiveInteger(limit, 20, 1, 100)
  const keys = await loadLatencyStateIndexKeys(latencyStateProbeIndexKey)
  if (keys.length === 0) return []

  const staleKeys: string[] = []
  const candidates: Array<NormalRouteLatencyProbeCandidate & { nextProbeAtMs: number }> = []
  for (const key of keys) {
    const state = await loadLatencyState(key)
    if (!state) {
      staleKeys.push(key)
      continue
    }
    if (!state.degradedUntilMs) {
      continue
    }
    if (state.degradedUntilMs <= now) {
      staleKeys.push(key)
      continue
    }
    if (!state.nextProbeAtMs || state.nextProbeAtMs > now) {
      continue
    }
    const candidate = probeCandidateFromState(key, state)
    if (!candidate) {
      staleKeys.push(key)
      continue
    }
    candidates.push({ ...candidate, nextProbeAtMs: state.nextProbeAtMs })
  }
  if (staleKeys.length > 0) {
    await removeLatencyStateIndexKeys(staleKeys)
  }
  return candidates
    .sort((left, right) => left.nextProbeAtMs - right.nextProbeAtMs || left.accountId.localeCompare(right.accountId))
    .slice(0, normalizedLimit)
    .map(({ nextProbeAtMs: _nextProbeAtMs, ...candidate }) => candidate)
}

export async function recordNormalRouteProbeFailureAsync(
  candidate: NormalRouteLatencyProbeCandidate,
  reason = '普通路由速度优先恢复探针未达标'
): Promise<NormalRouteLatencySlowResult | undefined> {
  return withLatencyStateMutationLock(candidate.stateKey, () => recordNormalRouteProbeFailureLockedAsync(candidate, reason))
}

async function recordNormalRouteProbeFailureLockedAsync(
  candidate: NormalRouteLatencyProbeCandidate,
  reason: string
): Promise<NormalRouteLatencySlowResult | undefined> {
  const current = await loadLatencyState(candidate.stateKey)
  const now = Date.now()
  if (!current || !current.degradedUntilMs || current.degradedUntilMs <= now) {
    await latencyStateStore.delete(candidate.stateKey)
    await removeLatencyStateIndexKeys([candidate.stateKey])
    return undefined
  }
  const config = current.config ?? candidate.config
  const degradedUntilMs = Math.max(current.degradedUntilMs, now + Math.max(60, config.degradedTtlSeconds) * 1000)
  const nextProbeAtMs = now + nextProbeDelayMs(config, candidate.stateKey)
  const slowCount = Math.max(current.slowCount, config.slowTriggerCount)
  await latencyStateStore.setJson(candidate.stateKey, {
    ...current,
    config,
    lastSlowAtMs: now,
    slowCount,
    degradedUntilMs,
    successCount: 0,
    nextProbeAtMs,
    reason
  }, latencyStateTtlMs(config, true))
  await addLatencyStateAllIndexKey(candidate.stateKey)
  await addLatencyStateProbeIndexKey(candidate.stateKey)
  return {
    accountId: current.accountId,
    slowCount,
    degraded: true,
    degradedUntil: new Date(degradedUntilMs).toISOString(),
    recoverySuccessCount: 0,
    nextProbeAt: new Date(nextProbeAtMs).toISOString()
  }
}

export async function discardNormalRouteLatencyProbeCandidateAsync(candidate: NormalRouteLatencyProbeCandidate): Promise<void> {
  await latencyStateStore.delete(candidate.stateKey)
  await removeLatencyStateIndexKeys([candidate.stateKey])
}

export async function clearNormalRouteLatencyDegradationForRouteStrategyAsync(routeStrategyId: string): Promise<number> {
  const normalizedRouteStrategyId = routeStrategyId.trim()
  if (!normalizedRouteStrategyId) return 0
  const keys = await loadLatencyStateIndexKeys(latencyStateAllIndexKey)
  if (keys.length === 0) return 0
  const keysToClear: string[] = []
  for (const key of keys) {
    const state = await loadLatencyState(key)
    if (!state || state.scope.routeStrategyId === normalizedRouteStrategyId) {
      keysToClear.push(key)
    }
  }
  await Promise.all(keysToClear.map((key) => latencyStateStore.delete(key)))
  await removeLatencyStateIndexKeys(keysToClear)
  return keysToClear.length
}

export async function clearNormalRouteLatencyDegradationForAccountBindingAsync(input: {
  systemAccountId: string
  accountId: string
  groupIds: Array<string | null | undefined>
}): Promise<number> {
  const systemAccountId = input.systemAccountId.trim()
  const accountId = input.accountId.trim()
  const groupIds = new Set(input.groupIds.map((groupId) => groupId?.trim()).filter((groupId): groupId is string => Boolean(groupId)))
  if (!systemAccountId || !accountId || groupIds.size === 0) return 0
  const keys = await loadLatencyStateIndexKeys(latencyStateAllIndexKey)
  if (keys.length === 0) return 0
  const keysToClear: string[] = []
  for (const key of keys) {
    const state = await loadLatencyState(key)
    if (!state) {
      keysToClear.push(key)
      continue
    }
    if (
      state.scope.systemAccountId === systemAccountId
      && groupIds.has(state.scope.groupId)
      && (state.accountId === accountId || runtimeAccountIdFromKey(state.runtimeKey) === accountId)
    ) {
      keysToClear.push(key)
    }
  }
  await Promise.all(keysToClear.map((key) => latencyStateStore.delete(key)))
  await removeLatencyStateIndexKeys(keysToClear)
  return keysToClear.length
}

export function normalRouteLatencyDegradationScope(input: {
  systemAccountId: string
  routeStrategyId?: string
  groupId: string
}): NormalRouteLatencyDegradationScope | undefined {
  const systemAccountId = input.systemAccountId.trim()
  const routeStrategyId = input.routeStrategyId?.trim()
  const groupId = input.groupId.trim()
  if (!systemAccountId || !routeStrategyId || !groupId) return undefined
  return { systemAccountId, routeStrategyId, groupId }
}

async function loadLatencyState(key: string): Promise<NormalRouteLatencyState | undefined> {
  const state = await latencyStateStore.getJson<NormalRouteLatencyState>(key)
  if (!state || typeof state !== 'object') return undefined
  if (typeof state.accountId !== 'string') return undefined
  if (typeof state.runtimeKey !== 'string') return undefined
  if (!isNormalRouteLatencyDegradationScope(state.scope)) return undefined
  if (!isRouteStrategySpeedFirstConfig(state.config)) return undefined
  if (!Number.isFinite(state.firstSlowAtMs) || !Number.isFinite(state.lastSlowAtMs)) return undefined
  if (!Number.isFinite(state.slowCount) || !Number.isFinite(state.successCount)) return undefined
  if (state.degradedUntilMs !== undefined && !Number.isFinite(state.degradedUntilMs)) return undefined
  if (state.nextProbeAtMs !== undefined && !Number.isFinite(state.nextProbeAtMs)) return undefined
  return state
}

function accountLatencyStateKey(scope: NormalRouteLatencyDegradationScope, account: SuppressibleGatewayAccount): string {
  return [
    latencyStateVersion,
    sanitizeKeyPart(scope.systemAccountId),
    sanitizeKeyPart(scope.routeStrategyId),
    sanitizeKeyPart(scope.groupId),
    sanitizeKeyPart(gatewayAccountRuntimeKey(account))
  ].join(':')
}

function probeCandidateFromState(key: string, state: NormalRouteLatencyState): NormalRouteLatencyProbeCandidate | undefined {
  if (!state.degradedUntilMs || !state.nextProbeAtMs) return undefined
  return {
    stateKey: key,
    accountId: state.accountId,
    accountName: state.accountName,
    runtimeKey: state.runtimeKey,
    scope: state.scope,
    config: state.config,
    degradedUntil: new Date(state.degradedUntilMs).toISOString(),
    nextProbeAt: new Date(state.nextProbeAtMs).toISOString(),
    recoverySuccessCount: state.successCount
  }
}

function latencyStateTtlMs(config: RouteStrategySpeedFirstConfig, degraded: boolean): number {
  const seconds = degraded ? config.degradedTtlSeconds : config.slowWindowSeconds
  return Math.max(1, seconds) * 1000
}

function nextProbeDelayMs(config: RouteStrategySpeedFirstConfig, key: string): number {
  const baseMs = Math.max(10, config.probeIntervalSeconds) * 1000
  const jitterRatio = stableProbeJitterRatio(key)
  return Math.max(1000, Math.trunc(baseMs + baseMs * jitterRatio))
}

function stableProbeJitterRatio(key: string): number {
  let hash = 0
  for (let index = 0; index < key.length; index += 1) {
    hash = (hash * 31 + key.charCodeAt(index)) >>> 0
  }
  return ((hash % 21) - 10) / 100
}

async function loadLatencyStateIndexKeys(indexKey: string): Promise<string[]> {
  const index = await latencyStateStore.getJson<{ keys?: unknown }>(indexKey)
  return normalizeLatencyStateIndexKeys(index?.keys)
}

async function addLatencyStateAllIndexKey(key: string): Promise<void> {
  await addLatencyStateIndexKey(latencyStateAllIndexKey, latencyStateAllIndexLockKey, key)
}

async function addLatencyStateProbeIndexKey(key: string): Promise<void> {
  await addLatencyStateIndexKey(latencyStateProbeIndexKey, latencyStateProbeIndexLockKey, key)
}

async function addLatencyStateIndexKey(indexKey: string, lockKey: string, key: string): Promise<void> {
  await mutateLatencyStateIndexKeys(indexKey, lockKey, (keys) => {
    if (keys.includes(key)) return keys
    return [...keys, key].slice(-latencyStateIndexMaxKeys)
  })
}

async function removeLatencyStateIndexKeys(keysToRemove: string[]): Promise<void> {
  if (keysToRemove.length === 0) return
  const removeSet = new Set(keysToRemove)
  await Promise.all([
    mutateLatencyStateIndexKeys(latencyStateAllIndexKey, latencyStateAllIndexLockKey, (keys) => keys.filter((key) => !removeSet.has(key))),
    mutateLatencyStateIndexKeys(latencyStateProbeIndexKey, latencyStateProbeIndexLockKey, (keys) => keys.filter((key) => !removeSet.has(key)))
  ])
}

async function mutateLatencyStateIndexKeys(indexKey: string, lockKey: string, mutator: (keys: string[]) => string[]): Promise<void> {
  const token = randomUUID()
  const locked = await acquireLatencyStateIndexLock(lockKey, token)
  if (!locked) {
    return
  }
  try {
    const current = await loadLatencyStateIndexKeys(indexKey)
    const next = normalizeLatencyStateIndexKeys(mutator(current))
    await latencyStateStore.setJson(indexKey, { keys: next }, latencyStateIndexTtlMs)
  } finally {
    await latencyStateStore.releaseLock(lockKey, token)
  }
}

async function withLatencyStateMutationLock<T>(key: string, operation: () => Promise<T>): Promise<T> {
  const lockKey = `${latencyStateVersion}:mutation-lock:${key}`
  const token = randomUUID()
  const locked = await acquireLatencyStateLock(lockKey, token)
  if (!locked) {
    return undefined as T
  }
  try {
    return await operation()
  } finally {
    await latencyStateStore.releaseLock(lockKey, token)
  }
}

async function acquireLatencyStateIndexLock(lockKey: string, token: string): Promise<boolean> {
  return acquireLatencyStateLock(lockKey, token, latencyStateIndexLockTtlMs)
}

async function acquireLatencyStateLock(lockKey: string, token: string, ttlMs = latencyStateMutationLockTtlMs): Promise<boolean> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (await latencyStateStore.acquireLock(lockKey, { ttlMs, token })) {
      return true
    }
    await delay(Math.min(100, 20 + attempt * 5))
  }
  return false
}

function normalizeLatencyStateIndexKeys(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const keys: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') continue
    const key = item.trim()
    if (!key || seen.has(key)) continue
    seen.add(key)
    keys.push(key)
  }
  return keys.slice(-latencyStateIndexMaxKeys)
}

function sanitizeKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || '_'
}

function optionalAccountName(account: SuppressibleGatewayAccount): string | undefined {
  const value = (account as { name?: unknown }).name
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isNormalRouteLatencyDegradationScope(value: unknown): value is NormalRouteLatencyDegradationScope {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return typeof record.systemAccountId === 'string' && record.systemAccountId.trim() !== ''
    && typeof record.routeStrategyId === 'string' && record.routeStrategyId.trim() !== ''
    && typeof record.groupId === 'string' && record.groupId.trim() !== ''
}

function isRouteStrategySpeedFirstConfig(value: unknown): value is RouteStrategySpeedFirstConfig {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return isFinitePositiveInteger(record.firstByteThresholdMs)
    && isFinitePositiveInteger(record.slowTriggerCount)
    && isFinitePositiveInteger(record.slowWindowSeconds)
    && isFinitePositiveInteger(record.recoverySuccessCount)
    && isFinitePositiveInteger(record.probeIntervalSeconds)
    && isFinitePositiveInteger(record.degradedTtlSeconds)
    && isFinitePositiveInteger(record.maxFirstByteRetriesPerRequest)
}

function isFinitePositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value > 0
}

function normalizePositiveInteger(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isInteger(numeric)) return fallback
  return Math.max(min, Math.min(max, numeric))
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
