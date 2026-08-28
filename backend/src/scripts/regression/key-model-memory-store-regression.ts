import assert from 'node:assert/strict'

import {
  InMemoryKeyModelRuntimeStore,
  keyModelClosedRetentionMs,
  keyModelReceiptRetentionMs,
  keyModelStateCapacity,
  type KeyModelFailureIntent
} from '../../modules/gateway/runtime/key-model-redis-store.js'
import { capabilityHash, createKeyModelOpenState, type CapabilityKey, type KeyModelState } from '../../modules/gateway/runtime/key-model-runtime.js'

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

const receiptStore = new InMemoryKeyModelRuntimeStore()
const receiptNow = Date.now()
const receiptCapability = { ...capability, keyFingerprint: 'memory-receipt-key' }
const receiptIntent: KeyModelFailureIntent = {
  ...intent,
  intentId: 'memory-receipt-expiry',
  requestId: 'memory-receipt-request',
  attemptId: 'memory-receipt-attempt',
  capability: receiptCapability,
  observedAtMs: receiptNow,
  permit: undefined
}
assert.equal((await receiptStore.recordFailure(receiptIntent)).status, 'applied')
const receiptInternals = receiptStore as unknown as { states: Map<string, KeyModelState> }
await receiptStore.listDue(receiptNow + keyModelReceiptRetentionMs + 1, 128)
receiptInternals.states.delete(capabilityHash(receiptCapability))
assert.equal(
  (await receiptStore.recordFailure({ ...receiptIntent, observedAtMs: receiptNow + keyModelReceiptRetentionMs + 1 })).status,
  'applied',
  '失败 intent 回执超过 5 分钟后不得无限保留幂等结果'
)

const retentionStore = new InMemoryKeyModelRuntimeStore()
const retentionCapability = { ...capability, keyFingerprint: 'memory-closed-retention-key' }
const retentionNow = Date.now()
assert.equal((await retentionStore.recordFailure({ ...intent, intentId: 'memory-closed-retention', capability: retentionCapability, observedAtMs: retentionNow, permit: undefined })).status, 'applied')
const acquired = await retentionStore.acquireRecoveryLease({ capability: retentionCapability, generation: 1, dispatchRevision: 1, leaseId: 'memory-closed-lease-1', nowMs: retentionNow + 5_000 })
assert.equal(acquired.status, 'applied')
let retained = acquired.state
for (let success = 1; success <= 3; success += 1) {
  const settled = await retentionStore.settleRecovery({
    capability: retentionCapability,
    generation: retained.generation,
    dispatchRevision: retained.dispatchRevision,
    leaseId: `memory-closed-lease-${success}`,
    outcome: 'complete_success',
    nowMs: retentionNow + 5_000 + (success - 1) * 10_000
  })
  if (success < 3) {
    retained = (await retentionStore.acquireRecoveryLease({
      capability: retentionCapability,
      generation: settled.state.generation,
      dispatchRevision: settled.state.dispatchRevision,
      leaseId: `memory-closed-lease-${success + 1}`,
      nowMs: retentionNow + 5_000 + success * 10_000
    })).state
  }
}
assert.equal((await retentionStore.get(retentionCapability))?.phase, 'CLOSED', 'CLOSED 状态必须保留 5 分钟')
await retentionStore.listDue(retentionNow + 5_000 + 2 * 10_000 + keyModelClosedRetentionMs + 1, 128)
assert.equal(await retentionStore.get(retentionCapability), undefined, 'CLOSED 状态到期后必须清理')

const capacityStore = new InMemoryKeyModelRuntimeStore()
const capacityInternals = capacityStore as unknown as {
  states: Map<string, KeyModelState>
  closedUntil: Map<string, number>
}
const capacityNow = Date.now()
const closedSeed = { ...createKeyModelOpenState({ ...capability, keyFingerprint: 'memory-capacity-closed-seed' }, capacityNow), phase: 'CLOSED' as const }
for (let index = 0; index < keyModelStateCapacity; index += 1) {
  const hash = `closed-${index}`
  capacityInternals.states.set(hash, { ...closedSeed, capabilityHash: hash })
  capacityInternals.closedUntil.set(hash, capacityNow + keyModelClosedRetentionMs)
}
assert.equal(
  (await capacityStore.recordFailure({ ...intent, intentId: 'memory-capacity-evicts-closed', capability: { ...capability, keyFingerprint: 'memory-capacity-evicts-closed' }, observedAtMs: capacityNow, permit: undefined })).status,
  'applied',
  '容量满时必须优先清理 CLOSED 状态，为新 OPEN 腾出空间'
)
capacityInternals.states.clear()
capacityInternals.closedUntil.clear()
for (let index = 0; index < keyModelStateCapacity; index += 1) {
  capacityInternals.states.set(`open-${index}`, { ...closedSeed, capabilityHash: `open-${index}`, phase: 'OPEN', retryAtMs: capacityNow + 5_000 })
}
assert.equal(
  (await capacityStore.recordFailure({ ...intent, intentId: 'memory-capacity-exhausted', capability: { ...capability, keyFingerprint: 'memory-capacity-exhausted' }, observedAtMs: capacityNow, permit: undefined })).status,
  'capacity_exhausted',
  '容量满且没有 CLOSED 状态时不得删除活动 state'
)

console.log('key-model-memory-store regression passed')
