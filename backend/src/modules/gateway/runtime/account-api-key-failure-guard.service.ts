import { runtimeConfig } from '../../../config/runtime.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../shared/runtime-state-store.js'
import type { AccountApiKeyRuntimeSelectionState, AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../../storage/openai-account-selector.types.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'

type FailureStatus = Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>

interface AccountApiKeyRuntimeTarget {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
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

interface ApiKeySuccessObservation {
  accountId: string
  keyFingerprint: string
  firstSeenMs: number
  lastSeenMs: number
  successCount: number
}

interface DistributedApiKeySuppression {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
  status: FailureStatus
  suppressUntil: string
}

export interface GatewayAccountApiKeyFailureGuardInput {
  status?: FailureStatus
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  trafficSource?: OpenAIGatewayTrafficSource
  clientIp?: string
  apiKeyId?: string
  source: string
}

export interface GatewayAccountApiKeyFailureGuardDecision {
  persist: boolean
  reason:
    | 'not_selected_api_key'
    | 'non_gateway_traffic'
    | 'gateway_local_only'
    | 'redis_transient_only'
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  successCount?: number
  failureRatio?: number
}

export interface GatewayAccountApiKeyLocalFailureGuardDecision {
  suppressed: boolean
  reason: 'not_selected_api_key' | 'suppressed' | 'redis_runtime_state'
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
const failureStormWindowMs = 10_000
const distributedFailureCounterTtlMs = localSuppressionMaxMs
const distributedStateStoreName = 'gateway-account-api-key-transient-avoidance'

const localApiKeySuppressions = new Map<string, LocalApiKeySuppression>()
const apiKeySuccessObservations = new Map<string, ApiKeySuccessObservation>()
let distributedStateStoreOverride: RuntimeStateStore | undefined
let distributedStateStore: RuntimeStateStore | undefined

export function recordGatewayAccountApiKeyFailureGuard(
  account: OpenAIAccountSecret,
  input: GatewayAccountApiKeyFailureGuardInput
): GatewayAccountApiKeyFailureGuardDecision {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { persist: false, reason: 'not_selected_api_key' }
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) {
    return input.trafficSource === 'gateway'
      ? { persist: false, reason: 'redis_transient_only' }
      : { persist: true, reason: 'non_gateway_traffic' }
  }

  const status = normalizeFailureStatus(input.status)
  if (input.trafficSource !== 'gateway') {
    rememberLocalApiKeySuppression(target, status, input.errorMessage)
    return { persist: true, reason: 'non_gateway_traffic' }
  }

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
  const store = gatewayAccountApiKeyTransientStateStore()
  const counterKey = distributedFailureCounterKey(target)
  const failureCount = await store.incr(counterKey, {
    ttlMs: distributedFailureCounterTtlMs,
    max: localSuppressionDelayMs.length
  })
  const delayMs = localSuppressionDelayMs[Math.min(failureCount - 1, localSuppressionDelayMs.length - 1)]
    ?? localSuppressionDelayMs[localSuppressionDelayMs.length - 1]
  const suppressUntil = new Date(Date.now() + Math.min(delayMs, localSuppressionMaxMs)).toISOString()
  await store.setJson<DistributedApiKeySuppression>(distributedSuppressionKey(target), {
    accountId: target.accountId,
    keyFingerprint: target.keyFingerprint,
    keyIndex: target.keyIndex,
    status: normalizeFailureStatus(input.status),
    suppressUntil
  }, delayMs)
  return true
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
  const now = Date.now()
  const states = await store.getJsonMany<DistributedApiKeySuppression>(fingerprints.map((keyFingerprint) => (
    distributedSuppressionKey({
      accountId: normalizedAccountId,
      keyFingerprint
    })
  )))
  const output: AccountApiKeyRuntimeSelectionState[] = []
  for (const [index, state] of states.entries()) {
    const keyFingerprint = fingerprints[index]
    if (!state || state.accountId !== normalizedAccountId || state.keyFingerprint !== keyFingerprint) continue
    const suppressUntilMs = Date.parse(state.suppressUntil)
    if (!Number.isFinite(suppressUntilMs) || suppressUntilMs <= now) continue
    output.push({
      keyFingerprint: state.keyFingerprint,
      keyIndex: state.keyIndex,
      status: state.status,
      nextProbeAt: state.suppressUntil
    })
  }
  return output
}

export async function clearGatewayAccountApiKeyTransientFailure(account: OpenAIAccountSecret): Promise<boolean> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return false
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) return false
  const store = gatewayAccountApiKeyTransientStateStore()
  await Promise.all([
    store.delete(distributedSuppressionKey(target)),
    store.delete(distributedFailureCounterKey(target))
  ])
  return true
}

