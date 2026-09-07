import assert from 'node:assert/strict'

import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type {
  GatewayProxyHealthRuntimeStateStore
} from '../../modules/gateway/runtime/proxy-health.service.js'
import {
  clearGatewayProxyHealthForTest,
  gatewayUpstreamBucketKeys,
  orderGatewayAccountsByUpstreamBucketHealthAsync,
  recordGatewayProxyFailureAsync,
  recordGatewayProxySuccessAsync,
  recordGatewayUpstreamBucketFailureAsync,
  recordGatewayUpstreamBucketSuccessAsync,
  setGatewayProxyHealthNowForTest,
  setGatewayProxyHealthRuntimeStateStoreForTest,
  suppressGatewayUpstreamBucketForSecondsAsync
} from '../../modules/gateway/runtime/proxy-health.service.js'
import { logger } from '../../shared/logger.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

interface BucketSnapshot {
  accountSamples: Array<[string, number]>
  failureCount: number
  reason: string
  avoidUntilMs?: number
  halfOpenAccountId?: string
  halfOpenUntilMs?: number
}

interface Deferred {
  promise: Promise<void>
  resolve: () => void
}

class AtomicRuntimeStateStore implements GatewayProxyHealthRuntimeStateStore {
  private readonly entries = new Map<string, unknown>()
  private readonly ttlByKey = new Map<string, number>()
  private readBarrier?: { key: string; remaining: number; gate: Deferred }
  private preReadPause?: { key: string; reached: Deferred; release: Deferred }
  private compareDeletePause?: { key: string; reached: Deferred; release: Deferred }

  async getJson<T>(key: string): Promise<T | undefined> {
    const preReadPause = this.preReadPause
    if (preReadPause?.key === key) {
      this.preReadPause = undefined
      preReadPause.reached.resolve()
      await preReadPause.release.promise
    }
    const snapshot = cloneValue(this.entries.get(key)) as T | undefined
    const barrier = this.readBarrier
    if (barrier?.key === key && barrier.remaining > 0) {
      barrier.remaining -= 1
      if (barrier.remaining === 0) {
        this.readBarrier = undefined
        barrier.gate.resolve()
      }
      await barrier.gate.promise
    }
    return snapshot
  }

