import { createHash } from 'node:crypto'

/**
 * A deliberately narrow circuit for one physical credential and one resolved
 * route.  It does not participate in account-circuit escalation.
 */
export type KeyModelPhase = 'CLOSED' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'
export type KeyModelOutcome = 'complete_success' | 'upstream_not_complete' | 'unknown'

export interface CapabilityKey {
  credentialSourceAccountId: string
  keyFingerprint: string
  clientModel: string
  clientEndpointFamily: string
  finalUpstreamModel: string
  upstreamEndpointMode: string
  dispatchRevision: number
}

export interface KeyModelState extends CapabilityKey {
  capabilityHash: string
  generation: number
  phase: KeyModelPhase
  backoffAttempt: number
  retryAtMs?: number
  recoverySuccessCount: number
  lastRecoverySuccessAtMs?: number
  lastObservedAtMs: number
  lastOutcome?: KeyModelOutcome
  probeLease?: { leaseId: string; leaseUntilMs: number; priorSuccessCount: number }
}

export type KeyModelMutationStatus = 'applied' | 'idempotent' | 'stale' | 'not_due' | 'lease_mismatch'

export const keyModelBackoffMs = [5_000, 15_000, 60_000, 5 * 60_000] as const
export const keyModelRecoverySuccessThreshold = 3
export const keyModelRecoverySuccessMaxGapMs = 2 * 60_000
export const keyModelRecoveryIntervalMs = 10_000
export const keyModelProbeTimeoutMs = 30_000
export const keyModelProbeLeaseMs = 45_000
export const keyModelProbeLeaseRenewMs = 10_000
export const keyModelForegroundLimit = 2
export const keyModelForegroundPrecommitLeaseMs = 90_000
export const keyModelForegroundLeaseRenewMs = 30_000
export const keyModelForegroundRedisOperationTimeoutMs = 100
export const keyModelMainProbeUnknownRetryMs = 10_000

export type KeyModelForegroundDecision = 'admitted' | 'busy' | 'blocked'

export interface MainProbeRoute {
  clientModel: string
  clientEndpointFamily: string
  finalUpstreamModel: string
  upstreamEndpointMode: string
}

export function keyModelBackoffDelayMs(attempt: number): number {
  return keyModelBackoffMs[Math.min(keyModelBackoffMs.length - 1, Math.max(0, Math.trunc(attempt) - 1))]!
}

export function capabilityHash(key: CapabilityKey): string {
  return createHash('sha256').update(canonicalCapabilityJson(key)).digest('hex')
}

export function canonicalCapabilityJson(key: CapabilityKey): string {
  const normalized = normalizeCapabilityKey(key)
  return JSON.stringify({
    clientEndpointFamily: normalized.clientEndpointFamily,
    clientModel: normalized.clientModel,
    credentialSourceAccountId: normalized.credentialSourceAccountId,
    dispatchRevision: normalized.dispatchRevision,
    finalUpstreamModel: normalized.finalUpstreamModel,
    keyFingerprint: normalized.keyFingerprint,
    upstreamEndpointMode: normalized.upstreamEndpointMode
  })
}

export function normalizeCapabilityKey(key: CapabilityKey): CapabilityKey {
  const normalized = {
    credentialSourceAccountId: requiredText(key.credentialSourceAccountId, 'credentialSourceAccountId'),
    keyFingerprint: requiredText(key.keyFingerprint, 'keyFingerprint'),
    clientModel: requiredText(key.clientModel, 'clientModel'),
    clientEndpointFamily: requiredText(key.clientEndpointFamily, 'clientEndpointFamily'),
    finalUpstreamModel: requiredText(key.finalUpstreamModel, 'finalUpstreamModel'),
    upstreamEndpointMode: requiredText(key.upstreamEndpointMode, 'upstreamEndpointMode'),
    dispatchRevision: key.dispatchRevision
  }
  if (!Number.isSafeInteger(normalized.dispatchRevision) || normalized.dispatchRevision < 1) {
    throw new Error('CapabilityKey dispatchRevision 必须是正整数')
  }
  return normalized
}

export function createKeyModelOpenState(key: CapabilityKey, nowMs: number): KeyModelState {
  const normalized = normalizeCapabilityKey(key)
  return {
    ...normalized,
    capabilityHash: capabilityHash(normalized),
    generation: 1,
    phase: 'OPEN',
    backoffAttempt: 1,
    retryAtMs: nowMs + keyModelBackoffDelayMs(1),
    recoverySuccessCount: 0,
    lastObservedAtMs: nowMs,
    lastOutcome: 'upstream_not_complete'
  }
}

