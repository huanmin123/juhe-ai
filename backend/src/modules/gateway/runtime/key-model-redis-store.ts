import { runtimeConfig } from '../../../config/runtime.js'
import { getRedisClient, runRedisOperationWithDeadline, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  capabilityHash,
  createKeyModelOpenState,
  keyModelForegroundLimit,
  keyModelForegroundPrecommitLeaseMs,
  keyModelForegroundRedisOperationTimeoutMs,
  keyModelMainProbeUnknownRetryMs,
  keyModelProbeLeaseMs,
  type CapabilityKey,
  type KeyModelState,
  acquireKeyModelRecoveryLease,
  settleKeyModelRecovery,
  type KeyModelMutationStatus,
  type KeyModelOutcome
} from './key-model-runtime.js'

export interface KeyModelRuntimeStore {
  get(capability: CapabilityKey): Promise<KeyModelState | undefined>
  recordFailure(intent: KeyModelFailureIntent): Promise<KeyModelFailureResult>
  admitForeground(capability: CapabilityKey, attemptId: string): Promise<KeyModelAdmissionResult>
  releaseForeground(permit: KeyModelForegroundPermit): Promise<boolean>
  renewForeground(permit: KeyModelForegroundPermit): Promise<KeyModelForegroundPermit | undefined>
  recordMainProbeFailure(capability: CapabilityKey, permit: KeyModelForegroundPermit): Promise<void>
  clearMainProbeFence(fence: KeyModelFenceReference, winnerKeyFingerprint: string): Promise<boolean>
  deferMainProbeFence(fence: KeyModelFenceReference): Promise<boolean>
  claimJ1Confirmation(sourceAccountId: string, dispatchRevision: number): Promise<boolean>
}

export interface InMemoryKeyModelRecoveryStore extends KeyModelRuntimeStore {
  listDue(nowMs: number, limit: number): Promise<KeyModelState[]>
  getRecoveryTarget(capability: CapabilityKey): KeyModelRecoveryTarget | undefined
  acquireRecoveryLease(input: { capability: CapabilityKey; generation: number; dispatchRevision: number; leaseId: string; nowMs: number }): Promise<{ status: KeyModelMutationStatus; state: KeyModelState }>
  renewRecoveryLease(input: { capabilityHash: string; generation: number; dispatchRevision: number; leaseId: string; nowMs: number }): Promise<boolean>
  settleRecovery(input: { capability: CapabilityKey; generation: number; dispatchRevision: number; leaseId: string; outcome: KeyModelOutcome; nowMs: number }): Promise<{ status: KeyModelMutationStatus; state: KeyModelState }>
}

export interface KeyModelFailureIntent {
  intentId: string
  requestId: string
  attemptId: string
  capability: CapabilityKey
  observedAtMs: number
  outcome: 'upstream_not_complete'
  sourceFence: string
  permit?: KeyModelForegroundPermit
  recoveryTarget?: KeyModelRecoveryTarget
}

export interface KeyModelRecoveryTarget {
  accountId: string
  groupId: string
  systemAccountId: string
}

export interface KeyModelFenceReference {
  capabilityHash: string
  keyFingerprint: string
  dispatchRevision: number
  ownerId: string
}

export type KeyModelFailureResult =
  | { status: 'applied' | 'idempotent'; state: KeyModelState }
  | { status: 'capacity_exhausted' | 'stale' }

export type KeyModelAdmissionResult =
  | { status: 'admitted'; permit: KeyModelForegroundPermit }
  | { status: 'busy' | 'blocked'; wakeSequence: number }

export interface KeyModelForegroundPermit {
  capabilityHash: string
  attemptId: string
  leaseUntilMs: number
}

export const keyModelStateCapacity = 50_000
export const keyModelClosedRetentionMs = 5 * 60_000
export const keyModelReceiptRetentionMs = 5 * 60_000

interface KeyModelRedisKeys {
  state(hash: string): string
  due: string
  closed: string
  receipt(intentId: string): string
  admission(hash: string): string
  admissionLease(hash: string, attemptId: string): string
  admissionWake(hash: string): string
  mainProbeFence(hash: string): string
  j1Confirmation(sourceAccountId: string, dispatchRevision: number): string
  admissionEvents: string
  capacity: string
}

export class RedisKeyModelRuntimeStore implements KeyModelRuntimeStore {
  readonly keys: KeyModelRedisKeys

