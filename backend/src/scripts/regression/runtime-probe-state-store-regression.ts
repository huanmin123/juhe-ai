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

assert.equal(await store.set({
  runtimeKey: 'acct_a',
  generation: generation2,
  nextProbeAtMs: now - 1,
  accountId: 'acct_a'
}, 60_000), true, '新 generation 状态应写入成功')
assert.equal(await store.set({
  runtimeKey: 'acct_a',
  generation: generation1,
  nextProbeAtMs: now + 120_000,
  accountId: 'acct_a'
}, 60_000), false, '旧 generation 状态不能覆盖新 generation 状态')
assert.equal((await store.get('acct_a'))?.generation, generation2, '旧 generation 写入被拒绝后应保留新状态')
assert.equal(await store.set({
  runtimeKey: 'acct_b',
  generation: await store.nextGeneration('acct_b', 60_000),
  nextProbeAtMs: now + 60_000,
  accountId: 'acct_b'
}, 60_000), true, '第二个探针状态应写入成功')

assert.deepEqual(await store.listDue(now, 10), ['acct_a'], 'due 索引只应返回到期 runtimeKey')
assert.equal((await store.get('acct_a'))?.generation, generation2, '探针状态应能按 runtimeKey 读取')

assert.equal(await store.deleteGeneration('acct_a', generation1), false, '旧 generation 不能删除新 generation 状态')
assert.equal((await store.get('acct_a'))?.generation, generation2, '旧 generation 删除失败后应保留新状态')
assert.equal(await store.deleteGeneration('acct_a', generation2), true, '当前 generation 应允许条件删除状态')
assert.equal(await store.get('acct_a'), undefined, '条件删除状态后不应再读取到探针状态')
assert.deepEqual(await store.listDue(now + 120_000, 10), ['acct_b'], '删除状态后 due 索引不应残留旧 runtimeKey')

await store.delete('acct_b')
assert.equal(await store.get('acct_b'), undefined, '无条件删除应继续可用于手动清理状态')

console.log('runtime-probe-state-store-regression passed')
