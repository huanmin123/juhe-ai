import {
  HOT_QUALITY_KEY_TTL_MS,
  HOT_QUALITY_MINUTE_BUCKET_COUNT,
  HOT_QUALITY_TERMINAL_TTL_MS,
  cloneHotQualityScope,
  firstByteHistogramBucket,
  hotQualityScopeKey,
  normalizeHotQualityScope,
  normalizedFirstByteMs,
  protocolHotQualityScope,
  type HotQualityAttemptMutationResult,
  type HotQualityFailureScope,
  type HotQualityScope,
  type HotQualitySnapshot,
  type HotQualityStore,
  type HotQualityStoreStats,
  type HotQualityTerminalMutationResult,
  type HotQualityTerminalOutcomeClass,
  type HotQualityTerminalRecord,
  type HotQualityTerminalSource,
} from './hot-quality-store.js'
import {
  createHotQualitySnapshot,
  type HotQualityBucketState
} from './hot-quality-snapshot.js'

export interface MemoryHotQualityStoreOptions {
  keyCapacity?: number
  attemptCapacity?: number
  keyTtlMs?: number
  terminalTtlMs?: number
  now?: () => number
}

interface MemoryHotQualityEntry {
  scopeKey: string
  scope: HotQualityScope
  buckets: Array<HotQualityBucketState | undefined>
  expiresAtMs: number
}

interface MemoryAttemptIdentity {
  attemptId: string
  requestedScopeKey: string
  effectiveScopeKey: string
  effectiveScope: HotQualityScope
  expiresAtMs: number
  terminal?: HotQualityTerminalRecord
}

export class MemoryHotQualityStore implements HotQualityStore {
  private readonly entries = new Map<string, MemoryHotQualityEntry>()
  private readonly attempts = new Map<string, MemoryAttemptIdentity>()
  private readonly terminalOutcomeAttempts = new Map<string, string>()
  private readonly keyCapacity: number
  private readonly attemptCapacity: number
  private readonly keyTtlMs: number
  private readonly terminalTtlMs: number
  private readonly now: () => number
  private keyCreationRefusals = 0
  private highCardinalityDegradations = 0
  private attemptCapacityRefusals = 0
  private terminalQualityKeyMisses = 0
  private nextCleanupAtMs = 0

  constructor(options: MemoryHotQualityStoreOptions = {}) {
    this.keyCapacity = positiveInteger(options.keyCapacity ?? 10_000, 'keyCapacity')
    this.attemptCapacity = positiveInteger(options.attemptCapacity ?? 100_000, 'attemptCapacity')
    this.keyTtlMs = positiveInteger(options.keyTtlMs ?? HOT_QUALITY_KEY_TTL_MS, 'keyTtlMs')
    this.terminalTtlMs = positiveInteger(options.terminalTtlMs ?? HOT_QUALITY_TERMINAL_TTL_MS, 'terminalTtlMs')
    if (this.terminalTtlMs < HOT_QUALITY_TERMINAL_TTL_MS) {
      throw new Error(`terminalTtlMs 不得少于 ${HOT_QUALITY_TERMINAL_TTL_MS}ms`)
    }
    this.now = options.now ?? Date.now
  }

  async recordAttempt(input: {
    attemptId: string
    scope: HotQualityScope
    nowMs?: number
  }): Promise<HotQualityAttemptMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const attemptId = boundedIdentity(input.attemptId, 'attemptId')
    const requestedScope = normalizeHotQualityScope(input.scope)
    const requestedScopeKey = hotQualityScopeKey(requestedScope)
    this.cleanup(now)

    const existingAttempt = this.freshAttempt(attemptId, now)
    if (existingAttempt) {
      return {
        status: existingAttempt.requestedScopeKey === requestedScopeKey ? 'idempotent' : 'attempt_conflict',
        requestedScope,
        effectiveScope: cloneHotQualityScope(existingAttempt.effectiveScope)
      }
    }
    if (this.attempts.size >= this.attemptCapacity) this.cleanup(now, true)
    if (this.attempts.size >= this.attemptCapacity) {
      this.attemptCapacityRefusals = increment(this.attemptCapacityRefusals)
      return { status: 'attempt_capacity_exhausted', requestedScope, effectiveScope: requestedScope }
    }

    const resolved = this.resolveAttemptEntry(requestedScope, requestedScopeKey, now)
    if (!resolved) {
      this.keyCreationRefusals = increment(this.keyCreationRefusals)
      return { status: 'key_capacity_exhausted', requestedScope, effectiveScope: requestedScope }
    }