  constructor(
    private readonly redisUrl = requiredRedisStateUrl(),
    private readonly getClient: () => Promise<RedisCommandClient> = () => getRedisClient(redisUrl),
    private readonly evalRunner?: (script: string, keys: string[], args: string[]) => Promise<unknown>
  ) {
    this.keys = keyModelRedisKeys()
  }

  async get(capability: CapabilityKey): Promise<KeyModelState | undefined> {
    const hash = capabilityHash(capability)
    const value = await this.readWithSingleRetry(this.keys.state(hash))
    if (value === null) return undefined
    return parseState(value, hash, capability.dispatchRevision)
  }

  async recordFailure(intent: KeyModelFailureIntent): Promise<KeyModelFailureResult> {
    validateFailureIntent(intent)
    const state = createKeyModelOpenState(intent.capability, intent.observedAtMs)
    const hash = state.capabilityHash
    if (intent.permit && intent.permit.capabilityHash !== hash) throw new Error('Key-model 失败意图 permit 与 CapabilityKey 不匹配')
    const result = redisArray(await this.evalWithSingleRetry('Key-model 失败意图写入', recordKeyModelFailureScript, [
      this.keys.state(state.capabilityHash),
      this.keys.due,
      this.keys.receipt(intent.intentId),
      this.keys.capacity,
      this.keys.admission(hash),
      this.keys.admissionLease(hash, intent.permit?.attemptId ?? intent.attemptId),
      this.keys.admissionWake(hash),
      this.keys.admissionEvents,
      this.keys.closed
    ], [
        JSON.stringify(state),
        String(intent.capability.dispatchRevision),
        '50000',
        String(5 * 60_000),
        intent.permit?.attemptId ?? intent.attemptId,
        hash
      ]))
    const status = String(result[0])
    if (status === 'capacity_exhausted' || status === 'stale') return { status }
    if (status !== 'applied' && status !== 'idempotent') throw new Error(`Key-model 失败意图返回未知状态：${status}`)
    return { status, state: parseState(String(result[1]), state.capabilityHash, intent.capability.dispatchRevision) }
  }

  async admitForeground(capability: CapabilityKey, attemptId: string): Promise<KeyModelAdmissionResult> {
    const hash = capabilityHash(capability)
    const normalizedAttemptId = requiredText(attemptId, 'attemptId')
    const result = redisArray(await this.evalWithSingleRetry('Key-model foreground admission', admitKeyModelForegroundScript, [
        this.keys.state(hash),
        this.keys.admission(hash),
        this.keys.admissionLease(hash, normalizedAttemptId),
        this.keys.admissionWake(hash),
        this.keys.mainProbeFence(hash)
      ], [normalizedAttemptId, String(keyModelForegroundPrecommitLeaseMs), String(keyModelForegroundLimit), String(capability.dispatchRevision)]))
    const status = String(result[0])
    const wakeSequence = finiteInteger(result[1])
    if (status === 'busy' || status === 'blocked') return { status, wakeSequence }
    if (status !== 'admitted' && status !== 'idempotent') throw new Error(`Key-model admission 返回未知状态：${status}`)
    return {
      status: 'admitted',
      permit: { capabilityHash: hash, attemptId: normalizedAttemptId, leaseUntilMs: finiteInteger(result[2]) }
    }
  }

  async releaseForeground(permit: KeyModelForegroundPermit): Promise<boolean> {
    const hash = requiredHash(permit.capabilityHash)
    const result = redisArray(await this.evalWithSingleRetry('Key-model foreground permit 释放', releaseKeyModelForegroundScript, [
        this.keys.admission(hash),
        this.keys.admissionLease(hash, requiredText(permit.attemptId, 'attemptId')),
        this.keys.admissionWake(hash),
        this.keys.admissionEvents
      ], [hash, requiredText(permit.attemptId, 'attemptId')]))
    return finiteInteger(result[0]) === 1
  }

