import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { operationDeduplicationService } from '../../modules/deduplication/deduplication.service.js'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))
const source = readFileSync(resolve(backendSrcRoot, 'modules/deduplication/deduplication.service.ts'), 'utf8')

assert(!source.includes('[...this.entries.values()]'), '防重复提交容量淘汰不应展开全部缓存条目')
assert(!source.includes('oldestKeys'), '防重复提交容量淘汰不应构造全量 oldestKeys 数组')
assert(source.includes('deduplicationCleanupBatchSize'), '防重复提交过期维护必须有固定批量上限')
assert(source.includes('this.cleanupExpiredEntries(now, deduplicationCleanupBatchSize)'), '防重复提交应复用固定批量过期清理')

const originalSort = Array.prototype.sort
let sortCalled = false

try {
  Array.prototype.sort = function patchedSort<T>(this: T[], compareFn?: (a: T, b: T) => number): T[] {
    sortCalled = true
    return originalSort.call(this, compareFn)
  }

  for (let index = 0; index <= 5000; index += 1) {
    const result = operationDeduplicationService.claim({
      key: `dedupe-maintenance-boundary:${index}`,
      operationKey: 'dedupe-maintenance-boundary',
      processingTtlMs: 120_000
    })
    assert.equal(result.claimed, true, '不同 key 的写请求不应被误判为重复提交')
  }

  const duplicate = operationDeduplicationService.claim({
    key: 'dedupe-maintenance-boundary:5000',
    operationKey: 'dedupe-maintenance-boundary',
    processingTtlMs: 120_000
  })
  assert.equal(duplicate.claimed, false, '同 key 仍应被防重复提交缓存拦截')
  assert.equal(sortCalled, false, '防重复提交缓存满载维护不应触发数组排序')

  console.log('防重复提交维护边界回归通过：缓存满载时按固定批量清理并淘汰最早条目，不再全量展开排序')
} finally {
  Array.prototype.sort = originalSort
}
