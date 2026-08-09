import { strict as assert } from 'node:assert'
import { setTimeout as sleep } from 'node:timers/promises'

import { createRetryQueue } from '../../shared/retry-queue.js'
import { fixedRetryPolicy } from '../../shared/retry-policy.js'

const releases: Array<() => void> = []
const started: string[] = []

const queue = createRetryQueue<{ id: string; revision: number }>({
  name: 'retry-queue-priority-regression',
  policy: fixedRetryPolicy('retry_queue_priority_regression', 1000, 1),
  concurrency: 4,
  reservedPriorityConcurrency: {
    priorityAtMost: 10,
    slots: 2
  },
  run: async (item) => {
    started.push(`${item.id}:${item.revision}`)
    await new Promise<void>((resolve) => releases.push(resolve))
    return true
  }
})

for (const id of ['normal-a', 'normal-b', 'normal-c', 'normal-d']) {
  assert.equal(queue.enqueue(id, { id, revision: 1 }, { priority: 20 }), true)
}
await waitFor(() => started.length === 2, '普通任务最多只能占用未保留的并发槽')
assert.deepEqual(started, ['normal-a:1', 'normal-b:1'])

assert.equal(queue.enqueue('activation', { id: 'activation', revision: 1 }, { priority: 0 }), true)
await waitFor(() => started.length === 3, '首次激活应立即使用保留槽位')
assert.equal(started[2], 'activation:1')

assert.equal(queue.enqueue('normal-a', { id: 'normal-a', revision: 3 }, {
  priority: 0,
  replaceExisting: true
}), true)
releaseOne()
await waitFor(() => started.includes('normal-a:3'), '运行中的重复任务应在当前任务结束后补执行最新版本')
assert.equal(started.filter((item) => item.startsWith('normal-a:')).length, 2, '运行中重复投递只能追加一次')

assert.equal(queue.enqueue('normal-d', { id: 'normal-d', revision: 2 }, {
  priority: 10,
  replaceExisting: true
}), true)
releaseOne()
await waitFor(() => started.includes('normal-d:2'), '等待中的重复任务应更新内容并提升优先级')

releaseAll()
await waitFor(() => queue.snapshot().pendingCount === 0 && queue.snapshot().runningCount === 0, '队列最终应清空')
await assertConditionalPriorityReplacement()
await assertRequestFailureUsesReservedConcurrency()
await assertDelayedFollowUp()

console.log('retry queue 优先级回归通过：保留并发、等待升级、延时尾随和运行后补执行符合契约')

async function assertConditionalPriorityReplacement(): Promise<void> {
  const pendingStarted: string[] = []
  const blockerRelease: { current?: () => void } = {}
  const pendingQueue = createRetryQueue<{ id: string; revision: number }>({
    name: 'retry-queue-conditional-pending-regression',
    policy: fixedRetryPolicy('retry_queue_conditional_pending_regression', 1000, 1),
    concurrency: 1,
    run: async (item) => {
      pendingStarted.push(`${item.id}:${item.revision}`)
      if (item.id === 'blocker') {
        await new Promise<void>((resolve) => { blockerRelease.current = resolve })
      }
      return true
    }
  })
  pendingQueue.enqueue('blocker', { id: 'blocker', revision: 1 }, { priority: 0 })
  await waitFor(() => pendingStarted.length === 1, '条件替换回归的阻塞任务应先开始')
  pendingQueue.enqueue('scheduled', { id: 'scheduled', revision: 1 }, { priority: 20 })
  assert.equal(pendingQueue.enqueue('scheduled', { id: 'scheduled', revision: 2 }, {
    priority: 15,
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  }), true, '更高优先级 request_failure 应替换等待中的 scheduled')
  assert.equal(pendingQueue.enqueue('scheduled', { id: 'scheduled', revision: 3 }, {
    priority: 15,
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  }), false, '同优先级 request_failure 不得反复替换等待任务')
  pendingQueue.enqueue('configuration', { id: 'configuration', revision: 1 }, { priority: 10 })
  assert.equal(pendingQueue.enqueue('configuration', { id: 'configuration', revision: 2 }, {
    priority: 15,
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  }), false, 'request_failure 不得覆盖更高优先级 configuration')
  blockerRelease.current?.()
  await waitFor(() => pendingQueue.snapshot().pendingCount === 0 && pendingQueue.snapshot().runningCount === 0, '条件替换等待队列应清空')
  assert.deepEqual(pendingStarted, ['blocker:1', 'configuration:1', 'scheduled:2'])

  const runningStarted: string[] = []
  const scheduledRelease: { current?: () => void } = {}
  const runningQueue = createRetryQueue<{ id: string; revision: number }>({
    name: 'retry-queue-conditional-running-regression',
    policy: fixedRetryPolicy('retry_queue_conditional_running_regression', 1000, 1),
    concurrency: 1,
    run: async (item) => {
      runningStarted.push(`${item.id}:${item.revision}`)
      if (item.revision === 1) {
        await new Promise<void>((resolve) => { scheduledRelease.current = resolve })
      }
      return true
    }
  })
  runningQueue.enqueue('scheduled', { id: 'scheduled', revision: 1 }, { priority: 20 })
  await waitFor(() => runningStarted.length === 1, '运行中替换回归的 scheduled 应先开始')
  assert.equal(runningQueue.enqueue('scheduled', { id: 'request-failure', revision: 2 }, {
    priority: 15,
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  }), true, '运行中的 scheduled 应保留一次 request_failure follow-up')
  assert.equal(runningQueue.enqueue('scheduled', { id: 'request-failure', revision: 3 }, {
    priority: 15,
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  }), true, '运行中的 request_failure follow-up 应仅保留最新一次')
  scheduledRelease.current?.()
  await waitFor(() => runningQueue.snapshot().pendingCount === 0 && runningQueue.snapshot().runningCount === 0, '运行中条件替换队列应清空')
  assert.deepEqual(runningStarted, ['scheduled:1', 'request-failure:3'])

  for (const scenario of [
    { id: 'activation', priority: 0 },
    { id: 'configuration', priority: 10 }
  ]) {
    const highPriorityStarted: string[] = []
    const highPriorityRelease: { current?: () => void } = {}
    const highPriorityQueue = createRetryQueue<{ id: string; revision: number }>({
      name: `retry-queue-running-${scenario.id}-regression`,
      policy: fixedRetryPolicy(`retry_queue_running_${scenario.id}_regression`, 1000, 1),
      concurrency: 1,
      run: async (item) => {
        highPriorityStarted.push(`${item.id}:${item.revision}`)
        if (item.revision === 1) {
          await new Promise<void>((resolve) => { highPriorityRelease.current = resolve })
        }
        return true
      }
    })
    highPriorityQueue.enqueue(scenario.id, { id: scenario.id, revision: 1 }, { priority: scenario.priority })
    await waitFor(() => highPriorityStarted.length === 1, `${scenario.id} 运行中任务应先开始`)
    assert.equal(highPriorityQueue.enqueue(scenario.id, { id: 'request-failure', revision: 2 }, {
      priority: 15,
      replaceExisting: true,
      replaceExistingOnlyIfHigherPriority: true,
      followUpWhenRunning: true
    }), false, `运行中的 ${scenario.id} 不得追加较低优先级 request_failure`)
    highPriorityRelease.current?.()
    await waitFor(
      () => highPriorityQueue.snapshot().pendingCount === 0 && highPriorityQueue.snapshot().runningCount === 0,
      `${scenario.id} 优先级回归队列应清空`
    )
    assert.deepEqual(highPriorityStarted, [`${scenario.id}:1`])
  }
}

