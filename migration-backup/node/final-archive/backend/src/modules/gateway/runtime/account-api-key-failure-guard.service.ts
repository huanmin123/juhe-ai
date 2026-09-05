import { runtimeConfig } from '../../../config/runtime.js'
import type { AccountApiKeyRuntimeSelectionState, AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../../storage/openai-account-selector.types.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import {
  RedisAccountApiKeyTransientStateStore,
  type AccountApiKeyTransientStateStore
} from './account-api-key-transient-redis-store.js'
import {
  authorizeAccountApiKeyPersistentMutationForTrafficSource,
  type AccountApiKeyPersistentMutationContext
} from './account-api-key-mutation-authority.js'

type FailureStatus = Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>

interface AccountApiKeyRuntimeTarget {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
  transientGeneration?: string
}

interface LocalApiKeySuppression {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
  status: FailureStatus
  failureCount: number
  firstFailedAtMs: number
  lastFailedAtMs: number
  suppressUntilMs: number
  reason?: string
}

interface LocalApiKeyObservationFence {
  latestEpoch: number
  expiresAtMs: number
}

export interface GatewayAccountApiKeyFailureGuardInput {
  status?: FailureStatus
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  trafficSource?: OpenAIGatewayTrafficSource
  mutationContext?: AccountApiKeyPersistentMutationContext
  clientIp?: string
  apiKeyId?: string
  observationEpoch?: number
  source: string
}

export interface GatewayAccountApiKeyFailureGuardDecision {
  persist: boolean
  reason:
    | 'not_selected_api_key'
    | 'persistent_mutation_authorized'
    | 'persistent_mutation_unauthorized'
    | 'gateway_local_only'
    | 'stale_gateway_observation'
    | 'redis_transient_only'
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  successCount?: number
  failureRatio?: number
}

export interface GatewayAccountApiKeyFailureGuardSnapshotEntry {
  accountId: string
  keyFingerprint: string
  status: FailureStatus
  localFailureCount: number
  stormFailureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  suppressed: boolean
}

const localSuppressionDelayMs = [3_000, 5_000, 10_000] as const
const localSuppressionMaxMs = 10 * 60_000
const localObservationFenceRetentionMs = 10 * 60_000
const localObservationFenceCapacity = 50_000
const distributedStateStoreName = 'gateway-account-api-key-transient-avoidance'

const localApiKeySuppressions = new Map<string, LocalApiKeySuppression>()
const localApiKeyObservationFences = new Map<string, LocalApiKeyObservationFence>()
let localApiKeyObservationEpoch = 0
let distributedStateStoreOverride: AccountApiKeyTransientStateStore | undefined
let distributedStateStore: AccountApiKeyTransientStateStore | undefined

export function captureGatewayAccountApiKeyFailureObservation(account: OpenAIAccountSecret): number | undefined {
  if (!canUseProcessLocalApiKeyRuntimeState()) return undefined
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) return undefined
  const now = Date.now()
  const epoch = nextLocalApiKeyObservationEpoch()
  rememberLocalApiKeyObservationFence(runtimeKey(target), epoch, now)
  return epoch
}

export function recordGatewayAccountApiKeyFailureGuard(
  account: OpenAIAccountSecret,
  input: GatewayAccountApiKeyFailureGuardInput
): GatewayAccountApiKeyFailureGuardDecision {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { persist: false, reason: 'not_selected_api_key' }
  }
  const persistentAuthorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
    'failure',
    input.trafficSource,
    input.mutationContext
  )
  if (persistentAuthorization.allowed) {
    return { persist: true, reason: 'persistent_mutation_authorized' }
  }
  if (input.mutationContext) {
    return { persist: false, reason: 'persistent_mutation_unauthorized' }
  }
  if (input.trafficSource !== 'gateway') {
    return { persist: false, reason: 'persistent_mutation_unauthorized' }
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) {
    return { persist: false, reason: 'redis_transient_only' }
  }

  if (!acceptLocalApiKeyFailureObservation(target, input.observationEpoch)) {
    return { persist: false, reason: 'stale_gateway_observation' }
  }
  const status = normalizeFailureStatus(input.status)
  rememberLocalApiKeySuppression(target, status, input.errorMessage)
  return { persist: false, reason: 'gateway_local_only' }
}

