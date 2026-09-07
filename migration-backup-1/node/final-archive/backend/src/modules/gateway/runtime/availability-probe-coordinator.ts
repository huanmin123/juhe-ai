import { randomUUID } from 'node:crypto'

import {
  createRuntimeProbeStateStore,
  type RuntimeProbeStateStore
} from '../../../shared/runtime-probe-state-store.js'

export type AvailabilityProbeKind = 'codex_source_avoidance' | 'account_health_check'
export type AvailabilityProbeOutcome = 'success' | 'health_failure' | 'unknown' | 'probe_task_failure' | 'canceled' | 'stale'

export interface AvailabilityProbeSourceFenceSettlementDisposition {
  disposition: 'retry' | 'terminal'
  /** The generation's already-committed result, when one exists. */
  completedOutcome?: AvailabilityProbeOutcome
}

interface AvailabilityProbeState {
  runtimeKey: string
  generation: number
  nextProbeAtMs: number
  accountRuntimeScope: string
  probeKind: AvailabilityProbeKind
  configRevision: number
  probeRunId?: string
  probeRunUntilMs?: number
  dispatchPending?: boolean
  dispatchPendingUntilMs?: number
  outcome?: AvailabilityProbeOutcome
  completedAtMs?: number
  sourceFences?: string[]
}

export interface ReplacedAvailabilityProbeFenceSettlement {
  generation: number
  configRevision: number
  outcome: AvailabilityProbeOutcome
  sourceFences: AvailabilityProbeSourceFence[]
}

export type AvailabilityProbeAcquireResult =
  | {
    disposition: 'owner'
    runtimeKey: string
    generation: number
    ownerToken: string
    /** Fences from the atomically replaced settled generation. */
    replacedFenceSettlement?: ReplacedAvailabilityProbeFenceSettlement
  }
  | { disposition: 'joined'; runtimeKey: string; generation: number; retryAtMs: number }

// The account health diagnostic deadline is 65 seconds by default. Keep the
// ownership lease longer than the full diagnostic ladder so a healthy owner
// is not taken over while it is still completing its bounded probe.
const defaultLeaseMs = 90_000
const defaultRetentionMs = 5 * 60_000
const availabilityProbeStateStore = createRuntimeProbeStateStore<AvailabilityProbeState>('gateway-availability-probe-coordinator')
let availabilityProbeStateStoreForTest: RuntimeProbeStateStore<AvailabilityProbeState> | undefined

/**
 * Shared, fenced ownership for availability probes. Redis is authoritative
 * whenever the runtime state driver is Redis; the store's memory driver is a
 * deliberate single-process fallback only.
 */
export async function acquireAvailabilityProbe(input: {
  accountRuntimeScope: string
  probeKind: AvailabilityProbeKind
  configRevision: number
  nowMs?: number
  leaseMs?: number
  retentionMs?: number
  sourceFence?: AvailabilityProbeSourceFence
  executionRole?: 'source_dispatch' | 'health_probe'
  /** A new request_failure must not consume a previously settled probe result. */
  forceNewGeneration?: boolean
}): Promise<AvailabilityProbeAcquireResult> {
  const accountRuntimeScope = input.accountRuntimeScope.trim()
  if (!accountRuntimeScope) throw new Error('availability probe requires an account runtime scope')
  const configRevision = normalizedRevision(input.configRevision)
  const runtimeKey = availabilityProbeRuntimeKey(accountRuntimeScope, input.probeKind, configRevision)
  const nowMs = input.nowMs ?? Date.now()
  const leaseMs = normalizedDuration(input.leaseMs ?? defaultLeaseMs)
  const retentionMs = normalizedDuration(input.retentionMs ?? defaultRetentionMs)
  const store = currentAvailabilityProbeStateStore()
  const current = await store.get(runtimeKey)
  const ownerToken = randomUUID()

  if (!current) {
    const generation = await store.nextGeneration(runtimeKey, retentionMs)
    const state: AvailabilityProbeState = {
      runtimeKey,
      generation,
      nextProbeAtMs: nowMs + leaseMs,
      accountRuntimeScope,
      probeKind: input.probeKind,
      configRevision,
      probeRunId: ownerToken,
      probeRunUntilMs: nowMs + leaseMs,
      ...(input.sourceFence ? { sourceFences: [encodeSourceFence(input.sourceFence)] } : {})
    }
    if (await store.setIfAbsent(state, retentionMs)) {
      return { disposition: 'owner', runtimeKey, generation, ownerToken }
    }
    return await joinOrTakeOverAvailabilityProbe(store, runtimeKey, ownerToken, nowMs, leaseMs, retentionMs, undefined, input.sourceFence, input.executionRole, {
      accountRuntimeScope,
      probeKind: input.probeKind,
      configRevision
    })
  }
  if (
    current.outcome !== undefined
    && (input.forceNewGeneration || (input.executionRole === 'source_dispatch' && input.sourceFence))
  ) {
    return await replaceSettledAvailabilityProbeGeneration({
      store,
      current,
      runtimeKey,
      accountRuntimeScope,
      probeKind: input.probeKind,
      configRevision,
      ownerToken,
      nowMs,
      leaseMs,
      retentionMs,
      sourceFence: input.sourceFence,
      executionRole: input.executionRole
    })
  }
  return await joinOrTakeOverAvailabilityProbe(store, runtimeKey, ownerToken, nowMs, leaseMs, retentionMs, current, input.sourceFence, input.executionRole, {
    accountRuntimeScope,
    probeKind: input.probeKind,
    configRevision
  })
}