  async renewForeground(permit: KeyModelForegroundPermit): Promise<KeyModelForegroundPermit | undefined> {
    const hash = requiredHash(permit.capabilityHash)
    const attemptId = requiredText(permit.attemptId, 'attemptId')
    const result = redisArray(await this.evalWithSingleRetry('Key-model foreground permit 续租', renewKeyModelForegroundScript, [
      this.keys.admission(hash),
      this.keys.admissionLease(hash, attemptId)
    ], [attemptId, String(keyModelForegroundPrecommitLeaseMs)]))
    if (String(result[0]) === 'lost') return undefined
    if (String(result[0]) !== 'renewed') throw new Error(`Key-model foreground 续租返回未知状态：${String(result[0])}`)
    return { capabilityHash: hash, attemptId, leaseUntilMs: finiteInteger(result[1]) }
  }

  async recordMainProbeFailure(capability: CapabilityKey, permit: KeyModelForegroundPermit): Promise<void> {
    const hash = capabilityHash(capability)
    if (permit.capabilityHash !== hash) throw new Error('MainProbe fence permit 与 CapabilityKey 不匹配')
    await this.evalWithSingleRetry('MainProbe foreground fence 写入', recordMainProbeFenceScript, [
      this.keys.mainProbeFence(hash),
      this.keys.admission(hash),
      this.keys.admissionLease(hash, permit.attemptId),
      this.keys.admissionWake(hash),
      this.keys.admissionEvents
    ], [permit.attemptId, hash, String(90_000)])
  }

  async clearMainProbeFence(fence: KeyModelFenceReference, winnerKeyFingerprint: string): Promise<boolean> {
    const hash = requiredHash(fence.capabilityHash)
    if (requiredText(fence.keyFingerprint, 'keyFingerprint') !== requiredText(winnerKeyFingerprint, 'winnerKeyFingerprint')) return false
    if (!Number.isSafeInteger(fence.dispatchRevision) || fence.dispatchRevision < 1) return false
    const result = redisArray(await this.evalWithSingleRetry('MainProbe fence 清理', clearMainProbeFenceScript, [
      this.keys.mainProbeFence(hash)
    ], [requiredText(fence.ownerId, 'ownerId')]))
    return finiteInteger(result[0]) === 1
  }

  async deferMainProbeFence(fence: KeyModelFenceReference): Promise<boolean> {
    const result = redisArray(await this.evalWithSingleRetry('MainProbe fence unknown 延后', deferMainProbeFenceScript, [
      this.keys.mainProbeFence(requiredHash(fence.capabilityHash))
    ], [requiredText(fence.ownerId, 'ownerId'), String(keyModelMainProbeUnknownRetryMs)]))
    return finiteInteger(result[0]) === 1
  }

  async claimJ1Confirmation(sourceAccountId: string, dispatchRevision: number): Promise<boolean> {
    const sourceHash = capabilityHash({
      credentialSourceAccountId: requiredText(sourceAccountId, 'credentialSourceAccountId'),
      keyFingerprint: 'j1-confirmation',
      clientModel: 'j1-confirmation',
      clientEndpointFamily: 'j1-confirmation',
      finalUpstreamModel: 'j1-confirmation',
      upstreamEndpointMode: 'j1-confirmation',
      dispatchRevision
    })
    const result = redisArray(await this.evalWithSingleRetry('Key-model J1 confirmation 限频', claimJ1ConfirmationScript, [
      this.keys.j1Confirmation(sourceHash, dispatchRevision)
    ], [String(2 * 60_000)]))
    return String(result[0]) === 'claimed'
  }

  private async readWithSingleRetry(key: string): Promise<string | null> {
    try {
      return await (await this.getClient()).get(key)
    } catch (firstError) {
      await wait(50)
      try {
        return await (await this.getClient()).get(key)
      } catch (secondError) {
        throw new AggregateError([firstError, secondError], 'Key-model Redis state 连续两次读取失败')
      }
    }
  }

  private async evalWithSingleRetry(operationName: string, script: string, keys: string[], args: string[]): Promise<unknown> {
    try {
      return await this.eval(operationName, script, keys, args)
    } catch (firstError) {
      await wait(50)
      try {
        return await this.eval(operationName, script, keys, args)
      } catch (secondError) {
        throw new AggregateError([firstError, secondError], `${operationName}连续两次失败`)
      }
    }
  }

  private eval(operationName: string, script: string, keys: string[], args: string[]): Promise<unknown> {
    if (this.evalRunner) return this.evalRunner(script, keys, args)
    return runRedisOperationWithDeadline(this.redisUrl, { timeoutMs: keyModelForegroundRedisOperationTimeoutMs, operationName }, (client) => (
      client.eval(script, { keys, arguments: args })
    ))
  }
}