export async function recordGatewayAccountApiKeyTransientFailure(
  account: OpenAIAccountSecret,
  input: Pick<GatewayAccountApiKeyFailureGuardInput, 'status'>
): Promise<boolean> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return false
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) return false
  if (!target.transientGeneration) return false
  const store = gatewayAccountApiKeyTransientStateStore()
  const result = await store.recordFailure({
    target,
    status: normalizeFailureStatus(input.status),
    expectedGeneration: target.transientGeneration
  })
  return result.applied
}

export async function loadGatewayAccountApiKeyTransientStatesForDispatch(
  accountId: string,
  keyFingerprints: Iterable<string>
): Promise<AccountApiKeyRuntimeSelectionState[]> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return localAccountApiKeyRuntimeStatesForDispatch(accountId)
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const fingerprints = [...new Set([...keyFingerprints].map((value) => value.trim()).filter(Boolean))]
  if (!fingerprints.length) return []
  const store = gatewayAccountApiKeyTransientStateStore()
  const states = await store.loadMany(normalizedAccountId, fingerprints)
  const output: AccountApiKeyRuntimeSelectionState[] = []
  for (const { state, suppressed } of states) {
    output.push({
      keyFingerprint: state.keyFingerprint,
      keyIndex: state.keyIndex,
      status: suppressed && state.status ? state.status : 'active',
      transientGeneration: state.generation,
      ...(suppressed && state.suppressUntilMs !== undefined
        ? { nextProbeAt: new Date(state.suppressUntilMs).toISOString() }
        : {})
    })
  }
  return output
}

export async function clearGatewayAccountApiKeyTransientFailure(
  account: OpenAIAccountSecret
): Promise<boolean> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return false
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) return false
  if (!target.transientGeneration) return false
  const store = gatewayAccountApiKeyTransientStateStore()
  const result = await store.recordSuccess({
    target,
    expectedGeneration: target.transientGeneration
  })
  return result.applied || result.state?.observationKind === 'success'
}

export function setGatewayAccountApiKeyTransientStateStoreForTest(store: AccountApiKeyTransientStateStore | undefined): void {
  distributedStateStoreOverride = store
  distributedStateStore = undefined
}

export function clearGatewayAccountApiKeyFailureGuard(account: OpenAIAccountSecret): boolean {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return false
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) return false
  const key = runtimeKey(target)
  const clearedLocal = localApiKeySuppressions.delete(key)
  return clearedLocal
}

export function recordGatewayAccountApiKeySuccessGuard(account: OpenAIAccountSecret): boolean {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return false
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) return false
  const now = Date.now()
  rememberLocalApiKeyObservationFence(runtimeKey(target), nextLocalApiKeyObservationEpoch(), now)
  return clearGatewayAccountApiKeyFailureGuard(account)
}

export function localAccountApiKeyRuntimeStatesForDispatch(accountId: string): AccountApiKeyRuntimeSelectionState[] {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) {
    return []
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) return []
  cleanupExpiredApiKeyRuntimeState()
  const now = Date.now()
  const states: AccountApiKeyRuntimeSelectionState[] = []
  for (const suppression of localApiKeySuppressions.values()) {
    if (suppression.accountId !== normalizedAccountId || suppression.suppressUntilMs <= now) {
      continue
    }
    states.push({
      keyFingerprint: suppression.keyFingerprint,
      keyIndex: suppression.keyIndex,
      status: suppression.status,
      nextProbeAt: new Date(suppression.suppressUntilMs).toISOString()
    })
  }
  return states
}

export function clearGatewayAccountApiKeyFailureGuardsForTest(): void {
  localApiKeySuppressions.clear()
  localApiKeyObservationFences.clear()
}

export function getGatewayAccountApiKeyFailureGuardSnapshotForTest(): GatewayAccountApiKeyFailureGuardSnapshotEntry[] {
  if (!canUseProcessLocalApiKeyRuntimeState()) return []
  cleanupExpiredApiKeyRuntimeState()
  const now = Date.now()
  const output: GatewayAccountApiKeyFailureGuardSnapshotEntry[] = []
  for (const suppression of localApiKeySuppressions.values()) {
    output.push({
      accountId: suppression.accountId,
      keyFingerprint: suppression.keyFingerprint,
      status: suppression.status,
      localFailureCount: suppression.failureCount,
      suppressed: suppression.suppressUntilMs > now
    })
  }
  return output
}

