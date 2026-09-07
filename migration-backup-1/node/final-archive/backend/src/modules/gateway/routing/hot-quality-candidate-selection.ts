import type {
  HotQualityReliabilityLevel,
  HotQualitySampleState,
  HotQualitySnapshot
} from '../runtime/hot-quality-store.js'

export type HotQualityRoutingMode = 'cost_first' | 'speed_first'
export type HotQualityDispatchIntent = 'primary_service' | 'same_tier_exploration'

export interface GatewayAccountConfigurationTier {
  modelMatchRank: number
  fallbackEnabled: boolean
  superPriorityEnabled: boolean
  priority: number
}

export interface HotQualityCandidate {
  accountId: string
  accountRuntimeKey: string
  routeScopeKey: string
  configurationTier: GatewayAccountConfigurationTier
  stableBindingOrder: number
  hotQuality?: HotQualitySnapshot
  latencyDegraded?: boolean
  lastExplorationAttemptAtMs?: number
}

export interface SameTierExplorationState {
  enabled: boolean
  eligibleFirstPrimaryDispatch: boolean
  creditAccrualAlreadyApplied?: boolean
  requestAlreadyExplored: boolean
  hasLeftHighestNormalTier: boolean
  credit: number
  cursor: number
  nowMs: number
  knownSampleStaleAfterMs: number
  targetInFlightRuntimeKeys?: readonly string[]
  targetCooldownUntilMsByRuntimeKey?: Readonly<Record<string, number>>
}

export type SameTierExplorationStatus =
  | 'not_configured'
  | 'disabled'
  | 'no_primary_candidate'
  | 'ineligible_primary_dispatch'
  | 'request_already_explored'
  | 'left_highest_normal_tier'
  | 'fallback_tier'
  | 'insufficient_credit'
  | 'no_eligible_target'
  | 'selected'

export interface SameTierExplorationExplanation {
  status: SameTierExplorationStatus
  creditBefore: number
  creditAccrued: number
  creditAfterAccrual: number
  creditSpendOnSuccessfulDispatch: number
  creditAfterSuccessfulDispatch: number
  creditAfterFailedDispatch: number
  cursorBefore: number
  cursorAfterSuccessfulDispatch: number
  cursorAfterFailedDispatch: number
  eligibleTargetAccountIds: readonly string[]
  fairCursorPeerAccountIds: readonly string[]
  selectedTargetAccountId?: string
}

export interface HotQualityCandidateSelectionExplanation {
  mode: HotQualityRoutingMode
  routeScopeKey: string
  selectionReason: 'no_candidate' | 'ranked_primary' | 'same_tier_exploration'
  baselinePrimaryAccountId?: string
  selectedAccountId?: string
  selectedAccountRuntimeKey?: string
  selectedTierKey?: string
  selectedSampleState?: HotQualitySampleState
  selectedReliabilityLevel?: HotQualityReliabilityLevel
  latencyDegradedOverrideApplied: boolean
  qualityReorderedTierKeys: readonly string[]
  duplicateRuntimeAccountIds: readonly string[]
  exploration: SameTierExplorationExplanation
}

export interface HotQualityCandidateDecision<TCandidate extends HotQualityCandidate> {
  selectedCandidate?: TCandidate
  dispatchIntent: HotQualityDispatchIntent
  qualityOrderedCandidates: readonly TCandidate[]
  orderedCandidates: readonly TCandidate[]
  explanation: HotQualityCandidateSelectionExplanation
}

export interface DecideHotQualityCandidateInput<TCandidate extends HotQualityCandidate> {
  mode: HotQualityRoutingMode
  routeScopeKey: string
  candidates: readonly TCandidate[]
  exploration?: SameTierExplorationState
}

export const SAME_TIER_EXPLORATION_CREDIT_PER_ELIGIBLE_DISPATCH = 0.05
export const SAME_TIER_EXPLORATION_CREDIT_CAP = 1
export const SAME_TIER_EXPLORATION_CREDIT_COST = 1
export const SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS = 60_000