    this.attempts.set(attemptId, {
      attemptId,
      requestedScopeKey,
      effectiveScopeKey: resolved.entry.scopeKey,
      effectiveScope: cloneHotQualityScope(resolved.entry.scope),
      expiresAtMs: expirationAt(now, this.terminalTtlMs)
    })
    const bucket = currentBucket(resolved.entry, now)
    bucket.attempts = increment(bucket.attempts)
    resolved.entry.expiresAtMs = expirationAt(now, this.keyTtlMs)
    if (resolved.degraded) this.highCardinalityDegradations = increment(this.highCardinalityDegradations)
    return {
      status: resolved.degraded ? 'degraded_to_protocol' : 'applied',
      requestedScope,
      effectiveScope: cloneHotQualityScope(resolved.entry.scope)
    }
  }

  async recordTerminal(input: {
    attemptId: string
    scope: HotQualityScope
    terminalOutcomeId: string
    outcomeClass: HotQualityTerminalOutcomeClass
    failureScope: HotQualityFailureScope
    source: HotQualityTerminalSource
    firstByteMs?: number
    nowMs?: number
  }): Promise<HotQualityTerminalMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const attemptId = boundedIdentity(input.attemptId, 'attemptId')
    const requestedScopeKey = hotQualityScopeKey(input.scope)
    const terminalOutcomeId = boundedIdentity(input.terminalOutcomeId, 'terminalOutcomeId')
    assertOutcomeClass(input.outcomeClass)
    assertFailureScope(input.failureScope)
    assertTerminalSource(input.source)
    const firstByteMs = input.firstByteMs === undefined ? undefined : normalizedFirstByteMs(input.firstByteMs)
    this.cleanup(now)
    const attempt = this.freshAttempt(attemptId, now)
    if (!attempt) return { status: 'attempt_not_found' }
    if (attempt.requestedScopeKey !== requestedScopeKey) {
      return { status: 'attempt_conflict', effectiveScope: cloneHotQualityScope(attempt.effectiveScope) }
    }

    if (attempt.terminal) {
      return {
        status: sameTerminal(attempt.terminal, { ...input, terminalOutcomeId }) ? 'idempotent' : 'terminal_conflict',
        terminal: { ...attempt.terminal },
        effectiveScope: cloneHotQualityScope(attempt.effectiveScope)
      }
    }
    let terminalOwner = this.terminalOutcomeAttempts.get(terminalOutcomeId)
    if (terminalOwner && !this.freshAttempt(terminalOwner, now)) {
      this.terminalOutcomeAttempts.delete(terminalOutcomeId)
      terminalOwner = undefined
    }
    if (terminalOwner && terminalOwner !== attemptId) {
      return { status: 'terminal_outcome_conflict', effectiveScope: cloneHotQualityScope(attempt.effectiveScope) }
    }

    const entry = this.entryForTerminal(attempt, now)
    if (!entry) {
      this.terminalQualityKeyMisses = increment(this.terminalQualityKeyMisses)
      return { status: 'quality_key_unavailable', effectiveScope: cloneHotQualityScope(attempt.effectiveScope) }
    }

    const terminal: HotQualityTerminalRecord = {
      terminalOutcomeId,
      outcomeClass: input.outcomeClass,
      failureScope: input.failureScope,
      source: input.source,
      createdAtMs: now
    }
    attempt.terminal = terminal
    attempt.expiresAtMs = expirationAt(now, this.terminalTtlMs)
    this.terminalOutcomeAttempts.set(terminalOutcomeId, attemptId)

    const bucket = currentBucket(entry, now)
    applyTerminal(bucket, input.outcomeClass, firstByteMs, now)
    entry.expiresAtMs = expirationAt(now, this.keyTtlMs)
    return {
      status: 'applied',
      terminal: { ...terminal },
      effectiveScope: cloneHotQualityScope(attempt.effectiveScope)
    }
  }

  async get(scope: HotQualityScope, nowMs = this.now()): Promise<HotQualitySnapshot | undefined> {
    const now = normalizedNow(nowMs)
    this.cleanup(now)
    const entry = this.freshEntry(hotQualityScopeKey(scope), now)
    return entry ? snapshot(entry, now) : undefined
  }

  async getTerminal(attemptId: string, nowMs = this.now()): Promise<HotQualityTerminalRecord | undefined> {
    const now = normalizedNow(nowMs)
    this.cleanup(now)
    const terminal = this.freshAttempt(boundedIdentity(attemptId, 'attemptId'), now)?.terminal
    return terminal ? { ...terminal } : undefined
  }

  async stats(nowMs = this.now()): Promise<HotQualityStoreStats> {
    this.cleanup(normalizedNow(nowMs), true)
    return {
      keyCount: this.entries.size,
      attemptIdentityCount: this.attempts.size,
      terminalIdentityCount: this.terminalOutcomeAttempts.size,
      keyCreationRefusals: this.keyCreationRefusals,
      highCardinalityDegradations: this.highCardinalityDegradations,
      attemptCapacityRefusals: this.attemptCapacityRefusals,
      terminalQualityKeyMisses: this.terminalQualityKeyMisses
    }
  }

  private resolveAttemptEntry(
    requestedScope: HotQualityScope,
    requestedScopeKey: string,
    now: number
  ): { entry: MemoryHotQualityEntry; degraded: boolean } | undefined {
    const existing = this.freshEntry(requestedScopeKey, now)
    if (existing) return { entry: existing, degraded: false }
    if (this.entries.size >= this.keyCapacity) this.cleanup(now, true)
    if (this.entries.size < this.keyCapacity) {
      return { entry: this.createEntry(requestedScope, requestedScopeKey, now), degraded: false }
    }
    const fallbackScope = protocolHotQualityScope(requestedScope)
    const fallback = this.freshEntry(hotQualityScopeKey(fallbackScope), now)
    return fallback ? { entry: fallback, degraded: true } : undefined
  }

  private entryForTerminal(attempt: MemoryAttemptIdentity, now: number): MemoryHotQualityEntry | undefined {
    const existing = this.freshEntry(attempt.effectiveScopeKey, now)
    if (existing) return existing
    if (this.entries.size >= this.keyCapacity) this.cleanup(now, true)
    if (this.entries.size >= this.keyCapacity) return undefined
    return this.createEntry(attempt.effectiveScope, attempt.effectiveScopeKey, now)
  }

  private createEntry(scope: HotQualityScope, scopeKey: string, now: number): MemoryHotQualityEntry {
    const entry: MemoryHotQualityEntry = {
      scopeKey,
      scope: cloneHotQualityScope(scope),
      buckets: Array.from({ length: HOT_QUALITY_MINUTE_BUCKET_COUNT }),
      expiresAtMs: expirationAt(now, this.keyTtlMs)
    }
    this.entries.set(scopeKey, entry)
    return entry
  }

  private freshEntry(scopeKey: string, now: number): MemoryHotQualityEntry | undefined {
    const entry = this.entries.get(scopeKey)
    if (!entry || entry.expiresAtMs > now) return entry
    this.entries.delete(scopeKey)
    return undefined
  }

  private freshAttempt(attemptId: string, now: number): MemoryAttemptIdentity | undefined {
    const attempt = this.attempts.get(attemptId)
    if (!attempt || attempt.expiresAtMs > now) return attempt
    this.attempts.delete(attemptId)
    if (attempt.terminal) this.terminalOutcomeAttempts.delete(attempt.terminal.terminalOutcomeId)
    return undefined
  }

  private cleanup(now: number, force = false): void {
    if (!force && now < this.nextCleanupAtMs) return
    for (const [scopeKey, entry] of this.entries) {
      if (entry.expiresAtMs <= now) this.entries.delete(scopeKey)
    }
    for (const [attemptId, attempt] of this.attempts) {
      if (attempt.expiresAtMs > now) continue
      this.attempts.delete(attemptId)
      if (attempt.terminal) this.terminalOutcomeAttempts.delete(attempt.terminal.terminalOutcomeId)
    }
    this.nextCleanupAtMs = expirationAt(now, 60_000)
  }
}

