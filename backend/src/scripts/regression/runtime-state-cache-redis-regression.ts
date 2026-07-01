import assert from 'node:assert/strict'
import { setTimeout as sleep } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'

const cacheUrl = optionalEnv('JUHE_RUNTIME_STATE_CACHE_REDIS_CACHE_URL') ?? optionalEnv('JUHE_AI_REDIS_CACHE_URL')
const stateUrl = optionalEnv('JUHE_RUNTIME_STATE_CACHE_REDIS_STATE_URL') ?? optionalEnv('JUHE_AI_REDIS_STATE_URL')

if (!cacheUrl || !stateUrl) {
  throw new Error('真实 Redis 回归需要配置 JUHE_AI_REDIS_CACHE_URL / JUHE_AI_REDIS_STATE_URL，或 JUHE_RUNTIME_STATE_CACHE_REDIS_CACHE_URL / JUHE_RUNTIME_STATE_CACHE_REDIS_STATE_URL')
}

runtimeConfig.cacheDriver = 'redis'
runtimeConfig.runtimeStateDriver = 'redis'
runtimeConfig.redis.cacheUrl = cacheUrl
runtimeConfig.redis.stateUrl = stateUrl

const suffix = safeRedisPart(`regression_${Date.now()}_${Math.random().toString(16).slice(2)}`)
const stateName = `runtime_state_${suffix}`
const cacheName = `shared_cache_${suffix}`
const accountId = `account_${suffix}`
const loginIp = `198.51.100.10-${suffix}`
const loginUser = `redis-${suffix}@example.test`
const loginFailIp = `198.51.100.11-${suffix}`
const loginFailUser = `redis-fail-${suffix}@example.test`
const captchaIp = `198.51.100.12-${suffix}`

const [
  { createDedicatedRedisClient, closeRedisClients },
  { createRuntimeStateStore },
  { createSharedJsonCache },
  accountConcurrency,
  loginGuard,
  captchaService
] = await Promise.all([
  import('../../shared/redis-client.js'),
  import('../../shared/runtime-state-store.js'),
  import('../../shared/cache.js'),
  import('../../shared/account-concurrency.js'),
  import('../../modules/auth/login-guard.service.js'),
  import('../../modules/auth/captcha.service.js')
])

const cacheClient = await createDedicatedRedisClient(cacheUrl)
const stateClient = await createDedicatedRedisClient(stateUrl)

const startedAt = Date.now()
const cleanupPatterns = [
  `juhe-ai:cache:${cacheName}:*`,
  `juhe-ai:cache-version:${cacheName}`,
  `juhe-ai:state:${stateName}:*`,
  `juhe-ai:state:auth_login_guard:login:ip:${loginIp}:*`,
  `juhe-ai:state:auth_login_guard:login:username:${loginUser}:*`,
  `juhe-ai:state:auth_login_guard:login:ip:${loginFailIp}:*`,
  `juhe-ai:state:auth_login_guard:login:username:${loginFailUser}:*`,
  `juhe-ai:state:auth_captcha:issue:${captchaIp}`,
  `juhe-ai:account-concurrency:${accountId}:*`,
  `juhe-ai:account-concurrency:${accountId}_parallel:*`,
  `juhe-ai:account-concurrency:${accountId}_external:*`
]

try {
  await cleanupRedisKeys()

  await verifySharedJsonCache()
  await verifyRuntimeStateStore()
  await verifyLoginGuard()
  await verifyCaptchaRuntimeState()
  await verifyAccountConcurrency()

  await cleanupRedisKeys()
  console.log(`真实 Redis 运行态 state/cache 回归通过，用时 ${Date.now() - startedAt}ms`)
} finally {
  await cleanupRedisKeys().catch(() => undefined)
  await closeRedisClient(cacheClient)
  await closeRedisClient(stateClient)
  await closeRedisClients()
}

async function verifySharedJsonCache(): Promise<void> {
  const cache = createSharedJsonCache<{ value: number }>({
    name: cacheName,
    max: 2,
    ttlMs: 1000
  })

  await cache.set('a', { value: 1 })
  assert.deepEqual(await cache.get('a'), { value: 1 }, 'Redis cache 应能读取刚写入的 JSON')

  await cache.delete('a')
  assert.equal(await cache.get('a'), undefined, 'Redis cache delete 后应读不到旧值')

  await cache.set('b', { value: 2 })
  await cache.clear()
  assert.equal(await cache.get('b'), undefined, 'Redis cache clear 后应切换命名空间版本')

  await cache.set('ttl', { value: 3 }, { ttlMs: 5 })
  await waitFor(async () => (await cache.get('ttl')) === undefined, 'Redis cache TTL 应按毫秒过期')
}