/**
 * Single-process equivalent of the Redis adapter. It intentionally shares the
 * exact pure state transitions and limits, but has no cross-process durability.
 */
export class InMemoryKeyModelRuntimeStore implements InMemoryKeyModelRecoveryStore {
  private readonly states = new Map<string, KeyModelState>()
  private readonly receipts = new Map<string, { state: KeyModelState; expiresAtMs: number }>()
  private readonly closedUntil = new Map<string, number>()
  private readonly permits = new Map<string, Map<string, number>>()
  private readonly wakes = new Map<string, number>()
  private readonly mainFences = new Map<string, { ownerId: string; expiresAtMs: number }>()
  private readonly j1Claims = new Map<string, number>()
  private readonly recoveryTargets = new Map<string, KeyModelRecoveryTarget>()

  async get(capability: CapabilityKey): Promise<KeyModelState | undefined> {
    this.cleanup(Date.now())
    const state = this.states.get(capabilityHash(capability))
    return state ? cloneMemoryState(state) : undefined
  }

  getRecoveryTarget(capability: CapabilityKey): KeyModelRecoveryTarget | undefined {
    const target = this.recoveryTargets.get(capabilityHash(capability))
    return target ? { ...target } : undefined
  }

  async listDue(nowMs: number, limit: number): Promise<KeyModelState[]> {
    if (!Number.isSafeInteger(nowMs) || nowMs < 1) throw new Error('Key-model memory listDue nowMs 无效')
    this.cleanup(nowMs)
    const boundedLimit = Math.max(0, Math.trunc(limit))
    return [...this.states.values()]
      .filter((state) => (state.phase === 'OPEN' || state.phase === 'RECOVERING') && (state.retryAtMs ?? Infinity) <= nowMs)
      .sort((left, right) => {
        const continuationOrder = Number(right.phase === 'RECOVERING') - Number(left.phase === 'RECOVERING')
        return continuationOrder || (left.retryAtMs ?? Infinity) - (right.retryAtMs ?? Infinity)
      })
      .slice(0, boundedLimit)
      .map(cloneMemoryState)
  }

  async acquireRecoveryLease(input: { capability: CapabilityKey; generation: number; dispatchRevision: number; leaseId: string; nowMs: number }): Promise<{ status: KeyModelMutationStatus; state: KeyModelState }> {
    this.cleanup(input.nowMs)
    const hash = capabilityHash(input.capability)
    const current = this.states.get(hash)
    if (!current) return { status: 'stale', state: createKeyModelOpenState(input.capability, input.nowMs) }
    const result = acquireKeyModelRecoveryLease(current, input)
    if (result.status === 'applied') this.states.set(hash, result.state)
    return { status: result.status, state: cloneMemoryState(result.state) }
  }

  async renewRecoveryLease(input: { capabilityHash: string; generation: number; dispatchRevision: number; leaseId: string; nowMs: number }): Promise<boolean> {
    this.cleanup(input.nowMs)
    const state = this.states.get(requiredHash(input.capabilityHash))
    if (!state || state.generation !== input.generation || state.dispatchRevision !== input.dispatchRevision || state.phase !== 'HALF_OPEN') return false
    if (!state.probeLease || state.probeLease.leaseId !== input.leaseId || state.probeLease.leaseUntilMs < input.nowMs) return false
    state.probeLease = { ...state.probeLease, leaseUntilMs: input.nowMs + keyModelProbeLeaseMs }
    this.states.set(state.capabilityHash, state)
    return true
  }

  async settleRecovery(input: { capability: CapabilityKey; generation: number; dispatchRevision: number; leaseId: string; outcome: KeyModelOutcome; nowMs: number }): Promise<{ status: KeyModelMutationStatus; state: KeyModelState }> {
    this.cleanup(input.nowMs)
    const hash = capabilityHash(input.capability)
    const current = this.states.get(hash)
    if (!current) return { status: 'stale', state: createKeyModelOpenState(input.capability, input.nowMs) }
    const result = settleKeyModelRecovery(current, input)
    if (result.status === 'applied') {
      this.states.set(hash, result.state)
      if (result.state.phase === 'CLOSED') this.closedUntil.set(hash, input.nowMs + keyModelClosedRetentionMs)
      else this.closedUntil.delete(hash)
    }
    return { status: result.status, state: cloneMemoryState(result.state) }
  }

