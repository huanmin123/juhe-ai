import { strict as assert } from 'node:assert'

import { proxyTestServiceTestHooks } from '../../modules/proxies/proxy-test.service.js'
import type { ProxyProfileTestConfig } from '../../storage/proxy.repository.js'

function proxy(id: string): ProxyProfileTestConfig {
  return {
    id,
    name: `proxy-${id}`,
    type: 'http',
    host: '127.0.0.1',
    port: 10_000 + Number(id.replace(/\D/g, '') || 0),
    enabled: true,
    testStatus: 'unknown',
    updatedAt: '2026-07-26T00:00:00.000Z',
    proxyUrl: `http://127.0.0.1:${10_000 + Number(id.replace(/\D/g, '') || 0)}`,
    configUpdatedAt: '2026-07-26T00:00:00.000Z'
  }
}

const sixCandidates = Array.from({ length: 6 }, (_value, index) => proxy(String(index + 1)))
let activeCount = 0
let maxActiveCount = 0
const concurrencySummary = await proxyTestServiceTestHooks.refreshBatch({
  limit: 6,
  concurrency: 2,
  runBudgetMs: 2_000,
  candidateDeadlineMs: 500
}, {
  listCandidates: async () => sixCandidates,
  acquireLease: async () => ({}),
  releaseLease: async () => true,
  runCandidate: async (candidate) => {
    activeCount += 1
    maxActiveCount = Math.max(maxActiveCount, activeCount)
    await new Promise((resolve) => setTimeout(resolve, 10))
    activeCount -= 1
    return {
      observationStatus: candidate.id === '2' ? 'failed' : 'passed',
      persisted: true,
      executionFailed: false
    }
  }
})
assert.equal(maxActiveCount, 2, '代理刷新真实执行并发必须限制为 2')
assert.equal(concurrencySummary.startedCount, 6, '预算充足时应处理目标批次全部候选')
assert.equal(concurrencySummary.processedCount, 6, '预算充足时全部候选都应完成')
assert.equal(concurrencySummary.observationFailedCount, 1, '有效失败观测必须单独统计')
assert.equal(concurrencySummary.outcome, 'success', '有效代理失败观测不应被误报为调度执行失败')

let budgetClockMs = 0
const candidateDeadlines: number[] = []
const budgetSummary = await proxyTestServiceTestHooks.refreshBatch({
  limit: 5,
  concurrency: 2,
  runBudgetMs: 45,
  candidateDeadlineMs: 25
}, {
  listCandidates: async () => Array.from({ length: 20 }, (_value, index) => proxy(`b${index + 1}`)),
  acquireLease: async () => ({}),
  releaseLease: async () => true,
  now: () => budgetClockMs,
  runCandidate: async (_candidate, deadlineAtMs) => {
    candidateDeadlines.push(deadlineAtMs)
    budgetClockMs += 30
    await Promise.resolve()
    return { observationStatus: 'passed', persisted: true, executionFailed: false }
  }
})
assert.equal(budgetSummary.startedCount, 2, '45ms 模拟预算耗尽后不得继续启动候选')
assert.equal(budgetSummary.deferredCount, 3, '未启动目标必须计入预算延期')
assert.equal(budgetSummary.outcome, 'partial', '预算延期必须产生真实 partial outcome')
assert.deepEqual(candidateDeadlines, [25, 45], '候选截止时间必须同时受自身上限和单轮截止时间约束')

const isolatedSummary = await proxyTestServiceTestHooks.refreshBatch({
  limit: 4,
  concurrency: 2,
  runBudgetMs: 2_000,
  candidateDeadlineMs: 500
}, {
  listCandidates: async () => Array.from({ length: 16 }, (_value, index) => proxy(`i${index + 1}`)),
  acquireLease: async () => ({}),
  releaseLease: async () => true,
  runCandidate: async (candidate) => {
    if (candidate.id === 'i1') throw new Error('single candidate failure')
    return {
      observationStatus: 'passed',
      persisted: candidate.id !== 'i2',
      executionFailed: false
    }
  }
})
assert.equal(isolatedSummary.startedCount, 4, '单候选异常不得阻断同轮其他候选')
assert.equal(isolatedSummary.processedCount, 3, '抛出异常的候选不得计为已完成')
assert.equal(isolatedSummary.executionFailureCount, 1, '单候选异常必须计入执行失败')
assert.equal(isolatedSummary.stalePersistCount, 1, 'CAS 拒绝的过期写回必须单独统计')
assert.equal(isolatedSummary.outcome, 'partial', '执行失败或过期写回必须产生 partial outcome')

