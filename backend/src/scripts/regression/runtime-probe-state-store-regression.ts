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
  halfOpenLeaseId?: string
  halfOpenLeaseUntilMs?: number
  halfOpenPreviousNextProbeAtMs?: number
  probeRunId?: string
  probeRunUntilMs?: number
  probeRunPreviousNextProbeAtMs?: number
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
assert.deepEqual(await store.scheduledRuntimeKeys(['acct_a', 'acct_b', 'acct_missing']), new Set(['acct_a', 'acct_b']), '批量调度查询应只返回 due index 中仍有效的 runtimeKey')
assert.equal((await store.get('acct_a'))?.generation, generation2, '探针状态应能按 runtimeKey 读取')

assert.equal(await store.deleteGeneration('acct_a', generation1), false, '旧 generation 不能删除新 generation 状态')
assert.equal((await store.get('acct_a'))?.generation, generation2, '旧 generation 删除失败后应保留新状态')
assert.equal(await store.deleteGeneration('acct_a', generation2), true, '当前 generation 应允许条件删除状态')
assert.equal(await store.get('acct_a'), undefined, '条件删除状态后不应再读取到探针状态')
assert.deepEqual(await store.scheduledRuntimeKeys(['acct_a', 'acct_b']), new Set(['acct_b']), '条件删除必须同步移除 due membership')
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

const leaseStore = store as typeof store & {
  acquireGenerationLease(runtimeKey: string, generation: number, leaseId: string, leaseUntilMs: number, ttlMs: number): Promise<ProbeState | undefined>
  releaseGenerationLease(runtimeKey: string, generation: number, leaseId: string, ttlMs: number): Promise<boolean>
  completeGenerationLease(runtimeKey: string, generation: number, leaseId: string): Promise<boolean>
}
assert.equal(typeof leaseStore.acquireGenerationLease, 'function', 'probe store 必须提供 generation 原子半开租约')
assert.equal(typeof leaseStore.releaseGenerationLease, 'function', 'probe store 必须提供 generation 条件租约释放')
const leaseGeneration = await leaseStore.nextGeneration('acct_half_open', 60_000)
await leaseStore.set({
  runtimeKey: 'acct_half_open',
  generation: leaseGeneration,
  nextProbeAtMs: now + 30_000,
  accountId: 'acct_half_open',
  phase: 'precheck_pending'
}, 60_000)
const [leaseA, leaseB] = await Promise.all([
  leaseStore.acquireGenerationLease('acct_half_open', leaseGeneration, 'lease-a', now + 10_000, 60_000),
  leaseStore.acquireGenerationLease('acct_half_open', leaseGeneration, 'lease-b', now + 10_000, 60_000)
])
assert.equal([leaseA, leaseB].filter(Boolean).length, 1, '同一 runtimeKey + generation 只能原子取得一个半开租约')
const acquiredLeaseId = leaseA?.halfOpenLeaseId ?? leaseB?.halfOpenLeaseId
assert(acquiredLeaseId, '单飞半开应返回实际取得的租约 ID')
assert.equal((await leaseStore.listDue(now + 1, 20)).includes('acct_half_open'), false, '半开租约期间必须把 due 推迟到 leaseUntil，避免后台探针并发')
assert.equal(
  await leaseStore.acquireGenerationLease('acct_half_open', leaseGeneration - 1, 'lease-stale', now + 20_000, 60_000),
  undefined,
  '旧 generation 不能取得当前状态的半开租约'
)
assert.equal(await leaseStore.releaseGenerationLease('acct_half_open', leaseGeneration, 'lease-wrong', 60_000), false, '错误租约不得释放当前半开')
assert.equal(await leaseStore.releaseGenerationLease('acct_half_open', leaseGeneration, acquiredLeaseId, 60_000), true, '失败或取消应按 generation + leaseId 恢复原 precheck')
const restoredPrecheck = await leaseStore.get('acct_half_open')
assert.equal(restoredPrecheck?.phase, 'precheck_pending', '释放半开租约后必须保留原 precheck 状态')
assert.equal(restoredPrecheck?.halfOpenLeaseId, undefined, '释放后不得残留租约 ID')
assert.equal(restoredPrecheck?.nextProbeAtMs, now + 30_000, '释放租约后必须恢复原后台探针时间')
const successLease = await leaseStore.acquireGenerationLease('acct_half_open', leaseGeneration, 'lease-success', now + 10_000, 60_000)
assert.equal(successLease?.halfOpenLeaseId, 'lease-success', '释放后应允许下一次单飞半开')
assert.equal(await leaseStore.completeGenerationLease('acct_half_open', leaseGeneration, acquiredLeaseId), false, '同 generation 旧租约不得误清新租约')
assert.equal(await leaseStore.completeGenerationLease('acct_half_open', leaseGeneration, 'lease-success'), true, '完整成功只能按匹配 generation + leaseId 清理软阻断')
assert.equal(await leaseStore.get('acct_half_open'), undefined, '完整成功条件删除后不应残留 precheck 或租约')

