import { strict as assert } from 'node:assert'

import {
  decideHotQualityCandidate,
  gatewayAccountConfigurationTierKey,
  type HotQualityCandidate,
  type SameTierExplorationState
} from '../../modules/gateway/routing/hot-quality-candidate-selection.js'
import type {
  HotQualityReliabilityLevel,
  HotQualitySampleState,
  HotQualitySnapshot
} from '../../modules/gateway/runtime/hot-quality-store.js'

const routeScopeKey = 'strategy-a:group-a:openai-v1:text:gpt'
const topTier = tier({ modelMatchRank: 0, superPriorityEnabled: true, priority: 0 })
const lowerTier = tier({ modelMatchRank: 0, superPriorityEnabled: false, priority: 10 })
const fallbackTier = tier({ modelMatchRank: 0, fallbackEnabled: true, priority: 0 })
assert.notEqual(gatewayAccountConfigurationTierKey(topTier), gatewayAccountConfigurationTierKey(lowerTier))
assert.notEqual(gatewayAccountConfigurationTierKey(topTier), gatewayAccountConfigurationTierKey(fallbackTier))
assert.notEqual(
  gatewayAccountConfigurationTierKey(topTier),
  gatewayAccountConfigurationTierKey({ ...topTier, modelMatchRank: 1 }),
  '完整 tier key 必须包含 modelMatchRank、fallback、superPriority 和 priority'
)

const reliableSlow = candidate('reliable-slow', 0, topTier, {
  hotQuality: snapshot('reliable-slow', 'known', 'healthy', {
    effectiveReliability: 0.93,
    firstByteEwma5m: 12_000,
    firstByteP95Bucket10m: 5
  })
})
const uncertainFast = candidate('uncertain-fast', 1, topTier, {
  hotQuality: snapshot('uncertain-fast', 'known', 'uncertain', {
    effectiveReliability: 0.82,
    firstByteEwma5m: 500,
    firstByteP95Bucket10m: 0
  })
})
const lowerHealthyFast = candidate('lower-healthy-fast', 2, lowerTier, {
  hotQuality: snapshot('lower-healthy-fast', 'known', 'healthy', {
    effectiveReliability: 0.99,
    firstByteEwma5m: 100,
    firstByteP95Bucket10m: 0
  })
})

const costDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [uncertainFast, lowerHealthyFast, reliableSlow]
})
assert.deepEqual(
  costDecision.orderedCandidates.map(item => item.accountId),
  ['reliable-slow', 'uncertain-fast', 'lower-healthy-fast'],
  '可靠性必须优先于速度，低层健康快账号不得越过完整配置层'
)
assert.equal(costDecision.selectedCandidate?.accountId, 'reliable-slow')
assert.equal(costDecision.explanation.qualityReorderedTierKeys.length, 1)
assert.equal(costDecision.explanation.latencyDegradedOverrideApplied, false)

const coldStableB = candidate('cold-b', 1, topTier)
const coldStableA = candidate('cold-a', 1, topTier, {
  hotQuality: snapshot('cold-a', 'cold', 'unhealthy', { effectiveReliability: 0 })
})
const coldDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [coldStableB, coldStableA]
})
assert.deepEqual(
  coldDecision.orderedCandidates.map(item => item.accountId),
  ['cold-a', 'cold-b'],
  'cold / missing snapshot 必须保持稳定中性顺序，不能被无穷大惩罚'
)
assert.equal(coldDecision.explanation.selectedSampleState, 'cold')
assert.equal(coldDecision.explanation.selectedReliabilityLevel, 'unknown')

const warmingParticipates = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [
    candidate('neutral-cold', 0, topTier),
    candidate('warming-above-neutral', 1, topTier, {
      hotQuality: snapshot('warming-above-neutral', 'warming', 'unknown', {
        effectiveReliability: 0.54,
        qualityAttempts10m: 2
      })
    })
  ]
})
assert.equal(
  warmingParticipates.selectedCandidate?.accountId,
  'warming-above-neutral',
  'warming 应以低置信度有效可靠性参与排序，cold 固定保持 0.5 中性基线'
)

