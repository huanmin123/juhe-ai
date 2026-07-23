import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import {
  getRequestContext,
  getRequestLogger,
  type GatewayRoutingDispatchSummary
} from '../../../shared/request-context.js'
import { MemoryGatewayRoutingObservabilityStore } from './routing-observability-memory-store.js'
import { RedisGatewayRoutingObservabilityStore } from './routing-observability-redis-store.js'
import {
  gatewayRoutingObservationMetricKey,
  type GatewayRoutingObservation,
  type GatewayRoutingObservationBatchEntry,
  type GatewayRoutingObservabilitySnapshot,
  type GatewayRoutingObservabilityStore
} from './routing-observability-store.js'

const perRequestObservationLimit = 128
const failureLogThrottleMs = 30_000

let storeSingleton: GatewayRoutingObservabilityStore | undefined
let storeIdentity = ''
let lastFailureLogAtMs = 0
let pendingObservations = new Map<string, GatewayRoutingObservationBatchEntry>()
let pendingObservationNowMs = 0
let observationFlushScheduled = false
let observationGeneration = 0

export async function recordGatewayRoutingObservation(
  observation: GatewayRoutingObservation,
  nowMs = Date.now()
): Promise<boolean> {
  captureRequestDispatchSummary(observation)
  logRoutingObservation(observation)
  try {
    await getGatewayRoutingObservabilityStore().record(observation, nowMs)
    return true
  } catch (error) {
    if (nowMs - lastFailureLogAtMs >= failureLogThrottleMs) {
      lastFailureLogAtMs = nowMs
      getRequestLogger().warn({
        event: 'gateway_routing_observability_write_failed',
        observationKind: observation.kind,
        error
      }, '网关路由观测写入失败')
    }
    return false
  }
}

export function observeGatewayRouting(observation: GatewayRoutingObservation, nowMs = Date.now()): void {
  captureRequestDispatchSummary(observation)
  logRoutingObservation(observation)
  const key = gatewayRoutingObservationMetricKey(observation)
  const pending = pendingObservations.get(key)
  if (pending) pending.count = Math.min(Number.MAX_SAFE_INTEGER, pending.count + 1)
  else pendingObservations.set(key, { observation, count: 1 })
  pendingObservationNowMs = Math.max(pendingObservationNowMs, normalizedObservationNow(nowMs))
  if (observationFlushScheduled) return
  observationFlushScheduled = true
  const generation = observationGeneration
  queueMicrotask(() => { void flushPendingObservations(generation) })
}

export function getGatewayRoutingObservabilityStore(): GatewayRoutingObservabilityStore {
  const identity = routingObservabilityStoreIdentity()
  if (storeSingleton && storeIdentity === identity) return storeSingleton

  if (runtimeConfig.runtimeMode === 'standalone') {
    if (runtimeConfig.runtimeStateDriver !== 'memory') {
      throw new Error('standalone routing observability 要求 memory runtime state driver')
    }
    storeSingleton = new MemoryGatewayRoutingObservabilityStore()
  } else {
    if (runtimeConfig.runtimeStateDriver !== 'redis') {
      throw new Error('performance routing observability 要求 redis runtime state driver')
    }
    const redisUrl = runtimeConfig.redis.stateUrl?.trim()
    if (!redisUrl) throw new Error('performance routing observability 缺少 JUHE_AI_REDIS_STATE_URL')
    storeSingleton = new RedisGatewayRoutingObservabilityStore(redisUrl)
  }
  storeIdentity = identity
  return storeSingleton
}

export function getGatewayRoutingObservabilitySnapshot(): Promise<GatewayRoutingObservabilitySnapshot> {
  return getGatewayRoutingObservabilityStore().snapshot()
}

export function resetGatewayRoutingObservabilityForTest(): void {
  observationGeneration += 1
  storeSingleton = undefined
  storeIdentity = ''
  lastFailureLogAtMs = 0
  pendingObservations.clear()
  pendingObservationNowMs = 0
  observationFlushScheduled = false
}

async function flushPendingObservations(generation: number): Promise<void> {
  if (generation !== observationGeneration) return
  observationFlushScheduled = false
  const batch = [...pendingObservations.values()]
  pendingObservations = new Map()
  const nowMs = pendingObservationNowMs
  pendingObservationNowMs = 0
  if (batch.length === 0) return
  try {
    await getGatewayRoutingObservabilityStore().recordBatch(batch, nowMs)
  } catch (error) {
    if (nowMs - lastFailureLogAtMs >= failureLogThrottleMs) {
      lastFailureLogAtMs = nowMs
      getRequestLogger().warn({
        event: 'gateway_routing_observability_write_failed',
        observationKind: 'batch',
        error
      }, '网关路由观测批量写入失败')
    }
  }
}