const wrongPhaseGeneration = await leaseStore.nextGeneration('acct_wrong_phase', 60_000)
await leaseStore.set({ runtimeKey: 'acct_wrong_phase', generation: wrongPhaseGeneration, nextProbeAtMs: now, accountId: 'acct_wrong_phase', phase: 'recovery_wait' }, 60_000)
assert.equal(await leaseStore.acquireGenerationLease('acct_wrong_phase', wrongPhaseGeneration, 'lease-wrong-phase', now + 10_000, 60_000), undefined, '原子 acquire 必须拒绝非 precheck_pending phase')

const runStore = store as typeof store & {
  acquireGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<ProbeState | undefined>
  renewGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<boolean>
  commitGenerationRun(state: ProbeState, runId: string, ttlMs: number): Promise<boolean>
  deleteGenerationRun(runtimeKey: string, generation: number, runId: string): Promise<boolean>
}
const runGeneration = await runStore.nextGeneration('acct_probe_run', 60_000)
await runStore.set({
  runtimeKey: 'acct_probe_run',
  generation: runGeneration,
  nextProbeAtMs: now,
  accountId: 'acct_probe_run',
  phase: 'precheck_pending'
}, 60_000)
const activeRun = await runStore.acquireGenerationRun('acct_probe_run', runGeneration, 'run-a', now + 10_000, 60_000)
assert.equal(activeRun?.probeRunId, 'run-a', '到期状态应能按 generation 原子取得后台执行令牌')
assert.equal(activeRun?.nextProbeAtMs, now + 10_000, '后台执行期间应把内部 due 推迟到 runUntil')
assert.equal(
  await runStore.renewGenerationRun('acct_probe_run', runGeneration, 'run-a', now + 15_000, 60_000),
  true,
  '当前 generation + runId 应能在最终副作用前原子续租'
)
assert.equal((await runStore.get('acct_probe_run'))?.probeRunUntilMs, now + 15_000, '原子续租应推迟 run 的过期时间')
assert.equal(
  await runStore.renewGenerationRun('acct_probe_run', runGeneration, 'run-stale', now + 20_000, 60_000),
  false,
  '迟到的旧 runId 不得取得最终副作用写入权'
)
assert.equal(await runStore.acquireGenerationRun('acct_probe_run', runGeneration, 'run-b', now + 20_000, 60_000), undefined, '有效后台 run 必须拒绝其他节点重复执行')
assert.equal(await leaseStore.acquireGenerationLease('acct_probe_run', runGeneration, 'lease-during-run', now + 20_000, 60_000), undefined, '有效后台 run 期间必须拒绝用户 half-open 租约')
assert.equal(await runStore.commitGenerationRun({ ...activeRun!, nextProbeAtMs: now + 30_000, accountId: 'acct_probe_run_committed' }, 'run-wrong', 60_000), false, '非当前 runId 不得提交后台执行结果')
assert.equal(await runStore.commitGenerationRun({ ...activeRun!, nextProbeAtMs: now + 30_000, accountId: 'acct_probe_run_committed' }, 'run-a', 60_000), true, '当前 generation + runId 应能原子提交状态与下一个 due')
const committedRun = await runStore.get('acct_probe_run')
assert.equal(committedRun?.accountId, 'acct_probe_run_committed', '后台 run 提交应保存新状态')
assert.equal(committedRun?.probeRunId, undefined, '后台 run 提交后不应残留执行令牌')
assert.equal(committedRun?.nextProbeAtMs, now + 30_000, '后台 run 提交应恢复真实下一次 due')