interface IndexedCandidate<TCandidate extends HotQualityCandidate> {
  candidate: TCandidate
  originalIndex: number
  tierKey: string
  sampleState: HotQualitySampleState
  reliabilityLevel: HotQualityReliabilityLevel
}

interface ExplorationRankedCandidate<TCandidate extends HotQualityCandidate> {
  indexed: IndexedCandidate<TCandidate>
  sampleRank: number
  sampleGap: number
  lastValidBusinessObservationAtMs: number
  lastExplorationAttemptAtMs: number
}

export function gatewayAccountConfigurationTierKey(tier: GatewayAccountConfigurationTier): string {
  const modelMatchRank = normalizedNonNegativeInteger(tier.modelMatchRank, 'modelMatchRank')
  const priority = normalizedInteger(tier.priority, 'priority')
  return `model=${modelMatchRank}|fallback=${tier.fallbackEnabled ? 1 : 0}|super=${tier.superPriorityEnabled ? 1 : 0}|priority=${priority}`
}

export function decideHotQualityCandidate<TCandidate extends HotQualityCandidate>(
  input: DecideHotQualityCandidateInput<TCandidate>
): HotQualityCandidateDecision<TCandidate> {
  const routeScopeKey = requiredKey(input.routeScopeKey, '路由范围')
  const mode = normalizedMode(input.mode)
  const { candidates, duplicateRuntimeAccountIds } = normalizeCandidates(input.candidates, routeScopeKey)
  const baseTierOrder = distinct(candidates.map(item => item.tierKey))
  const qualityReorderedTierKeys: string[] = []
  const qualityByTier = new Map<string, IndexedCandidate<TCandidate>[]>()

  for (const tierKey of baseTierOrder) {
    const originalTier = candidates.filter(item => item.tierKey === tierKey)
    const qualityTier = [...originalTier].sort(compareWithinTier)
    qualityByTier.set(tierKey, qualityTier)
    if (!sameCandidateOrder(originalTier, qualityTier)) qualityReorderedTierKeys.push(tierKey)
  }

  const costOrdered = baseTierOrder.flatMap(tierKey => qualityByTier.get(tierKey) ?? [])
  const qualityOrdered = mode === 'speed_first'
    ? [false, true].flatMap(latencyDegraded => baseTierOrder.flatMap(tierKey =>
        (qualityByTier.get(tierKey) ?? []).filter(item => (item.candidate.latencyDegraded === true) === latencyDegraded)
      ))
    : costOrdered
  const latencyDegradedOverrideApplied = mode === 'speed_first' && !sameCandidateOrder(costOrdered, qualityOrdered)
  const baselinePrimary = qualityOrdered[0]
  const exploration = decideExploration({
    mode,
    candidates,
    qualityOrdered,
    baseTierOrder,
    state: input.exploration
  })
  const selected = exploration.selected ?? baselinePrimary
  const finalOrdered = exploration.selected
    ? moveBefore(qualityOrdered, exploration.selected)
    : qualityOrdered
  const selectionReason = selected
    ? exploration.selected ? 'same_tier_exploration' : 'ranked_primary'
    : 'no_candidate'

  return {
    selectedCandidate: selected?.candidate,
    dispatchIntent: exploration.selected ? 'same_tier_exploration' : 'primary_service',
    qualityOrderedCandidates: qualityOrdered.map(item => item.candidate),
    orderedCandidates: finalOrdered.map(item => item.candidate),
    explanation: {
      mode,
      routeScopeKey,
      selectionReason,
      baselinePrimaryAccountId: baselinePrimary?.candidate.accountId,
      selectedAccountId: selected?.candidate.accountId,
      selectedAccountRuntimeKey: selected?.candidate.accountRuntimeKey,
      selectedTierKey: selected?.tierKey,
      selectedSampleState: selected?.sampleState,
      selectedReliabilityLevel: selected?.reliabilityLevel,
      latencyDegradedOverrideApplied,
      qualityReorderedTierKeys,
      duplicateRuntimeAccountIds,
      exploration: exploration.explanation
    }
  }
}

