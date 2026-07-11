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

console.log('retry queue 优先级回归通过：保留并发、等待升级和运行后补执行符合契约')

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
