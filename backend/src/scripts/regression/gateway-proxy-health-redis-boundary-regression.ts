import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../modules/gateway/runtime/proxy-health.service.ts', import.meta.url), 'utf8')
const halfOpenSource = source.slice(
  source.indexOf('async function ensureRedisHalfOpenProbe'),
  source.indexOf('export function recordGatewayUpstreamBucketFailure')
)
const failureMutationSource = source.slice(
  source.indexOf('async function recordGatewayUpstreamBucketFailureKeyAsync'),
  source.indexOf('function isHalfOpenProbeForAccount')
)
const successMutationSource = source.slice(
  source.indexOf('export async function recordGatewayUpstreamBucketSuccessAsync'),
  source.indexOf('export function recordGatewayProxySuccess')
)

assert.match(source, /createRuntimeStateStore\('gateway-upstream-bucket-health'\)/, 'proxy health Redis state must use the shared runtime-state namespace')
assert.doesNotMatch(source, /withRedisBucketEntryLock|acquireLock|releaseLock/, 'proxy health request paths must not wait on distributed locks')
assert.match(failureMutationSource, /mutateRedisBucketFailureEntry[\s\S]*compareSetJson/, 'concurrent failure samples must merge through CAS retries')
assert.match(halfOpenSource, /halfOpenUntilMs > now[\s\S]*return current[\s\S]*compareSetJson/, 'a live half-open lease must be reused before any CAS claim')
assert.doesNotMatch(halfOpenSource, /candidateAccountIds/, 'a live bucket lease must not be stolen by a disjoint request candidate set')
assert.match(source, /function claimRedisHalfOpenLeasesForAccounts[\s\S]*Promise\.all\(\[\.\.\.probeAccountByBucketKey\.entries\(\)\]/, 'independent expired bucket leases must be claimed in parallel')
assert.match(successMutationSource, /getRedisBucketFailureEntry[\s\S]*compareDeleteJson/, 'success must delete only the exact bucket snapshot it observed')
assert.match(successMutationSource, /successObservation = nextGatewayUpstreamBucketMutationObservation[\s\S]*getRedisBucketFailureEntry[\s\S]*gatewayUpstreamBucketFailureOccurredAfterObservation[\s\S]*compareDeleteJson/, 'success must fence failures newer than its method-entry observation before compare-delete')
assert.match(successMutationSource, /compareDeleteJson[\s\S]*getRedisBucketFailureEntry[\s\S]*sameGatewayUpstreamBucketFailureEvidence/, 'success must re-read after CAS contention and retry only when failure evidence is unchanged')
assert.match(source, /function sameGatewayUpstreamBucketFailureEvidence[\s\S]*failureCount[\s\S]*lastFailedAtMs[\s\S]*lastFailureGeneration[\s\S]*accountSamples/, 'success retry fencing must compare failure generation and failure evidence, excluding only half-open lease metadata')
assert.match(failureMutationSource, /lastFailureGeneration: latestFailure\.generation/, 'failure mutations must persist an ordered in-process generation for same-millisecond fencing')
assert.doesNotMatch(source, /upstreamBucketFailureStateStore\.delete|setRedisBucketFailureEntry/, 'bucket state must not use unconditional Redis overwrite or delete')
assert.match(source, /upstreamBucketFailureMaxAccountSamples = 256/, 'failure evidence payload must have an explicit sample bound')
assert.match(source, /function pruneAccountSamples[\s\S]*latestByAccountId[\s\S]*slice\(-upstreamBucketFailureMaxAccountSamples\)/, 'failure samples must deduplicate accounts and retain only the bounded newest set')
assert.match(source, /function gatewayFailureEvidenceAccountId[\s\S]*credentialSourceAccountId\?\.trim\(\) \|\| account\.id/, 'failure evidence must deduplicate authorized instances by physical credential source')
assert.match(source, /avoidUntilMs: Math\.max\(current\?\.avoidUntilMs \?\? 0, avoidUntilMs\)/, 'short explicit suppression must not shorten a longer avoid deadline')
assert.match(failureMutationSource, /Math\.max\(current\?\.avoidUntilMs \?\? 0, now \+ upstreamBucketFailureAvoidTtlMs\)/, 'default failures must not shorten a longer avoid deadline')
assert.match(source, /function redisBucketFailureEntryTtlMs[\s\S]*avoidRetentionMs[\s\S]*Math\.max\(1, Math\.trunc\(minimumTtlMs\), avoidRetentionMs, halfOpenRetentionMs\)/, 'Redis PX TTL must preserve the longest absolute bucket deadline')
assert.match(source, /function setMemoryBucketFailureEntry[\s\S]*existingExpiresAt[\s\S]*redisBucketFailureEntryTtlMs[\s\S]*Math\.max\(existingExpiresAt, now \+ effectiveTtlMs\)/, 'memory entry expiry must preserve the longest absolute bucket deadline')
assert.match(source, /recordGatewayProxySuccess\(account:[\s\S]*bucketScope: 'proxy'/, 'proxy-only success must not clear base URL or provider buckets')
assert.match(source, /recordGatewayProxySuccessAsync\(account:[\s\S]*bucketScope: 'proxy'/, 'async proxy-only success must not clear base URL or provider buckets')

console.log('gateway-proxy-health-redis-boundary-regression passed')