function decideExploration<TCandidate extends HotQualityCandidate>(input: {
  mode: HotQualityRoutingMode
  candidates: readonly IndexedCandidate<TCandidate>[]
  qualityOrdered: readonly IndexedCandidate<TCandidate>[]
  baseTierOrder: readonly string[]
  state?: SameTierExplorationState
}): {
  selected?: IndexedCandidate<TCandidate>
  explanation: SameTierExplorationExplanation
} {
  const state = input.state
  const primary = input.qualityOrdered[0]
  const highestNormalTierKey = input.baseTierOrder.find(tierKey => {
    const peer = input.candidates.find(candidate => candidate.tierKey === tierKey)
    return peer?.candidate.configurationTier.fallbackEnabled === false
  })
  const eligibleCreditAccrual = Boolean(
    state?.enabled
    && state.eligibleFirstPrimaryDispatch
    && !state.creditAccrualAlreadyApplied
    && !state.requestAlreadyExplored
    && !state.hasLeftHighestNormalTier
    && primary
    && primary.candidate.configurationTier.fallbackEnabled === false
    && primary.tierKey === highestNormalTierKey
  )
  const creditBefore = state ? normalizedCredit(state.credit) : 0
  const creditAccrued = eligibleCreditAccrual
    ? SAME_TIER_EXPLORATION_CREDIT_PER_ELIGIBLE_DISPATCH
    : 0
  const creditAfterAccrual = roundedCredit(Math.min(
    SAME_TIER_EXPLORATION_CREDIT_CAP,
    creditBefore + creditAccrued
  ))
  const cursorBefore = state ? normalizedCursor(state.cursor) : 0
  const baseExplanation = (status: SameTierExplorationStatus): SameTierExplorationExplanation => ({
    status,
    creditBefore,
    creditAccrued,
    creditAfterAccrual,
    creditSpendOnSuccessfulDispatch: 0,
    creditAfterSuccessfulDispatch: creditAfterAccrual,
    creditAfterFailedDispatch: creditAfterAccrual,
    cursorBefore,
    cursorAfterSuccessfulDispatch: cursorBefore,
    cursorAfterFailedDispatch: cursorBefore,
    eligibleTargetAccountIds: [],
    fairCursorPeerAccountIds: []
  })
  if (!state) return { explanation: baseExplanation('not_configured') }
  if (!primary) return { explanation: baseExplanation('no_primary_candidate') }
  if (!state.enabled) return { explanation: baseExplanation('disabled') }
  if (!state.eligibleFirstPrimaryDispatch) return { explanation: baseExplanation('ineligible_primary_dispatch') }
  if (state.requestAlreadyExplored) return { explanation: baseExplanation('request_already_explored') }

  if (primary.candidate.configurationTier.fallbackEnabled) {
    return { explanation: baseExplanation('fallback_tier') }
  }
  if (state.hasLeftHighestNormalTier || !highestNormalTierKey || primary.tierKey !== highestNormalTierKey) {
    return { explanation: baseExplanation('left_highest_normal_tier') }
  }
  if (creditAfterAccrual < SAME_TIER_EXPLORATION_CREDIT_COST) {
    return { explanation: baseExplanation('insufficient_credit') }
  }

  const inFlightKeys = new Set((state.targetInFlightRuntimeKeys ?? []).map(key => requiredKey(key, '探索在途账号键')))
  const nowMs = normalizedTimestamp(state.nowMs, '探索当前时间')
  const knownSampleStaleAfterMs = normalizedNonNegativeInteger(state.knownSampleStaleAfterMs, 'known 样本过旧阈值')
  const eligible = input.candidates
    .filter(candidate => candidate.tierKey === primary.tierKey)
    .filter(candidate => candidate.candidate.accountRuntimeKey !== primary.candidate.accountRuntimeKey)
    .filter(candidate => input.mode !== 'speed_first' || candidate.candidate.latencyDegraded !== true)
    .filter(candidate => !inFlightKeys.has(candidate.candidate.accountRuntimeKey))
    .filter(candidate => Math.max(
      normalizedCooldownUntil(state.targetCooldownUntilMsByRuntimeKey?.[candidate.candidate.accountRuntimeKey]),
      candidate.candidate.lastExplorationAttemptAtMs === undefined
        ? 0
        : normalizedPastTimestamp(candidate.candidate.lastExplorationAttemptAtMs, nowMs)
          + SAME_TIER_EXPLORATION_TARGET_COOLDOWN_MS
    ) <= nowMs)
    .map(candidate => explorationRank(candidate, nowMs))
    .filter(candidate => candidate.sampleRank < 2 || nowMs - candidate.lastValidBusinessObservationAtMs >= knownSampleStaleAfterMs)
    .sort(compareExplorationRank)

  if (eligible.length === 0) return { explanation: baseExplanation('no_eligible_target') }
  const best = eligible[0]!
  const equallyPreferred = eligible.filter(candidate => sameExplorationPriority(candidate, best))
  const selectedRanked = equallyPreferred[cursorBefore % equallyPreferred.length]!
  const selected = selectedRanked.indexed
  const eligibleTargetAccountIds = eligible.map(candidate => candidate.indexed.candidate.accountId)
  const fairCursorPeerAccountIds = equallyPreferred.map(candidate => candidate.indexed.candidate.accountId)
  return {
    selected,
    explanation: {
      status: 'selected',
      creditBefore,
      creditAccrued,
      creditAfterAccrual,
      creditSpendOnSuccessfulDispatch: SAME_TIER_EXPLORATION_CREDIT_COST,
      creditAfterSuccessfulDispatch: roundedCredit(creditAfterAccrual - SAME_TIER_EXPLORATION_CREDIT_COST),
      creditAfterFailedDispatch: creditAfterAccrual,
      cursorBefore,
      cursorAfterSuccessfulDispatch: cursorBefore === Number.MAX_SAFE_INTEGER ? 0 : cursorBefore + 1,
      cursorAfterFailedDispatch: cursorBefore,
      eligibleTargetAccountIds,
      fairCursorPeerAccountIds,
      selectedTargetAccountId: selected.candidate.accountId
    }
  }
}