let leaseAttemptCount = 0
const leaseSummary = await proxyTestServiceTestHooks.refreshBatch({
  limit: 2,
  concurrency: 2,
  runBudgetMs: 2_000,
  candidateDeadlineMs: 500
}, {
  listCandidates: async () => Array.from({ length: 8 }, (_value, index) => proxy(`l${index + 1}`)),
  acquireLease: async () => {
    leaseAttemptCount += 1
    return leaseAttemptCount <= 3 ? undefined : {}
  },
  releaseLease: async () => true,
  runCandidate: async () => ({ observationStatus: 'passed', persisted: true, executionFailed: false })
})
assert.equal(leaseSummary.skippedLeaseCount, 3, '被其他实例占用的候选必须非阻塞跳过')
assert.equal(leaseSummary.startedCount, 2, '租约冲突后应继续扫描后续候选填满本轮目标')
assert.equal(leaseSummary.deferredCount, 0, '后续候选补足目标时不应误报延期')
assert.equal(leaseSummary.outcome, 'success', '单纯租约冲突且目标已补足不应记为 partial')

const releaseSummary = await proxyTestServiceTestHooks.refreshBatch({
  limit: 1,
  concurrency: 2,
  runBudgetMs: 2_000,
  candidateDeadlineMs: 500
}, {
  listCandidates: async () => [proxy('r1')],
  acquireLease: async () => ({}),
  releaseLease: async () => false,
  runCandidate: async () => ({ observationStatus: 'passed', persisted: true, executionFailed: false })
})
assert.equal(releaseSummary.releaseFailureCount, 1, '租约释放失败必须可观测')
assert.equal(releaseSummary.outcome, 'partial', '租约释放失败必须产生 partial outcome')

const abortController = new AbortController()
let abortStartedCount = 0
let resolveTwoStarted: (() => void) | undefined
let releaseStarted: (() => void) | undefined
const twoStarted = new Promise<void>((resolve) => {
  resolveTwoStarted = resolve
})
const startedRelease = new Promise<void>((resolve) => {
  releaseStarted = resolve
})
const abortSummaryPromise = proxyTestServiceTestHooks.refreshBatch({
  limit: 6,
  concurrency: 2,
  runBudgetMs: 2_000,
  candidateDeadlineMs: 500,
  signal: abortController.signal
}, {
  listCandidates: async () => sixCandidates,
  acquireLease: async () => ({}),
  releaseLease: async () => true,
  runCandidate: async (_candidate, _deadlineAtMs, signal) => {
    assert.equal(signal, abortController.signal, '后台父 signal 必须透传到已启动的真实代理候选')
    abortStartedCount += 1
    if (abortStartedCount === 2) resolveTwoStarted?.()
    await startedRelease
    return { observationStatus: 'passed', persisted: true, executionFailed: false }
  }
})
await twoStarted
abortController.abort(new Error('scheduler stopping'))
releaseStarted?.()
const abortSummary = await abortSummaryPromise
assert.equal(abortSummary.startedCount, 2, '父任务取消后不得继续启动未领取代理候选')
assert.equal(abortSummary.processedCount, 2, '取消前已启动候选应按既有候选语义安全收口')
assert.equal(abortSummary.deferredCount, 4, '取消后未启动候选必须保留为延期任务')
assert.equal(abortSummary.outcome, 'partial', '父任务取消导致延期时必须报告 partial')

console.log(JSON.stringify({
  message: '代理延迟刷新有界执行回归通过',
  maxActiveCount,
  budgetStartedCount: budgetSummary.startedCount,
  isolatedExecutionFailureCount: isolatedSummary.executionFailureCount,
  skippedLeaseCount: leaseSummary.skippedLeaseCount,
  releaseFailureCount: releaseSummary.releaseFailureCount,
  abortedAdmissionStartedCount: abortSummary.startedCount
}))
