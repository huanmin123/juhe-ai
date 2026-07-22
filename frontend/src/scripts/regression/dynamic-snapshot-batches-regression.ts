import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const helperUrl = new URL('../../shared/dynamicSnapshotBatches.ts', import.meta.url)
assert.equal(existsSync(fileURLToPath(helperUrl)), true, '动态快照必须提供有界分批 helper')

const { loadDynamicSnapshotsInBatches } = await import(helperUrl.href)
const active = { value: 0, peak: 0 }
const batchResult = await loadDynamicSnapshotsInBatches(
  [...Array.from({ length: 205 }, (_, index) => ` acc_${index} `), 'acc_0', ''],
  async (ids) => {
    active.value += 1
    active.peak = Math.max(active.peak, active.value)
    await new Promise<void>((resolve) => setImmediate(resolve))
    active.value -= 1
    return ids
  }
)

assert.deepEqual(batchResult.map((item) => item.ids.length), [100, 100, 5], '动态快照应按 100 个 ID 分批并去重空值')
assert.deepEqual(batchResult.flatMap((item) => item.value ?? []), Array.from({ length: 205 }, (_, index) => `acc_${index}`), '批次结果必须保持原始 ID 顺序')
assert.equal(active.peak, 2, '动态快照并行请求不得超过两个')

const partialFailure = await loadDynamicSnapshotsInBatches(
  ['a', 'b', 'c'],
  async (ids) => {
    if (ids.includes('b')) throw new Error('snapshot unavailable')
    return ids.join(',')
  },
  { batchSize: 1, concurrency: 2 }
)
assert.equal(partialFailure[0]?.value, 'a')
assert.match(String(partialFailure[1]?.error), /snapshot unavailable/)
assert.equal(partialFailure[2]?.value, 'c', '单批失败不得中断其他动态快照批次')

console.log('动态快照分批回归通过：ID 去重、有界批次、并行上限和局部失败均符合契约')
