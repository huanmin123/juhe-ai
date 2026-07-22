import { createHash, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import {
  decideHotQualityCandidate,
  gatewayAccountConfigurationTierKey,
  type HotQualityCandidate,
  type HotQualityRoutingMode,
  type SameTierExplorationState
} from '../routing/hot-quality-candidate-selection.js'
import { gatewayAccountRuntimeKey } from './account-runtime-keys.js'
import { MemoryHotQualityStore } from './hot-quality-memory-store.js'
import { RedisHotQualityStore } from './hot-quality-redis-store.js'
import {
  HOT_QUALITY_UNKNOWN_MODEL_FAMILY,
  type HotQualityScope,
  type HotQualityStore
} from './hot-quality-store.js'
import { MemorySameTierExplorationStore } from './same-tier-exploration-memory-store.js'
import { RedisSameTierExplorationStore } from './same-tier-exploration-redis-store.js'
import type {
  SameTierExplorationReservation,
  SameTierExplorationStore
} from './same-tier-exploration-store.js'

const gatewayHotQualityCapacity = 10_000
const explorationReservationLeaseMs = 15_000

export interface GatewayHotQualityCandidateOrderInput {
  accounts: readonly UpstreamAccount[]
  modelPriority: GatewayAccountModelPriority
  mode: HotQualityRoutingMode
  systemAccountId: string
  routeStrategyId?: string
  groupId: string
  requestLane: OpenAIGatewayRequestLane
  requestId: string
  latencyDegradedAccountIds?: ReadonlySet<string>
  stableBindingOrderByRuntimeKey?: ReadonlyMap<string, number>
  eligibleFirstPrimaryDispatch: boolean
  nowMs?: number
}

export interface GatewayHotQualityExplorationReservation extends SameTierExplorationReservation {
  poolKey: string
}

export interface GatewayHotQualityCandidateOrderResult {
  accounts: UpstreamAccount[]
  qualityReorderedTierKeys: readonly string[]
  latencyDegradedOverrideApplied: boolean
  selectedAccountId?: string
  dispatchIntent: 'primary_service' | 'same_tier_exploration'
  explorationStatus: string
  explorationReservation?: GatewayHotQualityExplorationReservation
  settleExplorationAfterDispatch?: (outcome: 'dispatched' | 'not_dispatched') => Promise<void>
}

export interface GatewayHotQualityRuntime {
  hotQualityStore: HotQualityStore
  explorationStore: SameTierExplorationStore
}

let hotQualityStoreSingleton: HotQualityStore | undefined
let explorationStoreSingleton: SameTierExplorationStore | undefined
let runtimeIdentity = ''

export function getGatewayHotQualityRuntime(): GatewayHotQualityRuntime {
  const identity = gatewayHotQualityRuntimeIdentity()
  if (runtimeIdentity !== identity || !hotQualityStoreSingleton || !explorationStoreSingleton) {
    const redisUrl = runtimeConfig.redis.stateUrl?.trim()
    if (runtimeConfig.runtimeMode === 'standalone') {
      if (runtimeConfig.runtimeStateDriver !== 'memory') {
        throw new Error('standalone 热质量要求 memory runtime state driver')
      }
      hotQualityStoreSingleton = new MemoryHotQualityStore({ keyCapacity: gatewayHotQualityCapacity })
      explorationStoreSingleton = new MemorySameTierExplorationStore()
    } else {
      if (runtimeConfig.runtimeStateDriver !== 'redis') {
        throw new Error('performance 热质量要求 redis runtime state driver')
      }
      if (!redisUrl) {
        throw new Error('performance 热质量缺少 JUHE_AI_REDIS_STATE_URL')
      }
      hotQualityStoreSingleton = new RedisHotQualityStore({ redisUrl, keyCapacity: gatewayHotQualityCapacity })
      explorationStoreSingleton = new RedisSameTierExplorationStore({ redisUrl })
    }
    runtimeIdentity = identity
  }
  return { hotQualityStore: hotQualityStoreSingleton, explorationStore: explorationStoreSingleton }
}

export function resetGatewayHotQualityRuntimeForTest(): void {
  hotQualityStoreSingleton = undefined
  explorationStoreSingleton = undefined
  runtimeIdentity = ''
}

export async function orderGatewayAccountsByHotQualityAsync(
  input: GatewayHotQualityCandidateOrderInput
): Promise<GatewayHotQualityCandidateOrderResult> {
  const nowMs = normalizedNow(input.nowMs ?? Date.now())
  if (input.accounts.length === 0) {
    return {
      accounts: [],
      qualityReorderedTierKeys: [],
      latencyDegradedOverrideApplied: false,
      dispatchIntent: 'primary_service',
      explorationStatus: 'no_candidate'
    }
  }
  const runtime = getGatewayHotQualityRuntime()
  const stableOrders = stableBindingOrders(input.accounts, input.stableBindingOrderByRuntimeKey)
  const byProtocolProfile = groupByProtocolProfile(input.accounts)
  const orderedGroups: UpstreamAccount[][] = []
  let firstResult: Omit<GatewayHotQualityCandidateOrderResult, 'accounts'> | undefined

  for (const [protocolProfile, accounts] of byProtocolProfile) {
    const routeScopeKey = gatewayHotQualityRouteScopeKey({
      systemAccountId: input.systemAccountId,
      routeStrategyId: input.routeStrategyId,
      groupId: input.groupId,
      protocolProfile,
      requestLane: input.requestLane
    })
    const candidateScopes = accounts.map((account) => hotQualityScopeForAccount(account, input.requestLane))
    const snapshots = await Promise.all(candidateScopes.map((scope) => runtime.hotQualityStore.get(scope, nowMs)))
    const candidates: HotQualityCandidate[] = accounts.map((account, index) => {
      const runtimeKey = gatewayAccountRuntimeKey(account)
      return {
        accountId: account.id,
        accountRuntimeKey: runtimeKey,
        routeScopeKey,
        configurationTier: {
          modelMatchRank: modelRank(account, input.modelPriority),
          fallbackEnabled: account.fallbackEnabled,
          superPriorityEnabled: account.superPriorityEnabled,
          priority: account.priority
        },
        stableBindingOrder: stableOrders.get(runtimeKey) ?? index,
        hotQuality: snapshots[index],
        latencyDegraded: input.latencyDegradedAccountIds?.has(account.id) === true
      }
    })

    const isFirstProtocolGroup = firstResult === undefined
    const selection = isFirstProtocolGroup
      ? await selectFirstProtocolGroup({ runtime, input, candidates, routeScopeKey, protocolProfile, nowMs })
      : (() => {
        const decision = decideHotQualityCandidate({ mode: input.mode, routeScopeKey, candidates })
        return {
          accounts: decision.orderedCandidates,
          result: {
            qualityReorderedTierKeys: decision.explanation.qualityReorderedTierKeys,
            latencyDegradedOverrideApplied: decision.explanation.latencyDegradedOverrideApplied,
            selectedAccountId: decision.selectedCandidate?.accountId,
            dispatchIntent: decision.dispatchIntent,
            explorationStatus: decision.explanation.exploration.status
          } satisfies Omit<GatewayHotQualityCandidateOrderResult, 'accounts'>
        }
      })()
    const accountsByRuntimeKey = new Map(accounts.map((account) => [gatewayAccountRuntimeKey(account), account]))
    const orderedAccounts = selection.accounts.map((candidate) => {
      const account = accountsByRuntimeKey.get(candidate.accountRuntimeKey)
      if (!account) throw new Error(`热质量候选缺少账号 ${candidate.accountRuntimeKey}`)
      return account
    })
    orderedGroups.push(orderedAccounts)
    if (isFirstProtocolGroup) firstResult = selection.result
  }

  const first: Omit<GatewayHotQualityCandidateOrderResult, 'accounts'> = firstResult ?? {
    qualityReorderedTierKeys: [],
    latencyDegradedOverrideApplied: false,
    dispatchIntent: 'primary_service' as const,
    explorationStatus: 'no_candidate'
  }
  return {
    accounts: orderedGroups.flat(),
    qualityReorderedTierKeys: first.qualityReorderedTierKeys,
    latencyDegradedOverrideApplied: first.latencyDegradedOverrideApplied,
    selectedAccountId: first.selectedAccountId,
    dispatchIntent: first.dispatchIntent,
    explorationStatus: first.explorationStatus,
    explorationReservation: first.explorationReservation,
    settleExplorationAfterDispatch: first.settleExplorationAfterDispatch
  }
}

async function selectFirstProtocolGroup(input: {
  runtime: GatewayHotQualityRuntime
  input: GatewayHotQualityCandidateOrderInput
  candidates: HotQualityCandidate[]
  routeScopeKey: string
  protocolProfile: string
  nowMs: number
}): Promise<{
  accounts: readonly HotQualityCandidate[]
  result: Omit<GatewayHotQualityCandidateOrderResult, 'accounts'>
}> {
  const topCandidate = input.candidates[0]
  if (!topCandidate) throw new Error('热质量首协议候选为空')
  const poolKey = sameTierExplorationPoolKey(input.routeScopeKey, gatewayAccountConfigurationTierKey(topCandidate.configurationTier))
  const accrualToken = `${requiredKey(input.input.requestId, 'requestId')}:${input.protocolProfile}`
  const sharedState = await input.runtime.explorationStore.accrue({
    poolKey,
    accrualToken,
    eligible: input.input.eligibleFirstPrimaryDispatch,
    nowMs: input.nowMs
  })
  const decision = decideHotQualityCandidate({
    mode: input.input.mode,
    routeScopeKey: input.routeScopeKey,
    candidates: input.candidates,
    exploration: sameTierExplorationDecisionState(
      sharedState,
      input.nowMs,
      input.input.eligibleFirstPrimaryDispatch
    )
  })
  if (decision.dispatchIntent !== 'same_tier_exploration' || !decision.selectedCandidate) {
    return {
      accounts: decision.orderedCandidates,
      result: {
        qualityReorderedTierKeys: decision.explanation.qualityReorderedTierKeys,
        latencyDegradedOverrideApplied: decision.explanation.latencyDegradedOverrideApplied,
        selectedAccountId: decision.selectedCandidate?.accountId,
        dispatchIntent: decision.dispatchIntent,
        explorationStatus: decision.explanation.exploration.status
      }
    }
  }

  const reservationId = randomUUID()
  const reservation = await input.runtime.explorationStore.reserve({
    poolKey,
    reservationId,
    accountRuntimeKey: decision.selectedCandidate.accountRuntimeKey,
    leaseUntilMs: input.nowMs + explorationReservationLeaseMs,
    nowMs: input.nowMs
  })
  if (reservation.status !== 'reserved' || !reservation.reservation) {
    return {
      accounts: decision.qualityOrderedCandidates,
      result: {
        qualityReorderedTierKeys: decision.explanation.qualityReorderedTierKeys,
        latencyDegradedOverrideApplied: decision.explanation.latencyDegradedOverrideApplied,
        selectedAccountId: decision.qualityOrderedCandidates[0]?.accountId,
        dispatchIntent: 'primary_service',
        explorationStatus: `reservation_${reservation.status}`
      }
    }
  }
  const selectedReservation: GatewayHotQualityExplorationReservation = {
    poolKey,
    ...reservation.reservation
  }
  return {
    accounts: decision.orderedCandidates,
    result: {
      qualityReorderedTierKeys: decision.explanation.qualityReorderedTierKeys,
      latencyDegradedOverrideApplied: decision.explanation.latencyDegradedOverrideApplied,
      selectedAccountId: decision.selectedCandidate.accountId,
      dispatchIntent: 'same_tier_exploration',
      explorationStatus: 'reserved',
      explorationReservation: selectedReservation,
      settleExplorationAfterDispatch: async (outcome) => {
        await input.runtime.explorationStore.settle({
          poolKey,
          reservationId: selectedReservation.reservationId,
          accountRuntimeKey: selectedReservation.accountRuntimeKey,
          outcome
        })
      }
    }
  }
}

function sameTierExplorationDecisionState(
  state: Awaited<ReturnType<SameTierExplorationStore['get']>>,
  nowMs: number,
  eligibleFirstPrimaryDispatch: boolean
): SameTierExplorationState {
  return {
    enabled: true,
    eligibleFirstPrimaryDispatch,
    creditAccrualAlreadyApplied: true,
    requestAlreadyExplored: false,
    hasLeftHighestNormalTier: false,
    credit: state.credit,
    cursor: state.cursor,
    nowMs,
    knownSampleStaleAfterMs: 10 * 60_000,
    targetInFlightRuntimeKeys: state.reservations.map((reservation) => reservation.accountRuntimeKey),
    targetCooldownUntilMsByRuntimeKey: state.cooldownUntilMsByRuntimeKey
  }
}

function stableBindingOrders(
  accounts: readonly UpstreamAccount[],
  provided: ReadonlyMap<string, number> | undefined
): Map<string, number> {
  const result = new Map<string, number>()
  for (const [index, account] of accounts.entries()) {
    const runtimeKey = gatewayAccountRuntimeKey(account)
    const providedOrder = provided?.get(runtimeKey)
    result.set(runtimeKey, typeof providedOrder === 'number' && Number.isSafeInteger(providedOrder) && providedOrder >= 0
      ? providedOrder
      : index)
  }
  return result
}

function groupByProtocolProfile(accounts: readonly UpstreamAccount[]): Map<string, UpstreamAccount[]> {
  const groups = new Map<string, UpstreamAccount[]>()
  for (const account of accounts) {
    const profile = protocolProfileForAccount(account)
    const current = groups.get(profile)
    if (current) current.push(account)
    else groups.set(profile, [account])
  }
  return groups
}

function hotQualityScopeForAccount(account: UpstreamAccount, requestLane: OpenAIGatewayRequestLane): HotQualityScope {
  return {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: protocolProfileForAccount(account),
    requestLane,
    modelFamily: HOT_QUALITY_UNKNOWN_MODEL_FAMILY
  }
}

function protocolProfileForAccount(account: UpstreamAccount): string {
  return requiredKey(
    account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    'protocolProfile'
  )
}

function modelRank(account: UpstreamAccount, modelPriority: GatewayAccountModelPriority): number {
  const value = modelPriority.rankByAccountId.get(account.id) ?? 3
  if (!Number.isSafeInteger(value) || value < 0) return 3
  return value
}

export function gatewayHotQualityRouteScopeKey(input: {
  systemAccountId: string
  routeStrategyId?: string
  groupId: string
  protocolProfile: string
  requestLane: OpenAIGatewayRequestLane
}): string {
  return encodedKey([
    requiredKey(input.systemAccountId, 'systemAccountId'),
    input.routeStrategyId?.trim() || 'direct',
    requiredKey(input.groupId, 'groupId'),
    requiredKey(input.protocolProfile, 'protocolProfile'),
    input.requestLane
  ])
}

export function sameTierExplorationPoolKey(routeScopeKey: string, configurationTierKey: string): string {
  return encodedKey([requiredKey(routeScopeKey, 'routeScopeKey'), requiredKey(configurationTierKey, 'configurationTierKey')])
}

function gatewayHotQualityRuntimeIdentity(): string {
  if (runtimeConfig.runtimeMode === 'standalone') return 'standalone:memory'
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    throw new Error('performance 热质量要求 redis runtime state driver')
  }
  const redisUrl = runtimeConfig.redis.stateUrl?.trim()
  if (!redisUrl) throw new Error('performance 热质量缺少 JUHE_AI_REDIS_STATE_URL')
  return `performance:redis:${createHash('sha256').update(redisUrl).digest('hex')}`
}

function encodedKey(parts: readonly string[]): string {
  return parts.map((part) => `${Buffer.byteLength(part, 'utf8')}:${part}`).join('|')
}

function requiredKey(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new RangeError(`${name}不能为空`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('当前时间必须是非负安全整数')
  return value
}
