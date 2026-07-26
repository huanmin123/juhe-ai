import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  ensurePublishedModelCatalogSnapshotsInitializedAsync
} from '../../modules/model-pricing/published-model-catalog.service.js'
import {
  modelCatalogReadinessUrl,
  parseModelCatalogReadinessCli,
  readApiKeyFromMode0600File,
  verifyModelCatalogReadiness
} from '../operations/model-catalog-readiness.js'
import type { GatewayModelCatalogSnapshot } from '../../storage/gateway-model-catalog-snapshot.repository.js'

const existingSnapshot: GatewayModelCatalogSnapshot = {
  systemAccountId: '',
  protocol: 'openai',
  variant: 'default',
  payload: { object: 'list', data: [{ id: 'gpt-test' }] },
  modelCount: 1,
  revision: 'existing',
  createdAt: '2026-07-26T00:00:00.000Z',
  updatedAt: '2026-07-26T00:00:00.000Z'
}

const publishedModelCatalogServiceSource = readFileSync(
  new URL('../../modules/model-pricing/published-model-catalog.service.ts', import.meta.url),
  'utf8'
)
assert.match(
  publishedModelCatalogServiceSource,
  /rebuildAll:\s*\(\)\s*=>\s*enqueueSnapshotRebuild\(async\s*\(\)\s*=>\s*\{[\s\S]*?return rebuildPublishedModelCatalogSnapshotsAfterModelChangeInternalAsync\(\)[\s\S]*?\}\)/,
  '首次初始化必须直接进入内部重建队列，不能触发 allRebuildAgain 二次重建'
)

let rebuildCount = 0
let unchangedLeaseAttempts = 0
const unchanged = await ensurePublishedModelCatalogSnapshotsInitializedAsync({
  findInitialSnapshot: async () => existingSnapshot,
  rebuildAll: async () => { rebuildCount += 1; return 1 },
  acquireInitializationLease: async () => { unchangedLeaseAttempts += 1; return true }
})
assert.deepEqual(unchanged, { action: 'unchanged', modelCount: 1, snapshotOwners: 0 })
assert.equal(rebuildCount, 0, '已有持久化快照时不得重建')
assert.equal(unchangedLeaseAttempts, 0, '已有持久化快照时不得触碰初始化租约')

let concurrentSnapshot: GatewayModelCatalogSnapshot | undefined
let concurrentLeaseAcquireCount = 0
let concurrentLeaseReleaseCount = 0
const concurrentDependencies = {
  findInitialSnapshot: async () => concurrentSnapshot,
  rebuildAll: async () => {
    rebuildCount += 1
    concurrentSnapshot = { ...existingSnapshot, revision: 'initialized-concurrently' }
    return 3
  },
  acquireInitializationLease: async () => { concurrentLeaseAcquireCount += 1; return true },
  releaseInitializationLease: async () => { concurrentLeaseReleaseCount += 1 }
}
const concurrentResults = await Promise.all([
  ensurePublishedModelCatalogSnapshotsInitializedAsync(concurrentDependencies),
  ensurePublishedModelCatalogSnapshotsInitializedAsync(concurrentDependencies)
])
assert.deepEqual(concurrentResults, [
  { action: 'initialized', modelCount: 1, snapshotOwners: 3 },
  { action: 'initialized', modelCount: 1, snapshotOwners: 3 }
])
assert.equal(rebuildCount, 1, '同进程两个并发 ensure 必须且只能初始化一次')
assert.equal(concurrentLeaseAcquireCount, 1, '同进程 singleflight 只能竞争一次共享初始化租约')
assert.equal(concurrentLeaseReleaseCount, 1, '同进程 singleflight 只能释放一次共享初始化租约')

