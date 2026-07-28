import assert from 'node:assert/strict'

import type { RuntimeStateStore } from '../../shared/runtime-state-store.js'
import {
  providerOAuthRefreshLockRetryMs,
  providerOAuthRefreshLockTtlMs,
  providerOAuthRefreshLockWaitMs,
  runWithProviderOAuthRefreshLock
} from '../../modules/providers/drivers/_shared/oauth-refresh-lock.js'

class TestLockStore implements RuntimeStateStore {
  private readonly locks = new Map<string, { token: string; expiresAt: number }>()
  renewCalls = 0

  async acquireLock(key: string, options: { ttlMs: number; token: string }): Promise<boolean> {
    const current = this.freshLock(key)
    if (current) return false
    this.locks.set(key, { token: options.token, expiresAt: Date.now() + options.ttlMs })
    return true
  }

  async renewLock(key: string, options: { ttlMs: number; token: string }): Promise<boolean> {
    this.renewCalls += 1
    const current = this.freshLock(key)
    if (current?.token !== options.token) return false
    current.expiresAt = Date.now() + options.ttlMs
    return true
  }

  async releaseLock(key: string, token: string): Promise<void> {
    if (this.freshLock(key)?.token === token) this.locks.delete(key)
  }

  replaceLock(key: string, token: string, ttlMs: number): void {
    this.locks.set(key, { token, expiresAt: Date.now() + ttlMs })
  }

  private freshLock(key: string): { token: string; expiresAt: number } | undefined {
    const current = this.locks.get(key)
    if (!current) return undefined
    if (current.expiresAt <= Date.now()) {
      this.locks.delete(key)
      return undefined
    }
    return current
  }

  async getJson<T>(): Promise<T | undefined> { return undefined }
  async getJsonMany<T>(keys: string[]): Promise<Array<T | undefined>> { return keys.map(() => undefined) }
  async getDeleteJson<T>(): Promise<T | undefined> { return undefined }
  async setJson<T>(): Promise<void> {}
  async compareSetJson<T>(): Promise<boolean> { return false }
  async compareDeleteJson<T>(): Promise<boolean> { return false }
  async delete(): Promise<void> {}
  async incr(): Promise<number> { return 0 }
}

assert.equal(providerOAuthRefreshLockTtlMs, 90_000, '刷新锁 TTL 必须覆盖 Anthropic/Grok token exchange 超时')
assert.equal(providerOAuthRefreshLockWaitMs, 30_000, '手工与 dispatch 刷新最多等待 30 秒')
assert.equal(providerOAuthRefreshLockRetryMs, 250, '跨进程锁应按 OpenAI OAuth 的 250ms 节奏重试')

const store = new TestLockStore()
let releaseFirst: (() => void) | undefined
const firstGate = new Promise<void>((resolve) => { releaseFirst = resolve })
const order: string[] = []
const first = runWithProviderOAuthRefreshLock('anthropic', 'shared-account', async () => {
  order.push('first-enter')
  await firstGate
  order.push('first-exit')
  return 'first'
}, { lockStore: store, waitMs: 1_000, retryMs: 5 })

await waitUntil(() => order.includes('first-enter'))
const second = runWithProviderOAuthRefreshLock('anthropic', 'shared-account', async () => {
  order.push('second-enter')
  return 'second'
}, { lockStore: store, waitMs: 1_000, retryMs: 5 })
await delay(20)
assert.deepEqual(order, ['first-enter'], '同一供应商与物理账户只能有一个 token exchange 进入临界区')
releaseFirst?.()
assert.deepEqual(await Promise.all([first, second]), ['first', 'second'])
assert.deepEqual(order, ['first-enter', 'first-exit', 'second-enter'])

const independent = await Promise.all([
  runWithProviderOAuthRefreshLock('anthropic', 'same-id', async () => 'anthropic', { lockStore: store }),
  runWithProviderOAuthRefreshLock('xai', 'same-id', async () => 'xai', { lockStore: store })
])
assert.deepEqual(independent, ['anthropic', 'xai'], '供应商必须参与锁 key，避免不同供应商同 id 互相阻塞')

await store.acquireLock('xai:busy-account', { ttlMs: 1_000, token: 'external-holder' })
await assert.rejects(
  runWithProviderOAuthRefreshLock('xai', 'busy-account', async () => undefined, {
    lockStore: store,
    waitMs: 20,
    retryMs: 5
  }),
  /正在其他节点刷新/u,
  '锁等待超时必须失败，不能在没有互斥的情况下继续刷新'
)

const abortController = new AbortController()
const cancelled = runWithProviderOAuthRefreshLock('xai', 'busy-account', async () => undefined, {
  lockStore: store,
  waitMs: 1_000,
  retryMs: 5,
  signal: abortController.signal
})
abortController.abort(new Error('caller cancelled'))
await assert.rejects(cancelled, /caller cancelled/u, '锁等待必须响应调用方取消')

await assert.rejects(
  runWithProviderOAuthRefreshLock('anthropic', 'release-on-error', async () => {
    throw new Error('task failed')
  }, { lockStore: store }),
  /task failed/u
)
assert.equal(await store.acquireLock('anthropic:release-on-error', { ttlMs: 1_000, token: 'probe' }), true, '任务失败也必须释放刷新锁')

let releaseLongTask: (() => void) | undefined
const longTaskGate = new Promise<void>((resolve) => { releaseLongTask = resolve })
const longTask = runWithProviderOAuthRefreshLock('gemini', 'renewed-account', async () => {
  await longTaskGate
  return 'renewed'
}, { lockStore: store, lockTtlMs: 60, waitMs: 1_000, retryMs: 5 })
await waitUntil(() => store.renewCalls >= 2)
assert.equal(
  await store.acquireLock('gemini:renewed-account', { ttlMs: 60, token: 'competing-holder' }),
  false,
  '刷新任务跨过初始 TTL 后仍必须由持有者续租保持互斥'
)
releaseLongTask?.()
assert.equal(await longTask, 'renewed')

const lostLockTask = runWithProviderOAuthRefreshLock('xai', 'lost-account', async (signal) => {
  await new Promise<void>((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(signal.reason), { once: true })
  })
}, { lockStore: store, lockTtlMs: 60, waitMs: 1_000, retryMs: 5 })
await delay(10)
store.replaceLock('xai:lost-account', 'replacement-holder', 1_000)
await assert.rejects(lostLockTask, /刷新锁已丢失/u, '锁所有权丢失必须中止临界区任务，不能继续写回 token')

console.log('provider OAuth refresh lock regression passed')

async function waitUntil(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1_000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待测试条件超时')
    await delay(1)
  }
}

async function delay(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