async function verifyRuntimeStateStore(): Promise<void> {
  const stateStore = createRuntimeStateStore(stateName)

  await stateStore.setJson('profile', { ok: true }, 1000)
  assert.deepEqual(await stateStore.getJson('profile'), { ok: true }, 'Redis state 应能读取刚写入的 JSON')
  assert.deepEqual(await stateStore.getDeleteJson('profile'), { ok: true }, 'Redis state getDeleteJson 应返回旧值并原子删除')
  assert.equal(await stateStore.getJson('profile'), undefined, 'Redis state getDeleteJson 后应读不到旧值')

  await stateStore.setJson('profile', { ok: true }, 1000)
  await stateStore.delete('profile')
  assert.equal(await stateStore.getJson('profile'), undefined, 'Redis state delete 后应读不到旧值')

  assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 1)
  assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 2)
  assert.equal(await stateStore.incr('counter', { ttlMs: 1000, max: 2 }), 3)
  assert.equal(await stateStore.getJson('counter'), 2, 'Redis state incr 超过 max 时不应继续写大计数')

  assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-a' }), true)
  assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), false)
  await stateStore.releaseLock('lock', 'token-b')
  assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), false, '错误 token 不应释放 Redis lock')
  await stateStore.releaseLock('lock', 'token-a')
  assert.equal(await stateStore.acquireLock('lock', { ttlMs: 1000, token: 'token-b' }), true)

  await stateStore.setJson('ttl', { value: 1 }, 5)
  await waitFor(async () => (await stateStore.getJson('ttl')) === undefined, 'Redis state TTL 应按毫秒过期')
}

async function verifyLoginGuard(): Promise<void> {
  await loginGuard.recordSuccessfulLoginAsync(loginIp, loginUser)
  assert.equal((await loginGuard.checkLoginAllowedAsync(loginIp, loginUser)).blocked, false, '清理后登录防护不应锁定用户')

  for (let index = 0; index < 10; index += 1) {
    await loginGuard.recordFailedLoginAsync(loginFailIp, loginFailUser)
  }
  assert.equal((await loginGuard.checkLoginAllowedAsync(loginFailIp, loginFailUser)).blocked, true, 'Redis 登录防护应在失败阈值后锁定')

  await loginGuard.recordSuccessfulLoginAsync(loginFailIp, loginFailUser)
  assert.equal((await loginGuard.checkLoginAllowedAsync(loginFailIp, loginFailUser)).blocked, false, '成功登录应清理 Redis 登录防护计数和锁')
}

async function verifyCaptchaRuntimeState(): Promise<void> {
  const challenge = await captchaService.createCaptchaChallengeAsync()
  const answer = captchaService.captchaAnswerForTest(challenge.captchaId)
  assert.ok(answer, 'Redis 验证码回归应能通过测试夹具读取刚生成的答案')
  assert.equal(await stateClient.get(`juhe-ai:state:auth_captcha:challenge:${challenge.captchaId}`) !== null, true, 'Redis 验证码 challenge 应写入 runtime state')
  assert.equal(await captchaService.verifyCaptchaChallengeAsync(challenge.captchaId, answer), true, 'Redis 验证码应支持正确答案校验')
  assert.equal(await stateClient.get(`juhe-ai:state:auth_captcha:challenge:${challenge.captchaId}`), null, 'Redis 验证码校验后应原子消费 challenge')
  assert.equal(await captchaService.verifyCaptchaChallengeAsync(challenge.captchaId, answer), false, 'Redis 验证码 challenge 不应允许重复使用')

  let blocked = false
  for (let index = 0; index < 80; index += 1) {
    const result = await captchaService.consumeCaptchaIssueAllowanceAsync(captchaIp)
    if (result.blocked) {
      blocked = true
      assert.ok(result.retryAfterSeconds && result.retryAfterSeconds > 0, 'Redis 验证码限频应返回 Retry-After 秒数')
      break
    }
  }
  assert.equal(blocked, true, 'Redis 验证码限频应在单 IP 高频生成后触发')
}