export interface AvailabilityProbeSourceFence {
  stateKey: string
  accountId: string
  sourceGeneration: number
  sourceFenceId: string
}

export async function availabilityProbeSourceFences(runtimeKey: string, generation: number): Promise<AvailabilityProbeSourceFence[]> {
  const state = await currentAvailabilityProbeStateStore().get(runtimeKey)
  if (!state || state.generation !== generation) return []
  return (state.sourceFences ?? []).flatMap(decodeSourceFence)
}

export async function settleAvailabilityProbe(input: {
  runtimeKey: string
  generation: number
  ownerToken: string
  outcome: AvailabilityProbeOutcome
  nowMs?: number
  retentionMs?: number
}): Promise<boolean> {
  const current = await currentAvailabilityProbeStateStore().get(input.runtimeKey)
  if (!current || current.generation !== input.generation || current.probeRunId !== input.ownerToken) return false
  const nowMs = input.nowMs ?? Date.now()
  const next: AvailabilityProbeState = {
    ...current,
    nextProbeAtMs: nowMs,
    probeRunId: undefined,
    probeRunUntilMs: undefined,
    dispatchPending: undefined,
    dispatchPendingUntilMs: undefined,
    outcome: input.outcome,
    completedAtMs: nowMs
  }
  // commitGenerationRun is the fencing point. A stale owner cannot settle a
  // replacement generation or a lease that another owner has taken over.
  return await currentAvailabilityProbeStateStore().commitGenerationRun(
    next,
    input.ownerToken,
    normalizedDuration(input.retentionMs ?? defaultRetentionMs)
  )
}

/**
 * Hands an acquired generation to the component that will execute the actual
 * availability probe. The owner token fences this hand-off, so a stale source
 * activation cannot make a replacement generation runnable.
 */
export async function releaseAvailabilityProbeForExecution(input: {
  runtimeKey: string
  generation: number
  ownerToken: string
  nowMs?: number
  leaseMs?: number
  retentionMs?: number
}): Promise<boolean> {
  const store = currentAvailabilityProbeStateStore()
  const current = await store.get(input.runtimeKey)
  if (
    !current
    || current.generation !== input.generation
    || current.probeRunId !== input.ownerToken
    || current.outcome !== undefined
  ) {
    return false
  }
  const next: AvailabilityProbeState = {
    ...current,
    nextProbeAtMs: input.nowMs ?? Date.now(),
    probeRunId: undefined,
    probeRunUntilMs: undefined,
    dispatchPending: true,
    dispatchPendingUntilMs: (input.nowMs ?? Date.now()) + normalizedDuration(input.leaseMs ?? defaultLeaseMs)
  }
  return await store.commitGenerationRun(
    next,
    input.ownerToken,
    normalizedDuration(input.retentionMs ?? defaultRetentionMs)
  )
}

/**
 * Completes a generation handed to another process. The returned worker
 * message carries the original fence and generation; it can therefore never
 * settle a replacement generation or an unrelated source.
 */
export async function settleDispatchedAvailabilityProbeBySourceFence(input: {
  runtimeKey: string
  generation: number
  sourceFence: AvailabilityProbeSourceFence
  outcome: AvailabilityProbeOutcome
  nowMs?: number
  leaseMs?: number
  retentionMs?: number
}): Promise<boolean> {
  const store = currentAvailabilityProbeStateStore()
  const current = await store.get(input.runtimeKey)
  if (
    !current
    || current.generation !== input.generation
    || current.outcome !== undefined
    || current.dispatchPending !== true
    || !(current.sourceFences ?? []).includes(encodeSourceFence(input.sourceFence))
  ) {
    return false
  }
  const ownerToken = randomUUID()
  const nowMs = input.nowMs ?? Date.now()
  const taken = await store.acquireGenerationRun(
    input.runtimeKey,
    input.generation,
    ownerToken,
    nowMs + normalizedDuration(input.leaseMs ?? defaultLeaseMs),
    normalizedDuration(input.retentionMs ?? defaultRetentionMs)
  )
  if (taken?.probeRunId !== ownerToken) return false
  return await settleAvailabilityProbe({
    runtimeKey: input.runtimeKey,
    generation: input.generation,
    ownerToken,
    outcome: input.outcome,
    nowMs,
    retentionMs: input.retentionMs
  })
}