const expiredTakeoverGeneration = await runStore.nextGeneration('acct_expired_run_takeover', 60_000)
await runStore.set({ runtimeKey: 'acct_expired_run_takeover', generation: expiredTakeoverGeneration, nextProbeAtMs: now, accountId: 'acct_expired_run_takeover' }, 60_000)
assert(await runStore.acquireGenerationRun('acct_expired_run_takeover', expiredTakeoverGeneration, 'run-old', now - 1, 60_000), '应能建立已过期后台 run')
const replacementRun = await runStore.acquireGenerationRun('acct_expired_run_takeover', expiredTakeoverGeneration, 'run-new', now + 10_000, 60_000)
assert.equal(replacementRun?.probeRunId, 'run-new', '已过期后台 run 必须允许新 runId 接管')
assert.equal(await runStore.commitGenerationRun({ ...replacementRun!, nextProbeAtMs: now + 20_000 }, 'run-new', 60_000), true, '接管后的新 run 应能正常提交')

const expiredRun = await runStore.acquireGenerationRun('acct_probe_run', runGeneration, 'run-expired', now - 1, 60_000)
assert.equal(expiredRun?.probeRunId, 'run-expired', '测试应能建立已过期 run 以模拟节点中断')
const takeoverLease = await leaseStore.acquireGenerationLease('acct_probe_run', runGeneration, 'lease-after-expired-run', now + 20_000, 60_000)
assert.equal(takeoverLease?.halfOpenLeaseId, 'lease-after-expired-run', '后台 run 过期后应允许 half-open 接管')
assert.equal(takeoverLease?.probeRunId, undefined, 'half-open 接管必须清理过期 runId，防止迟到提交')
assert.equal(await runStore.commitGenerationRun({ ...expiredRun!, nextProbeAtMs: now + 40_000 }, 'run-expired', 60_000), false, 'half-open 接管后的迟到 run 结果不得覆盖新租约')
assert.equal(await leaseStore.releaseGenerationLease('acct_probe_run', runGeneration, 'lease-after-expired-run', 60_000), true, '接管的 half-open 租约应可正常释放')

const blockingLease = await leaseStore.acquireGenerationLease('acct_probe_run', runGeneration, 'lease-blocks-run', now + 20_000, 60_000)
assert.equal(blockingLease?.halfOpenLeaseId, 'lease-blocks-run', '应能建立有效 half-open 租约')
assert.equal(await runStore.acquireGenerationRun('acct_probe_run', runGeneration, 'run-during-lease', now + 30_000, 60_000), undefined, '有效 half-open 租约期间必须拒绝后台 run')
assert.equal(await leaseStore.releaseGenerationLease('acct_probe_run', runGeneration, 'lease-blocks-run', 60_000), true, '阻塞 run 的租约应可正常释放')

const deletableRun = await runStore.acquireGenerationRun('acct_probe_run', runGeneration, 'run-delete', now + 10_000, 60_000)
assert.equal(deletableRun?.probeRunId, 'run-delete', '删除测试前应取得 run')
assert.equal(await runStore.deleteGenerationRun('acct_probe_run', runGeneration, 'run-wrong'), false, '错误 runId 不得删除当前状态')
assert.equal(await runStore.deleteGenerationRun('acct_probe_run', runGeneration - 1, 'run-delete'), false, '旧 generation 的 run 不得删除当前状态')
assert.equal(await runStore.deleteGenerationRun('acct_probe_run', runGeneration, 'run-delete'), true, '当前 generation + runId 应能原子删除状态与 due')
assert.equal(
  await runStore.renewGenerationRun('acct_probe_run', runGeneration, 'run-delete', now + 20_000, 60_000),
  false,
  '真实请求成功清理运行态后，迟到探针失败不得重新获取写入权'
)
assert.deepEqual(await runStore.scheduledRuntimeKeys(['acct_probe_run']), new Set(), '按 run 删除后 due membership 不应残留')

console.log('runtime-probe-state-store-regression passed')
