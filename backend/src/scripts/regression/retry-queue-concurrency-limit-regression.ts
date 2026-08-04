import { strict as assert } from 'node:assert'
import { setTimeout as sleep } from 'node:timers/promises'

import { createRetryQueue } from '../../shared/retry-queue.js'
import { fixedRetryPolicy } from '../../shared/retry-policy.js'

const releases: Array<() => void> = []
const started: string[] = []
let activeCount = 0
let maxActiveCount = 0

const queue = createRetryQueue<{ id: string }>({
  name: 'retry-queue-concurrency-limit-regression',
  policy: fixedRetryPolicy('retry_queue_concurrency_limit_regression', 1000, 1),
  concurrency: 2,
  run: async (item) => {
    activeCount += 1
    maxActiveCount = Math.max(maxActiveCount, activeCount)
    started.push(item.id)
    await new Promise<void>((resolve) => releases.push(resolve))
    activeCount -= 1
    return true
  }
})

for (const id of ['a', 'b', 'c', 'd']) {
  assert.equal(queue.enqueue(id, { id }), true)
}

await waitFor(() => started.length === 2, '初始并发应只启动 2 个队列项')
assert.equal(maxActiveCount, 2, '初始运行并发不应超过 2')
assert.deepEqual(started, ['a', 'b'], '队列应按 key 顺序启动到期任务')
const initialSnapshot = queue.snapshot()
assert.deepEqual(initialSnapshot, {
  name: 'retry-queue-concurrency-limit-regression',
  pendingCount: 2,
  runningCount: 2,
  nextRunAt: initialSnapshot.nextRunAt
}, 'snapshot 应区分运行中和待运行队列项')

queue.setConcurrency(3)
await waitFor(() => started.length === 3, '动态提升并发后应补启动第 3 个队列项')
assert.equal(maxActiveCount, 3, '动态提升后运行并发不应超过 3')
assert.equal(queue.snapshot().runningCount, 3, '动态提升后 snapshot 应显示 3 个运行中队列项')
assert.equal(queue.snapshot().pendingCount, 1, '动态提升后 snapshot 应保留 1 个待运行队列项')

releaseStartedItems()
await waitFor(() => started.length === 4, '释放运行项后应启动剩余队列项')
assert.ok(maxActiveCount <= 3, '释放过程中运行并发不应超过当前上限 3')

releaseStartedItems()
await waitFor(() => {
  const snapshot = queue.snapshot()
  return snapshot.pendingCount === 0 && snapshot.runningCount === 0
}, '所有队列项释放后应清空队列')
assert.ok(maxActiveCount <= 3, '整个执行过程不应超过当前并发上限')

console.log('retry queue 并发上限回归通过：p-limit 门禁保留运行态快照和动态并发调整')

function releaseStartedItems(): void {
  for (const release of releases.splice(0)) {
    release()
  }
}

async function waitFor(condition: () => boolean, message: string): Promise<void> {
  const deadlineMs = Date.now() + 2000
  while (Date.now() < deadlineMs) {
    if (condition()) {
      return
    }
    await sleep(10)
  }
  assert.fail(message)
}
