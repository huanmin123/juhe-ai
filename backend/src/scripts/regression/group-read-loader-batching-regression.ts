import assert from 'node:assert/strict'

import { loadSharedCacheEntriesByBatches } from '../../storage/shared-cache-read-batching.js'

const ids = [...Array.from({ length: 205 }, (_value, index) => `group-${index}`), 'group-10', '']
let active = 0
let maxActive = 0
const values = await loadSharedCacheEntriesByBatches(ids, async (id) => {
  active += 1
  maxActive = Math.max(maxActive, active)
  await new Promise<void>((resolve) => setImmediate(resolve))
  active -= 1
  return id === 'group-7' ? undefined : `cached:${id}`
})

assert.equal(values.length, 205, 'shared cache 批量读取必须去重并忽略空 ID')
assert.equal(maxActive, 100, 'shared cache 批量读取最多只能并发 100 个 ID')
assert.deepEqual(values[0], ['group-0', 'cached:group-0'])
assert.deepEqual(values[7], ['group-7', undefined], 'cache miss 必须保留为 miss，不得伪造命中值')
assert.deepEqual(values.at(-1), ['group-204', 'cached:group-204'])

await assert.rejects(
  loadSharedCacheEntriesByBatches(['ok', 'failure'], async (id) => {
    if (id === 'failure') throw new Error('shared-cache failure')
    return 'cached:ok'
  }),
  /shared-cache failure/,
  'shared cache 读取失败必须继续向调用方传播'
)

console.log('group-read-loader-batching-regression passed')