  async compareSetJson<T>(
    key: string,
    expectedValue: T | undefined,
    nextValue: T,
    ttlMs: number
  ): Promise<boolean> {
    assert(ttlMs > 0, 'CAS write must retain a positive TTL')
    const hasCurrent = this.entries.has(key)
    const current = this.entries.get(key)
    if (expectedValue === undefined) {
      if (hasCurrent) return false
    } else if (!hasCurrent || JSON.stringify(current) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.entries.set(key, cloneValue(nextValue))
    this.ttlByKey.set(key, ttlMs)
    return true
  }

  async compareDeleteJson<T>(key: string, expectedValue: T): Promise<boolean> {
    const pause = this.compareDeletePause
    if (pause?.key === key) {
      this.compareDeletePause = undefined
      pause.reached.resolve()
      await pause.release.promise
    }
    if (!this.entries.has(key) || JSON.stringify(this.entries.get(key)) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.entries.delete(key)
    this.ttlByKey.delete(key)
    return true
  }

  armConcurrentReadBarrier(key: string, participants: number): void {
    assert(participants > 1)
    this.readBarrier = { key, remaining: participants, gate: deferred() }
  }

  pauseNextCompareDelete(key: string): { reached: Promise<void>; release: () => void } {
    const reached = deferred()
    const release = deferred()
    this.compareDeletePause = { key, reached, release }
    return {
      reached: reached.promise,
      release: release.resolve
    }
  }

  pauseNextGetBeforeRead(key: string): { reached: Promise<void>; release: () => void } {
    const reached = deferred()
    const release = deferred()
    this.preReadPause = { key, reached, release }
    return {
      reached: reached.promise,
      release: release.resolve
    }
  }

  snapshot<T>(key: string): T | undefined {
    return cloneValue(this.entries.get(key)) as T | undefined
  }

  ttlMs(key: string): number | undefined {
    return this.ttlByKey.get(key)
  }

  clear(): void {
    this.entries.clear()
    this.ttlByKey.clear()
    this.readBarrier = undefined
    this.preReadPause = undefined
    this.compareDeletePause = undefined
  }
}

logger.level = 'silent'
const stateStore = new AtomicRuntimeStateStore()
setGatewayProxyHealthRuntimeStateStoreForTest(stateStore)

try {
  await testConcurrentFailureSamplesAreMerged(stateStore)
  await testAuthorizedInstancesSharePhysicalFailureEvidence(stateStore)
  await testFailureSamplesRemainBounded(stateStore)
  await testHalfOpenLeaseHasSingleOwner(stateStore)
  await testSuccessRetriesAfterHalfOpenLeaseOnlyMutation(stateStore)
  await testProxySuccessDoesNotClearRedisUpstreamBuckets(stateStore)
  await testStaleSuccessCannotDeleteNewFailure(stateStore)
  await testSuccessObservationFencesDelayedRead(stateStore)
  await testAvoidDeadlineDoesNotShrink(stateStore)
  console.log('gateway-proxy-health-redis-atomic-regression passed')
} finally {
  setGatewayProxyHealthRuntimeStateStoreForTest(undefined)
  setGatewayProxyHealthNowForTest(undefined)
  clearGatewayProxyHealthForTest()
}

async function testAuthorizedInstancesSharePhysicalFailureEvidence(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  setGatewayProxyHealthNowForTest(12_000)
  const firstAuthorized = account(
    'redis-authorized-instance-a',
    'redis-authorized-shared',
    'https://authorized.example/v1',
    'redis-physical-shared'
  )
  const secondAuthorized = account(
    'redis-authorized-instance-b',
    'redis-authorized-shared',
    'https://authorized.example/v1',
    'redis-physical-shared'
  )
  const independent = account(
    'redis-authorized-independent',
    'redis-authorized-shared',
    'https://authorized.example/v1',
    'redis-physical-independent'
  )

  await recordGatewayProxyFailureAsync(firstAuthorized, 'first_authorized_failure')
  const duplicatePhysicalDecision = await recordGatewayProxyFailureAsync(secondAuthorized, 'second_authorized_failure')
  assert.equal(duplicatePhysicalDecision.suspected, false, 'Redis failure evidence must not count two grants of one credential source twice')
  assert.equal(duplicatePhysicalDecision.distinctAccountCount, 1, 'Redis samples must deduplicate by physical credential source')
  const duplicateSnapshot = store.snapshot<BucketSnapshot>(proxyBucketStateKey(firstAuthorized))
  assert.deepEqual(
    duplicateSnapshot?.accountSamples.map(([accountId]) => accountId),
    ['redis-physical-shared'],
    'Redis payload must store the physical credential identity instead of authorization instance ids'
  )

  const independentDecision = await recordGatewayProxyFailureAsync(independent, 'independent_physical_failure')
  assert.equal(independentDecision.suspected, true, 'a second independent physical credential may open the shared proxy bucket')
  assert.equal(independentDecision.distinctAccountCount, 2, 'Redis threshold must count independent physical credentials')
}

async function testConcurrentFailureSamplesAreMerged(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  setGatewayProxyHealthNowForTest(10_000)
  const accounts = Array.from({ length: 64 }, (_, index) => account(`redis-merge-${index}`, 'redis-merge-shared'))
  const stateKey = proxyBucketStateKey(accounts[0]!)
  store.armConcurrentReadBarrier(stateKey, accounts.length)

  const decisions = await Promise.all(accounts.map((item) => recordGatewayProxyFailureAsync(item, 'concurrent_transport_failure')))
  const snapshot = store.snapshot<BucketSnapshot>(stateKey)
  assert(snapshot, 'concurrent failures must leave a shared proxy bucket snapshot')
  assert.equal(snapshot.failureCount, accounts.length, 'CAS retries must not lose concurrent failure increments')
  assert.equal(
    new Set(snapshot.accountSamples.map(([accountId]) => accountId)).size,
    accounts.length,
    'CAS retries must merge every distinct account sample'
  )
  assert(snapshot.avoidUntilMs && snapshot.avoidUntilMs > 10_000, 'merged distinct samples must open the shared bucket')
  assert(decisions.some((decision) => decision.suspected), 'at least one applied mutation must observe the distinct-account threshold')
}

async function testHalfOpenLeaseHasSingleOwner(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-half-open-a', 'redis-half-open-shared')
  const second = account('redis-half-open-b', 'redis-half-open-shared')
  const fallback = account('redis-half-open-fallback', 'redis-half-open-other', 'https://fallback.example/v1')
  const stateKey = proxyBucketStateKey(first)

  setGatewayProxyHealthNowForTest(20_000)
  await recordGatewayProxyFailureAsync(first, 'first_failure')
  await recordGatewayProxyFailureAsync(second, 'second_failure')
  setGatewayProxyHealthNowForTest(80_001)
  store.armConcurrentReadBarrier(stateKey, 2)

  const [firstOrder, secondOrder] = await Promise.all([
    orderGatewayAccountsByUpstreamBucketHealthAsync([first, fallback]),
    orderGatewayAccountsByUpstreamBucketHealthAsync([second, fallback])
  ])
  const snapshot = store.snapshot<BucketSnapshot>(stateKey)
  assert(snapshot?.halfOpenAccountId, 'expired avoid window must atomically assign a half-open owner')
  assert(snapshot.halfOpenUntilMs && snapshot.halfOpenUntilMs > 80_001, 'half-open owner must retain a live lease')
  const ownerOrder = snapshot.halfOpenAccountId === first.id ? firstOrder : secondOrder
  const nonOwnerOrder = snapshot.halfOpenAccountId === first.id ? secondOrder : firstOrder
  const nonOwnerAccount = snapshot.halfOpenAccountId === first.id ? second : first
  assert.equal(ownerOrder.accounts[0]?.id, snapshot.halfOpenAccountId, 'router containing the lease owner must keep that probe dispatchable')
  assert.deepEqual(nonOwnerOrder.halfOpenAccountIds, [], 'disjoint candidates must not steal an active lease from another router')
  assert(nonOwnerOrder.avoidedAccountIds.includes(nonOwnerAccount.id), 'non-owner candidate must remain avoided while the shared lease is active')
  assert.equal(nonOwnerOrder.accounts[0]?.id, fallback.id, 'router without the lease owner must prefer an unrelated healthy bucket')
}

async function testSuccessRetriesAfterHalfOpenLeaseOnlyMutation(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-success-half-open-a', 'redis-success-half-open-shared')
  const second = account('redis-success-half-open-b', 'redis-success-half-open-shared')
  const fallback = account('redis-success-half-open-fallback', 'redis-success-half-open-other', 'https://fallback.example/v1')
  const stateKey = proxyBucketStateKey(first)

  setGatewayProxyHealthNowForTest(25_000)
  await recordGatewayProxyFailureAsync(first, 'first_failure')
  await recordGatewayProxyFailureAsync(second, 'second_failure')
  setGatewayProxyHealthNowForTest(85_001)

  const pause = store.pauseNextCompareDelete(stateKey)
  const success = recordGatewayProxySuccessAsync(first)
  await pause.reached
  const order = await orderGatewayAccountsByUpstreamBucketHealthAsync([first, second, fallback])
  assert(order.halfOpenAccountIds.length === 1, 'expired bucket must acquire one half-open lease during the success race')
  const leasedSnapshot = store.snapshot<BucketSnapshot>(stateKey)
  assert(leasedSnapshot?.halfOpenAccountId, 'half-open claim must change the Redis payload before stale compare-delete resumes')
  pause.release()

  assert.equal(await success, true, 'success must re-read and clear when only half-open lease metadata changed')
  assert.equal(store.snapshot(stateKey), undefined, 'half-open-only CAS contention must not leave stale failure evidence behind')
}

async function testProxySuccessDoesNotClearRedisUpstreamBuckets(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-proxy-success-scope-a', 'redis-proxy-success-scope-a', 'https://redis-success-scope.example/v1')
  const second = account('redis-proxy-success-scope-b', 'redis-proxy-success-scope-b', 'https://redis-success-scope.example/v1')
  const fallback = account('redis-proxy-success-scope-fallback', 'redis-proxy-success-scope-c', 'https://redis-success-scope-fallback.example/v1')

  setGatewayProxyHealthNowForTest(27_000)
  await recordGatewayUpstreamBucketFailureAsync(first, 'first_upstream_failure')
  await recordGatewayUpstreamBucketFailureAsync(second, 'second_upstream_failure')
  assert.equal(
    (await orderGatewayAccountsByUpstreamBucketHealthAsync([first, second, fallback])).applied,
    true,
    'Redis shared Base URL evidence must open before testing scoped success cleanup'
  )

  assert.equal(await recordGatewayProxySuccessAsync(first), true, 'async proxy success should clear only the matching proxy bucket')
  assert.equal(
    (await orderGatewayAccountsByUpstreamBucketHealthAsync([first, second, fallback])).applied,
    true,
    'async proxy-only success must not clear Redis Base URL or provider evidence'
  )
  assert.equal(
    await recordGatewayUpstreamBucketSuccessAsync(first, { bucketScope: 'upstream' }),
    true,
    'async full upstream success may clear Redis upstream buckets'
  )
  assert.equal(
    (await orderGatewayAccountsByUpstreamBucketHealthAsync([first, second, fallback])).applied,
    false,
    'Redis ordering should recover only after upstream-scope success cleanup'
  )
}

async function testStaleSuccessCannotDeleteNewFailure(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-success-race-a', 'redis-success-race-shared')
  const second = account('redis-success-race-b', 'redis-success-race-shared')
  const fallback = account('redis-success-race-fallback', 'redis-success-race-other', 'https://fallback.example/v1')
  const stateKey = proxyBucketStateKey(first)

  setGatewayProxyHealthNowForTest(30_000)
  await recordGatewayProxyFailureAsync(first, 'first_failure')
  await recordGatewayProxyFailureAsync(second, 'second_failure')
  const pause = store.pauseNextCompareDelete(stateKey)
  const staleSuccess = recordGatewayProxySuccessAsync(first)
  await pause.reached

  setGatewayProxyHealthNowForTest(30_001)
  await recordGatewayProxyFailureAsync(first, 'newer_failure')
  pause.release()
  assert.equal(await staleSuccess, false, 'stale success must report that its old snapshot was not cleared')

  const snapshot = store.snapshot<BucketSnapshot>(stateKey)
  assert(snapshot, 'newer failure must remain after stale compare-delete loses the race')
  assert.equal(snapshot.failureCount, 3, 'newer failure increment must remain intact')
  assert.equal(snapshot.reason, 'newer_failure', 'stale success must not delete the newer failure payload')
  const order = await orderGatewayAccountsByUpstreamBucketHealthAsync([first, second, fallback])
  assert.equal(order.applied, true, 'the surviving newer failure must keep the affected proxy bucket avoided')
}

async function testSuccessObservationFencesDelayedRead(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-success-observation-a', 'redis-success-observation-shared')
  const second = account('redis-success-observation-b', 'redis-success-observation-shared')
  const stateKey = proxyBucketStateKey(first)

  setGatewayProxyHealthNowForTest(35_000)
  await recordGatewayProxyFailureAsync(first, 'first_failure')
  await recordGatewayProxyFailureAsync(second, 'second_failure')
  setGatewayProxyHealthNowForTest(35_001)
  const pause = store.pauseNextGetBeforeRead(stateKey)
  const earlierSuccess = recordGatewayProxySuccessAsync(first)
  await pause.reached

  await recordGatewayProxyFailureAsync(first, 'same_millisecond_newer_failure')
  pause.release()
  assert.equal(await earlierSuccess, false, 'success observed before a same-millisecond failure must not clear that newer generation')

  const snapshot = store.snapshot<BucketSnapshot>(stateKey)
  assert(snapshot, 'new failure must remain when success Redis GET resumes after the failure write')
  assert.equal(snapshot.failureCount, 3, 'delayed success read must not discard the newer failure increment')
  assert.equal(snapshot.reason, 'same_millisecond_newer_failure', 'delayed success read must preserve the newer failure payload')
}

async function testFailureSamplesRemainBounded(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  setGatewayProxyHealthNowForTest(15_000)
  const accounts = Array.from({ length: 320 }, (_, index) => account(`redis-bounded-${index}`, 'redis-bounded-shared'))
  for (const item of accounts) {
    await recordGatewayProxyFailureAsync(item, 'bounded_sample_failure')
  }
  const snapshot = store.snapshot<BucketSnapshot>(proxyBucketStateKey(accounts[0]!))
  assert(snapshot, 'bounded sample storm must leave a shared bucket snapshot')
  assert.equal(snapshot.failureCount, accounts.length, 'sample compaction must not change the total failure counter')
  assert.equal(snapshot.accountSamples.length, 256, 'shared Redis payload must retain at most 256 distinct account samples')
  assert(snapshot.accountSamples.some(([accountId]) => accountId === accounts.at(-1)?.id), 'bounded samples must retain the newest account evidence')
}

async function testAvoidDeadlineDoesNotShrink(store: AtomicRuntimeStateStore): Promise<void> {
  store.clear()
  const first = account('redis-monotonic-a', 'redis-monotonic-shared')
  const stateKey = proxyBucketStateKey(first)
  const startedAtMs = 40_000
  setGatewayProxyHealthNowForTest(startedAtMs)
  await suppressGatewayUpstreamBucketForSecondsAsync(first, 600, 'long_explicit_suppression', { bucketScope: 'proxy' })
  const initial = store.snapshot<BucketSnapshot>(stateKey)
  const initialTtlMs = store.ttlMs(stateKey)
  assert(initial?.avoidUntilMs && initialTtlMs, 'long suppression must persist an avoid deadline and TTL')

  setGatewayProxyHealthNowForTest(startedAtMs + 1)
  await recordGatewayProxyFailureAsync(first, 'short_default_failure')
  await suppressGatewayUpstreamBucketForSecondsAsync(first, 1, 'short_explicit_suppression', { bucketScope: 'proxy' })
  const after = store.snapshot<BucketSnapshot>(stateKey)
  const afterTtlMs = store.ttlMs(stateKey)
  assert(after?.avoidUntilMs && afterTtlMs, 'later short mutations must retain the bucket snapshot')
  assert.equal(after.avoidUntilMs, initial.avoidUntilMs, 'short failure or suppression must not shorten an existing avoid deadline')
  assert(
    startedAtMs + 1 + afterTtlMs >= startedAtMs + initialTtlMs,
    'short mutations must not move the absolute Redis expiry earlier'
  )
}

function proxyBucketStateKey(value: OpenAIAccountSecret): string {
  const bucketKey = gatewayUpstreamBucketKeys(value, 'proxy')[0]
  assert(bucketKey, 'test account must produce a proxy bucket key')
  return `bucket:${bucketKey}`
}

function account(
  id: string,
  proxyProfileId: string,
  baseUrl = 'https://shared.example/v1',
  credentialSourceAccountId?: string
): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'sys_admin',
    name: id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    type: 'api_key',
    status: 'active',
    credentials: {},
    apiKey: 'sk-test',
    baseUrl,
    concurrencyLimit: 20,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'responses_sse',
    schedulable: true,
    proxyProfileId,
    credentialSourceAccountId,
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    streamFailureCount: 0
  } as OpenAIAccountSecret
}

function deferred(): Deferred {
  let resolve!: () => void
  const promise = new Promise<void>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function cloneValue<T>(value: T): T {
  if (value === undefined) return value
  return JSON.parse(JSON.stringify(value)) as T
}