const degradedTop = candidate('degraded-top', 0, topTier, {
  latencyDegraded: true,
  hotQuality: snapshot('degraded-top', 'known', 'healthy', {
    effectiveReliability: 0.99,
    firstByteEwma5m: 100
  })
})
const normalLower = candidate('normal-lower', 1, lowerTier, {
  hotQuality: snapshot('normal-lower', 'warming', 'unknown', {
    effectiveReliability: 0.51,
    firstByteEwma5m: 4_000,
    qualityAttempts10m: 2
  })
})
const speedDecision = decideHotQualityCandidate({
  mode: 'speed_first',
  routeScopeKey,
  candidates: [degradedTop, normalLower]
})
assert.equal(speedDecision.selectedCandidate?.accountId, 'normal-lower')
assert.equal(speedDecision.explanation.latencyDegradedOverrideApplied, true)
assert.throws(
  () => decideHotQualityCandidate({
    mode: 'speed_first',
    routeScopeKey,
    candidates: [degradedTop, { ...normalLower, routeScopeKey: 'strategy-b:group-b' }]
  }),
  /路由范围/,
  '候选裁决不得接纳路由范围外账号'
)

const explorationCandidates = [
  candidate('primary-known', 0, topTier, {
    hotQuality: snapshot('primary-known', 'known', 'healthy', {
      effectiveReliability: 0.95,
      lastValidBusinessObservationAtMs: 990_000
    })
  }),
  candidate('cold-1', 1, topTier),
  candidate('cold-2', 2, topTier),
  candidate('warming-gap-1', 3, topTier, {
    hotQuality: snapshot('warming-gap-1', 'warming', 'unknown', {
      qualityAttempts10m: 2,
      lastValidBusinessObservationAtMs: 980_000
    })
  }),
  candidate('lower-cold', 4, lowerTier),
  candidate('fallback-cold', 5, fallbackTier)
]

const insufficientCredit = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: explorationCandidates,
  exploration: exploration({ credit: 0.9 })
})
assert.equal(insufficientCredit.dispatchIntent, 'primary_service')
assert.equal(insufficientCredit.explanation.exploration.status, 'insufficient_credit')
assert.equal(insufficientCredit.explanation.exploration.creditAfterAccrual, 0.95)

const firstExploration = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: explorationCandidates,
  exploration: exploration({ credit: 0.95, cursor: 0 })
})
assert.equal(firstExploration.dispatchIntent, 'same_tier_exploration')
assert.equal(firstExploration.selectedCandidate?.accountId, 'cold-1')
assert.deepEqual(
  firstExploration.explanation.exploration.eligibleTargetAccountIds,
  ['cold-1', 'cold-2', 'warming-gap-1'],
  '解释必须列出全部同层可探索目标，且不包含低优先级或备用账号'
)
assert.deepEqual(firstExploration.explanation.exploration.fairCursorPeerAccountIds, ['cold-1', 'cold-2'])
assert.equal(firstExploration.explanation.exploration.creditAfterAccrual, 1)
assert.equal(firstExploration.explanation.exploration.creditAfterSuccessfulDispatch, 0)
assert.equal(firstExploration.explanation.exploration.creditAfterFailedDispatch, 1, '派发失败必须归还 credit')
assert.equal(firstExploration.explanation.exploration.cursorAfterSuccessfulDispatch, 1)
assert.equal(firstExploration.explanation.exploration.cursorAfterFailedDispatch, 0)
const repeatedFirstExploration = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: explorationCandidates,
  exploration: exploration({ credit: 0.95, cursor: 0 })
})
assert.deepEqual(
  {
    selectedAccountId: repeatedFirstExploration.selectedCandidate?.accountId,
    orderedAccountIds: repeatedFirstExploration.orderedCandidates.map(item => item.accountId),
    explanation: repeatedFirstExploration.explanation
  },
  {
    selectedAccountId: firstExploration.selectedCandidate?.accountId,
    orderedAccountIds: firstExploration.orderedCandidates.map(item => item.accountId),
    explanation: firstExploration.explanation
  },
  '相同快照、credit 与 cursor 必须产生完全确定的裁决和解释'
)

const secondExploration = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: explorationCandidates,
  exploration: exploration({ credit: 1, cursor: 1 })
})
assert.equal(secondExploration.selectedCandidate?.accountId, 'cold-2', '公平 cursor 必须轮转同等 cold 目标')
assert.equal(secondExploration.explanation.exploration.cursorAfterSuccessfulDispatch, 2)

