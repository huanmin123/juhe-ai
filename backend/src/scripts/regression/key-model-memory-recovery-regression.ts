import assert from 'node:assert/strict'

import { InMemoryKeyModelRuntimeStore, type KeyModelFailureIntent } from '../../modules/gateway/runtime/key-model-redis-store.js'
import { KeyModelMemoryRecoveryRunner } from '../../modules/gateway/runtime/key-model-memory-recovery.js'
import type { CapabilityKey } from '../../modules/gateway/runtime/key-model-runtime.js'

function capability(key: string): CapabilityKey {
  return {
    credentialSourceAccountId: 'recovery-memory-account',
    keyFingerprint: key,
    clientModel: 'gpt-5.5',
    clientEndpointFamily: 'responses',
    finalUpstreamModel: 'gpt-5.5',
    upstreamEndpointMode: 'responses_sse',
    dispatchRevision: 1
  }
}

async function fail(store: InMemoryKeyModelRuntimeStore, key: CapabilityKey, nowMs: number): Promise<void> {
  const admission = await store.admitForeground(key, `attempt-${key.keyFingerprint}-${nowMs}`)
  assert.equal(admission.status, 'admitted')
  const intent: KeyModelFailureIntent = {
    intentId: `intent-${key.keyFingerprint}-${nowMs}`,
    requestId: `request-${key.keyFingerprint}-${nowMs}`,
    attemptId: admission.permit.attemptId,
    capability: key,
    observedAtMs: nowMs,
    outcome: 'upstream_not_complete',
    sourceFence: `fence-${key.keyFingerprint}`,
    permit: admission.permit,
    recoveryTarget: { accountId: key.credentialSourceAccountId, groupId: 'group-1', systemAccountId: 'system-1' }
  }
  assert.equal((await store.recordFailure(intent)).status, 'applied')
}

const store = new InMemoryKeyModelRuntimeStore()
const times = [6_000, 96_000, 216_000]
let nowMs = 1_000
const key = capability('key-a')
await fail(store, key, nowMs)
const outcomes = ['complete_success', 'complete_success', 'complete_success'] as const
let probeCalls = 0
const runner = new KeyModelMemoryRecoveryRunner({
  store,
  now: () => nowMs,
  probe: async () => outcomes[probeCalls++] ?? 'unknown'
})

nowMs = 5_999
assert.equal((await runner.sweep()).startedCount, 0)
nowMs = times[0]!
assert.equal((await runner.sweep()).settledCount, 1)
assert.equal((await store.get(key))?.recoverySuccessCount, 1)
nowMs = times[1]!
assert.equal((await runner.sweep()).settledCount, 1)
assert.equal((await store.get(key))?.recoverySuccessCount, 2, '排队 90 秒仍应保留真实恢复成功计数')
nowMs = times[2]!
assert.equal((await runner.sweep()).settledCount, 1)
assert.equal(await store.get(key), undefined, '第三次真实恢复成功应关闭并移除状态')

const resetKey = capability('key-reset')
await fail(store, resetKey, 300_000)
nowMs = 305_000
const resetRunner = new KeyModelMemoryRecoveryRunner({ store, now: () => nowMs, probe: async () => 'complete_success' })
await resetRunner.sweep()
nowMs = 426_000
await resetRunner.sweep()
assert.equal((await store.get(resetKey))?.recoverySuccessCount, 1, '真实成功间隔超过 2 分钟必须重新计为 1')

const priorityStore = new InMemoryKeyModelRuntimeStore()
const priorityRecover = capability('key-priority-recover')
const priorityOpen = capability('key-priority-open')
await fail(priorityStore, priorityRecover, 500_000)
const priorityCalls: string[] = []
const priorityRunner = new KeyModelMemoryRecoveryRunner({
  store: priorityStore,
  now: () => nowMs,
  probe: async ({ state }) => {
    priorityCalls.push(state.keyFingerprint)
    return 'complete_success'
  }
})
nowMs = 505_000
await priorityRunner.sweep()
await fail(priorityStore, priorityOpen, 510_000)
nowMs = 515_000
await priorityRunner.sweep()
assert.equal(priorityCalls.indexOf('key-priority-recover') < priorityCalls.indexOf('key-priority-open'), true, 'RECOVERING continuation 必须优先于普通 OPEN probe')
assert.equal((await priorityStore.get(priorityOpen))?.phase, 'RECOVERING', '普通 OPEN 可借用非保留槽，但必须在 continuation 之后执行')

console.log('key-model-memory-recovery regression passed')
