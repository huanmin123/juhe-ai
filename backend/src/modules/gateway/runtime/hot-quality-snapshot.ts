import {
  HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS,
  HOT_QUALITY_FIRST_BYTE_EWMA_ALPHA,
  HOT_QUALITY_MINUTE_BUCKET_COUNT,
  cloneHotQualityScope,
  type HotQualityCounters,
  type HotQualityFirstByteHistogram,
  type HotQualityMinuteBucket,
  type HotQualityScope,
  type HotQualitySnapshot,
  type HotQualityWindowSnapshot
} from './hot-quality-store.js'

export interface HotQualityBucketState {
  minuteStartedAtMs: number
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
  firstByteHistogram: [number, number, number, number, number, number, number, number]
  lastCompletedAtMs?: number
  lastFailureAtMs?: number
}

export interface HotQualitySnapshotState {
  scopeKey: string
  scope: HotQualityScope
  buckets: readonly HotQualityBucketState[]
  expiresAtMs: number
}

export function createHotQualitySnapshot(state: HotQualitySnapshotState, now: number): HotQualitySnapshot {
  const currentMinute = Math.floor(now / 60_000)
  const minuteBuckets = state.buckets
    .filter((bucket) => {
      const bucketMinute = Math.floor(bucket.minuteStartedAtMs / 60_000)
      return bucketMinute <= currentMinute && bucketMinute > currentMinute - HOT_QUALITY_MINUTE_BUCKET_COUNT
    })
    .sort((left, right) => left.minuteStartedAtMs - right.minuteStartedAtMs)
  const window5m = windowSnapshot(minuteBuckets, currentMinute, 5)
  const window10m = windowSnapshot(minuteBuckets, currentMinute, 10)
  const window30m = windowSnapshot(minuteBuckets, currentMinute, 30)
  const reliability = window5m.adjustedCompletionRate * 0.6
    + window10m.adjustedCompletionRate * 0.3
    + window30m.adjustedCompletionRate * 0.1
  const confidence = Math.min(1, window10m.qualityAttempts / 10)
  const effectiveReliability = 0.5 + (reliability - 0.5) * confidence
  return {
    scopeKey: state.scopeKey,
    scope: cloneHotQualityScope(state.scope),
    minuteBuckets: minuteBuckets.map(cloneMinuteBucket),
    window5m,
    window10m,
    window30m,
    reliability,
    confidence,
    effectiveReliability,
    reliabilityLevel: reliabilityLevel(window5m.qualityAttempts, window10m.qualityAttempts, effectiveReliability),
    sampleState: window30m.qualityAttempts === 0 ? 'cold' : window10m.qualityAttempts < 3 ? 'warming' : 'known',
    firstByteEwma5m: firstByteEwma(minuteBuckets, currentMinute),
    firstByteP95Bucket10m: p95HistogramBucket(window10m.firstByteHistogram, window10m.firstByteSampleCount),
    expiresAtMs: state.expiresAtMs
  }
}

function windowSnapshot(
  buckets: HotQualityBucketState[],
  currentMinute: number,
  minutes: 5 | 10 | 30
): HotQualityWindowSnapshot {
  const counters = emptyCounters()
  for (const bucket of buckets) {
    const bucketMinute = Math.floor(bucket.minuteStartedAtMs / 60_000)
    if (bucketMinute <= currentMinute - minutes) continue
    mergeCounters(counters, bucket)
  }
  const qualityAttempts = add(add(counters.completedResponses, counters.localTransportFailures), counters.explicitPolicyFailures)
  return {
    ...cloneCounters(counters),
    minutes,
    qualityAttempts,
    adjustedCompletionRate: (counters.completedResponses + 2) / (qualityAttempts + 4)
  }
}

function firstByteEwma(buckets: HotQualityBucketState[], currentMinute: number): number | undefined {
  let ewma: number | undefined
  for (const bucket of buckets) {
    const bucketMinute = Math.floor(bucket.minuteStartedAtMs / 60_000)
    if (bucketMinute <= currentMinute - 5 || bucket.firstByteSampleCount === 0) continue
    const average = bucket.firstByteSumMs / bucket.firstByteSampleCount
    ewma = ewma === undefined
      ? average
      : ewma * (1 - HOT_QUALITY_FIRST_BYTE_EWMA_ALPHA) + average * HOT_QUALITY_FIRST_BYTE_EWMA_ALPHA
  }
  return ewma
}

function p95HistogramBucket(histogram: HotQualityFirstByteHistogram, samples: number): number | undefined {
  if (samples === 0) return undefined
  const target = Math.ceil(samples * 0.95)
  let cumulative = 0
  for (let index = 0; index < HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS.length; index += 1) {
    cumulative += histogram[index]!
    if (cumulative >= target) return index
  }
  return HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS.length - 1
}

function reliabilityLevel(
  qualityAttempts5m: number,
  qualityAttempts10m: number,
  effectiveReliability: number
): HotQualitySnapshot['reliabilityLevel'] {
  if (qualityAttempts10m < 3) return 'unknown'
  if (qualityAttempts5m >= 3 && effectiveReliability >= 0.85) return 'healthy'
  if (qualityAttempts5m >= 3 && effectiveReliability < 0.6) return 'unhealthy'
  return 'uncertain'
}

function mergeCounters(target: HotQualityBucketState, source: HotQualityBucketState): void {
  target.attempts = add(target.attempts, source.attempts)
  target.completedResponses = add(target.completedResponses, source.completedResponses)
  target.localTransportFailures = add(target.localTransportFailures, source.localTransportFailures)
  target.timeouts = add(target.timeouts, source.timeouts)
  target.readInterruptions = add(target.readInterruptions, source.readInterruptions)
  target.incompleteResponses = add(target.incompleteResponses, source.incompleteResponses)
  target.explicitPolicyFailures = add(target.explicitPolicyFailures, source.explicitPolicyFailures)
  target.unknownOutcomes = add(target.unknownOutcomes, source.unknownOutcomes)
  target.clientCancellations = add(target.clientCancellations, source.clientCancellations)
  target.firstByteSampleCount = add(target.firstByteSampleCount, source.firstByteSampleCount)
  target.firstByteSumMs = add(target.firstByteSumMs, source.firstByteSumMs)
  for (let index = 0; index < target.firstByteHistogram.length; index += 1) {
    target.firstByteHistogram[index] = add(target.firstByteHistogram[index]!, source.firstByteHistogram[index]!)
  }
  target.lastCompletedAtMs = maximum(target.lastCompletedAtMs, source.lastCompletedAtMs)
  target.lastFailureAtMs = maximum(target.lastFailureAtMs, source.lastFailureAtMs)
}

function cloneMinuteBucket(bucket: HotQualityBucketState): HotQualityMinuteBucket {
  return { ...cloneCounters(bucket), minuteStartedAtMs: bucket.minuteStartedAtMs }
}

function cloneCounters(counters: HotQualityBucketState): HotQualityCounters {
  return { ...counters, firstByteHistogram: [...counters.firstByteHistogram] as unknown as HotQualityFirstByteHistogram }
}

function emptyCounters(): HotQualityBucketState {
  return {
    minuteStartedAtMs: 0,
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

function add(left: number, right: number): number {
  return Math.min(Number.MAX_SAFE_INTEGER, left + right)
}

function maximum(left: number | undefined, right: number | undefined): number | undefined {
  if (left === undefined) return right
  if (right === undefined) return left
  return Math.max(left, right)
}