export function settleKeyModelRecovery(
  state: KeyModelState,
  input: { generation: number; dispatchRevision: number; leaseId: string; outcome: KeyModelOutcome; nowMs: number }
): { status: KeyModelMutationStatus; state: KeyModelState } {
  const current = cloneState(state)
  if (current.generation !== input.generation || current.dispatchRevision !== input.dispatchRevision) return { status: 'stale', state: current }
  if (current.phase !== 'HALF_OPEN' || current.probeLease?.leaseId !== input.leaseId) return { status: 'lease_mismatch', state: current }
  if (current.probeLease.leaseUntilMs < input.nowMs) return { status: 'stale', state: current }

  current.probeLease = undefined
  current.lastObservedAtMs = input.nowMs
  current.lastOutcome = input.outcome
  if (input.outcome === 'unknown') {
    current.phase = current.recoverySuccessCount > 0 ? 'RECOVERING' : 'OPEN'
    current.retryAtMs = input.nowMs + keyModelRecoveryIntervalMs
    return { status: 'applied', state: current }
  }
  if (input.outcome === 'upstream_not_complete') {
    current.phase = 'OPEN'
    current.backoffAttempt = Math.min(4, current.backoffAttempt + 1)
    current.recoverySuccessCount = 0
    current.lastRecoverySuccessAtMs = undefined
    current.retryAtMs = input.nowMs + keyModelBackoffDelayMs(current.backoffAttempt)
    return { status: 'applied', state: current }
  }

  const withinGap = current.lastRecoverySuccessAtMs === undefined
    || input.nowMs - current.lastRecoverySuccessAtMs <= keyModelRecoverySuccessMaxGapMs
  const nextSuccessCount = withinGap ? current.recoverySuccessCount + 1 : 1
  if (nextSuccessCount >= keyModelRecoverySuccessThreshold) {
    return {
      status: 'applied',
      state: {
        ...current,
        phase: 'CLOSED',
        backoffAttempt: 0,
        retryAtMs: undefined,
        recoverySuccessCount: 0,
        lastRecoverySuccessAtMs: undefined
      }
    }
  }
  current.phase = 'RECOVERING'
  current.recoverySuccessCount = nextSuccessCount
  current.lastRecoverySuccessAtMs = input.nowMs
  current.retryAtMs = input.nowMs + keyModelRecoveryIntervalMs
  return { status: 'applied', state: current }
}

export function acquireKeyModelRecoveryLease(
  state: KeyModelState,
  input: { generation: number; dispatchRevision: number; leaseId: string; nowMs: number }
): { status: KeyModelMutationStatus; state: KeyModelState } {
  const current = cloneState(state)
  if (current.generation !== input.generation || current.dispatchRevision !== input.dispatchRevision) return { status: 'stale', state: current }
  if ((current.phase !== 'OPEN' && current.phase !== 'RECOVERING') || (current.retryAtMs ?? Infinity) > input.nowMs) return { status: 'not_due', state: current }
  if (current.probeLease && current.probeLease.leaseUntilMs >= input.nowMs) return { status: 'lease_mismatch', state: current }
  current.phase = 'HALF_OPEN'
  current.probeLease = { leaseId: requiredText(input.leaseId, 'leaseId'), leaseUntilMs: input.nowMs + keyModelProbeLeaseMs, priorSuccessCount: current.recoverySuccessCount }
  return { status: 'applied', state: current }
}

export function isKeyModelBlocked(state: KeyModelState): boolean {
  return state.phase !== 'CLOSED'
}

// The Redis adapter owns the count. This pure decision keeps the account
// concurrency release rule independent from any status/error-body heuristic.
export function decideKeyModelForegroundAdmission(input: {
  phase: KeyModelPhase
  activeUncommitted: number
}): KeyModelForegroundDecision {
  if (input.phase !== 'CLOSED') return 'blocked'
  if (!Number.isSafeInteger(input.activeUncommitted) || input.activeUncommitted < 0) {
    throw new Error('foreground activeUncommitted 无效')
  }
  return input.activeUncommitted >= keyModelForegroundLimit ? 'busy' : 'admitted'
}

export function matchesMainProbeRoute(key: Pick<CapabilityKey, 'clientModel' | 'clientEndpointFamily' | 'finalUpstreamModel' | 'upstreamEndpointMode'>, main: MainProbeRoute): boolean {
  return key.clientModel.trim() === main.clientModel.trim()
    && key.clientEndpointFamily.trim() === main.clientEndpointFamily.trim()
    && key.finalUpstreamModel.trim() === main.finalUpstreamModel.trim()
    && key.upstreamEndpointMode.trim() === main.upstreamEndpointMode.trim()
}

function requiredText(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`CapabilityKey 缺少 ${name}`)
  return normalized
}

function cloneState(state: KeyModelState): KeyModelState {
  return { ...state, probeLease: state.probeLease ? { ...state.probeLease } : undefined }
}