function explorationRank<TCandidate extends HotQualityCandidate>(
  indexed: IndexedCandidate<TCandidate>,
  nowMs: number
): ExplorationRankedCandidate<TCandidate> {
  const sampleRank = indexed.sampleState === 'cold' ? 0 : indexed.sampleState === 'warming' ? 1 : 2
  const qualityAttempts10m = indexed.candidate.hotQuality?.window10m.qualityAttempts ?? 0
  const sampleGap = Math.max(0, 3 - qualityAttempts10m)
  return {
    indexed,
    sampleRank,
    sampleGap,
    lastValidBusinessObservationAtMs: lastValidBusinessObservationAtMs(indexed.candidate.hotQuality, nowMs),
    lastExplorationAttemptAtMs: normalizedPastTimestamp(indexed.candidate.lastExplorationAttemptAtMs, nowMs)
  }
}

function compareExplorationRank<TCandidate extends HotQualityCandidate>(
  left: ExplorationRankedCandidate<TCandidate>,
  right: ExplorationRankedCandidate<TCandidate>
): number {
  return left.sampleRank - right.sampleRank
    || right.sampleGap - left.sampleGap
    || left.lastValidBusinessObservationAtMs - right.lastValidBusinessObservationAtMs
    || left.lastExplorationAttemptAtMs - right.lastExplorationAttemptAtMs
    || left.indexed.candidate.accountId.localeCompare(right.indexed.candidate.accountId)
}

