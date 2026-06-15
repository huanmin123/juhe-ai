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

interface ApiKeyFailureStorm {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
  status: FailureStatus
  firstSeenMs: number
  lastSeenMs: number
  failureCount: number
  clientIps: Set<string>
  apiKeyIds: Set<string>
  reason?: string
}

interface ApiKeySuccessObservation {
  accountId: string
  keyFingerprint: string
  firstSeenMs: number
  lastSeenMs: number
  successCount: number
}

interface ApiKeyFailureStormDecision {
  trigger: boolean
  successCount: number
  failureRatio: number
  skippedReason?:
    | 'below_threshold'
    | 'observation_window'
    | 'recent_success'
    | 'failure_ratio'
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
    | 'policy_error'
    | 'failure_storm_confirmed'
    | 'failure_storm_pending'
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
const failureStormWindowMs = 10_000
const failureStormThresholdCount = 5
const failureStormDistinctIpThreshold = 2
const failureStormMinObservationMs = 2_000
const failureStormRecentSuccessGraceMs = 5_000
const failureStormFailureRatioThreshold = 0.9

const localApiKeySuppressions = new Map<string, LocalApiKeySuppression>()
const apiKeyFailureStorms = new Map<string, ApiKeyFailureStorm>()
const apiKeySuccessObservations = new Map<string, ApiKeySuccessObservation>()

export function recordGatewayAccountApiKeyFailureGuard(
  account: OpenAIAccountSecret,
  input: GatewayAccountApiKeyFailureGuardInput
): GatewayAccountApiKeyFailureGuardDecision {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { persist: false, reason: 'not_selected_api_key' }
  }

  const status = normalizeFailureStatus(input.status)
  if (input.trafficSource !== 'gateway') {
    rememberLocalApiKeySuppression(target, status, input.errorMessage)
    return { persist: true, reason: 'non_gateway_traffic' }
  }

  rememberLocalApiKeySuppression(target, status, input.errorMessage)
  if (status === 'error') {
    return { persist: true, reason: 'policy_error' }
  }

  const storm = rememberApiKeyFailureStorm(target, status, input)
  const decision = shouldPersistApiKeyFailureStorm(target, storm)
  const metadata = {
    failureCount: storm.failureCount,
    distinctClientIpCount: storm.clientIps.size,
    distinctApiKeyCount: storm.apiKeyIds.size,
    successCount: decision.successCount,
    failureRatio: decision.failureRatio
  }
  if (decision.trigger) {
    return {
      persist: true,
      reason: 'failure_storm_confirmed',
      ...metadata
    }
  }
  return {
    persist: false,
    reason: 'failure_storm_pending',
    ...metadata
  }
}

export function clearGatewayAccountApiKeyFailureGuard(account: OpenAIAccountSecret): boolean {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return false
  }
  const key = runtimeKey(target)
  const clearedLocal = localApiKeySuppressions.delete(key)
  const clearedStorm = apiKeyFailureStorms.delete(key)
  return clearedLocal || clearedStorm
}

export function recordGatewayAccountApiKeySuccessGuard(account: OpenAIAccountSecret): boolean {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return false
  }
  rememberApiKeySuccessObservation(target)
  return clearGatewayAccountApiKeyFailureGuard(account)
}

export function localAccountApiKeyRuntimeStatesForDispatch(accountId: string): AccountApiKeyRuntimeSelectionState[] {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) {
    return []
  }
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
  apiKeyFailureStorms.clear()
  apiKeySuccessObservations.clear()
}

export function getGatewayAccountApiKeyFailureGuardSnapshotForTest(): GatewayAccountApiKeyFailureGuardSnapshotEntry[] {
  cleanupExpiredApiKeyRuntimeState()
  const now = Date.now()
  const output: GatewayAccountApiKeyFailureGuardSnapshotEntry[] = []
  for (const [key, suppression] of localApiKeySuppressions.entries()) {
    const storm = apiKeyFailureStorms.get(key)
    output.push({
      accountId: suppression.accountId,
      keyFingerprint: suppression.keyFingerprint,
      status: suppression.status,
      localFailureCount: suppression.failureCount,
      stormFailureCount: storm?.failureCount,
      distinctClientIpCount: storm?.clientIps.size,
      distinctApiKeyCount: storm?.apiKeyIds.size,
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

function rememberApiKeyFailureStorm(
  target: AccountApiKeyRuntimeTarget,
  status: FailureStatus,
  input: GatewayAccountApiKeyFailureGuardInput
): ApiKeyFailureStorm {
  cleanupExpiredApiKeyRuntimeState()
  const now = Date.now()
  const key = runtimeKey(target)
  const current = apiKeyFailureStorms.get(key)
  const storm: ApiKeyFailureStorm = current && now - current.firstSeenMs <= failureStormWindowMs
    ? current
    : {
        accountId: target.accountId,
        keyFingerprint: target.keyFingerprint,
        keyIndex: target.keyIndex,
        status,
        firstSeenMs: now,
        lastSeenMs: now,
        failureCount: 0,
        clientIps: new Set<string>(),
        apiKeyIds: new Set<string>()
      }
  storm.status = status
  storm.failureCount += 1
  storm.lastSeenMs = now
  storm.reason = input.errorMessage
  const clientIp = input.clientIp?.trim()
  if (clientIp) {
    storm.clientIps.add(clientIp)
  }
  const apiKeyId = input.apiKeyId?.trim()
  if (apiKeyId) {
    storm.apiKeyIds.add(apiKeyId)
  }
  apiKeyFailureStorms.set(key, storm)
  return storm
}

function rememberApiKeySuccessObservation(target: AccountApiKeyRuntimeTarget): void {
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

function shouldPersistApiKeyFailureStorm(
  target: AccountApiKeyRuntimeTarget,
  storm: ApiKeyFailureStorm
): ApiKeyFailureStormDecision {
  const now = Date.now()
  const successObservation = apiKeySuccessObservations.get(runtimeKey(target))
  const successCount = successObservation?.successCount ?? 0
  const total = storm.failureCount + successCount
  const failureRatio = total > 0 ? storm.failureCount / total : 1

  if (storm.failureCount < failureStormThresholdCount || storm.clientIps.size < failureStormDistinctIpThreshold) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'below_threshold' }
  }
  if (now - storm.firstSeenMs < failureStormMinObservationMs) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'observation_window' }
  }
  if (successObservation && now - successObservation.lastSeenMs <= failureStormRecentSuccessGraceMs) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'recent_success' }
  }
  if (failureRatio < failureStormFailureRatioThreshold) {
    return { trigger: false, successCount, failureRatio, skippedReason: 'failure_ratio' }
  }
  return { trigger: true, successCount, failureRatio }
}

function cleanupExpiredApiKeyRuntimeState(): void {
  const now = Date.now()
  for (const [key, suppression] of localApiKeySuppressions.entries()) {
    if (suppression.suppressUntilMs <= now) {
      localApiKeySuppressions.delete(key)
    }
  }
  for (const [key, storm] of apiKeyFailureStorms.entries()) {
    if (now - storm.lastSeenMs > failureStormWindowMs) {
      apiKeyFailureStorms.delete(key)
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

function normalizeFailureStatus(status: GatewayAccountApiKeyFailureGuardInput['status']): FailureStatus {
  if (status === 'rate_limited' || status === 'error') {
    return status
  }
  return 'temporary_unavailable'
}
