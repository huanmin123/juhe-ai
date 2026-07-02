import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'

runtimeConfig.runtimeStateDriver = 'memory'

const { createRuntimeProbeStateStore } = await import('../../shared/runtime-probe-state-store.js')

interface ProbeState {
  runtimeKey: string
  generation: number
  nextProbeAtMs: number
  accountId: string
  startedAtMs?: number
  lastObservedAtMs?: number
  failureCount?: number
  precheckRequested?: boolean
  clientIpMarkers?: string[]
  distinctClientIpCount?: number
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

const mergeOptions = {
  incrementFields: ['failureCount'],
  maxFields: ['lastObservedAtMs'],
  minFields: ['startedAtMs', 'nextProbeAtMs'],
  booleanOrFields: ['precheckRequested'],
  unionArrayFields: [{ field: 'clientIpMarkers', countField: 'distinctClientIpCount', maxItems: 8 }]
} as const
const mergeGeneration1 = await store.nextGeneration('acct_merge', 60_000)
const mergeGeneration2 = await store.nextGeneration('acct_merge', 60_000)
const mergeGeneration3 = await store.nextGeneration('acct_merge', 60_000)
const mergedFirst = await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration1,
  nextProbeAtMs: now + 30_000,
  accountId: 'acct_merge',
  startedAtMs: now + 1_000,
  lastObservedAtMs: now + 1_000,
  failureCount: 1,
  precheckRequested: false,
  clientIpMarkers: ['ip-a'],
  distinctClientIpCount: 1
}, 60_000, mergeOptions)
assert.equal(mergedFirst?.failureCount, 1, '首次 merge 应写入失败次数')
const mergedSecond = await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration2,
  nextProbeAtMs: now + 10_000,
  accountId: 'acct_merge',
  startedAtMs: now,
  lastObservedAtMs: now + 2_000,
  failureCount: 1,
  precheckRequested: true,
  clientIpMarkers: ['ip-a', 'ip-b'],
  distinctClientIpCount: 2
}, 60_000, mergeOptions)
assert.equal(mergedSecond?.generation, mergeGeneration2, 'merge 后应保留最新 generation')
assert.equal(mergedSecond?.failureCount, 2, '并发失败 merge 应按增量累加失败次数')
assert.equal(mergedSecond?.nextProbeAtMs, now + 10_000, 'merge 应保留更早的下一次探针时间')
assert.equal(mergedSecond?.startedAtMs, now, 'merge 应保留更早的观察开始时间')
assert.equal(mergedSecond?.lastObservedAtMs, now + 2_000, 'merge 应保留更新的观察时间')
assert.equal(mergedSecond?.precheckRequested, true, 'merge 应保留任一来源的 precheck 请求')
assert.deepEqual(mergedSecond?.clientIpMarkers, ['ip-a', 'ip-b'], 'merge 应合并并去重观测集合')
assert.equal(mergedSecond?.distinctClientIpCount, 2, 'merge 应按观测集合更新 distinct 计数')
assert.equal(await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration1,
  nextProbeAtMs: now,
  accountId: 'acct_merge',
  failureCount: 1
}, 60_000, mergeOptions), undefined, '旧 generation merge 不能覆盖新状态')
const mergedThird = await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration3,
  nextProbeAtMs: now + 20_000,
  accountId: 'acct_merge',
  failureCount: 1,
  clientIpMarkers: ['ip-c'],
  distinctClientIpCount: 1
}, 60_000, mergeOptions)
assert.equal(mergedThird?.failureCount, 3, '后续新 generation merge 应继续累加失败次数')
assert.equal(mergedThird?.distinctClientIpCount, 3, '后续新 generation merge 应继续累计 distinct 观测')

console.log('runtime-probe-state-store-regression passed')