function sameExplorationPriority<TCandidate extends HotQualityCandidate>(
  left: ExplorationRankedCandidate<TCandidate>,
  right: ExplorationRankedCandidate<TCandidate>
): boolean {
  return left.sampleRank === right.sampleRank
    && left.sampleGap === right.sampleGap
    && left.lastValidBusinessObservationAtMs === right.lastValidBusinessObservationAtMs
    && left.lastExplorationAttemptAtMs === right.lastExplorationAttemptAtMs
}

function compareWithinTier<TCandidate extends HotQualityCandidate>(
  left: IndexedCandidate<TCandidate>,
  right: IndexedCandidate<TCandidate>
): number {
  const reliability = reliabilityRank(left.reliabilityLevel) - reliabilityRank(right.reliabilityLevel)
  if (reliability !== 0) return reliability

  const effectiveReliability = effectiveReliabilityForOrdering(right)
    - effectiveReliabilityForOrdering(left)
  if (effectiveReliability !== 0) return effectiveReliability

  if (left.sampleState !== 'cold' && right.sampleState !== 'cold') {
    const speed = compareSpeed(left.candidate.hotQuality, right.candidate.hotQuality)
    if (speed !== 0) return speed
  }

  return normalizedBindingOrder(left.candidate.stableBindingOrder)
    - normalizedBindingOrder(right.candidate.stableBindingOrder)
    || left.candidate.accountId.localeCompare(right.candidate.accountId)
    || left.originalIndex - right.originalIndex
}

function compareSpeed(left: HotQualitySnapshot | undefined, right: HotQualitySnapshot | undefined): number {
  const leftEwma = normalizedOptionalDuration(left?.firstByteEwma5m)
  const rightEwma = normalizedOptionalDuration(right?.firstByteEwma5m)
  if (leftEwma !== undefined && rightEwma !== undefined && leftEwma !== rightEwma) return leftEwma - rightEwma
  const leftP95 = normalizedOptionalDuration(left?.firstByteP95Bucket10m)
  const rightP95 = normalizedOptionalDuration(right?.firstByteP95Bucket10m)
  if (leftP95 !== undefined && rightP95 !== undefined && leftP95 !== rightP95) return leftP95 - rightP95
  return 0
}

function normalizeCandidates<TCandidate extends HotQualityCandidate>(
  candidates: readonly TCandidate[],
  routeScopeKey: string
): {
  candidates: IndexedCandidate<TCandidate>[]
  duplicateRuntimeAccountIds: string[]
} {
  const seenRuntimeKeys = new Set<string>()
  const normalized: IndexedCandidate<TCandidate>[] = []
  const duplicateRuntimeAccountIds: string[] = []
  for (const [originalIndex, candidate] of candidates.entries()) {
    const accountId = requiredKey(candidate.accountId, '账号 ID')
    const accountRuntimeKey = requiredKey(candidate.accountRuntimeKey, '账号运行态键')
    const candidateRouteScopeKey = requiredKey(candidate.routeScopeKey, '候选路由范围')
    if (candidateRouteScopeKey !== routeScopeKey) {
      throw new RangeError(`候选账号 ${accountId} 不属于当前路由范围`)
    }
    normalizedBindingOrder(candidate.stableBindingOrder)
    const tierKey = gatewayAccountConfigurationTierKey(candidate.configurationTier)
    if (seenRuntimeKeys.has(accountRuntimeKey)) {
      duplicateRuntimeAccountIds.push(accountId)
      continue
    }
    seenRuntimeKeys.add(accountRuntimeKey)
    const sampleState = candidate.hotQuality?.sampleState ?? 'cold'
    normalized.push({
      candidate,
      originalIndex,
      tierKey,
      sampleState,
      reliabilityLevel: sampleState === 'cold'
        ? 'unknown'
        : candidate.hotQuality?.reliabilityLevel ?? 'unknown'
    })
  }
  return { candidates: normalized, duplicateRuntimeAccountIds }
}

function reliabilityRank(level: HotQualityReliabilityLevel): number {
  if (level === 'healthy') return 0
  if (level === 'uncertain') return 1
  if (level === 'unknown') return 2
  return 3
}