  async recordFailure(intent: KeyModelFailureIntent): Promise<KeyModelFailureResult> {
    validateFailureIntent(intent)
    this.cleanup(intent.observedAtMs)
    const hash = capabilityHash(intent.capability)
    if (intent.permit && intent.permit.capabilityHash !== hash) throw new Error('Key-model 失败意图 permit 与 CapabilityKey 不匹配')
    const priorReceipt = this.receipts.get(intent.intentId)
    if (priorReceipt) {
      await this.releaseForegroundIfPresent(intent.permit, hash)
      return { status: 'idempotent', state: cloneMemoryState(priorReceipt.state) }
    }
    const current = this.states.get(hash)
    if (current && current.dispatchRevision > intent.capability.dispatchRevision) {
      await this.releaseForegroundIfPresent(intent.permit, hash)
      return { status: 'stale' }
    }
    // Standalone has no shared Redis clock; use the captured failure fact so
    // memory transitions follow the same deterministic contract.
    const now = intent.observedAtMs
    if (!current && !this.ensureStateCapacity()) {
      await this.releaseForegroundIfPresent(intent.permit, hash)
      return { status: 'capacity_exhausted' }
    }
    const state = current && current.dispatchRevision === intent.capability.dispatchRevision && current.phase !== 'CLOSED'
      ? { ...current, lastObservedAtMs: now, lastOutcome: 'upstream_not_complete' as const }
      : { ...createKeyModelOpenState(intent.capability, now), generation: current ? current.generation + 1 : 1 }
    this.states.set(hash, state)
    this.closedUntil.delete(hash)
    if (intent.recoveryTarget) this.recoveryTargets.set(hash, normalizeRecoveryTarget(intent.recoveryTarget))
    this.receipts.set(intent.intentId, { state: cloneMemoryState(state), expiresAtMs: now + keyModelReceiptRetentionMs })
    await this.releaseForegroundIfPresent(intent.permit, hash)
    return { status: current && current.dispatchRevision === intent.capability.dispatchRevision && current.phase !== 'CLOSED' ? 'idempotent' : 'applied', state: cloneMemoryState(state) }
  }

  async admitForeground(capability: CapabilityKey, attemptId: string): Promise<KeyModelAdmissionResult> {
    const hash = capabilityHash(capability)
    const normalizedAttemptId = requiredText(attemptId, 'attemptId')
    const existing = this.permits.get(hash)?.get(normalizedAttemptId)
    if (existing !== undefined) return { status: 'admitted', permit: { capabilityHash: hash, attemptId: normalizedAttemptId, leaseUntilMs: existing } }
    const now = Date.now()
    this.cleanup(now)
    const state = this.states.get(hash)
    const fence = this.mainFences.get(hash)
    if (fence && fence.expiresAtMs <= now) this.mainFences.delete(hash)
    if ((state && state.dispatchRevision === capability.dispatchRevision && state.phase !== 'CLOSED') || this.mainFences.has(hash)) {
      return { status: 'blocked', wakeSequence: this.wakes.get(hash) ?? 0 }
    }
    const active = this.permits.get(hash) ?? new Map<string, number>()
    for (const [id, lease] of active) if (lease <= now) active.delete(id)
    if (active.size >= keyModelForegroundLimit) return { status: 'busy', wakeSequence: this.wakes.get(hash) ?? 0 }
    const leaseUntilMs = now + keyModelForegroundPrecommitLeaseMs
    active.set(normalizedAttemptId, leaseUntilMs)
    this.permits.set(hash, active)
    return { status: 'admitted', permit: { capabilityHash: hash, attemptId: normalizedAttemptId, leaseUntilMs } }
  }

  async releaseForeground(permit: KeyModelForegroundPermit): Promise<boolean> {
    const active = this.permits.get(requiredHash(permit.capabilityHash))
    if (!active || !active.delete(requiredText(permit.attemptId, 'attemptId'))) return false
    this.wakes.set(permit.capabilityHash, (this.wakes.get(permit.capabilityHash) ?? 0) + 1)
    return true
  }

  async renewForeground(permit: KeyModelForegroundPermit): Promise<KeyModelForegroundPermit | undefined> {
    const hash = requiredHash(permit.capabilityHash)
    const active = this.permits.get(hash)
    if (!active || !active.has(permit.attemptId)) return undefined
    const leaseUntilMs = Date.now() + keyModelForegroundPrecommitLeaseMs
    active.set(permit.attemptId, leaseUntilMs)
    return { ...permit, leaseUntilMs }
  }

