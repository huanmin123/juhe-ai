import { strict as assert } from 'node:assert'

import { createRetryQueue } from '../../shared/retry-queue.js'

let release: (() => void) | undefined
const running = new Promise<void>((resolve) => { release = resolve })
const queue = createRetryQueue<{ id: string }>({
  name: 'stop-drain-regression',
  policy: { name: 'stop-drain', strategy: 'sequence', delayMs: 0, delaysMs: [], maxRetries: 0 },
  concurrency: 1,
  run: async () => {
    await running
    return true
  }
})

assert.equal(queue.enqueue('running', { id: 'running' }), true)
assert.equal(queue.enqueue('pending', { id: 'pending' }), true)
await waitFor(() => queue.snapshot().runningCount === 1 && queue.snapshot().pendingCount === 1)

const timedOut = await queue.stopAndDrain(10)
assert.deepEqual(timedOut, { drained: false, activeCount: 1 })
assert.equal(queue.enqueue('after-stop', { id: 'after-stop' }), false)
release?.()
await waitFor(() => queue.snapshot().runningCount === 0)

const quickQueue = createRetryQueue<{ id: string }>({
  name: 'stop-drain-quick-regression',
  policy: { name: 'stop-drain-quick', strategy: 'sequence', delayMs: 0, delaysMs: [], maxRetries: 0 },
  run: async () => true
})
assert.equal(quickQueue.enqueue('one', { id: 'one' }), true)
const drained = await quickQueue.stopAndDrain(1000)
assert.deepEqual(drained, { drained: true, activeCount: 0 })

process.stdout.write('retry_queue_stop_drain_passed\n')

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('retry queue regression timed out')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
