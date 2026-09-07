import { strict as assert } from 'node:assert'

import { createRetryQueue } from '../../shared/retry-queue.js'
import { fixedRetryPolicy } from '../../shared/retry-policy.js'

const originalSort = Array.prototype.sort
let sortCalled = false

try {
  Array.prototype.sort = function patchedSort<T>(this: T[], compareFn?: (a: T, b: T) => number): T[] {
    sortCalled = true
    return originalSort.call(this, compareFn)
  }

  const queue = createRetryQueue<{ id: string }>({
    name: 'retry-queue-linear-scan-regression',
    policy: fixedRetryPolicy('retry_queue_linear_scan_regression', 1000, 1),
    run: () => true
  })
  for (let index = 0; index < 1000; index += 1) {
    assert.equal(queue.enqueue(`key-${index.toString().padStart(4, '0')}`, { id: String(index) }), true)
  }

  const snapshot = queue.snapshot()
  assert.equal(snapshot.pendingCount, 1000, 'retry queue snapshot 应统计待执行项')
  assert.equal(snapshot.runningCount, 0, 'retry queue snapshot 应统计运行中项')
  assert.equal(sortCalled, false, 'retry queue 取最近任务和 snapshot 不应通过数组排序实现')

  console.log('retry queue 线性扫描回归通过：待执行项选择和 snapshot 不再分配数组排序')
} finally {
  Array.prototype.sort = originalSort
}
