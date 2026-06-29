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
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  successCount?: number
  failureRatio?: number
}

export interface GatewayAccountApiKeyLocalFailureGuardDecision {
  suppressed: boolean
  reason: 'not_selected_api_key' | 'suppressed'
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

const localApiKeySuppressions = new Map<string, LocalApiKeySuppression>()
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
  return { persist: false, reason: 'gateway_local_only' }
}

export function recordGatewayAccountApiKeyLocalFailureGuard(
  account: OpenAIAccountSecret,
  input: Pick<GatewayAccountApiKeyFailureGuardInput, 'status' | 'errorMessage'>
): GatewayAccountApiKeyLocalFailureGuardDecision {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { suppressed: false, reason: 'not_selected_api_key' }
  }
  rememberLocalApiKeySuppression(target, normalizeFailureStatus(input.status), input.errorMessage)
  return { suppressed: true, reason: 'suppressed' }
}

export function clearGatewayAccountApiKeyFailureGuard(account: OpenAIAccountSecret): boolean {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return false
  }
  const key = runtimeKey(target)
  const clearedLocal = localApiKeySuppressions.delete(key)
  return clearedLocal
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
  apiKeySuccessObservations.clear()
}

export function getGatewayAccountApiKeyFailureGuardSnapshotForTest(): GatewayAccountApiKeyFailureGuardSnapshotEntry[] {
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

function normalizeFailureStatus(status: GatewayAccountApiKeyFailureGuardInput['status']): FailureStatus {
  if (status === 'rate_limited' || status === 'error') {
    return status
  }
  return 'temporary_unavailable'
}
