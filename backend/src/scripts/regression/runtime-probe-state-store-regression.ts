import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'

runtimeConfig.runtimeStateDriver = 'memory'

const { createRuntimeProbeStateStore } = await import('../../shared/runtime-probe-state-store.js')

interface ProbeState {
  runtimeKey: string
  generation: number
  nextProbeAtMs: number
  accountId: string
  phase?: 'recovery_wait' | 'precheck_pending'
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

const firstSignalGeneration = await store.nextGeneration('acct_signal_once', 60_000)
assert.equal(await store.setIfAbsent({
  runtimeKey: 'acct_signal_once',
  generation: firstSignalGeneration,
  nextProbeAtMs: now + 30_000,
  accountId: 'acct_signal_once',
  failureCount: 1
}, 60_000), true, '首次用户失败信号应创建后台核实事件')
assert.equal(await store.setIfAbsent({
  runtimeKey: 'acct_signal_once',
  generation: await store.nextGeneration('acct_signal_once', 60_000),
  nextProbeAtMs: now,
  accountId: 'acct_signal_once',
  failureCount: 99
}, 120_000), false, '后续用户失败信号不得改写或续期已有后台事件')
assert.equal((await store.get('acct_signal_once'))?.generation, firstSignalGeneration, '重复信号不得替换在途探针 generation')
assert.equal((await store.get('acct_signal_once'))?.failureCount, 1, '重复信号不得把用户失败计数合并进后台确认状态')
await store.delete('acct_signal_once')

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
  preserveCurrentFields: ['phase'],
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
  phase: 'precheck_pending',
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
  phase: 'recovery_wait',
  startedAtMs: now,
  lastObservedAtMs: now + 2_000,
  failureCount: 1,
  precheckRequested: true,
  clientIpMarkers: ['ip-a', 'ip-b'],
  distinctClientIpCount: 2
}, 60_000, mergeOptions)
assert.equal(mergedSecond?.generation, mergeGeneration1, '同一事件的新失败观测不得滚动 generation 使在途探针失效')
assert.equal(mergedSecond?.phase, 'precheck_pending', '迟到的用户请求观测不得把后台已确认的 precheck 状态降级回 recovery_wait')
assert.equal(mergedSecond?.failureCount, 2, '并发失败 merge 应按增量累加失败次数')
assert.equal(mergedSecond?.nextProbeAtMs, now + 10_000, 'merge 应保留更早的下一次探针时间')
assert.equal(mergedSecond?.startedAtMs, now, 'merge 应保留更早的观察开始时间')
assert.equal(mergedSecond?.lastObservedAtMs, now + 2_000, 'merge 应保留更新的观察时间')
assert.equal(mergedSecond?.precheckRequested, true, 'merge 应保留任一来源的 precheck 请求')
assert.deepEqual(mergedSecond?.clientIpMarkers, ['ip-a', 'ip-b'], 'merge 应合并并去重观测集合')
assert.equal(mergedSecond?.distinctClientIpCount, 2, 'merge 应按观测集合更新 distinct 计数')
const mergedLateOlderGeneration = await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration1,
  nextProbeAtMs: now,
  accountId: 'acct_merge',
  failureCount: 1,
  precheckRequested: true,
  clientIpMarkers: ['ip-late'],
  distinctClientIpCount: 1
}, 60_000, mergeOptions)
assert.equal(mergedLateOlderGeneration?.generation, mergeGeneration1, '同一事件 merge 应始终保留已建立的 generation')
assert.equal(mergedLateOlderGeneration?.failureCount, 3, '旧 generation 迟到观测仍应累加失败次数')
assert.equal(mergedLateOlderGeneration?.precheckRequested, true, '旧 generation 迟到观测仍应合并 OR 字段')
assert.deepEqual(mergedLateOlderGeneration?.clientIpMarkers, ['ip-a', 'ip-b', 'ip-late'], '旧 generation 迟到观测仍应合并 distinct marker')
const mergedThird = await store.merge({
  runtimeKey: 'acct_merge',
  generation: mergeGeneration3,
  nextProbeAtMs: now + 20_000,
  accountId: 'acct_merge',
  failureCount: 1,
  clientIpMarkers: ['ip-c'],
  distinctClientIpCount: 1
}, 60_000, mergeOptions)
assert.equal(mergedThird?.generation, mergeGeneration1, '后续观测只能合并事实，不能替换事件 generation')
assert.equal(mergedThird?.failureCount, 4, '后续新 generation merge 应继续累加失败次数')
assert.equal(mergedThird?.distinctClientIpCount, 4, '后续新 generation merge 应继续累计 distinct 观测')

console.log('runtime-probe-state-store-regression passed')
