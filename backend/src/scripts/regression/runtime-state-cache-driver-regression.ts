import assert from 'node:assert/strict'
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

await stateStore.setJson('profile', { ok: true }, 1000)
assert.deepEqual(await stateStore.getJson('profile'), { ok: true })
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