  async recordMainProbeFailure(capability: CapabilityKey, permit: KeyModelForegroundPermit): Promise<void> {
    const hash = capabilityHash(capability)
    if (permit.capabilityHash !== hash) throw new Error('MainProbe fence permit 与 CapabilityKey 不匹配')
    await this.releaseForeground(permit)
    this.mainFences.set(hash, { ownerId: permit.attemptId, expiresAtMs: Date.now() + 90_000 })
  }

  async clearMainProbeFence(fence: KeyModelFenceReference, winnerKeyFingerprint: string): Promise<boolean> {
    if (fence.keyFingerprint !== winnerKeyFingerprint) return false
    const current = this.mainFences.get(requiredHash(fence.capabilityHash))
    if (!current || current.ownerId !== fence.ownerId) return false
    this.mainFences.delete(fence.capabilityHash)
    return true
  }

  async deferMainProbeFence(fence: KeyModelFenceReference): Promise<boolean> {
    const current = this.mainFences.get(requiredHash(fence.capabilityHash))
    if (!current || current.ownerId !== fence.ownerId) return false
    current.expiresAtMs = Date.now() + keyModelMainProbeUnknownRetryMs
    return true
  }

  async claimJ1Confirmation(sourceAccountId: string, dispatchRevision: number): Promise<boolean> {
    const key = `${sourceAccountId}:${dispatchRevision}`
    const now = Date.now()
    if ((this.j1Claims.get(key) ?? 0) > now) return false
    this.j1Claims.set(key, now + 2 * 60_000)
    return true
  }

  private async releaseForegroundIfPresent(permit: KeyModelForegroundPermit | undefined, hash: string): Promise<void> {
    if (permit) await this.releaseForeground(permit)
  }

  private cleanup(nowMs: number): void {
    for (const [intentId, receipt] of this.receipts) {
      if (receipt.expiresAtMs <= nowMs) this.receipts.delete(intentId)
    }
    for (const [hash, retainedUntilMs] of this.closedUntil) {
      if (retainedUntilMs > nowMs) continue
      const state = this.states.get(hash)
      if (state?.phase === 'CLOSED') {
        this.states.delete(hash)
        this.recoveryTargets.delete(hash)
      }
      this.closedUntil.delete(hash)
    }
  }

  private ensureStateCapacity(): boolean {
    if (this.states.size < keyModelStateCapacity) return true
    for (const [hash] of [...this.closedUntil].sort((left, right) => left[1] - right[1])) {
      const state = this.states.get(hash)
      if (state?.phase === 'CLOSED') {
        this.states.delete(hash)
        this.recoveryTargets.delete(hash)
      }
      this.closedUntil.delete(hash)
      if (this.states.size < keyModelStateCapacity) return true
    }
    return false
  }
}

function cloneMemoryState(state: KeyModelState): KeyModelState {
  return { ...state, probeLease: state.probeLease ? { ...state.probeLease } : undefined }
}

export async function settleMainProbeFenceOutcome(input: {
  fence: KeyModelFenceReference
  outcome: string
  winnerKeyFingerprint?: string
}): Promise<boolean> {
  const store = createKeyModelRuntimeStore()
  if (input.outcome === 'complete_success') {
    if (!input.winnerKeyFingerprint || input.winnerKeyFingerprint !== input.fence.keyFingerprint) return true
    await store.clearMainProbeFence(input.fence, input.winnerKeyFingerprint)
    return true
  }
  if (input.outcome === 'framing_complete_neutral' || input.outcome === 'probe_task_failure' || input.outcome === 'stale') {
    await store.deferMainProbeFence(input.fence)
    return true
  }
  return true
}

export function createKeyModelRuntimeStore(): KeyModelRuntimeStore {
  return runtimeConfig.runtimeStateDriver === 'redis'
    ? new RedisKeyModelRuntimeStore()
    : inMemoryKeyModelRuntimeStore
}

const inMemoryKeyModelRuntimeStore = new InMemoryKeyModelRuntimeStore()

export function getInMemoryKeyModelRuntimeStore(): InMemoryKeyModelRecoveryStore {
  return inMemoryKeyModelRuntimeStore
}

