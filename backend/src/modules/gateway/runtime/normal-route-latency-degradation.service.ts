import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../../shared/concurrency-governor.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../shared/rfc3339.js'
import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'
import type { RouteStrategySpeedFirstConfig } from '../../../domain/types.js'
import { gatewayAccountRuntimeKey, runtimeAccountIdFromKey, type SuppressibleGatewayAccount } from './account-runtime-keys.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'

export interface NormalRouteLatencyDegradationScope {
  systemAccountId: string
  routeStrategyId: string
  groupId: string
}

export interface NormalRouteSpeedFirstRuntimeConfig extends RouteStrategySpeedFirstConfig {
  firstByteDeadlineMs: number
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
  generation: string
  accountId: string
  accountName?: string
  runtimeKey: string
  scope: NormalRouteLatencyDegradationScope
  config: NormalRouteSpeedFirstRuntimeConfig
  degradedUntil: string
  nextProbeAt: string
  recoverySuccessCount: number
}

interface NormalRouteLatencyState {
  generation: string
  accountId: string
  accountName?: string
  runtimeKey: string
  scope: NormalRouteLatencyDegradationScope
  config: NormalRouteSpeedFirstRuntimeConfig
  firstSlowAtMs: number
  lastSlowAtMs: number
  slowCount: number
  degradedUntilMs?: number
  successCount: number
  nextProbeAtMs?: number
  reason: string
}

interface NormalRouteLatencyStateLock {
  key: string
  lockKey: string
  token: string
}

type NormalRouteLatencyStatePredicate = (state: NormalRouteLatencyState) => boolean

export interface NormalRouteLatencyGenerationEvent {
  version: string
  publishedAt: string
}

const latencyStateStore = createRuntimeStateStore('gateway-normal-route-latency-degradation')
const latencyStateVersion = 'v1'
const latencyStateGenerationKey = `${latencyStateVersion}:generation`
const latencyStateAllIndexKey = `${latencyStateVersion}:all-index`
const latencyStateProbeIndexKey = `${latencyStateVersion}:probe-index`
const latencyStateAllIndexLockKey = `${latencyStateVersion}:all-index-lock`
const latencyStateProbeIndexLockKey = `${latencyStateVersion}:probe-index-lock`
const latencyStateIndexMaxKeys = 10_000
const latencyStateIndexTtlMs = 24 * 60 * 60 * 1000
const latencyStateGenerationTtlMs = 48 * 60 * 60 * 1000
const latencyStateExactClearConcurrency = runtimeConfig.concurrency.globalMax
const latencyStateLockAcquireMaxAttempts = 50
const latencyStateLockAcquireMaxDelayMs = 100
const latencyStateMutationLockTtlMs =
  2 * latencyStateLockAcquireMaxAttempts * latencyStateLockAcquireMaxDelayMs
  + 5000
const latencyStateIndexLockTtlMs = latencyStateMutationLockTtlMs
const latencyStateGenerationCasMaxAttempts = 8
const latencyStateIndexCasMaxAttempts = 8
const latencyStateInitialGenerationEvent: NormalRouteLatencyGenerationEvent = {
  version: 'initial',
  publishedAt: '1970-01-01T00:00:00.000Z'
}

