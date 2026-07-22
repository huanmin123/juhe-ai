declare const hotQualityModelFamilyBrand: unique symbol

export type HotQualityModelFamily = string & { readonly [hotQualityModelFamilyBrand]: true }
export type HotQualityRequestLane = 'text' | 'image'
export type HotQualityReliabilityLevel = 'unknown' | 'healthy' | 'unhealthy' | 'uncertain'
export type HotQualitySampleState = 'cold' | 'warming' | 'known'

export interface HotQualityScope {
  accountRuntimeKey: string
  protocolProfile: string
  requestLane: HotQualityRequestLane
  modelFamily: HotQualityModelFamily
}

export type HotQualityTerminalOutcomeClass =
  | 'completed_response'
  | 'explicit_policy_failure'
  | 'transport_failure'
  | 'timeout'
  | 'read_interruption'
  | 'incomplete_response'
  | 'unknown'
  | 'client_cancellation'

export type HotQualityFailureScope = 'none' | 'key' | 'protocol_model' | 'account' | 'upstream_bucket'
export type HotQualityTerminalSource = 'gateway_transport' | 'explicit_policy' | 'request_lifecycle'

export interface HotQualityTerminalRecord {
  terminalOutcomeId: string
  outcomeClass: HotQualityTerminalOutcomeClass
  failureScope: HotQualityFailureScope
  source: HotQualityTerminalSource
  createdAtMs: number
}

export type HotQualityFirstByteHistogram = readonly [number, number, number, number, number, number, number, number]

export interface HotQualityCounters {
  attempts: number
  completedResponses: number
  localTransportFailures: number
  timeouts: number
  readInterruptions: number
  incompleteResponses: number
  explicitPolicyFailures: number
  unknownOutcomes: number
  clientCancellations: number
  firstByteSampleCount: number
  firstByteSumMs: number
  firstByteHistogram: HotQualityFirstByteHistogram
  lastCompletedAtMs?: number
  lastFailureAtMs?: number
}

export interface HotQualityMinuteBucket extends HotQualityCounters {
  minuteStartedAtMs: number
}

export interface HotQualityWindowSnapshot extends HotQualityCounters {
  minutes: 5 | 10 | 30
  qualityAttempts: number
  adjustedCompletionRate: number
}

export interface HotQualitySnapshot {
  scopeKey: string
  scope: HotQualityScope
  minuteBuckets: readonly HotQualityMinuteBucket[]
  window5m: HotQualityWindowSnapshot
  window10m: HotQualityWindowSnapshot
  window30m: HotQualityWindowSnapshot
  reliability: number
  confidence: number
  effectiveReliability: number
  reliabilityLevel: HotQualityReliabilityLevel
  sampleState: HotQualitySampleState
  firstByteEwma5m?: number
  firstByteP95Bucket10m?: number
  expiresAtMs: number
}

export type HotQualityAttemptMutationStatus =
  | 'applied'
  | 'idempotent'
  | 'degraded_to_protocol'
  | 'attempt_conflict'
  | 'key_capacity_exhausted'
  | 'attempt_capacity_exhausted'

export interface HotQualityAttemptMutationResult {
  status: HotQualityAttemptMutationStatus
  requestedScope: HotQualityScope
  effectiveScope: HotQualityScope
}

export type HotQualityTerminalMutationStatus =
  | 'applied'
  | 'idempotent'
  | 'attempt_conflict'
  | 'attempt_not_found'
  | 'terminal_conflict'
  | 'terminal_outcome_conflict'
  | 'quality_key_unavailable'

export interface HotQualityTerminalMutationResult {
  status: HotQualityTerminalMutationStatus
  terminal?: HotQualityTerminalRecord
  effectiveScope?: HotQualityScope
}

export interface HotQualityStoreStats {
  keyCount: number
  attemptIdentityCount: number
  terminalIdentityCount: number
  keyCreationRefusals: number
  highCardinalityDegradations: number
  attemptCapacityRefusals: number
  terminalQualityKeyMisses: number
}

export interface HotQualityStore {
  recordAttempt(input: {
    attemptId: string
    scope: HotQualityScope
    nowMs?: number
  }): Promise<HotQualityAttemptMutationResult>
  recordTerminal(input: {
    attemptId: string
    scope: HotQualityScope
    terminalOutcomeId: string
    outcomeClass: HotQualityTerminalOutcomeClass
    failureScope: HotQualityFailureScope
    source: HotQualityTerminalSource
    firstByteMs?: number
    nowMs?: number
  }): Promise<HotQualityTerminalMutationResult>
  get(scope: HotQualityScope, nowMs?: number): Promise<HotQualitySnapshot | undefined>
  getTerminal(attemptId: string, nowMs?: number): Promise<HotQualityTerminalRecord | undefined>
  stats(nowMs?: number): Promise<HotQualityStoreStats>
}

