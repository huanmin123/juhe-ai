import assert from 'node:assert/strict'

import { SharedReadCache } from '../../shared/shared-read-cache.js'

interface RecordValue {
  value: string
}

class MemoryStorage<V> {
  readonly values = new Map<string, V>()
  setCount = 0
  deleteCount = 0
  clearCount = 0

  async get(key: string): Promise<V | undefined> {
    return this.values.get(key)
  }

  async set(key: string, value: V): Promise<void> {
    this.setCount += 1
    this.values.set(key, value)
  }

  async delete(key: string): Promise<void> {
    this.deleteCount += 1
    this.values.delete(key)
  }

  async clear(): Promise<void> {
    this.clearCount += 1
    this.values.clear()
  }
}

class DelayedSetStorage<V> extends MemoryStorage<V> {
  readonly setStarted = deferred<void>()
  readonly releaseSet = deferred<void>()

  override async set(key: string, value: V): Promise<void> {
    this.setStarted.resolve(undefined)
    await this.releaseSet.promise
    await super.set(key, value)
  }
}

const storage = new MemoryStorage<RecordValue>()
const cache = new SharedReadCache<RecordValue>(storage)
let loads = 0
let releaseLoader: (() => void) | undefined
const loaderReady = new Promise<void>((resolve) => { releaseLoader = resolve })

const firstLoad = cache.load('scope-a', async () => {
  loads += 1
  await loaderReady
  return { value: 'fresh' }
})
const secondLoad = cache.load('scope-a', async () => {
  loads += 1
  return { value: 'wrong' }
})
for (let attempt = 0; attempt < 10 && loads === 0; attempt += 1) await Promise.resolve()
assert.equal(loads, 1, '相同缓存键应合并并发回源')
releaseLoader?.()
assert.deepEqual(await Promise.all([firstLoad, secondLoad]), [{ value: 'fresh' }, { value: 'fresh' }])
assert.equal(storage.setCount, 1, '并发回源只应写入一次')

assert.deepEqual(await cache.load('scope-a', async () => ({ value: 'stale-loader' })), { value: 'fresh' })
assert.equal(loads, 1, '命中后端缓存不应再次回源')

await cache.invalidate('scope-a')
assert.deepEqual(await cache.load('scope-a', async () => ({ value: 'reloaded' })), { value: 'reloaded' })
assert.equal(storage.deleteCount, 1, '按键失效应删除对应缓存')

await cache.load('scope-b', async () => ({ value: 'other' }))
await cache.invalidateDomain()
assert.equal(storage.clearCount, 1, '整域失效应清理领域缓存')
assert.deepEqual(await cache.load('scope-b', async () => ({ value: 'reloaded-other' })), { value: 'reloaded-other' })

const delayedStorage = new DelayedSetStorage<RecordValue>()
const delayedCache = new SharedReadCache<RecordValue>(delayedStorage)
const delayedLoad = delayedCache.load('scope-race', async () => ({ value: 'late-stale' }))
await delayedStorage.setStarted.promise
const delayedInvalidation = delayedCache.invalidateDomain()
delayedStorage.releaseSet.resolve(undefined)
await Promise.all([delayedLoad, delayedInvalidation])
assert.equal(delayedStorage.values.has('scope-race'), false, '整域失效必须等待已经开始的旧代次写入并在其后清理，不能让迟到 set 复活旧值')
assert.deepEqual(
  await delayedCache.load('scope-race', async () => ({ value: 'after-invalidation' })),
  { value: 'after-invalidation' },
  '失效完成后的首个读取必须重新回源'
)

console.log('页面数据后端 read-through 缓存回归通过：命中、并发合并、按键/整域失效')

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  return {
    promise: new Promise<T>((next) => { resolve = next }),
    resolve
  }
}
