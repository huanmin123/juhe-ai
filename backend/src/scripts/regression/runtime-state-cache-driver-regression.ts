import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { tryAcquireAccountConcurrencyAsync, clearAccountConcurrency } from '../../shared/account-concurrency.js'
import { createSharedJsonCache } from '../../shared/cache.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'
import { checkLoginAllowedAsync, recordFailedLoginAsync, recordSuccessfulLoginAsync } from '../../modules/auth/login-guard.service.js'

runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'

const suffix = `${Date.now()}_${Math.random().toString(16).slice(2)}`

const stateStore = createRuntimeStateStore(`regression_state_${suffix}`)
const compareSetStateStore = stateStore as unknown as {
  compareSetJson<T>(
    key: string,
    expectedValue: T | undefined,
    nextValue: T,
    ttlMs: number
  ): Promise<boolean>
  compareDeleteJson<T>(
    key: string,
    expectedValue: T
  ): Promise<boolean>
}

await stateStore.setJson('profile', { ok: true }, 1000)
assert.deepEqual(await stateStore.getJson('profile'), { ok: true })
assert.deepEqual(await stateStore.getDeleteJson('profile'), { ok: true })
assert.equal(await stateStore.getJson('profile'), undefined)
await stateStore.setJson('profile', { ok: true }, 1000)
await stateStore.delete('profile')
assert.equal(await stateStore.getJson('profile'), undefined)

assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 1)
assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 2)
assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 3)
assert.equal(await stateStore.getJson('counter'), 2)

assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-a' }), true)
assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), false)
await stateStore.releaseLock('lock', 'token-b')
assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), false)
await stateStore.releaseLock('lock', 'token-a')
assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), true)

await stateStore.setJson('ttl', { value: 1 }, 1)
await sleep(5)
assert.equal(await stateStore.getJson('ttl'), undefined)

assert.equal(
  await compareSetStateStore.compareSetJson('cas', undefined, { version: 1 }, 1000),
  true,
  'memory compareSetJson 应能原子创建不存在的 key'
)
assert.equal(
  await compareSetStateStore.compareSetJson('cas', { version: 0 }, { version: 2 }, 1000),
  false,
  'memory compareSetJson expected 不匹配时不能覆盖当前值'
)
assert.deepEqual(await stateStore.getJson('cas'), { version: 1 })
assert.equal(
  await compareSetStateStore.compareSetJson('cas', { version: 1 }, { version: 2 }, 1000),
  true,
  'memory compareSetJson expected 匹配时应原子更新'
)
await stateStore.setJson('cas-stale-index', { keys: ['a'] }, 1000)
const staleIndexSnapshot = await stateStore.getJson<{ keys: string[] }>('cas-stale-index')
assert(staleIndexSnapshot, 'memory stale index CAS 回归应读取旧快照')
assert.equal(
  await compareSetStateStore.compareSetJson(
    'cas-stale-index',
    staleIndexSnapshot,
    { keys: ['a', 'b'] },
    1000
  ),
  true,
  'memory compareSetJson 应允许第一个旧快照 owner 提交'
)
assert.equal(
  await compareSetStateStore.compareSetJson(
    'cas-stale-index',
    staleIndexSnapshot,
    { keys: ['a', 'c'] },
    1000
  ),
  false,
  'memory compareSetJson 必须拒绝第二个陈旧快照 owner 覆盖'
)
assert.deepEqual(await stateStore.getJson('cas-stale-index'), { keys: ['a', 'b'] })
await stateStore.setJson('cas-delete', { mutation: 'current' }, 1000)
assert.equal(
  await compareSetStateStore.compareDeleteJson('cas-delete', { mutation: 'stale' }),
  false,
  'memory compareDeleteJson 不能删除 expected 不匹配的新值'
)
assert.equal(
  await compareSetStateStore.compareDeleteJson('cas-delete', { mutation: 'current' }),
  true,
  'memory compareDeleteJson 应原子删除精确匹配值'
)
assert.equal(await stateStore.getJson('cas-delete'), undefined)
await stateStore.setJson('cas-refresh', { version: 3 }, 30)
await sleep(5)
assert.equal(
  await compareSetStateStore.compareSetJson('cas-refresh', { version: 3 }, { version: 3 }, 200),
  true,
  'memory compareSetJson 相同值 CAS 应刷新 TTL'
)
await sleep(50)
assert.deepEqual(
  await stateStore.getJson('cas-refresh'),
  { version: 3 },
  'memory compareSetJson 刷新后的值不能按旧 TTL 过期'
)

const runtimeStateStoreSource = readFileSync(
  new URL('../../shared/runtime-state-store.ts', import.meta.url),
  'utf8'
)
assert.match(
  runtimeStateStoreSource,
  /compareSetJsonScript/,
  'Redis RuntimeStateStore 应声明 compareSetJson Lua'
)
assert.match(
  runtimeStateStoreSource,
  /eval\(compareSetJsonScript/,
  'Redis compareSetJson 必须通过 Lua 原子执行'
)
assert.match(
  runtimeStateStoreSource,
  /current ~= ARGV\[1\][\s\S]*SET[\s\S]*PX/,
  'Redis compareSetJson Lua 应比较当前 raw JSON 后原子 SET PX'
)
assert.match(
  runtimeStateStoreSource,
  /compareDeleteJsonScript/,
  'Redis RuntimeStateStore 应声明 compareDeleteJson Lua'
)
assert.match(
  runtimeStateStoreSource,
  /eval\(compareDeleteJsonScript/,
  'Redis compareDeleteJson 必须通过 Lua 原子执行'
)
assert.match(
  runtimeStateStoreSource,
  /current ~= ARGV\[1\][\s\S]*DEL/,
  'Redis compareDeleteJson Lua 应仅删除精确匹配的 raw JSON'
)

await recordSuccessfulLoginAsync('198.51.100.10', `async-${suffix}@example.test`)
assert.equal((await checkLoginAllowedAsync('198.51.100.10', `async-${suffix}@example.test`)).blocked, false)
for (let index = 0; index < 10; index += 1) {
  await recordFailedLoginAsync('198.51.100.11', `async-fail-${suffix}@example.test`)
}
assert.equal((await checkLoginAllowedAsync('198.51.100.11', `async-fail-${suffix}@example.test`)).blocked, true)

const slot = await tryAcquireAccountConcurrencyAsync(`account-${suffix}`, 2, { lane: 'text' })
assert.equal(slot.acquired, true)
slot.release()
clearAccountConcurrency()

const cache = createSharedJsonCache<{ value: number }>({
  name: `regression_cache_${suffix}`,
  max: 2,
  ttlMs: 1000
})

await cache.set('a', { value: 1 })
assert.deepEqual(await cache.get('a'), { value: 1 })
await cache.delete('a')
assert.equal(await cache.get('a'), undefined)
await cache.set('b', { value: 2 })
await cache.clear()
assert.equal(await cache.get('b'), undefined)

console.log('运行态 store 与异步 JSON cache driver 回归通过')