async function verifyAccountConcurrency(): Promise<void> {
  const first = await accountConcurrency.tryAcquireAccountConcurrencyAsync(accountId, 2, { lane: 'text' })
  const second = await accountConcurrency.tryAcquireAccountConcurrencyAsync(accountId, 2, { lane: 'text' })
  const third = await accountConcurrency.tryAcquireAccountConcurrencyAsync(accountId, 2, { lane: 'text' })

  assert.equal(first.acquired, true)
  assert.equal(second.acquired, true)
  assert.equal(third.acquired, false, 'Redis 账号并发 Lua 应拒绝超过总并发上限的占用')
  assert.equal(third.current, 2)
  assert.equal(accountConcurrency.getAccountCurrentConcurrency(accountId), 2, 'Redis 模式成功占槽后应同步维护 server 本地当前并发')
  assert.equal(accountConcurrency.snapshotAccountConcurrency()[accountId], 2, 'Redis 模式 server 快照应能展示当前账号并发')
  assert.equal((await accountConcurrency.loadAccountCurrentConcurrencyByIdsAsync([accountId])).get(accountId), 2, 'Redis 模式批量读取应以 Redis 当前并发为事实来源')

  first.release()
  second.release()
  await waitForRedisKeyAbsent(`juhe-ai:account-concurrency:${accountId}:total`)
  await waitForLocalConcurrency(accountId, 0)

  const reacquired = await accountConcurrency.tryAcquireAccountConcurrencyAsync(accountId, 2, { lane: 'text' })
  assert.equal(reacquired.acquired, true, 'Redis 账号并发释放后应允许再次占用')
  assert.equal(accountConcurrency.snapshotAccountConcurrency()[accountId], 1, 'Redis 模式重新占槽后 server 快照应恢复为 1')
  reacquired.release()
  await waitForRedisKeyAbsent(`juhe-ai:account-concurrency:${accountId}:total`)
  await waitForLocalConcurrency(accountId, 0)

  const externalAccountId = `${accountId}_external`
  await stateClient.set(`juhe-ai:account-concurrency:${externalAccountId}:total`, '7')
  assert.equal(accountConcurrency.snapshotAccountConcurrency()[externalAccountId], undefined, '外部进程写入的 Redis 并发不应出现在本地 in-flight 诊断快照')
  assert.equal((await accountConcurrency.loadAccountCurrentConcurrencyByIdsAsync([externalAccountId])).get(externalAccountId), 7, 'Redis 模式列表并发批量读取必须能看到其他进程写入的当前并发')

  const parallelAccountId = `${accountId}_parallel`
  const parallelSlots = await Promise.all(
    Array.from({ length: 20 }, () => accountConcurrency.tryAcquireAccountConcurrencyAsync(parallelAccountId, 5, { lane: 'image', laneLimit: 5 }))
  )
  const acquiredSlots = parallelSlots.filter((slot) => slot.acquired)
  assert.equal(acquiredSlots.length, 5, '并发争抢 Redis Lua 时最多只能拿到配置上限数量的槽位')
  assert.equal(accountConcurrency.getAccountCurrentConcurrency(parallelAccountId), 5, 'Redis 并发争抢成功槽位应同步到 server 本地当前并发')

  for (const slot of acquiredSlots) {
    slot.release()
  }
  await waitForRedisKeyAbsent(`juhe-ai:account-concurrency:${parallelAccountId}:total`)
  await waitForLocalConcurrency(parallelAccountId, 0)
}

async function waitForRedisKeyAbsent(key: string): Promise<void> {
  await waitFor(async () => (await stateClient.get(key)) === null, `Redis key ${key} 应被释放`)
}

async function waitForLocalConcurrency(accountId: string, expected: number): Promise<void> {
  await waitFor(
    async () => accountConcurrency.getAccountCurrentConcurrency(accountId) === expected,
    `本地账号并发 ${accountId} 应变为 ${expected}`
  )
}

async function waitFor(predicate: () => Promise<boolean>, message: string): Promise<void> {
  const deadline = Date.now() + 3000
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      if (await predicate()) return
    } catch (error) {
      lastError = error
    }
    await sleep(20)
  }
  assert.fail(lastError instanceof Error ? `${message}: ${lastError.message}` : message)
}

async function cleanupRedisKeys(): Promise<void> {
  for (const pattern of cleanupPatterns) {
    const client = pattern.startsWith('juhe-ai:cache') ? cacheClient : stateClient
    await deleteRedisKeysByPattern(client, pattern)
  }
}

async function deleteRedisKeysByPattern(client: typeof cacheClient, pattern: string): Promise<number> {
  let cursor = '0'
  let deletedCount = 0
  do {
    const response = await client.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '200'])
    const [nextCursor, keys] = parseScanResponse(response)
    cursor = nextCursor
    for (let index = 0; index < keys.length; index += 100) {
      const chunk = keys.slice(index, index + 100)
      if (chunk.length === 0) continue
      deletedCount += numericRedisResult(await client.sendCommand(['DEL', ...chunk]))
    }
  } while (cursor !== '0')
  return deletedCount
}

function parseScanResponse(value: unknown): [string, string[]] {
  if (!Array.isArray(value)) return ['0', []]
  const cursor = String(value[0] ?? '0')
  const keys = Array.isArray(value[1]) ? value[1].map(String) : []
  return [cursor, keys]
}

async function closeRedisClient(client: typeof cacheClient): Promise<void> {
  if (client.quit) {
    await client.quit().catch(() => undefined)
    return
  }
  client.destroy?.()
}

function optionalEnv(name: string): string | undefined {
  const value = process.env[name]?.trim()
  return value ? value : undefined
}

function safeRedisPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}
