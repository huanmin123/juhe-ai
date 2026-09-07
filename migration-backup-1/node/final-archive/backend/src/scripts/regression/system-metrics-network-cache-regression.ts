import assert from 'node:assert/strict'

import { createCachedNetworkMetricsSampler } from '../../modules/background/system-metrics-sampler.service.js'

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (error: unknown) => void
}

let currentTimeMs = 0
let readCount = 0
const reads: Array<Deferred<{ rxBytes: number; txBytes: number } | undefined>> = []
const sampler = createCachedNetworkMetricsSampler({
  now: () => currentTimeMs,
  refreshIntervalMs: 60_000,
  failureRetryMs: 15_000,
  maxStaleMs: 300_000,
  readCounters: () => {
    readCount += 1
    const pending = deferred<{ rxBytes: number; txBytes: number } | undefined>()
    reads.push(pending)
    return pending.promise
  }
})

assert.deepEqual(await sampler.current(), {}, '首次读取必须立即返回空缓存，不能等待 Windows 子进程')
await Promise.all(Array.from({ length: 20 }, () => sampler.current()))
assert.equal(readCount, 1, '并发缓存未命中只能启动一个网络计数器读取')

reads[0].resolve({ rxBytes: 1_000, txBytes: 2_000 })
await settleAsyncRefresh()
assert.deepEqual(await sampler.current(), {
  networkRxTotalBytes: 1_000,
  networkTxTotalBytes: 2_000
}, '首次成功只提供累计值')

currentTimeMs = 59_999
assert.deepEqual(await sampler.current(), {
  networkRxTotalBytes: 1_000,
  networkTxTotalBytes: 2_000
}, '刷新周期内必须复用缓存')
assert.equal(readCount, 1, '刷新周期内不得重复启动计数器读取')

currentTimeMs = 60_000
assert.deepEqual(await sampler.current(), {
  networkRxTotalBytes: 1_000,
  networkTxTotalBytes: 2_000
}, '到期刷新期间必须继续返回旧缓存')
assert.equal(readCount, 2, '缓存到期应异步启动下一轮读取')
reads[1].resolve({ rxBytes: 7_000, txBytes: 5_000 })
await settleAsyncRefresh()
assert.deepEqual(await sampler.current(), {
  networkRxBytesPerSecond: 100,
  networkTxBytesPerSecond: 50,
  networkRxTotalBytes: 7_000,
  networkTxTotalBytes: 5_000
}, '成功刷新应按实际采样间隔计算吞吐')

currentTimeMs = 120_000
await sampler.current()
assert.equal(readCount, 3, '下一刷新周期应启动一次读取')
reads[2].reject(new Error('模拟 Windows 网络计数器失败'))
await settleAsyncRefresh()
assert.equal((await sampler.current()).networkRxBytesPerSecond, 100, '短时失败必须保留最近成功缓存')

currentTimeMs = 134_999
await sampler.current()
assert.equal(readCount, 3, '失败重试窗口内不得忙循环')
currentTimeMs = 135_000
await Promise.all([sampler.current(), sampler.current(), sampler.current()])
assert.equal(readCount, 4, '失败重试到期后的并发调用仍只能单飞')
reads[3].reject(new Error('模拟连续失败'))
await settleAsyncRefresh()

currentTimeMs = 360_001
assert.deepEqual(await sampler.current(), {}, '超过最大陈旧期不得继续伪装成实时网络指标')

const resetSampler = createCachedNetworkMetricsSampler({
  now: () => currentTimeMs,
  refreshIntervalMs: 1,
  readCounters: async () => currentTimeMs === 360_001
    ? { rxBytes: 10_000, txBytes: 20_000 }
    : { rxBytes: 100, txBytes: 200 }
})
await resetSampler.current()
await settleAsyncRefresh()
currentTimeMs += 1
await resetSampler.current()
await settleAsyncRefresh()
assert.deepEqual(await resetSampler.current(), {
  networkRxTotalBytes: 100,
  networkTxTotalBytes: 200
}, '网络计数器回退或重置时不得产生负吞吐')

console.log('系统指标 Windows 网络缓存回归通过：SWR、单飞、失败重试、陈旧失效和计数器重置均受保护')

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void
  let rejectPromise!: (error: unknown) => void
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return { promise, resolve: resolvePromise, reject: rejectPromise }
}

async function settleAsyncRefresh(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve))
}