const ownerRebuildStarted = deferred<void>()
const allowOwnerRebuild = deferred<void>()
const waiterLeaseAttempted = deferred<void>()
const crossProcessSnapshotReady = deferred<void>()
let crossProcessSnapshot: GatewayModelCatalogSnapshot | undefined
let crossProcessLeaseToken: string | undefined
let crossProcessRebuildCount = 0
let waiterRebuildCount = 0
const acquireCrossProcessLease = async (token: string): Promise<boolean> => {
  if (crossProcessLeaseToken) {
    waiterLeaseAttempted.resolve()
    return false
  }
  crossProcessLeaseToken = token
  return true
}
const releaseCrossProcessLease = async (token: string): Promise<void> => {
  if (crossProcessLeaseToken === token) crossProcessLeaseToken = undefined
}
const ownerEnsure = ensurePublishedModelCatalogSnapshotsInitializedAsync({
  findInitialSnapshot: async () => crossProcessSnapshot,
  rebuildAll: async () => {
    crossProcessRebuildCount += 1
    ownerRebuildStarted.resolve()
    await allowOwnerRebuild.promise
    crossProcessSnapshot = { ...existingSnapshot, revision: 'cross-process-initialized' }
    crossProcessSnapshotReady.resolve()
    return 4
  },
  acquireInitializationLease: acquireCrossProcessLease,
  releaseInitializationLease: releaseCrossProcessLease
})
await ownerRebuildStarted.promise
const waiterEnsure = ensurePublishedModelCatalogSnapshotsInitializedAsync({
  findInitialSnapshot: async () => crossProcessSnapshot,
  rebuildAll: async () => { waiterRebuildCount += 1; return 99 },
  acquireInitializationLease: acquireCrossProcessLease,
  releaseInitializationLease: releaseCrossProcessLease,
  delay: async () => { await crossProcessSnapshotReady.promise }
})
await waiterLeaseAttempted.promise
allowOwnerRebuild.resolve()
assert.deepEqual(await ownerEnsure, { action: 'initialized', modelCount: 1, snapshotOwners: 4 })
assert.deepEqual(await waiterEnsure, { action: 'unchanged', modelCount: 1, snapshotOwners: 0 })
assert.equal(crossProcessRebuildCount, 1, '跨槽持锁方只能执行一次首次初始化')
assert.equal(waiterRebuildCount, 0, '跨槽等待方在持锁方初始化后必须只读返回')

let failedLeaseRebuildCount = 0
await assert.rejects(
  () => ensurePublishedModelCatalogSnapshotsInitializedAsync({
    findInitialSnapshot: async () => undefined,
    rebuildAll: async () => { failedLeaseRebuildCount += 1; return 1 },
    acquireInitializationLease: async () => { throw new Error('redis lease unavailable') }
  }),
  /redis lease unavailable/
)
assert.equal(failedLeaseRebuildCount, 0, '初始化租约获取失败时必须 fail-closed，禁止无锁重建')

let timedOutLeaseRebuildCount = 0
await assert.rejects(
  () => ensurePublishedModelCatalogSnapshotsInitializedAsync({
    findInitialSnapshot: async () => undefined,
    rebuildAll: async () => { timedOutLeaseRebuildCount += 1; return 1 },
    acquireInitializationLease: () => new Promise<boolean>(() => undefined),
    leaseCommandTimeoutMs: 5
  }),
  /初始化租约获取超时/
)
assert.equal(timedOutLeaseRebuildCount, 0, '初始化租约获取超时时必须 fail-closed，禁止无锁重建')

let waitNow = 0
let waitingRebuildCount = 0
await assert.rejects(
  () => ensurePublishedModelCatalogSnapshotsInitializedAsync({
    findInitialSnapshot: async () => undefined,
    rebuildAll: async () => { waitingRebuildCount += 1; return 1 },
    acquireInitializationLease: async () => false,
    now: () => waitNow,
    delay: async (milliseconds) => { waitNow += milliseconds },
    initializationWaitTimeoutMs: 10,
    initializationWaitIntervalMs: 4
  }),
  /等待持锁进程生成全局 OpenAI 快照超时/
)
assert.equal(waitingRebuildCount, 0, '等待持锁进程超时时必须 fail-closed，禁止旁路重建')