export function setGatewayAccountApiKeyTransientStateStoreForTest(store: RuntimeStateStore | undefined): void {
  distributedStateStoreOverride = store
  distributedStateStore = undefined
}

export function recordGatewayAccountApiKeyLocalFailureGuard(
  account: OpenAIAccountSecret,
  input: Pick<GatewayAccountApiKeyFailureGuardInput, 'status' | 'errorMessage'>
): GatewayAccountApiKeyLocalFailureGuardDecision {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { suppressed: false, reason: 'not_selected_api_key' }
  }
  if (!canUseProcessLocalApiKeyRuntimeState()) {
    return { suppressed: false, reason: 'redis_runtime_state' }
  }
  rememberLocalApiKeySuppression(target, normalizeFailureStatus(input.status), input.errorMessage)
  return { suppressed: true, reason: 'suppressed' }
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
  rememberApiKeySuccessObservation(target)
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
  apiKeySuccessObservations.clear()
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

function rememberApiKeySuccessObservation(target: AccountApiKeyRuntimeTarget): void {
  if (!canUseProcessLocalApiKeyRuntimeState()) return
  cleanupExpiredApiKeyRuntimeState()
  const now = Date.now()
  const key = runtimeKey(target)
  const current = apiKeySuccessObservations.get(key)
  const observation: ApiKeySuccessObservation = current && now - current.firstSeenMs <= failureStormWindowMs
    ? current
    : {
        accountId: target.accountId,
        keyFingerprint: target.keyFingerprint,
        firstSeenMs: now,
        lastSeenMs: now,
        successCount: 0
      }
  observation.lastSeenMs = now
  observation.successCount += 1
  apiKeySuccessObservations.set(key, observation)
}

function cleanupExpiredApiKeyRuntimeState(): void {
  if (!canUseProcessLocalApiKeyRuntimeState()) return
  const now = Date.now()
  for (const [key, suppression] of localApiKeySuppressions.entries()) {
    if (suppression.suppressUntilMs <= now) {
      localApiKeySuppressions.delete(key)
    }
  }
  for (const [key, observation] of apiKeySuccessObservations.entries()) {
    if (now - observation.lastSeenMs > failureStormWindowMs) {
      apiKeySuccessObservations.delete(key)
    }
  }
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
    keyIndex: Number.isInteger(account.selectedApiKeyIndex) ? account.selectedApiKeyIndex : undefined
  }
}

function runtimeKey(target: AccountApiKeyRuntimeTarget): string {
  return `${target.accountId}:${target.keyFingerprint}`
}

function distributedSuppressionKey(target: Pick<AccountApiKeyRuntimeTarget, 'accountId' | 'keyFingerprint'>): string {
  return `suppression:${target.accountId}:${target.keyFingerprint}`
}

function distributedFailureCounterKey(target: Pick<AccountApiKeyRuntimeTarget, 'accountId' | 'keyFingerprint'>): string {
  return `failure-count:${target.accountId}:${target.keyFingerprint}`
}

function gatewayAccountApiKeyTransientStateStore(): RuntimeStateStore {
  if (distributedStateStoreOverride) return distributedStateStoreOverride
  distributedStateStore ??= createRuntimeStateStore(distributedStateStoreName)
  return distributedStateStore
}

function canUseProcessLocalApiKeyRuntimeState(): boolean {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return true
  localApiKeySuppressions.clear()
  apiKeySuccessObservations.clear()
  return false
}

function normalizeFailureStatus(status: GatewayAccountApiKeyFailureGuardInput['status']): FailureStatus {
  if (status === 'rate_limited' || status === 'error') {
    return status
  }
  return 'temporary_unavailable'
}