function normalizedObservationNow(value: number): number {
  return Number.isSafeInteger(value) && value >= 0 ? value : Date.now()
}

function captureRequestDispatchSummary(observation: GatewayRoutingObservation): void {
  const context = getRequestContext()
  if (!context) return
  const summary = context.gatewayRoutingDispatchSummary ??= emptyDispatchSummary()
  if (summary.observedEvents >= perRequestObservationLimit) {
    summary.droppedEvents = Math.min(Number.MAX_SAFE_INTEGER, summary.droppedEvents + 1)
    return
  }
  summary.observedEvents += 1
  switch (observation.kind) {
    case 'attempt':
      if (observation.outcome === 'started') summary.attemptsStarted += 1
      else if (observation.outcome === 'completed') summary.attemptsCompleted += 1
      else summary.attemptsFailed += 1
      return
    case 'circuit_transition':
      if (observation.from !== observation.to) summary.circuitTransitions += 1
      return
    case 'circuit_mutation':
      if (observation.status !== 'applied') summary.circuitSkips += 1
      if (observation.status === 'stale_generation' || observation.status === 'stale_dispatch_revision' || observation.status === 'state_mismatch') {
        summary.circuitCasConflicts += 1
      }
      if (observation.leaseKind) {
        if (observation.status === 'applied') summary.circuitLeasesAcquired += 1
        else summary.circuitLeasesRejected += 1
      }
      return
    case 'circuit_dispatch':
      summary.circuitSkips += 1
      return
    case 'hot_quality_mutation':
      if (observation.status === 'idempotent') summary.hotQualityDeduplications += 1
      if (observation.status === 'conflict') summary.hotQualityConflicts += 1
      return
    case 'exploration':
      if (observation.outcome === 'reserved') summary.explorationsReserved += 1
      if (observation.outcome === 'dispatched') summary.explorationsDispatched += 1
      return
    case 'tier_escape':
      if (observation.outcome === 'applied') summary.tierEscapes += 1
      return
    case 'budget':
      if (observation.outcome === 'wall_exhausted') summary.wallBudgetExhausted += 1
      if (observation.outcome === 'precommit_clipped') summary.precommitClipped += 1
      if (observation.outcome === 'client_handoff') summary.clientHandoffs += 1
  }
}

function emptyDispatchSummary(): GatewayRoutingDispatchSummary {
  return {
    observedEvents: 0,
    droppedEvents: 0,
    attemptsStarted: 0,
    attemptsCompleted: 0,
    attemptsFailed: 0,
    circuitTransitions: 0,
    circuitSkips: 0,
    circuitCasConflicts: 0,
    circuitLeasesAcquired: 0,
    circuitLeasesRejected: 0,
    hotQualityDeduplications: 0,
    hotQualityConflicts: 0,
    explorationsReserved: 0,
    explorationsDispatched: 0,
    tierEscapes: 0,
    wallBudgetExhausted: 0,
    precommitClipped: 0,
    clientHandoffs: 0
  }
}

function logRoutingObservation(observation: GatewayRoutingObservation): void {
  const requestLogger = getRequestLogger()
  if (observation.kind === 'circuit_transition') {
    if (observation.from === observation.to) return
    const fields = {
      event: 'gateway_account_circuit_transition',
      from: observation.from,
      to: observation.to,
      source: observation.source
    }
    if (observation.to === 'OPEN') requestLogger.warn(fields, '账户短电路状态转换')
    else requestLogger.info(fields, '账户短电路状态转换')
    return
  }
  if (observation.kind === 'circuit_mutation' && observation.status !== 'applied') {
    requestLogger.debug({
      event: 'gateway_account_circuit_dispatch_skipped',
      operation: observation.operation,
      status: observation.status,
      leaseKind: observation.leaseKind
    }, '账户短电路派发被跳过')
    return
  }
  if (observation.kind === 'circuit_dispatch') {
    requestLogger.debug({
      event: 'gateway_account_circuit_dispatch_skipped',
      outcome: observation.outcome,
      phase: observation.phase
    }, '账户短电路派发被跳过')
  }
}

function routingObservabilityStoreIdentity(): string {
  if (runtimeConfig.runtimeMode === 'standalone') return 'standalone:memory'
  const redisUrl = runtimeConfig.redis.stateUrl?.trim()
  if (runtimeConfig.runtimeStateDriver !== 'redis' || !redisUrl) {
    throw new Error('performance routing observability 要求 Redis runtime state')
  }
  return `performance:redis:${createHash('sha256').update(redisUrl).digest('hex')}`
}
