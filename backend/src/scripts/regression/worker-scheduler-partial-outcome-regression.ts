import assert from 'node:assert/strict'

import { logger } from '../../shared/logger.js'
import { WorkerScheduler } from '../../modules/background/worker-scheduler.js'

logger.level = 'silent'

const scheduler = new WorkerScheduler()
let run = 0
scheduler.schedule({
  name: 'partial-outcome-test',
  intervalMs: 10,
  task: async () => {
    run += 1
    if (run === 1) {
      return {
        outcome: 'partial' as const,
        warning: '1 个候选刷新失败'
      }
    }
  }
})

await waitFor(() => scheduler.snapshots()[0]?.runCount === 1)
const partial = scheduler.snapshots()[0]
assert.equal(partial?.failureCount, 0, '部分失败不能计入后台任务执行失败')
assert.equal(partial?.partialCount, 1)
assert.equal(partial?.lastWarning, '1 个候选刷新失败')
assert.ok(partial?.lastWarningAt)
assert.equal(partial?.lastError, undefined)

await waitFor(() => (scheduler.snapshots()[0]?.runCount ?? 0) >= 2)
const recovered = scheduler.snapshots()[0]
assert.equal(recovered?.successCount, 1)
assert.equal(recovered?.partialCount, 1)
assert.equal(recovered?.runCount, (recovered?.successCount ?? 0) + (recovered?.partialCount ?? 0) + (recovered?.failureCount ?? 0))
assert.equal(recovered?.lastWarning, undefined, '后续完整成功后应清除当前部分失败提示')
assert.ok(recovered?.lastWarningAt, '历史部分失败时间应保留用于展示恢复状态')

scheduler.stop()
console.log('worker scheduler partial outcome regression passed')

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1_000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待 worker scheduler 状态超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