const guardedExploration = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: explorationCandidates,
  exploration: exploration({
    credit: 1,
    targetInFlightRuntimeKeys: ['cold-1'],
    targetCooldownUntilMsByRuntimeKey: { 'cold-2': 1_060_000 }
  })
})
assert.equal(
  guardedExploration.selectedCandidate?.accountId,
  'warming-gap-1',
  '探索目标的单飞在途与 60 秒冷却必须阻止重复锤击，但不得跨层选低优先级或备用账号'
)

const warmingOnly = explorationCandidates.filter(item => !item.accountId.startsWith('cold-'))
const warmingDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: warmingOnly,
  exploration: exploration({ credit: 1 })
})
assert.equal(warmingDecision.selectedCandidate?.accountId, 'warming-gap-1')

const knownFresh = candidate('known-fresh', 1, topTier, {
  hotQuality: snapshot('known-fresh', 'known', 'healthy', {
    lastValidBusinessObservationAtMs: 999_000
  })
})
const noStaleKnownTarget = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [explorationCandidates[0]!, knownFresh],
  exploration: exploration({ credit: 1 })
})
assert.equal(noStaleKnownTarget.dispatchIntent, 'primary_service')
assert.equal(noStaleKnownTarget.explanation.exploration.status, 'no_eligible_target')

const knownStale = candidate('known-stale', 1, topTier, {
  hotQuality: snapshot('known-stale', 'known', 'healthy', {
    lastValidBusinessObservationAtMs: 300_000
  })
})
const staleKnownDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [explorationCandidates[0]!, knownFresh, knownStale],
  exploration: exploration({ credit: 1 })
})
assert.equal(staleKnownDecision.selectedCandidate?.accountId, 'known-stale')

const speedExploration = decideHotQualityCandidate({
  mode: 'speed_first',
  routeScopeKey,
  candidates: [
    explorationCandidates[0]!,
    { ...explorationCandidates[1]!, latencyDegraded: true },
    explorationCandidates[2]!
  ],
  exploration: exploration({ credit: 1 })
})
assert.equal(speedExploration.selectedCandidate?.accountId, 'cold-2', '快速模式探索不得选择 latency_degraded 账号')

const leftHighestTier = decideHotQualityCandidate({
  mode: 'speed_first',
  routeScopeKey,
  candidates: [degradedTop, normalLower],
  exploration: exploration({ credit: 1 })
})
assert.equal(leftHighestTier.dispatchIntent, 'primary_service')
assert.equal(leftHighestTier.explanation.exploration.status, 'left_highest_normal_tier')

const fallbackOnly = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [candidate('fallback-primary', 0, fallbackTier), candidate('fallback-peer', 1, fallbackTier)],
  exploration: exploration({ credit: 0 })
})
assert.equal(fallbackOnly.explanation.exploration.status, 'fallback_tier')
assert.equal(fallbackOnly.explanation.exploration.creditAfterAccrual, 0, '备用层派发不得补充探索 credit')

const duplicateRuntimeDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [
    candidate('runtime-a-first', 2, topTier, { accountRuntimeKey: 'runtime-a' }),
    candidate('runtime-a-duplicate', 0, topTier, { accountRuntimeKey: 'runtime-a' }),
    candidate('runtime-b', 1, topTier)
  ]
})
assert.deepEqual(duplicateRuntimeDecision.orderedCandidates.map(item => item.accountId), ['runtime-b', 'runtime-a-first'])
assert.deepEqual(duplicateRuntimeDecision.explanation.duplicateRuntimeAccountIds, ['runtime-a-duplicate'])

const noCandidateDecision = decideHotQualityCandidate({
  mode: 'cost_first',
  routeScopeKey,
  candidates: [],
  exploration: exploration({ credit: 0, eligibleFirstPrimaryDispatch: true })
})
assert.equal(noCandidateDecision.selectedCandidate, undefined)
assert.equal(noCandidateDecision.dispatchIntent, 'primary_service')
assert.equal(noCandidateDecision.explanation.selectionReason, 'no_candidate')
assert.equal(noCandidateDecision.explanation.exploration.status, 'no_primary_candidate')
assert.equal(noCandidateDecision.explanation.exploration.creditAfterAccrual, 0, '没有真实候选派发时不得凭空产生 credit')

console.log('hot quality candidate selection regression passed')

