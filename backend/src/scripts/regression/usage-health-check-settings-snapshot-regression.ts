import assert from 'node:assert/strict'

import { UsageHealthCheckSettingsSnapshotCache } from '../../storage/usage-health-check-settings-snapshot.js'

let loadCount = 0
const cache = new UsageHealthCheckSettingsSnapshotCache(async () => {
  loadCount += 1
  return {
    accountHealthCheckIntervalHours: 18,
    accountHealthCheckJitterMinutes: 45,
    accountHealthCheckFailureThreshold: 4
  }
}, { ttlMs: 60_000 })

const [first, concurrent] = await Promise.all([cache.read(1_000), cache.read(1_000)])
assert.equal(loadCount, 1, '同一刷新窗口的并发读取只能触发一次 settings loader')
assert.equal(first.intervalHours, 18)
assert.deepEqual(concurrent, first)

await cache.read(30_000)
assert.equal(loadCount, 1, '60 秒近端快照有效期内不得重复读取 Redis/数据库 settings')

cache.invalidate()
await cache.read(30_001)
assert.equal(loadCount, 2, 'settings_updated 显式失效后下一次读取必须刷新快照')

let failureCount = 0
const failing = new UsageHealthCheckSettingsSnapshotCache(async () => {
  failureCount += 1
  throw new Error('simulated settings backend outage')
}, { failureRetryMs: 5_000 })
const fallback = await failing.read(100_000)
assert.equal(fallback.intervalHours, 12, 'settings 读取失败必须回退健康检测默认间隔')
assert.equal(fallback.jitterMinutes, 120, 'settings 读取失败必须回退健康检测默认错峰')
assert.equal(fallback.failureThreshold, 3, 'settings 读取失败必须回退健康检测默认阈值')
await failing.read(101_000)
assert.equal(failureCount, 1, 'settings 故障短退避期间不得按 usage 批次重复访问失败依赖')
await failing.read(106_000)
assert.equal(failureCount, 2, 'settings 故障退避结束后应允许重新加载')

console.log('usage health-check settings snapshot regression passed')