async function assertRequestFailureUsesReservedConcurrency(): Promise<void> {
  const started: string[] = []
  const releases: Array<() => void> = []
  let blocking = true
  const healthQueue = createRetryQueue<{ id: string }>({
    name: 'retry-queue-request-failure-reservation-regression',
    policy: fixedRetryPolicy('retry_queue_request_failure_reservation_regression', 1000, 1),
    concurrency: 10,
    reservedPriorityConcurrency: {
      priorityAtMost: 15,
      slots: 3
    },
    run: async (item) => {
      started.push(item.id)
      if (blocking) await new Promise<void>((resolve) => releases.push(resolve))
      return true
    }
  })
  for (let index = 1; index <= 8; index += 1) {
    healthQueue.enqueue(`scheduled-${index}`, { id: `scheduled-${index}` }, { priority: 20 })
  }
  await waitFor(() => started.length === 7, '周期检查只能占用 7 个普通并发槽')
  healthQueue.enqueue('request-failure', { id: 'request-failure' }, { priority: 15 })
  await waitFor(() => started.includes('request-failure'), '请求失败探针必须立即使用保留并发槽')
  assert.equal(started.includes('scheduled-8'), false, '第 8 个周期检查不得占用保留并发槽')
  blocking = false
  for (const release of releases.splice(0)) release()
  await waitFor(() => healthQueue.snapshot().pendingCount === 0 && healthQueue.snapshot().runningCount === 0, '保留并发回归队列应清空')
}

async function assertDelayedFollowUp(): Promise<void> {
  const started: string[] = []
  let releaseFirst!: () => void
  const delayedQueue = createRetryQueue<{ revision: number }>({
    name: 'retry-queue-delayed-follow-up-regression',
    policy: fixedRetryPolicy('retry_queue_delayed_follow_up_regression', 1000, 1),
    concurrency: 1,
    run: async (item) => {
      started.push(String(item.revision))
      if (item.revision === 1) {
        await new Promise<void>((resolve) => { releaseFirst = resolve })
      }
      return true
    }
  })
  assert.equal(delayedQueue.enqueue('account', { revision: 1 }), true)
  await waitFor(() => started.length === 1, '延时尾随回归必须先运行首轮任务')
  const followUpEnqueuedAtMs = Date.now()
  assert.equal(delayedQueue.enqueue('account', { revision: 2 }, {
    followUpWhenRunning: true,
    delayMs: 80
  }), true, '运行中的任务必须接受延时尾随')
  releaseFirst()
  await sleep(25)
  assert.deepEqual(started, ['1'], '延时尾随不能在首轮完成后立即运行')
  await waitFor(() => started.length === 2, '延时尾随必须在指定等待后运行')
  assert(Date.now() - followUpEnqueuedAtMs >= 60, '延时尾随不能忽略 enqueue 的 delayMs')
  await waitFor(() => delayedQueue.snapshot().pendingCount === 0 && delayedQueue.snapshot().runningCount === 0, '延时尾随队列应清空')
}

function releaseOne(): void {
  releases.shift()?.()
}

function releaseAll(): void {
  for (const release of releases.splice(0)) release()
}

async function waitFor(condition: () => boolean, message: string): Promise<void> {
  const deadlineMs = Date.now() + 3000
  while (Date.now() < deadlineMs) {
    if (condition()) return
    await sleep(10)
  }
  assert.fail(message)
}