// Distinguishes a durable outcome that is merely early from one that belongs
// to a replaced/already-completed generation. The Go outcome cursor must not
// skip the former, while the latter is safe to acknowledge.
export async function availabilityProbeSourceFenceSettlementDisposition(input: {
  runtimeKey: string
  generation: number
  sourceFence: AvailabilityProbeSourceFence
  /** Test-only clock injection; production callers use the wall clock. */
  nowMs?: number
}): Promise<AvailabilityProbeSourceFenceSettlementDisposition> {
  const current = await currentAvailabilityProbeStateStore().get(input.runtimeKey)
  // The coordinator state is deliberately ephemeral. Once its retention has
  // elapsed or a Gateway restarts without the memory fallback, no later
  // source-fenced outcome can safely recreate or settle that generation.
  // It is therefore terminal, rather than an unbounded cursor retry.
  if (!current) return { disposition: 'terminal' }
  if (current.generation !== input.generation) return { disposition: 'terminal' }
  if (!(current.sourceFences ?? []).includes(encodeSourceFence(input.sourceFence))) return { disposition: 'terminal' }
  if (current.outcome !== undefined) return { disposition: 'terminal', completedOutcome: current.outcome }
  const nowMs = input.nowMs ?? Date.now()
  if ((current.probeRunUntilMs ?? 0) > nowMs) return { disposition: 'retry' }
  if (current.dispatchPending === true && (current.dispatchPendingUntilMs ?? 0) > nowMs) return { disposition: 'retry' }
  return { disposition: 'terminal' }
}

export async function getAvailabilityProbeState(runtimeKey: string): Promise<Readonly<AvailabilityProbeState> | undefined> {
  return await currentAvailabilityProbeStateStore().get(runtimeKey)
}

export const getAvailabilityProbeStateForTest = getAvailabilityProbeState

export function availabilityProbeRuntimeKey(
  accountRuntimeScope: string,
  probeKind: AvailabilityProbeKind,
  configRevision: number
): string {
  return `availability:${accountRuntimeScope.trim()}:${probeKind}:r${normalizedRevision(configRevision)}`
}

export function setAvailabilityProbeStateStoreForTest(store: RuntimeProbeStateStore<AvailabilityProbeState> | undefined): void {
  availabilityProbeStateStoreForTest = store
}

async function joinOrTakeOverAvailabilityProbe(
  store: RuntimeProbeStateStore<AvailabilityProbeState>,
  runtimeKey: string,
  ownerToken: string,
  nowMs: number,
  leaseMs: number,
  retentionMs: number,
  providedCurrent?: AvailabilityProbeState,
  sourceFence?: AvailabilityProbeSourceFence,
  executionRole?: 'source_dispatch' | 'health_probe',
  replacement?: Pick<AvailabilityProbeState, 'accountRuntimeScope' | 'probeKind' | 'configRevision'>
): Promise<AvailabilityProbeAcquireResult> {
  let current = providedCurrent ?? await store.get(runtimeKey)
  if (!current) {
    // The contender won neither the absent write nor a stable read. Treat it
    // as a bounded join and let the caller retry after one lease interval.
    return { disposition: 'joined', runtimeKey, generation: 0, retryAtMs: nowMs + leaseMs }
  }
  if (sourceFence) {
    const merged = await store.merge({
      ...current,
      sourceFences: [encodeSourceFence(sourceFence)]
    }, retentionMs, {
      preserveCurrentFields: ['probeRunId', 'probeRunUntilMs', 'outcome', 'completedAtMs'],
      unionArrayFields: [{ field: 'sourceFences', maxItems: 64 }]
    })
    current = merged ?? current
  }
  if (current.outcome) {
    if (sourceFence && executionRole === 'source_dispatch' && replacement) {
      // merge() is also the completion/join boundary. If the owner settled
      // before this fence was merged, atomically replace that settled epoch;
      // never let a late fence consume the old success.
      return await replaceSettledAvailabilityProbeGeneration({
        store,
        current,
        runtimeKey,
        ...replacement,
        ownerToken,
        nowMs,
        leaseMs,
        retentionMs,
        sourceFence
      })
    }
    return {
      disposition: 'joined',
      runtimeKey,
      generation: current.generation,
      retryAtMs: current.completedAtMs ?? nowMs
    }
  }
  if (current.dispatchPending && executionRole === 'source_dispatch' && (current.dispatchPendingUntilMs ?? 0) > nowMs) {
    // The source owner already accepted one background dispatch. Source
    // observers must keep joining until that worker acquires the same
    // generation, otherwise a quick release could append a second follow-up.
    return { disposition: 'joined', runtimeKey, generation: current.generation, retryAtMs: current.dispatchPendingUntilMs! }
  }
  if ((current.probeRunUntilMs ?? 0) > nowMs) {
    return { disposition: 'joined', runtimeKey, generation: current.generation, retryAtMs: current.probeRunUntilMs! }
  }
  const taken = await store.acquireGenerationRun(runtimeKey, current.generation, ownerToken, nowMs + leaseMs, retentionMs)
  if (taken?.probeRunId === ownerToken) {
    return { disposition: 'owner', runtimeKey, generation: current.generation, ownerToken }
  }
  const latest = taken ?? await store.get(runtimeKey)
  return {
    disposition: 'joined',
    runtimeKey,
    generation: latest?.generation ?? current.generation,
    retryAtMs: latest?.probeRunUntilMs ?? latest?.nextProbeAtMs ?? nowMs + leaseMs
  }
}

