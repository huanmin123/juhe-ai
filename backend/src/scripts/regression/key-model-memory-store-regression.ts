import assert from 'node:assert/strict'

import {
  InMemoryKeyModelRuntimeStore,
  type KeyModelFailureIntent
} from '../../modules/gateway/runtime/key-model-redis-store.js'
import { type CapabilityKey } from '../../modules/gateway/runtime/key-model-runtime.js'

const capability: CapabilityKey = {
  credentialSourceAccountId: 'memory-account',
  keyFingerprint: 'memory-key',
  clientModel: 'gpt-5.5',
  clientEndpointFamily: 'responses',
  finalUpstreamModel: 'gpt-5.5',
  upstreamEndpointMode: 'responses_sse',
  dispatchRevision: 1
}

const store = new InMemoryKeyModelRuntimeStore()
const admissions = await Promise.all(Array.from({ length: 10 }, (_, index) => store.admitForeground(capability, `attempt-${index}`)))
assert.equal(admissions.filter((item) => item.status === 'admitted').length, 2, '单机内存 admission 同一 CapabilityKey 最多允许 2 个未提交请求')
assert.equal(admissions.filter((item) => item.status === 'busy').length, 8, '单机内存 admission 超出 2 个请求必须返回 busy')

const permit = admissions.find((item) => item.status === 'admitted')
assert.equal(await store.releaseForeground(permit!.permit), true)
const intent: KeyModelFailureIntent = {
  intentId: 'memory-intent-1',
  requestId: 'memory-request-1',
  attemptId: permit!.permit.attemptId,
  capability,
  observedAtMs: Date.now(),
  outcome: 'upstream_not_complete',
  sourceFence: 'memory-fence',
  permit: permit!.permit
}
const failure = await store.recordFailure(intent)
assert.equal(failure.status, 'applied')
assert.equal(failure.state.phase, 'OPEN')
assert.equal(failure.state.retryAtMs! - failure.state.lastObservedAtMs, 5_000)
assert.equal((await store.admitForeground(capability, 'blocked-after-failure')).status, 'blocked')

console.log('key-model-memory-store regression passed')
