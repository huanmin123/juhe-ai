import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import {
  canUseProcessLocalAppCacheAsFactSource,
  createSharedJsonCache
} from '../../shared/cache.js'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))
const originalCacheDriver = runtimeConfig.cacheDriver
const originalRedisCacheUrl = runtimeConfig.redis.cacheUrl

try {
  await assertSharedCacheDriverBoundary()
  assertRepositoryLookupRedisBoundary()
} finally {
  runtimeConfig.cacheDriver = originalCacheDriver
  runtimeConfig.redis.cacheUrl = originalRedisCacheUrl
}

console.log('资源 lookup Redis cache 边界回归通过：Redis driver 下 shared cache 不回退 memory，lookup 不以本地 LRU 作为跨进程事实来源')

async function assertSharedCacheDriverBoundary(): Promise<void> {
  runtimeConfig.cacheDriver = 'memory'
  runtimeConfig.redis.cacheUrl = undefined
  assert.equal(canUseProcessLocalAppCacheAsFactSource(), true, 'memory driver 下进程内 AppCache 可作为本进程事实来源')

  const cache = createSharedJsonCache<{ value: number }>({
    name: `repository_lookup_boundary_${Date.now()}_${Math.random().toString(16).slice(2)}`,
    max: 2,
    ttlMs: 1000
  })

  await cache.set('stale', { value: 1 })
  assert.deepEqual(await cache.get('stale'), { value: 1 }, 'memory shared cache 应保留单进程缓存能力')

  runtimeConfig.cacheDriver = 'redis'
  assert.equal(canUseProcessLocalAppCacheAsFactSource(), false, 'Redis driver 下进程内 AppCache 不能作为跨进程事实来源')
  await assert.rejects(
    cache.get('stale'),
    /JUHE_AI_REDIS_CACHE_URL/,
    '同一个 shared cache 实例在切到 Redis driver 后必须走 Redis，不能继续读取旧 memory 值'
  )

  const redisCache = createSharedJsonCache<{ value: number }>({
    name: `repository_lookup_boundary_redis_${Date.now()}_${Math.random().toString(16).slice(2)}`,
    max: 2,
    ttlMs: 1000
  })
  await assert.rejects(
    redisCache.set('value', { value: 2 }),
    /JUHE_AI_REDIS_CACHE_URL/,
    'Redis driver 未配置 Redis URL 时必须失败，不能创建 memory fallback'
  )
}

function assertRepositoryLookupRedisBoundary(): void {
  const cacheSource = readSource('shared/cache.ts')
  const repositoryLookupSource = readSource('storage/repository-lookups.ts')
  const syncBody = functionBody(repositoryLookupSource, 'loadCachedRowsByIds')
  const asyncBody = functionBody(repositoryLookupSource, 'loadCachedRowsByIdsAsync')

  assert(cacheSource.includes('class DriverSharedJsonCache'), 'shared JSON cache 应按调用时 driver 分派')
  assert(cacheSource.includes('new RedisSharedJsonCache(this.options)'), 'Redis driver 下 shared JSON cache 应创建 Redis 实现')
  assert(cacheSource.includes('return this.memoryCache'), 'memory fallback 只能在非 Redis driver 下使用')

  assert(
    syncBody.includes('if (!canUseProcessLocalAppCacheAsFactSource())'),
    '同步 lookup helper 在 Redis driver 下必须跳过进程内 LRU'
  )
  assert(
    syncBody.includes('高性能模式禁止同步资源 lookup 绕过 Redis shared cache'),
    '同步 lookup helper 在 Redis driver 下必须 fail-fast，不能绕过 Redis shared cache 直查 DB'
  )

  const redisBranchIndex = asyncBody.indexOf("if (runtimeConfig.cacheDriver === 'redis')")
  const sharedReadIndex = asyncBody.indexOf('const sharedResults = await Promise.all(ids.map(async (id) =>')
  const sharedWriteIndex = asyncBody.indexOf('await setLookupSharedCacheEntryAsync(sharedCache, row.id, row)')
  const localReadIndex = asyncBody.indexOf('const localMissIds: string[] = []')
  assert(redisBranchIndex >= 0, '异步 lookup helper 必须有 Redis driver 分支')
  assert(sharedReadIndex > redisBranchIndex, '异步 lookup Redis 分支应读取 Redis shared cache')
  assert(sharedWriteIndex > sharedReadIndex, '异步 lookup Redis miss 回源后必须等待 Redis shared cache 写入')
  assert(localReadIndex > sharedReadIndex, '异步 lookup Redis 分支必须早于本地 LRU 读取')
  assert(!asyncBody.includes('localMissIds.map(async (id) => {'), '异步 lookup 不能在本地 LRU miss 后才读取 Redis shared cache')
  assert(!asyncBody.includes('cache.set(id, cached)'), 'Redis shared cache 命中不能再回填本地 LRU 作为后续事实来源')
  assert(!repositoryLookupSource.includes('repository_lookup_shared_cache_read_failed'), 'Redis shared cache 失败必须抛错，不能日志吞掉后回退')
}

function readSource(relativePath: string): string {
  return readFileSync(resolve(backendSrcRoot, relativePath), 'utf8')
}

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const returnTypeStart = source.indexOf('):', start)
  assert(returnTypeStart >= 0, `函数 ${functionName} 缺少返回类型`)
  const openBrace = source.indexOf('{', returnTypeStart)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)

  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}