function lastValidBusinessObservationAtMs(snapshot: HotQualitySnapshot | undefined, nowMs: number): number {
  if (!snapshot) return 0
  return Math.max(
    normalizedPastTimestamp(snapshot.window30m.lastCompletedAtMs, nowMs),
    normalizedPastTimestamp(snapshot.window30m.lastFailureAtMs, nowMs),
    normalizedPastTimestamp(snapshot.window10m.lastCompletedAtMs, nowMs),
    normalizedPastTimestamp(snapshot.window10m.lastFailureAtMs, nowMs),
    normalizedPastTimestamp(snapshot.window5m.lastCompletedAtMs, nowMs),
    normalizedPastTimestamp(snapshot.window5m.lastFailureAtMs, nowMs)
  )
}

function moveBefore<TCandidate extends HotQualityCandidate>(
  candidates: readonly IndexedCandidate<TCandidate>[],
  selected: IndexedCandidate<TCandidate>
): IndexedCandidate<TCandidate>[] {
  return [selected, ...candidates.filter(candidate => candidate !== selected)]
}

function sameCandidateOrder<TCandidate extends HotQualityCandidate>(
  left: readonly IndexedCandidate<TCandidate>[],
  right: readonly IndexedCandidate<TCandidate>[]
): boolean {
  return left.length === right.length && left.every((candidate, index) => candidate === right[index])
}

function distinct(values: readonly string[]): string[] {
  return [...new Set(values)]
}

function normalizedMode(value: HotQualityRoutingMode): HotQualityRoutingMode {
  if (value !== 'cost_first' && value !== 'speed_first') throw new TypeError('热质量路由模式无效')
  return value
}

function requiredKey(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new TypeError(`${name}不能为空`)
  return normalized
}

function normalizedCredit(value: number): number {
  if (!Number.isFinite(value) || value < 0 || value > SAME_TIER_EXPLORATION_CREDIT_CAP) {
    throw new RangeError('同层探索 credit 必须位于 0..1')
  }
  return roundedCredit(value)
}

function roundedCredit(value: number): number {
  return Math.round(value * 1_000_000) / 1_000_000
}

function normalizedCursor(value: number): number {
  return normalizedNonNegativeInteger(value, '同层探索 cursor')
}

function normalizedBindingOrder(value: number): number {
  return normalizedNonNegativeInteger(value, '稳定绑定顺序')
}

function normalizedInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value)) throw new RangeError(`${name} 必须是安全整数`)
  return value
}

function normalizedNonNegativeInteger(value: number, name: string): number {
  const normalized = normalizedInteger(value, name)
  if (normalized < 0) throw new RangeError(`${name} 不能为负数`)
  return normalized
}

function normalizedTimestamp(value: number, name: string): number {
  if (!Number.isFinite(value)) throw new RangeError(`${name} 必须是有限数值`)
  return Math.trunc(value)
}

function normalizedPastTimestamp(value: number | undefined, nowMs: number): number {
  if (value === undefined || !Number.isFinite(value)) return 0
  return Math.max(0, Math.min(nowMs, Math.trunc(value)))
}

function normalizedCooldownUntil(value: number | undefined): number {
  if (value === undefined) return 0
  if (!Number.isFinite(value)) throw new RangeError('探索冷却截止时间必须是有限数值')
  return Math.trunc(value)
}

function normalizedReliability(value: number | undefined): number {
  if (value === undefined || !Number.isFinite(value)) return 0.5
  return Math.max(0, Math.min(1, value))
}

function effectiveReliabilityForOrdering<TCandidate extends HotQualityCandidate>(
  candidate: IndexedCandidate<TCandidate>
): number {
  return candidate.sampleState === 'cold'
    ? 0.5
    : normalizedReliability(candidate.candidate.hotQuality?.effectiveReliability)
}

function normalizedOptionalDuration(value: number | undefined): number | undefined {
  if (value === undefined || !Number.isFinite(value) || value < 0) return undefined
  return value
}