export async function orderGatewayAccountsByNormalRouteLatencyDegradationAsync<T extends SuppressibleGatewayAccount>(
  accounts: T[],
  scope: NormalRouteLatencyDegradationScope | undefined,
  config: NormalRouteSpeedFirstRuntimeConfig | undefined,
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

  const generation = await loadLatencyStateGeneration()
  const now = Date.now()
  const states = await Promise.all(accounts.map(async (account) => ({
    account,
    state: await loadLatencyState(accountLatencyStateKey(scope, account), generation)
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
  config: NormalRouteSpeedFirstRuntimeConfig | undefined,
  reason = '普通路由速度优先首字等待超时'
): Promise<NormalRouteLatencySlowResult | undefined> {
  if (!scope || !config) return undefined
  const key = accountLatencyStateKey(scope, account)
  const generation = await loadLatencyStateGeneration()
  return withLatencyStateMutationLock(key, generation, () =>
    recordNormalRouteFirstByteSlowLockedAsync(account, scope, config, reason, key, generation)
  )
}

async function recordNormalRouteFirstByteSlowLockedAsync(
  account: SuppressibleGatewayAccount,
  scope: NormalRouteLatencyDegradationScope,
  config: NormalRouteSpeedFirstRuntimeConfig,
  reason: string,
  key: string,
  generation: string
): Promise<NormalRouteLatencySlowResult> {
  const now = Date.now()
  const current = await loadLatencyState(key, generation)
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
    generation,
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
  await writeLatencyStateAndIndexesStrictAsync(
    key,
    current,
    state,
    latencyStateTtlMs(config, degraded),
    degraded
  )
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
  config: NormalRouteSpeedFirstRuntimeConfig | undefined,
  firstByteMs: number | undefined
): Promise<NormalRouteLatencySuccessResult | undefined> {
  if (!scope || !config || firstByteMs === undefined || firstByteMs > config.firstByteDeadlineMs) return undefined
  const key = accountLatencyStateKey(scope, account)
  const generation = await loadLatencyStateGeneration()
  return withLatencyStateMutationLock(key, generation, () =>
    recordNormalRouteFirstByteSuccessLockedAsync(account, config, key, generation)
  )
}

async function recordNormalRouteFirstByteSuccessLockedAsync(
  account: SuppressibleGatewayAccount,
  config: NormalRouteSpeedFirstRuntimeConfig,
  key: string,
  generation: string
): Promise<NormalRouteLatencySuccessResult | undefined> {
  const current = await loadLatencyState(key, generation)
  if (!current) return undefined
  const now = Date.now()
  if (!current.degradedUntilMs || current.degradedUntilMs <= now) {
    await deleteLatencyStateAndIndexesStrictAsync(key)
    return {
      accountId: account.id,
      cleared: true,
      recoverySuccessCount: 0,
      requiredRecoverySuccessCount: config.recoverySuccessCount
    }
  }
  const successCount = current.successCount + 1
  if (successCount >= config.recoverySuccessCount) {
    await deleteLatencyStateAndIndexesStrictAsync(key)
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
  const generation = await loadLatencyStateGeneration()
  const current = await loadLatencyState(accountLatencyStateKey(scope, account), generation)
  return Boolean(current?.degradedUntilMs && current.degradedUntilMs > Date.now())
}

export async function listNormalRouteLatencyProbeCandidatesAsync(
  limit = 20,
  now = Date.now()
): Promise<NormalRouteLatencyProbeCandidate[]> {
  const normalizedLimit = normalizePositiveInteger(limit, 20, 1, 100)
  const keys = await loadLatencyStateIndexKeys(latencyStateProbeIndexKey)
  if (keys.length === 0) return []

  const generation = await loadLatencyStateGeneration()
  const candidates: Array<NormalRouteLatencyProbeCandidate & { nextProbeAtMs: number }> = []
  for (const key of keys) {
    const state = await loadLatencyState(key, generation)
    if (!state?.degradedUntilMs || state.degradedUntilMs <= now) {
      continue
    }
    if (!state.nextProbeAtMs || state.nextProbeAtMs > now) {
      continue
    }
    const candidate = probeCandidateFromState(key, state)
    if (candidate) {
      candidates.push({ ...candidate, nextProbeAtMs: state.nextProbeAtMs })
    }
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
  return withLatencyStateMutationLock(candidate.stateKey, candidate.generation, () =>
    recordNormalRouteProbeFailureLockedAsync(candidate, reason)
  )
}

export async function deferNormalRouteLatencyProbeCandidateAsync(
  candidate: NormalRouteLatencyProbeCandidate
): Promise<boolean> {
  return (await withLatencyStateMutationLock(candidate.stateKey, candidate.generation, async () => {
    const current = await loadLatencyState(candidate.stateKey, candidate.generation)
    const now = Date.now()
    if (!current) return false
    if (!current.degradedUntilMs || current.degradedUntilMs <= now) {
      await deleteLatencyStateAndIndexesStrictAsync(candidate.stateKey)
      return false
    }
    const config = current.config ?? candidate.config
    const state: NormalRouteLatencyState = {
      ...current,
      config,
      nextProbeAtMs: now + nextProbeDelayMs(config, candidate.stateKey)
    }
    await writeLatencyStateAndIndexesStrictAsync(
      candidate.stateKey,
      current,
      state,
      latencyStateTtlMs(config, true),
      true
    )
    return true
  })) ?? false
}

async function recordNormalRouteProbeFailureLockedAsync(
  candidate: NormalRouteLatencyProbeCandidate,
  reason: string
): Promise<NormalRouteLatencySlowResult | undefined> {
  const current = await loadLatencyState(candidate.stateKey, candidate.generation)
  const now = Date.now()
  if (!current) return undefined
  if (!current.degradedUntilMs || current.degradedUntilMs <= now) {
    await deleteLatencyStateAndIndexesStrictAsync(candidate.stateKey)
    return undefined
  }
  const config = current.config ?? candidate.config
  const degradedUntilMs = Math.max(current.degradedUntilMs, now + Math.max(60, config.degradedTtlSeconds) * 1000)
  const nextProbeAtMs = now + nextProbeDelayMs(config, candidate.stateKey)
  const slowCount = Math.max(current.slowCount, config.slowTriggerCount)
  const state: NormalRouteLatencyState = {
    ...current,
    config,
    lastSlowAtMs: now,
    slowCount,
    degradedUntilMs,
    successCount: 0,
    nextProbeAtMs,
    reason
  }
  await writeLatencyStateAndIndexesStrictAsync(
    candidate.stateKey,
    current,
    state,
    latencyStateTtlMs(config, true),
    true
  )
  return {
    accountId: current.accountId,
    slowCount,
    degraded: true,
    degradedUntil: new Date(degradedUntilMs).toISOString(),
    recoverySuccessCount: 0,
    nextProbeAt: new Date(nextProbeAtMs).toISOString()
  }
}

export async function discardNormalRouteLatencyProbeCandidateAsync(
  candidate: NormalRouteLatencyProbeCandidate
): Promise<void> {
  await withLatencyStateMutationLock(candidate.stateKey, candidate.generation, async () => {
    const current = await loadLatencyState(candidate.stateKey, candidate.generation)
    if (!current) return
    await deleteLatencyStateAndIndexesStrictAsync(candidate.stateKey)
  })
}

export async function clearNormalRouteLatencyDegradationForRouteStrategyAsync(
  routeStrategyId: string
): Promise<number> {
  const normalizedRouteStrategyId = routeStrategyId.trim()
  if (!normalizedRouteStrategyId) return 0
  const keys = await loadLatencyStateIndexKeys(latencyStateAllIndexKey)
  if (keys.length === 0) return 0
  const generation = await loadLatencyStateGeneration()
  return clearCurrentGenerationLatencyStateKeysAsync(
    keys,
    generation,
    (state) => state.scope.routeStrategyId === normalizedRouteStrategyId
  )
}

export async function clearAllNormalRouteLatencyDegradationAsync(
  event: NormalRouteLatencyGenerationEvent
): Promise<void | false> {
  const normalizedEvent = normalizeLatencyGenerationEvent(event)
  for (let attempt = 0; attempt < latencyStateGenerationCasMaxAttempts; attempt += 1) {
    const current = await loadLatencyGenerationEvent()
    if (current && compareLatencyGenerationEvents(normalizedEvent, current) <= 0) {
      if (await latencyStateStore.compareSetJson(
        latencyStateGenerationKey,
        current,
        current,
        latencyStateGenerationTtlMs
      )) {
        return
      }
      continue
    }
    if (!await latencyStateStore.compareSetJson(
      latencyStateGenerationKey,
      current,
      normalizedEvent,
      latencyStateGenerationTtlMs
    )) {
      continue
    }
    return
  }
  return false
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
  const generation = await loadLatencyStateGeneration()
  return clearCurrentGenerationLatencyStateKeysAsync(
    keys,
    generation,
    (state) =>
      state.scope.systemAccountId === systemAccountId
      && groupIds.has(state.scope.groupId)
      && (state.accountId === accountId || runtimeAccountIdFromKey(state.runtimeKey) === accountId)
  )
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

async function loadLatencyStateGeneration(): Promise<string> {
  return latencyGenerationToken(await loadOrCreateLatencyGenerationEvent())
}

async function loadLatencyGenerationEvent(): Promise<NormalRouteLatencyGenerationEvent | undefined> {
  for (let attempt = 0; attempt < latencyStateGenerationCasMaxAttempts; attempt += 1) {
    const event = await latencyStateStore.getJson<unknown>(latencyStateGenerationKey)
    if (event === undefined) return undefined
    if (!isNormalRouteLatencyGenerationEvent(event)) {
      throw new Error('普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
    }
    const normalizedEvent = normalizeLatencyGenerationEvent(event)
    if (JSON.stringify(event) === JSON.stringify(normalizedEvent)) {
      return normalizedEvent
    }
    if (await latencyStateStore.compareSetJson(
      latencyStateGenerationKey,
      event,
      normalizedEvent,
      latencyStateGenerationTtlMs
    )) {
      return normalizedEvent
    }
  }
  throw new Error(
    `普通路由速度优先 generation marker canonical CAS 重试耗尽（${latencyStateGenerationCasMaxAttempts} 次）`
  )
}

async function loadOrCreateLatencyGenerationEvent(): Promise<NormalRouteLatencyGenerationEvent> {
  for (let attempt = 0; attempt < latencyStateGenerationCasMaxAttempts; attempt += 1) {
    const current = await loadLatencyGenerationEvent()
    if (current) return current
    if (await latencyStateStore.compareSetJson(
      latencyStateGenerationKey,
      undefined,
      latencyStateInitialGenerationEvent,
      latencyStateGenerationTtlMs
    )) {
      return latencyStateInitialGenerationEvent
    }
  }
  throw new Error(
    `普通路由速度优先 generation marker CAS 初始化重试耗尽（${latencyStateGenerationCasMaxAttempts} 次）`
  )
}

async function renewLatencyStateGenerationAsync(generation: string): Promise<boolean> {
  const current = await loadLatencyGenerationEvent()
  if (!current || latencyGenerationToken(current) !== generation) {
    return false
  }
  return latencyStateStore.compareSetJson(
    latencyStateGenerationKey,
    current,
    current,
    latencyStateGenerationTtlMs
  )
}

function normalizeLatencyGenerationEvent(
  event: NormalRouteLatencyGenerationEvent
): NormalRouteLatencyGenerationEvent {
  const version = event.version.trim()
  if (!version) {
    throw new Error('普通路由速度优先 generation event 缺少 version')
  }
  const publishedAt = requiredRfc3339Instant(
    event.publishedAt,
    '普通路由速度优先 generation event publishedAt'
  )
  return { version, publishedAt }
}

function compareLatencyGenerationEvents(
  left: NormalRouteLatencyGenerationEvent,
  right: NormalRouteLatencyGenerationEvent
): number {
  const leftPublishedAtMs = rfc3339InstantMilliseconds(left.publishedAt)
  const rightPublishedAtMs = rfc3339InstantMilliseconds(right.publishedAt)
  if (leftPublishedAtMs === undefined || rightPublishedAtMs === undefined) {
    throw new Error('普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  const publishedAtDifference = leftPublishedAtMs - rightPublishedAtMs
  if (publishedAtDifference !== 0) {
    return publishedAtDifference
  }
  if (left.version === right.version) return 0
  return left.version > right.version ? 1 : -1
}

function latencyGenerationToken(
  event: NormalRouteLatencyGenerationEvent | undefined
): string {
  if (!event) return 'initial'
  const publishedAtMs = rfc3339InstantMilliseconds(event.publishedAt)
  if (publishedAtMs === undefined) {
    throw new Error('普通路由速度优先 generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return JSON.stringify([publishedAtMs, event.version])
}

async function loadLatencyState(
  key: string,
  generation: string
): Promise<NormalRouteLatencyState | undefined> {
  const state = await loadLatencyStateRaw(key)
  return state?.generation === generation ? state : undefined
}

async function loadLatencyStateRaw(key: string): Promise<NormalRouteLatencyState | undefined> {
  const state = await latencyStateStore.getJson<NormalRouteLatencyState>(key)
  if (!state || typeof state !== 'object') return undefined
  if (typeof state.generation !== 'string' || !state.generation) return undefined
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

function probeCandidateFromState(
  key: string,
  state: NormalRouteLatencyState
): NormalRouteLatencyProbeCandidate | undefined {
  if (!state.degradedUntilMs || !state.nextProbeAtMs) return undefined
  return {
    stateKey: key,
    generation: state.generation,
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

function latencyStateTtlMs(config: NormalRouteSpeedFirstRuntimeConfig, degraded: boolean): number {
  const seconds = degraded ? config.degradedTtlSeconds : config.slowWindowSeconds
  return Math.max(1, seconds) * 1000
}

function nextProbeDelayMs(config: NormalRouteSpeedFirstRuntimeConfig, key: string): number {
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
  return (await loadLatencyStateIndexSnapshot(indexKey)).keys
}

async function loadLatencyStateIndexSnapshot(
  indexKey: string
): Promise<{ value: unknown; keys: string[] }> {
  const value = await latencyStateStore.getJson<unknown>(indexKey)
  const index = value && typeof value === 'object' && !Array.isArray(value)
    ? value as { keys?: unknown }
    : undefined
  return {
    value,
    keys: normalizeLatencyStateIndexKeys(index?.keys)
  }
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

async function removeLatencyStateIndexKeysStrict(keysToRemove: string[]): Promise<void> {
  if (keysToRemove.length === 0) return
  const removeSet = new Set(keysToRemove)
  await mutateLatencyStateIndexKeys(
    latencyStateProbeIndexKey,
    latencyStateProbeIndexLockKey,
    (keys) => keys.filter((key) => !removeSet.has(key))
  )
  await mutateLatencyStateIndexKeys(
    latencyStateAllIndexKey,
    latencyStateAllIndexLockKey,
    (keys) => keys.filter((key) => !removeSet.has(key))
  )
}

async function mutateLatencyStateIndexKeys(
  indexKey: string,
  lockKey: string,
  mutator: (keys: string[]) => string[]
): Promise<void> {
  const token = randomUUID()
  const locked = await acquireLatencyStateIndexLock(lockKey, token)
  if (!locked) {
    throw new Error(`普通路由速度优先索引锁获取失败：${indexKey}`)
  }
  try {
    for (let attempt = 0; attempt < latencyStateIndexCasMaxAttempts; attempt += 1) {
      const current = await loadLatencyStateIndexSnapshot(indexKey)
      const next = {
        keys: normalizeLatencyStateIndexKeys(mutator(current.keys))
      }
      if (await latencyStateStore.compareSetJson(
        indexKey,
        current.value,
        next,
        latencyStateIndexTtlMs
      )) {
        return
      }
    }
    throw new Error(
      `普通路由速度优先索引 CAS 重试耗尽（${latencyStateIndexCasMaxAttempts} 次）：${indexKey}`
    )
  } finally {
    await latencyStateStore.releaseLock(lockKey, token)
  }
}

async function writeLatencyStateAndIndexesStrictAsync(
  key: string,
  previous: NormalRouteLatencyState | undefined,
  state: NormalRouteLatencyState,
  ttlMs: number,
  addProbeIndex: boolean
): Promise<void> {
  await latencyStateStore.setJson(key, state, ttlMs)
  let allIndexApplied = false
  let probeIndexApplied = false
  try {
    await addLatencyStateAllIndexKey(key)
    allIndexApplied = true
    if (addProbeIndex) {
      await addLatencyStateProbeIndexKey(key)
      probeIndexApplied = true
    }
  } catch (error) {
    const rollbackErrors: unknown[] = []
    let stateRolledBack = false
    try {
      stateRolledBack = previous
        ? await latencyStateStore.compareSetJson(
          key,
          state,
          previous,
          latencyStateTtlMs(previous.config, Boolean(previous.degradedUntilMs))
        )
        : await latencyStateStore.compareDeleteJson(key, state)
      if (!stateRolledBack) {
        rollbackErrors.push(
          new Error(`普通路由速度优先 state rollback CAS 失败：${key}`)
        )
      }
    } catch (rollbackError) {
      rollbackErrors.push(rollbackError)
    }
    if (stateRolledBack && !previous && (allIndexApplied || probeIndexApplied)) {
      try {
        await removeLatencyStateIndexKeysStrict([key])
      } catch (rollbackError) {
        rollbackErrors.push(rollbackError)
      }
    }
    if (rollbackErrors.length > 0) {
      throw new AggregateError(
        [error, ...rollbackErrors],
        `普通路由速度优先 state/index 写入失败且回滚存在 ${rollbackErrors.length} 个错误`
      )
    }
    throw error
  }
}

async function withLatencyStateMutationLock<T>(
  key: string,
  generation: string,
  operation: () => Promise<T>
): Promise<T | undefined> {
  const lock = await acquireLatencyStateMutationLock(key)
  if (!lock) return undefined
  try {
    if (!await renewLatencyStateGenerationAsync(generation)) {
      return undefined
    }
    return await operation()
  } finally {
    await latencyStateStore.releaseLock(lock.lockKey, lock.token)
  }
}

async function clearCurrentGenerationLatencyStateKeysAsync(
  keys: string[],
  generation: string,
  predicate: NormalRouteLatencyStatePredicate
): Promise<number> {
  if (keys.length === 0) return 0
  let nextIndex = 0
  const workers = Array.from(
    { length: Math.min(latencyStateExactClearConcurrency, keys.length) },
    async () => {
      let workerCleared = 0
      while (nextIndex < keys.length) {
        const key = keys[nextIndex]
        nextIndex += 1
        if (!key) continue
        workerCleared += await runWithGlobalBackgroundConcurrencySlot(async () => await clearCurrentGenerationLatencyStateKeyAsync(
          key,
          generation,
          predicate
        ))
      }
      return workerCleared
    }
  )
  const workerResults = await Promise.allSettled(workers)
  const errors = workerResults.flatMap((result) =>
    result.status === 'rejected' ? [result.reason] : []
  )
  if (errors.length > 0) {
    throw new AggregateError(
      errors,
      `普通路由速度优先逐 key 精确清理存在 ${errors.length} 个失败`
    )
  }
  return workerResults.reduce(
    (total, result) => total + (result.status === 'fulfilled' ? result.value : 0),
    0
  )
}

async function clearCurrentGenerationLatencyStateKeyAsync(
  key: string,
  generation: string,
  predicate: NormalRouteLatencyStatePredicate
): Promise<number> {
  const lock = await acquireLatencyStateMutationLockStrictAsync(key)
  try {
    if (!await renewLatencyStateGenerationAsync(generation)) return 0
    const state = await loadLatencyStateRaw(key)
    if (!state) {
      await removeLatencyStateIndexKeysStrict([key])
      return 0
    }
    if (state.generation !== generation || !predicate(state)) {
      return 0
    }
    await deleteLatencyStateAndIndexesStrictAsync(key)
    return 1
  } finally {
    await latencyStateStore.releaseLock(lock.lockKey, lock.token)
  }
}

async function deleteLatencyStateAndIndexesStrictAsync(key: string): Promise<void> {
  await latencyStateStore.delete(key)
  await removeLatencyStateIndexKeysStrict([key])
}

async function acquireLatencyStateMutationLock(
  key: string,
  ttlMs = latencyStateMutationLockTtlMs
): Promise<NormalRouteLatencyStateLock | undefined> {
  const lockKey = latencyStateMutationLockKey(key)
  const token = randomUUID()
  const locked = await acquireLatencyStateLock(lockKey, token, ttlMs)
  return locked ? { key, lockKey, token } : undefined
}

async function acquireLatencyStateMutationLockStrictAsync(key: string): Promise<NormalRouteLatencyStateLock> {
  const lock = await acquireLatencyStateMutationLock(key)
  if (!lock) {
    throw new Error(`普通路由速度优先状态 mutation lock 获取失败：${key}`)
  }
  return lock
}

function latencyStateMutationLockKey(key: string): string {
  return `${latencyStateVersion}:mutation-lock:${key}`
}

async function acquireLatencyStateIndexLock(lockKey: string, token: string): Promise<boolean> {
  return acquireLatencyStateLock(lockKey, token, latencyStateIndexLockTtlMs)
}

async function acquireLatencyStateLock(
  lockKey: string,
  token: string,
  ttlMs = latencyStateMutationLockTtlMs
): Promise<boolean> {
  for (let attempt = 0; attempt < latencyStateLockAcquireMaxAttempts; attempt += 1) {
    if (await latencyStateStore.acquireLock(lockKey, { ttlMs, token })) {
      return true
    }
    await delay(Math.min(latencyStateLockAcquireMaxDelayMs, 20 + attempt * 5))
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

function isRouteStrategySpeedFirstConfig(value: unknown): value is NormalRouteSpeedFirstRuntimeConfig {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return isFinitePositiveInteger(record.firstByteDeadlineMs)
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

function isNormalRouteLatencyGenerationEvent(
  value: unknown
): value is NormalRouteLatencyGenerationEvent {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return typeof record.version === 'string'
    && record.version.trim() !== ''
    && typeof record.publishedAt === 'string'
    && rfc3339InstantMilliseconds(record.publishedAt) !== undefined
}

function normalizePositiveInteger(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isInteger(numeric)) return fallback
  return Math.max(min, Math.min(max, numeric))
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