async function replaceSettledAvailabilityProbeGeneration(input: {
  store: RuntimeProbeStateStore<AvailabilityProbeState>
  current: AvailabilityProbeState
  runtimeKey: string
  accountRuntimeScope: string
  probeKind: AvailabilityProbeKind
  configRevision: number
  ownerToken: string
  nowMs: number
  leaseMs: number
  retentionMs: number
  sourceFence?: AvailabilityProbeSourceFence
  executionRole?: 'source_dispatch' | 'health_probe'
}): Promise<AvailabilityProbeAcquireResult> {
  // Replace a settled result in one state-store transaction: deleting first
  // opens a window where a concurrent source can join the stale generation.
  const generation = await input.store.nextGeneration(input.runtimeKey, input.retentionMs)
  const next: AvailabilityProbeState = {
    runtimeKey: input.runtimeKey,
    generation,
    nextProbeAtMs: input.nowMs + input.leaseMs,
    accountRuntimeScope: input.accountRuntimeScope,
    probeKind: input.probeKind,
    configRevision: input.configRevision,
    probeRunId: input.ownerToken,
    probeRunUntilMs: input.nowMs + input.leaseMs,
    ...(input.sourceFence ? { sourceFences: [encodeSourceFence(input.sourceFence)] } : {})
  }
  const replaced = await input.store.replaceSettledGeneration(next, input.current.generation, input.retentionMs)
  if (!replaced) {
    const latest = await input.store.get(input.runtimeKey)
    if (latest?.outcome !== undefined) {
      return await replaceSettledAvailabilityProbeGeneration({ ...input, current: latest, ownerToken: randomUUID() })
    }
    return latest
      ? await joinOrTakeOverAvailabilityProbe(
          input.store,
          input.runtimeKey,
          input.ownerToken,
          input.nowMs,
          input.leaseMs,
          input.retentionMs,
          latest,
          input.sourceFence,
          input.executionRole,
          input
        )
      : { disposition: 'joined', runtimeKey: input.runtimeKey, generation: input.current.generation, retryAtMs: input.nowMs + input.leaseMs }
  }
  const replacedFenceSettlement = settlementFromReplacedGeneration(replaced)
  return {
    disposition: 'owner',
    runtimeKey: input.runtimeKey,
    generation,
    ownerToken: input.ownerToken,
    ...(replacedFenceSettlement ? { replacedFenceSettlement } : {})
  }
}

function settlementFromReplacedGeneration(state: AvailabilityProbeState): ReplacedAvailabilityProbeFenceSettlement | undefined {
  if (!state.outcome) return undefined
  return {
    generation: state.generation,
    configRevision: state.configRevision,
    outcome: state.outcome,
    sourceFences: (state.sourceFences ?? []).flatMap(decodeSourceFence)
  }
}

function encodeSourceFence(fence: AvailabilityProbeSourceFence): string {
  return JSON.stringify([fence.stateKey, fence.accountId, fence.sourceGeneration, fence.sourceFenceId])
}

function decodeSourceFence(value: string): AvailabilityProbeSourceFence[] {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed) || parsed.length !== 4) return []
    const [stateKey, accountId, sourceGeneration, sourceFenceId] = parsed
    return typeof stateKey === 'string' && stateKey && typeof accountId === 'string' && accountId
      && typeof sourceGeneration === 'number' && Number.isFinite(sourceGeneration)
      && typeof sourceFenceId === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(sourceFenceId)
      ? [{ stateKey, accountId, sourceGeneration, sourceFenceId: sourceFenceId.toLowerCase() }]
      : []
  } catch {
    return []
  }
}

function currentAvailabilityProbeStateStore(): RuntimeProbeStateStore<AvailabilityProbeState> {
  return availabilityProbeStateStoreForTest ?? availabilityProbeStateStore
}

function normalizedDuration(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}

function normalizedRevision(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}