export function keyModelRedisKeys(): KeyModelRedisKeys {
  const prefix = redisNamespacedKey('gateway-account-circuit-key-model')
  return {
    state: (hash) => `${prefix}:state:${requiredHash(hash)}`,
    due: `${prefix}:due`,
    closed: `${prefix}:closed`,
    receipt: (intentId) => `${prefix}:receipt:${requiredText(intentId, 'intentId')}`,
    admission: (hash) => `${prefix}:admission:${requiredHash(hash)}`,
    admissionLease: (hash, attemptId) => `${prefix}:admissionLease:${requiredHash(hash)}:${requiredText(attemptId, 'attemptId')}`,
    admissionWake: (hash) => `${prefix}:admissionWake:${requiredHash(hash)}`,
    mainProbeFence: (hash) => `${prefix}:mainProbeFence:${requiredHash(hash)}`,
    j1Confirmation: (sourceHash, revision) => `${prefix}:j1Confirmation:${requiredHash(sourceHash)}:${positiveInteger(revision, 'dispatchRevision')}`,
    admissionEvents: `${prefix}:admission-events`,
    capacity: `${prefix}:capacity`
  }
}

export const recordKeyModelFailureScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local incoming = cjson.decode(ARGV[1])
incoming.lastObservedAtMs = now
incoming.retryAtMs = now + 5000
local function releasePermit()
  if redis.call('DEL', KEYS[6]) == 0 then return end
  redis.call('ZREM', KEYS[5], ARGV[5])
  local wake = redis.call('INCR', KEYS[7])
  redis.call('PUBLISH', KEYS[8], ARGV[6] .. ':' .. tostring(wake))
end
local receipt = redis.call('GET', KEYS[3])
if receipt then
  releasePermit()
  local current = redis.call('GET', KEYS[1])
  return {'idempotent', current or receipt}
end
local currentRaw = redis.call('GET', KEYS[1])
if currentRaw then
  local current = cjson.decode(currentRaw)
  if tonumber(current.dispatchRevision) > tonumber(ARGV[2]) then releasePermit(); return {'stale', ''} end
  if tonumber(current.dispatchRevision) == tonumber(ARGV[2]) and current.phase ~= 'CLOSED' then
    current.lastObservedAtMs = now
    current.lastOutcome = 'upstream_not_complete'
    local encoded = cjson.encode(current)
    redis.call('SET', KEYS[1], encoded)
    redis.call('SET', KEYS[3], encoded, 'PX', ARGV[4])
    releasePermit()
    return {'idempotent', encoded}
  end
  incoming.generation = tonumber(current.generation or 0) + 1
elseif tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[3]) then
  local removed = 0
  local closed = redis.call('ZRANGE', KEYS[9], 0, 999)
  for _, hash in ipairs(closed) do
    local closedState = redis.call('GET', string.gsub(KEYS[1], '[^:]+$', hash))
    if closedState and cjson.decode(closedState).phase == 'CLOSED' then
      redis.call('DEL', string.gsub(KEYS[1], '[^:]+$', hash))
      redis.call('ZREM', KEYS[2], hash)
      redis.call('DECR', KEYS[4])
      removed = removed + 1
      if tonumber(redis.call('GET', KEYS[4]) or '0') < tonumber(ARGV[3]) then break end
    end
    redis.call('ZREM', KEYS[9], hash)
  end
  if tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[3]) then
    releasePermit()
    return {'capacity_exhausted', ''}
  end
  redis.call('INCR', KEYS[4])
else
  redis.call('INCR', KEYS[4])