await assert.rejects(
  () => ensurePublishedModelCatalogSnapshotsInitializedAsync({
    findInitialSnapshot: async () => undefined,
    rebuildAll: async () => 1,
    acquireInitializationLease: async () => true,
    releaseInitializationLease: async () => undefined
  }),
  /全局 OpenAI 快照仍不存在/
)

const protectedKeyPath = resolve('model-catalog-readiness-test.key')
const secret = 'sk-readiness-secret-that-must-not-be-logged'
assert.equal(readApiKeyFromMode0600File(protectedKeyPath, {
  platform: 'darwin',
  stat: () => ({ isFile: () => true, mode: 0o100600, size: secret.length + 1 }),
  readFile: () => `${secret}\n`
}), secret)
assert.throws(() => readApiKeyFromMode0600File(protectedKeyPath, {
  platform: 'darwin',
  stat: () => ({ isFile: () => true, mode: 0o100640, size: secret.length }),
  readFile: () => secret
}), /0600/)

assert.equal(modelCatalogReadinessUrl('http://127.0.0.1:3211').toString(), 'http://127.0.0.1:3211/v1/models')
assert.equal(modelCatalogReadinessUrl('http://[::1]:3211').toString(), 'http://[::1]:3211/v1/models')
for (const unsafeUrl of [
  'http://localhost:3211',
  'https://127.0.0.1:3211',
  'http://192.168.1.203:3211',
  'http://127.0.0.1:3211/prefix'
]) {
  assert.throws(() => modelCatalogReadinessUrl(unsafeUrl), /readiness/)
}

let timeoutValue = 0
const readiness = await verifyModelCatalogReadiness({
  baseUrl: 'http://127.0.0.1:3211',
  apiKeyFile: protectedKeyPath
}, {
  readApiKey: () => secret,
  timeoutSignal: (milliseconds) => {
    timeoutValue = milliseconds
    return new AbortController().signal
  },
  fetch: (async (input, init) => {
    assert.equal(String(input), 'http://127.0.0.1:3211/v1/models')
    assert.equal(init?.method, 'GET')
    assert.equal(new Headers(init?.headers).get('authorization'), `Bearer ${secret}`)
    assert.equal(init?.redirect, 'error')
    return new Response(JSON.stringify({ object: 'list', data: [{ id: 'gpt-test' }] }), {
      status: 200,
      headers: { 'content-type': 'application/json' }
    })
  }) as typeof fetch
})
assert.equal(timeoutValue, 2_000, 'readiness 必须使用 2s 总超时')
assert.equal(readiness.modelCount, 1)

for (const response of [
  new Response(JSON.stringify({ object: 'list', data: [] }), { status: 200 }),
  new Response(JSON.stringify({ object: 'object', data: [{ id: 'gpt-test' }] }), { status: 200 }),
  new Response(JSON.stringify({ object: 'list', data: [{ id: 'gpt-test' }] }), { status: 503 })
]) {
  await assert.rejects(
    () => verifyModelCatalogReadiness({ baseUrl: 'http://127.0.0.1:3211', apiKeyFile: protectedKeyPath }, {
      readApiKey: () => secret,
      fetch: (async () => response) as typeof fetch
    }),
    (error: unknown) => {
      assert(error instanceof Error)
      assert(!error.message.includes(secret), '失败输出不得泄漏 API key')
      return true
    }
  )
}

assert.deepEqual(parseModelCatalogReadinessCli([
  '--base-url', 'http://127.0.0.1:3211',
  '--api-key-file', protectedKeyPath
]), {
  baseUrl: 'http://127.0.0.1:3211',
  apiKeyFile: protectedKeyPath
})
assert.throws(() => parseModelCatalogReadinessCli(['--base-url', 'http://127.0.0.1:3211']), /api-key-file/)

console.log('模型目录首次初始化与部署 readiness 回归通过')

function deferred<T>() {
  let resolvePromise!: (value: T | PromiseLike<T>) => void
  let rejectPromise!: (reason?: unknown) => void
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return {
    promise,
    resolve: (value?: T) => resolvePromise(value as T),
    reject: rejectPromise
  }
}
