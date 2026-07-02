import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'

runtimeConfig.runtimeStateDriver = 'memory'

const { createRuntimeProbeStateStore } = await import('../../shared/runtime-probe-state-store.js')

interface ProbeState {
  runtimeKey: string
  generation: number
  nextProbeAtMs: number
  accountId: string
}

const store = createRuntimeProbeStateStore<ProbeState>(`runtime-probe-state-regression-${Date.now()}`)
const now = Date.now()

const generation1 = await store.nextGeneration('acct_a', 60_000)
const generation2 = await store.nextGeneration('acct_a', 60_000)
assert.equal(generation1, 1, '探针 generation 应从 1 开始')
assert.equal(generation2, 2, '探针 generation 应按 runtimeKey 单调递增')

await store.set({
  runtimeKey: 'acct_a',
  generation: generation2,
  nextProbeAtMs: now - 1,
  accountId: 'acct_a'
}, 60_000)
await store.set({
  runtimeKey: 'acct_b',
  generation: await store.nextGeneration('acct_b', 60_000),
  nextProbeAtMs: now + 60_000,
  accountId: 'acct_b'
}, 60_000)

assert.deepEqual(await store.listDue(now, 10), ['acct_a'], 'due 索引只应返回到期 runtimeKey')
assert.equal((await store.get('acct_a'))?.generation, generation2, '探针状态应能按 runtimeKey 读取')

assert.equal(await store.acquireLock('acct_a', 'token-a', 60_000), true, '首次加锁应成功')
assert.equal(await store.acquireLock('acct_a', 'token-b', 60_000), false, '已有锁未释放时第二个 token 不应成功')
await store.releaseLock('acct_a', 'wrong-token')
assert.equal(await store.acquireLock('acct_a', 'token-c', 60_000), false, '错误 token 不能释放探针锁')
await store.releaseLock('acct_a', 'token-a')
assert.equal(await store.acquireLock('acct_a', 'token-c', 60_000), true, '正确 token 释放后应允许重新加锁')
await store.releaseLock('acct_a', 'token-c')

await store.delete('acct_a')
assert.equal(await store.get('acct_a'), undefined, '删除状态后不应再读取到探针状态')
assert.deepEqual(await store.listDue(now + 120_000, 10), ['acct_b'], '删除状态后 due 索引不应残留旧 runtimeKey')

console.log('runtime-probe-state-store-regression passed')