function currentBucket(entry: MemoryHotQualityEntry, now: number): HotQualityBucketState {
  const minute = Math.floor(now / 60_000)
  const index = minute % HOT_QUALITY_MINUTE_BUCKET_COUNT
  const minuteStartedAtMs = minute * 60_000
  let bucket = entry.buckets[index]
  if (!bucket || bucket.minuteStartedAtMs !== minuteStartedAtMs) {
    bucket = { minuteStartedAtMs, ...emptyCounters() }
    entry.buckets[index] = bucket
  }
  return bucket
}

function applyTerminal(
  bucket: HotQualityBucketState,
  outcomeClass: HotQualityTerminalOutcomeClass,
  firstByteMs: number | undefined,
  now: number
): void {
  switch (outcomeClass) {
    case 'completed_response':
      bucket.completedResponses = increment(bucket.completedResponses)
      bucket.lastCompletedAtMs = maximum(bucket.lastCompletedAtMs, now)
      break
    case 'explicit_policy_failure':
      bucket.explicitPolicyFailures = increment(bucket.explicitPolicyFailures)
      bucket.lastFailureAtMs = maximum(bucket.lastFailureAtMs, now)
      break
    case 'transport_failure':
      bucket.localTransportFailures = increment(bucket.localTransportFailures)
      bucket.lastFailureAtMs = maximum(bucket.lastFailureAtMs, now)
      break
    case 'timeout':
      bucket.localTransportFailures = increment(bucket.localTransportFailures)
      bucket.timeouts = increment(bucket.timeouts)
      bucket.lastFailureAtMs = maximum(bucket.lastFailureAtMs, now)
      break
    case 'read_interruption':
      bucket.localTransportFailures = increment(bucket.localTransportFailures)
      bucket.readInterruptions = increment(bucket.readInterruptions)
      bucket.lastFailureAtMs = maximum(bucket.lastFailureAtMs, now)
      break
    case 'incomplete_response':
      bucket.localTransportFailures = increment(bucket.localTransportFailures)
      bucket.incompleteResponses = increment(bucket.incompleteResponses)
      bucket.lastFailureAtMs = maximum(bucket.lastFailureAtMs, now)
      break
    case 'unknown':
      bucket.unknownOutcomes = increment(bucket.unknownOutcomes)
      break
    case 'client_cancellation':
      bucket.clientCancellations = increment(bucket.clientCancellations)
      break
  }
  if (firstByteMs === undefined || outcomeClass === 'unknown' || outcomeClass === 'client_cancellation') return
  const sample = normalizedFirstByteMs(firstByteMs)
  bucket.firstByteSampleCount = increment(bucket.firstByteSampleCount)
  bucket.firstByteSumMs = add(bucket.firstByteSumMs, sample)
  const histogramIndex = firstByteHistogramBucket(sample)
  bucket.firstByteHistogram[histogramIndex] = increment(bucket.firstByteHistogram[histogramIndex]!)
}