function rememberLocalApiKeySuppression(
  target: AccountApiKeyRuntimeTarget,
  status: FailureStatus,
  reason?: string
): void {
  if (!canUseProcessLocalApiKeyRuntimeState()) return
  const now = Date.now()
  const key = runtimeKey(target)
  const current = localApiKeySuppressions.get(key)
  const failureCount = (current?.failureCount ?? 0) + 1
  const delayMs = localSuppressionDelayMs[Math.min(failureCount - 1, localSuppressionDelayMs.length - 1)]
    ?? localSuppressionDelayMs[localSuppressionDelayMs.length - 1]
  localApiKeySuppressions.set(key, {
    accountId: target.accountId,
    keyFingerprint: target.keyFingerprint,
    keyIndex: target.keyIndex,
    status,
    failureCount,
    firstFailedAtMs: current?.firstFailedAtMs ?? now,
    lastFailedAtMs: now,
    suppressUntilMs: now + Math.min(delayMs, localSuppressionMaxMs),
    reason
  })
}

function cleanupExpiredApiKeyRuntimeState(now = Date.now()): void {
  if (!canUseProcessLocalApiKeyRuntimeState()) return
  for (const [key, suppression] of localApiKeySuppressions.entries()) {
    if (suppression.suppressUntilMs <= now) {
      localApiKeySuppressions.delete(key)
    }
  }
}

function acceptLocalApiKeyFailureObservation(
  target: AccountApiKeyRuntimeTarget,
  observationEpoch: number | undefined
): boolean {
  const now = Date.now()
  const key = runtimeKey(target)
  if (observationEpoch === undefined) {
    rememberLocalApiKeyObservationFence(key, nextLocalApiKeyObservationEpoch(), now)
    return true
  }
  if (!Number.isSafeInteger(observationEpoch) || observationEpoch <= 0) return false
  const current = localApiKeyObservationFences.get(key)
  if (!current || current.expiresAtMs <= now || current.latestEpoch !== observationEpoch) {
    if (current?.expiresAtMs !== undefined && current.expiresAtMs <= now) {
      localApiKeyObservationFences.delete(key)
    }
    return false
  }
  current.expiresAtMs = now + localObservationFenceRetentionMs
  return true
}

function rememberLocalApiKeyObservationFence(key: string, epoch: number, now: number): void {
  localApiKeyObservationFences.delete(key)
  localApiKeyObservationFences.set(key, {
    latestEpoch: epoch,
    expiresAtMs: now + localObservationFenceRetentionMs
  })
  while (localApiKeyObservationFences.size > localObservationFenceCapacity) {
    const oldestKey = localApiKeyObservationFences.keys().next().value as string | undefined
    if (!oldestKey) break
    localApiKeyObservationFences.delete(oldestKey)
  }
}

function nextLocalApiKeyObservationEpoch(): number {
  if (localApiKeyObservationEpoch >= Number.MAX_SAFE_INTEGER) {
    localApiKeyObservationEpoch = 0
    localApiKeyObservationFences.clear()
  }
  localApiKeyObservationEpoch += 1
  return localApiKeyObservationEpoch
}

function accountApiKeyRuntimeTarget(account: OpenAIAccountSecret): AccountApiKeyRuntimeTarget | undefined {
  const keyFingerprint = account.selectedApiKeyFingerprint?.trim()
  if (!keyFingerprint) {
    return undefined
  }
  const accountId = (account.credentialSourceAccountId ?? account.id).trim()
  if (!accountId) {
    return undefined
  }
  return {
    accountId,
    keyFingerprint,
    keyIndex: Number.isInteger(account.selectedApiKeyIndex) ? account.selectedApiKeyIndex : undefined,
    transientGeneration: account.selectedApiKeyTransientGeneration?.trim() || undefined
  }
}

function runtimeKey(target: AccountApiKeyRuntimeTarget): string {
  return `${target.accountId}:${target.keyFingerprint}`
}

function gatewayAccountApiKeyTransientStateStore(): AccountApiKeyTransientStateStore {
  if (distributedStateStoreOverride) return distributedStateStoreOverride
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  distributedStateStore ??= new RedisAccountApiKeyTransientStateStore({
    redisUrl,
    name: distributedStateStoreName,
    suppressionDelayMs: localSuppressionDelayMs
  })
  return distributedStateStore
}

function canUseProcessLocalApiKeyRuntimeState(): boolean {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return true
  localApiKeySuppressions.clear()
  localApiKeyObservationFences.clear()
  return false
}

function normalizeFailureStatus(status: GatewayAccountApiKeyFailureGuardInput['status']): FailureStatus {
  if (status === 'rate_limited' || status === 'error') {
    return status
  }
  return 'temporary_unavailable'
}