function candidate(
  accountId: string,
  stableBindingOrder: number,
  configurationTier: HotQualityCandidate['configurationTier'],
  options: Partial<Omit<HotQualityCandidate, 'accountId' | 'accountRuntimeKey' | 'routeScopeKey' | 'configurationTier' | 'stableBindingOrder'>> & {
    accountRuntimeKey?: string
  } = {}
): HotQualityCandidate {
  return {
    accountId,
    accountRuntimeKey: options.accountRuntimeKey ?? accountId,
    routeScopeKey,
    configurationTier,
    stableBindingOrder,
    hotQuality: options.hotQuality,
    latencyDegraded: options.latencyDegraded,
    lastExplorationAttemptAtMs: options.lastExplorationAttemptAtMs
  }
}

function tier(input: Partial<HotQualityCandidate['configurationTier']>): HotQualityCandidate['configurationTier'] {
  const result = {
    modelMatchRank: input.modelMatchRank ?? 0,
    fallbackEnabled: input.fallbackEnabled ?? false,
    superPriorityEnabled: input.superPriorityEnabled ?? false,
    priority: input.priority ?? 0
  }
  assert.equal(gatewayAccountConfigurationTierKey(result).length > 0, true)
  return result
}

function exploration(overrides: Partial<SameTierExplorationState> = {}): SameTierExplorationState {
  return {
    enabled: true,
    eligibleFirstPrimaryDispatch: overrides.eligibleFirstPrimaryDispatch ?? true,
    requestAlreadyExplored: false,
    hasLeftHighestNormalTier: false,
    credit: 0,
    cursor: 0,
    nowMs: 1_000_000,
    knownSampleStaleAfterMs: 10 * 60_000,
    targetInFlightRuntimeKeys: [],
    targetCooldownUntilMsByRuntimeKey: {},
    ...overrides
  }
}

function snapshot(
  accountRuntimeKey: string,
  sampleState: HotQualitySampleState,
  reliabilityLevel: HotQualityReliabilityLevel,
  options: {
    effectiveReliability?: number
    firstByteEwma5m?: number
    firstByteP95Bucket10m?: number
    qualityAttempts10m?: number
    lastValidBusinessObservationAtMs?: number
  } = {}
): HotQualitySnapshot {
  const qualityAttempts10m = options.qualityAttempts10m ?? (sampleState === 'cold' ? 0 : sampleState === 'warming' ? 2 : 10)
  const counters = {
    attempts: qualityAttempts10m,
    completedResponses: qualityAttempts10m,
    localTransportFailures: 0,
    timeouts: 0,
    readInterruptions: 0,
    incompleteResponses: 0,
    explicitPolicyFailures: 0,
    unknownOutcomes: 0,
    clientCancellations: 0,
    firstByteSampleCount: options.firstByteEwma5m === undefined ? 0 : qualityAttempts10m,
    firstByteSumMs: options.firstByteEwma5m === undefined ? 0 : options.firstByteEwma5m * qualityAttempts10m,
    firstByteHistogram: [0, 0, 0, 0, 0, 0, 0, 0] as const,
    lastCompletedAtMs: options.lastValidBusinessObservationAtMs,
    lastFailureAtMs: undefined
  }
  return {
    scopeKey: accountRuntimeKey,
    scope: {
      accountRuntimeKey,
      protocolProfile: 'openai-v1',
      requestLane: 'text',
      modelFamily: 'gpt' as HotQualitySnapshot['scope']['modelFamily']
    },
    minuteBuckets: [],
    window5m: { ...counters, minutes: 5, qualityAttempts: qualityAttempts10m, adjustedCompletionRate: 0.9 },
    window10m: { ...counters, minutes: 10, qualityAttempts: qualityAttempts10m, adjustedCompletionRate: 0.9 },
    window30m: { ...counters, minutes: 30, qualityAttempts: qualityAttempts10m, adjustedCompletionRate: 0.9 },
    reliability: options.effectiveReliability ?? 0.5,
    confidence: sampleState === 'known' ? 1 : qualityAttempts10m / 10,
    effectiveReliability: options.effectiveReliability ?? 0.5,
    reliabilityLevel,
    sampleState,
    firstByteEwma5m: options.firstByteEwma5m,
    firstByteP95Bucket10m: options.firstByteP95Bucket10m,
    expiresAtMs: 2_000_000
  }
}
