import assert from 'node:assert/strict'

import { InMemoryKeyModelRuntimeStore, type KeyModelFailureIntent } from '../../modules/gateway/runtime/key-model-redis-store.js'
import { KeyModelMemoryRecoveryRunner } from '../../modules/gateway/runtime/key-model-memory-recovery.js'
import type { CapabilityKey } from '../../modules/gateway/runtime/key-model-runtime.js'

function capability(key: string, source = 'recovery-memory-account'): CapabilityKey {
  return {
    credentialSourceAccountId: source,
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

async function recoverOnce(store: InMemoryKeyModelRuntimeStore, key: CapabilityKey, nowMs: number): Promise<void> {
  const current = await store.get(key)
  assert(current)
  const leaseId = `seed-recovery-${key.keyFingerprint}-${nowMs}`
  const acquired = await store.acquireRecoveryLease({
    capability: key,
    generation: current.generation,
    dispatchRevision: current.dispatchRevision,
    leaseId,
    nowMs
  })
  assert.equal(acquired.status, 'applied')
  const settled = await store.settleRecovery({
    capability: key,
    generation: acquired.state.generation,
    dispatchRevision: acquired.state.dispatchRevision,
    leaseId,
    outcome: 'complete_success',
    nowMs
  })
  assert.equal(settled.status, 'applied')
  assert.equal(settled.state.phase, 'RECOVERING')
}

async function waitForStarts(started: Promise<void>, label: string): Promise<void> {
  await Promise.race([
    started,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error(`${label} 未在 2 秒内启动`)), 2_000))
  ])
}

const store = new InMemoryKeyModelRuntimeStore()
let nowMs = Date.now()
const times = [nowMs + 5_000, nowMs + 95_000, nowMs + 215_000]
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
assert.equal((await store.get(key))?.phase, 'CLOSED', '第三次真实恢复成功后 CLOSED 状态必须保留 5 分钟供幂等与读取')
await store.listDue(nowMs + 5 * 60_000 + 1, 128)
assert.equal(await store.get(key), undefined, 'CLOSED 保留 5 分钟后应被清理')

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

const batchStore = new InMemoryKeyModelRuntimeStore()
for (let index = 0; index < 129; index += 1) {
  await fail(batchStore, capability(`key-batch-${index}`, `batch-source-${index}`), 600_000)
}
const batchRunner = new KeyModelMemoryRecoveryRunner({
  store: batchStore,
  now: () => 605_000,
  probe: async () => 'unknown'
})
assert.equal((await batchRunner.sweep()).dueCount, 128, '单机 recovery 每轮最多取得 128 个到期 CapabilityKey')

const continuationSourceStore = new InMemoryKeyModelRuntimeStore()
const continuationSourceKeys = ['key-continuation-source-a', 'key-continuation-source-b', 'key-continuation-source-c']
  .map((fingerprint) => capability(fingerprint, 'recovery-memory-source-limit'))
for (const item of continuationSourceKeys) {
  await fail(continuationSourceStore, item, 700_000)
  await recoverOnce(continuationSourceStore, item, 705_000)
}
let activeContinuations = 0
let maxActiveContinuations = 0
let releaseContinuations: (() => void) | undefined
let twoContinuationsStarted: (() => void) | undefined
const continuationsReleased = new Promise<void>((resolve) => { releaseContinuations = resolve })
const twoContinuationsObserved = new Promise<void>((resolve) => { twoContinuationsStarted = resolve })
const continuationSourceRunner = new KeyModelMemoryRecoveryRunner({
  store: continuationSourceStore,
  now: () => 715_000,
  probe: async () => {
    activeContinuations += 1
    maxActiveContinuations = Math.max(maxActiveContinuations, activeContinuations)
    if (activeContinuations === 2) twoContinuationsStarted?.()
    await continuationsReleased
    activeContinuations -= 1
    return 'unknown'
  }
})
const continuationSourceSweep = continuationSourceRunner.sweep()
await waitForStarts(twoContinuationsObserved, '同源 continuation')
await new Promise<void>((resolve) => setImmediate(resolve))
assert.equal(maxActiveContinuations, 2, '同一来源的 RECOVERING continuation 每轮最多并发 2 个')
releaseContinuations?.()
await continuationSourceSweep

const reservationStore = new InMemoryKeyModelRuntimeStore()
const recoveringKey = capability('key-reservation-recover', 'recovery-memory-reservation-source')
await fail(reservationStore, recoveringKey, 800_000)
await recoverOnce(reservationStore, recoveringKey, 805_000)
const sameSourceOpenKeys = ['key-reservation-open-a', 'key-reservation-open-b']
  .map((fingerprint) => capability(fingerprint, 'recovery-memory-reservation-source'))
for (const item of sameSourceOpenKeys) await fail(reservationStore, item, 810_000)
for (let index = 0; index < 25; index += 1) {
  await fail(reservationStore, capability(`key-reservation-open-${index}`, `recovery-memory-reservation-open-${index}`), 810_000)
}
let startedRecovering = 0
let startedOpen = 0
let maxReservationSourceProbes = 0
const activeBySource = new Map<string, number>()
let releaseReservation: (() => void) | undefined
let reservationStarts: (() => void) | undefined
const reservationReleased = new Promise<void>((resolve) => { releaseReservation = resolve })
const reservationObserved = new Promise<void>((resolve) => { reservationStarts = resolve })
const reservationRunner = new KeyModelMemoryRecoveryRunner({
  store: reservationStore,
  now: () => 815_000,
  probe: async ({ state }) => {
    const source = state.credentialSourceAccountId
    const active = (activeBySource.get(source) ?? 0) + 1
    activeBySource.set(source, active)
    maxReservationSourceProbes = Math.max(maxReservationSourceProbes, active)
    if (state.phase === 'HALF_OPEN' && state.keyFingerprint === recoveringKey.keyFingerprint) startedRecovering += 1
    else startedOpen += 1
    if (startedRecovering === 1 && startedOpen === 24) reservationStarts?.()
    await reservationReleased
    activeBySource.set(source, active - 1)
    return 'unknown'
  }
})
const reservationSweep = reservationRunner.sweep()
await waitForStarts(reservationObserved, 'continuation 保留槽')
assert.equal(startedRecovering, 1, '24 个 OPEN 压力下 RECOVERING continuation 必须获得优先启动')
assert.equal(startedOpen, 24, '有 continuation 等待时 OPEN 只能使用 32 个全局槽中的 24 个')
assert.equal(maxReservationSourceProbes, 2, '同源必须为 continuation 保留 1 个槽，至多允许一个 OPEN 借用余下槽位')
releaseReservation?.()
await reservationSweep

console.log('key-model-memory-recovery regression passed')