end
local encoded = cjson.encode(incoming)
redis.call('SET', KEYS[1], encoded)
redis.call('ZADD', KEYS[2], incoming.retryAtMs, incoming.capabilityHash)
redis.call('SET', KEYS[3], encoded, 'PX', ARGV[4])
releasePermit()
return {'applied', encoded}
`

export const clearMainProbeFenceScript = `
local current = redis.call('GET', KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {0} end
redis.call('DEL', KEYS[1])
return {1}
`

export const deferMainProbeFenceScript = `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then return {0} end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {1}
`

export const admitKeyModelForegroundScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local existing = redis.call('GET', KEYS[3])
if existing then return {'idempotent', redis.call('GET', KEYS[4]) or '0', existing} end
local stateRaw = redis.call('GET', KEYS[1])
if stateRaw then
  local state = cjson.decode(stateRaw)
  if tonumber(state.dispatchRevision) == tonumber(ARGV[4]) and state.phase ~= 'CLOSED' then
    return {'blocked', redis.call('GET', KEYS[4]) or '0', '0'}
  end
end
if redis.call('EXISTS', KEYS[5]) == 1 then return {'blocked', redis.call('GET', KEYS[4]) or '0', '0'} end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
local count = tonumber(redis.call('ZCARD', KEYS[2]) or '0')
if count >= tonumber(ARGV[3]) then return {'busy', redis.call('GET', KEYS[4]) or '0', '0'} end
local leaseUntil = now + tonumber(ARGV[2])
redis.call('SET', KEYS[3], leaseUntil, 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], leaseUntil, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return {'admitted', redis.call('GET', KEYS[4]) or '0', tostring(leaseUntil)}
`

export const releaseKeyModelForegroundScript = `
if redis.call('DEL', KEYS[2]) == 0 then return {0, redis.call('GET', KEYS[3]) or '0'} end
redis.call('ZREM', KEYS[1], ARGV[2])
local wake = redis.call('INCR', KEYS[3])
redis.call('PUBLISH', KEYS[4], ARGV[1] .. ':' .. tostring(wake))
return {1, wake}
`

export const renewKeyModelForegroundScript = `
local existing = redis.call('GET', KEYS[2])
if not existing then return {'lost', '0'} end
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local leaseUntil = now + tonumber(ARGV[2])
redis.call('SET', KEYS[2], leaseUntil, 'PX', ARGV[2])
redis.call('ZADD', KEYS[1], leaseUntil, ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {'renewed', tostring(leaseUntil)}
`

export const recordMainProbeFenceScript = `
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
if redis.call('DEL', KEYS[3]) == 1 then redis.call('ZREM', KEYS[2], ARGV[1]) end
local wake = redis.call('INCR', KEYS[4])
redis.call('PUBLISH', KEYS[5], ARGV[2] .. ':' .. tostring(wake))
return {'applied', wake}
`

export const claimJ1ConfirmationScript = `
if redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[1]) then return {'claimed'} end
return {'limited'}
`

function validateFailureIntent(intent: KeyModelFailureIntent): void {
  requiredText(intent.intentId, 'intentId')
  requiredText(intent.requestId, 'requestId')
  requiredText(intent.attemptId, 'attemptId')
  requiredText(intent.sourceFence, 'sourceFence')
  if (intent.outcome !== 'upstream_not_complete') throw new Error('Key-model 失败意图 outcome 只能为 upstream_not_complete')
  if (!Number.isSafeInteger(intent.observedAtMs) || intent.observedAtMs < 1) throw new Error('Key-model observedAtMs 无效')
  capabilityHash(intent.capability)
}

function parseState(value: string, expectedHash: string, expectedRevision: number): KeyModelState {
  const parsed = JSON.parse(value) as KeyModelState
  if (parsed.capabilityHash !== expectedHash || parsed.dispatchRevision !== expectedRevision) throw new Error('Key-model Redis state 完整性校验失败')
  return parsed
}

function requiredRedisStateUrl(): string {
  const url = runtimeConfig.redis.stateUrl?.trim()
  if (!url) throw new Error('启用 Key-model Redis state 必须配置 JUHE_AI_REDIS_STATE_URL')
  return url
}

function requiredText(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`Key-model 缺少 ${name}`)
  return normalized
}

function normalizeRecoveryTarget(target: KeyModelRecoveryTarget): KeyModelRecoveryTarget {
  return {
    accountId: requiredText(target.accountId, 'recoveryTarget.accountId'),
    groupId: requiredText(target.groupId, 'recoveryTarget.groupId'),
    systemAccountId: requiredText(target.systemAccountId, 'recoveryTarget.systemAccountId')
  }
}

function requiredHash(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (!/^[a-f0-9]{64}$/.test(normalized)) throw new Error('Key-model capabilityHash 无效')
  return normalized
}

function finiteInteger(value: unknown): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 0) throw new Error(`Key-model Redis 数字结果无效：${String(value)}`)
  return normalized
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 1) throw new Error(`Key-model ${name} 无效`)
  return value
}

function redisArray(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error('Key-model Redis 返回值必须为数组')
  return value
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
