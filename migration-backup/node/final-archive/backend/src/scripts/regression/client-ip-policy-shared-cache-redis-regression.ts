import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'

const cacheUrl = optionalEnv('JUHE_CLIENT_IP_POLICY_SHARED_CACHE_REDIS_URL') ?? optionalEnv('JUHE_AI_REDIS_CACHE_URL')

if (!cacheUrl) {
  throw new Error('客户端 IP 封禁策略 Redis shared cache 回归需要配置 JUHE_AI_REDIS_CACHE_URL 或 JUHE_CLIENT_IP_POLICY_SHARED_CACHE_REDIS_URL')
}
assertRedisCleanupAllowed(cacheUrl)

const tempRoot = resolve(tmpdir(), `juhe-ai-client-ip-policy-shared-cache-redis-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })

runtimeConfig.cacheDriver = 'redis'
runtimeConfig.redis.cacheUrl = cacheUrl
runtimeConfig.redis.namespace = `client-ip-policy-${Date.now()}`
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'stats-worker'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const [
  { createDedicatedRedisClient, closeRedisClients },
  { redisNamespacedKey },
  { logger },
  databaseModule,
  clientIpStats,
  clientIpPolicyCache
] = await Promise.all([
  import('../../shared/redis-client.js'),
  import('../../shared/redis-namespace.js'),
  import('../../shared/logger.js'),
  import('../../storage/database.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../modules/gateway/runtime/client-ip-policy-cache.service.js')
])

logger.level = 'silent'

const redisClient = await createDedicatedRedisClient(cacheUrl)
const cacheKeyPattern = redisNamespacedKey('juhe-ai:cache:gateway:client-ip-policy-by-ip:*')
const cacheVersionKey = redisNamespacedKey('juhe-ai:cache-version:gateway:client-ip-policy-by-ip')

try {
  await cleanupRedisKeys()

  const normalizedIp = clientIpStats.normalizeClientIpForStats('203.0.113.222')
  assert(normalizedIp, '测试 IPv4 应可规范化')
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO client_ip_registry (
      ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
      first_seen_at, last_seen_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    normalizedIp.ipHash,
    normalizedIp.bucketNo,
    normalizedIp.aggregateIpKey,
    normalizedIp.clientIp,
    normalizedIp.ipVersion,
    now,
    now,
    now,
    now
  )
  const policy = await clientIpStats.createClientIpPolicyAsync({
    ipHash: normalizedIp.ipHash,
    policyType: 'blacklist',
    reason: 'redis shared cache regression',
    actorSystemAccountId: 'sys_admin'
  })

  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp)).blacklistPolicy?.id,
    policy.id,
    '首次判定应从数据库回源并写入 Redis shared cache'
  )
  await waitFor(async () => (await countRedisKeys(cacheKeyPattern)) > 0, 'IP 封禁策略单 IP 条目应写入 Redis shared cache')

  clientIpPolicyCache.replaceClientIpPolicyCacheLocal([], { skipSharedCache: true })
  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp, { cacheOnly: true })).blacklistPolicy?.id,
    policy.id,
    '清空本地快照后，cacheOnly 判定应能读取单 IP Redis shared cache'
  )

  await clientIpStats.disableClientIpPolicies({
    ipHash: normalizedIp.ipHash,
    policyType: 'blacklist',
    reason: 'redis shared cache regression disabled',
    actorSystemAccountId: 'sys_admin'
  })
  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp, { cacheOnly: true })).blacklistPolicy?.id,
    policy.id,
    '数据库禁用策略后，未失效的 cacheOnly 判定仍会命中旧 Redis shared cache'
  )
  await clientIpPolicyCache.reloadClientIpPolicyCacheLocal({ bypassSharedCache: true })
  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp, { cacheOnly: true })).blocked,
    false,
    '绕过 Redis shared cache 重载后，cacheOnly 判定应看到单 IP shared cache 已清理'
  )
  const recreatedPolicy = await clientIpStats.createClientIpPolicyAsync({
    ipHash: normalizedIp.ipHash,
    policyType: 'blacklist',
    reason: 'redis shared cache regression recreated',
    actorSystemAccountId: 'sys_admin'
  })

  await cleanupRedisKeys()
  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp, { cacheOnly: true })).blocked,
    false,
    '清理 Redis shared cache 后，cacheOnly 判定不应回源数据库'
  )
  assert.equal(
    (await clientIpPolicyCache.inspectClientIpPolicy(normalizedIp.clientIp)).blacklistPolicy?.id,
    recreatedPolicy.id,
    'Redis shared cache miss 后，普通判定应从数据库回源并重建 shared cache'
  )

  await cleanupRedisKeys()
  console.log('客户端 IP 封禁策略 Redis shared cache 回归通过：单 IP 条目可跨本地清空恢复，cacheOnly 不回源数据库')
} finally {
  await cleanupRedisKeys().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await closeRedisClient(redisClient)
  await closeRedisClients()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function cleanupRedisKeys(): Promise<void> {
  await deleteRedisKeysByPattern(cacheKeyPattern)
  await redisClient.del(cacheVersionKey)
}

async function countRedisKeys(pattern: string): Promise<number> {
  let cursor = '0'
  let count = 0
  do {
    const response = await redisClient.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '200'])
    const [nextCursor, keys] = parseScanResponse(response)
    cursor = nextCursor
    count += keys.length
  } while (cursor !== '0')
  return count
}

async function deleteRedisKeysByPattern(pattern: string): Promise<number> {
  let cursor = '0'
  let deletedCount = 0
  do {
    const response = await redisClient.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '200'])
    const [nextCursor, keys] = parseScanResponse(response)
    cursor = nextCursor
    for (let index = 0; index < keys.length; index += 100) {
      const chunk = keys.slice(index, index + 100)
      if (chunk.length === 0) continue
      deletedCount += numericRedisResult(await redisClient.sendCommand(['DEL', ...chunk]))
    }
  } while (cursor !== '0')
  return deletedCount
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

function parseScanResponse(value: unknown): [string, string[]] {
  if (!Array.isArray(value)) return ['0', []]
  const cursor = String(value[0] ?? '0')
  const keys = Array.isArray(value[1]) ? value[1].map(String) : []
  return [cursor, keys]
}

async function closeRedisClient(client: typeof redisClient): Promise<void> {
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

function assertRedisCleanupAllowed(redisUrl: string): void {
  if (optionalEnv('JUHE_AI_ALLOW_CLIENT_IP_POLICY_SHARED_CACHE_CLEANUP') === '1') {
    return
  }
  let hostname = ''
  try {
    hostname = new URL(redisUrl).hostname.toLowerCase().replace(/^\[(.*)\]$/, '$1')
  } catch {
    hostname = ''
  }
  if (hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1') {
    return
  }
  throw new Error('客户端 IP 封禁策略 Redis shared cache 回归会清理当前 JUHE_AI_REDIS_NAMESPACE 下的 client-ip-policy cache key；非本机 Redis 必须先确认是测试实例，并设置 JUHE_AI_ALLOW_CLIENT_IP_POLICY_SHARED_CACHE_CLEANUP=1')
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}