export interface HotQualityModelFamilyCatalog {
  readonly knownFamilies: readonly HotQualityModelFamily[]
  resolve(candidate: string | null | undefined): HotQualityModelFamily
}

export const HOT_QUALITY_UNKNOWN_MODEL_FAMILY = 'unknown' as HotQualityModelFamily
export const HOT_QUALITY_MODEL_FAMILY_CATALOG_LIMIT = 256
export const HOT_QUALITY_MINUTE_BUCKET_COUNT = 30
export const HOT_QUALITY_KEY_TTL_MS = 40 * 60_000
export const HOT_QUALITY_TERMINAL_TTL_MS = 60 * 60_000
export const HOT_QUALITY_FIRST_BYTE_EWMA_ALPHA = 0.4
export const HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS = Object.freeze([
  1_000,
  2_000,
  5_000,
  10_000,
  20_000,
  30_000,
  60_000,
  null
] as const)

export function createHotQualityModelFamilyCatalog(
  families: readonly string[],
  limit = HOT_QUALITY_MODEL_FAMILY_CATALOG_LIMIT
): HotQualityModelFamilyCatalog {
  const normalizedLimit = positiveInteger(limit, '模型 family 目录容量')
  const normalized = new Set<string>()
  for (const family of families) {
    const value = normalizeKnownModelFamily(family)
    if (value === HOT_QUALITY_UNKNOWN_MODEL_FAMILY) continue
    normalized.add(value)
    if (normalized.size > normalizedLimit) {
      throw new Error(`热质量最多允许 ${normalizedLimit} 个模型 family`)
    }
  }
  const knownFamilies = Object.freeze([...normalized].sort().map((family) => family as HotQualityModelFamily))
  const knownSet = new Set<string>(knownFamilies)
  return Object.freeze({
    knownFamilies,
    resolve(candidate: string | null | undefined): HotQualityModelFamily {
      const value = normalizeCandidateModelFamily(candidate)
      return value && knownSet.has(value) ? value as HotQualityModelFamily : HOT_QUALITY_UNKNOWN_MODEL_FAMILY
    }
  })
}

export function hotQualityScopeKey(scope: HotQualityScope): string {
  const normalized = normalizeHotQualityScope(scope)
  return encodedScopeKey([
    normalized.accountRuntimeKey,
    normalized.protocolProfile,
    normalized.requestLane,
    normalized.modelFamily
  ])
}

export function protocolHotQualityScope(scope: HotQualityScope): HotQualityScope {
  const normalized = normalizeHotQualityScope(scope)
  return {
    accountRuntimeKey: normalized.accountRuntimeKey,
    protocolProfile: normalized.protocolProfile,
    requestLane: normalized.requestLane,
    modelFamily: HOT_QUALITY_UNKNOWN_MODEL_FAMILY
  }
}

export function cloneHotQualityScope(scope: HotQualityScope): HotQualityScope {
  return { ...scope }
}

export function normalizeHotQualityScope(scope: HotQualityScope): HotQualityScope {
  return {
    accountRuntimeKey: requiredPart(scope.accountRuntimeKey, 'accountRuntimeKey'),
    protocolProfile: requiredPart(scope.protocolProfile, 'protocolProfile'),
    requestLane: requestLane(scope.requestLane),
    modelFamily: requiredPart(scope.modelFamily, 'modelFamily') as HotQualityModelFamily
  }
}

export function firstByteHistogramBucket(firstByteMs: number): number {
  const sample = normalizedFirstByteMs(firstByteMs)
  for (let index = 0; index < HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS.length - 1; index += 1) {
    if (sample <= HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS[index]!) return index
  }
  return HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS.length - 1
}

export function normalizedFirstByteMs(value: number): number {
  if (!Number.isFinite(value) || value < 0) throw new Error('首字耗时必须是非负有限数值')
  return Math.min(Number.MAX_SAFE_INTEGER, Math.round(value))
}

function normalizeKnownModelFamily(value: string): string {
  const normalized = normalizeCandidateModelFamily(value)
  if (!normalized) throw new Error('模型 family 不能为空、包含控制字符或超过 128 字符')
  return normalized
}

function normalizeCandidateModelFamily(value: string | null | undefined): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim().toLowerCase()
  if (!normalized || normalized.length > 128 || /[\u0000-\u001f\u007f]/.test(normalized)) return undefined
  return normalized
}

function requestLane(value: HotQualityRequestLane): HotQualityRequestLane {
  if (value !== 'text' && value !== 'image') throw new Error('热质量 requestLane 必须是 text 或 image')
  return value
}

function requiredPart(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`热质量作用域缺少 ${name}`)
  return normalized
}

function encodedScopeKey(parts: string[]): string {
  return parts.map((part) => `${Buffer.byteLength(part, 'utf8')}:${part}`).join('|')
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} 必须是正整数`)
  return value
}