function snapshot(entry: MemoryHotQualityEntry, now: number): HotQualitySnapshot {
  return createHotQualitySnapshot({
    scopeKey: entry.scopeKey,
    scope: entry.scope,
    buckets: entry.buckets.filter((bucket): bucket is HotQualityBucketState => bucket !== undefined),
    expiresAtMs: entry.expiresAtMs
  }, now)
}

function emptyCounters(): Omit<HotQualityBucketState, 'minuteStartedAtMs'> {
  return {
    attempts: 0,
    completedResponses: 0,
    localTransportFailures: 0,
    timeouts: 0,
    readInterruptions: 0,
    incompleteResponses: 0,
    explicitPolicyFailures: 0,
    unknownOutcomes: 0,
    clientCancellations: 0,
    firstByteSampleCount: 0,
    firstByteSumMs: 0,
    firstByteHistogram: [0, 0, 0, 0, 0, 0, 0, 0]
  }
}

function sameTerminal(
  terminal: HotQualityTerminalRecord,
  input: {
    terminalOutcomeId: string
    outcomeClass: HotQualityTerminalOutcomeClass
    failureScope: HotQualityFailureScope
    source: HotQualityTerminalSource
  }
): boolean {
  return terminal.terminalOutcomeId === input.terminalOutcomeId
    && terminal.outcomeClass === input.outcomeClass
    && terminal.failureScope === input.failureScope
    && terminal.source === input.source
}

function assertOutcomeClass(value: HotQualityTerminalOutcomeClass): void {
  if (
    value !== 'completed_response'
    && value !== 'explicit_policy_failure'
    && value !== 'transport_failure'
    && value !== 'timeout'
    && value !== 'read_interruption'
    && value !== 'incomplete_response'
    && value !== 'unknown'
    && value !== 'client_cancellation'
  ) throw new Error('热质量 outcomeClass 非法')
}

function assertFailureScope(value: HotQualityFailureScope): void {
  if (
    value !== 'none'
    && value !== 'key'
    && value !== 'protocol_model'
    && value !== 'account'
    && value !== 'upstream_bucket'
  ) throw new Error('热质量 failureScope 非法')
}

function assertTerminalSource(value: HotQualityTerminalSource): void {
  if (value !== 'gateway_transport' && value !== 'explicit_policy' && value !== 'request_lifecycle') {
    throw new Error('热质量 terminal source 非法')
  }
}

function boundedIdentity(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > 256) throw new Error(`${name} 必须是 1 到 256 字符`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('nowMs 必须是非负安全整数')
  return value
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} 必须是正整数`)
  return value
}

function increment(value: number): number {
  return value >= Number.MAX_SAFE_INTEGER ? Number.MAX_SAFE_INTEGER : value + 1
}

function add(left: number, right: number): number {
  return Math.min(Number.MAX_SAFE_INTEGER, left + right)
}

function expirationAt(now: number, ttlMs: number): number {
  return Math.min(Number.MAX_SAFE_INTEGER, now + ttlMs)
}

function maximum(left: number | undefined, right: number | undefined): number | undefined {
  if (left === undefined) return right
  if (right === undefined) return left
  return Math.max(left, right)
}
