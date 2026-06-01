import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  clearAccountConcurrency,
  getAccountCurrentConcurrency,
  tryAcquireAccountConcurrency
} from '../../shared/account-concurrency.js'

const source = readFileSync(new URL('../../shared/account-concurrency.ts', import.meta.url), 'utf8')
const laneGetterBody = functionBody(source, 'getAccountCurrentConcurrency')
assert(source.includes('currentConcurrencyByAccountLaneKey'), '账号并发应维护 lane 级计数索引')
assert(!laneGetterBody.includes('slots.values()'), '读取账号 lane 并发不应遍历该账号全部 in-flight slot')
assert(laneGetterBody.includes('currentConcurrencyByAccountLaneKey.get'), '读取账号 lane 并发应直接查询 lane 级计数索引')

clearAccountConcurrency()

const textSlot = tryAcquireAccountConcurrency('acct_lane_index', 3, { lane: 'text' })
const imageSlotA = tryAcquireAccountConcurrency('acct_lane_index', 3, { lane: 'image', laneLimit: 2 })
const imageSlotB = tryAcquireAccountConcurrency('acct_lane_index', 3, { lane: 'image', laneLimit: 2 })
assert.equal(textSlot.acquired, true, '文本并发槽应可占用')
assert.equal(imageSlotA.acquired, true, '第一个图像并发槽应可占用')
assert.equal(imageSlotB.acquired, true, '第二个图像并发槽应可占用')
assert.equal(getAccountCurrentConcurrency('acct_lane_index'), 3, '总并发计数应包含文本和图像槽')
assert.equal(getAccountCurrentConcurrency('acct_lane_index', 'text'), 1, '文本 lane 计数应独立维护')

const originalValues = Map.prototype.values
let valuesCalled = false
try {
  Map.prototype.values = function patchedValues<K, V>(this: Map<K, V>): MapIterator<V> {
    valuesCalled = true
    return originalValues.call(this)
  }
  assert.equal(getAccountCurrentConcurrency('acct_lane_index', 'image'), 2, '图像 lane 计数应独立维护')
  assert.equal(valuesCalled, false, '读取图像 lane 计数不应触发 Map.values 遍历')
} finally {
  Map.prototype.values = originalValues
}

imageSlotA.release()
assert.equal(getAccountCurrentConcurrency('acct_lane_index', 'image'), 1, '释放图像槽后 lane 计数应递减')
textSlot.release()
imageSlotB.release()
assert.equal(getAccountCurrentConcurrency('acct_lane_index'), 0, '全部释放后总并发应归零')
assert.equal(getAccountCurrentConcurrency('acct_lane_index', 'image'), 0, '全部释放后图像 lane 应归零')
clearAccountConcurrency()

console.log('账号并发 lane 计数回归通过：图像/文本通道读取走 O(1) 计数索引，不遍历 in-flight slot')

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = sourceText.indexOf('{', start)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return sourceText.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体未闭合`)
}
