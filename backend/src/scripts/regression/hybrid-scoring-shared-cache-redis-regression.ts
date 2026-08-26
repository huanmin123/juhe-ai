import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearHybridScoringCacheForTest,
  clearHybridScoringSharedCacheForTest,
  getHybridScoringSharedCacheEntryForTest,
  setHybridScoringSharedCacheEntryForTest
} from '../../modules/gateway/hybrid/scoring.service.js'
import { closeRedisClients } from '../../shared/redis-client.js'

const redisUrl = optionalEnv('JUHE_HYBRID_SCORING_SHARED_CACHE_REDIS_URL') ?? optionalEnv('JUHE_AI_REDIS_CACHE_URL')
if (!redisUrl) {
  throw new Error('混合路由评分 Redis shared cache 回归需要配置 JUHE_AI_REDIS_CACHE_URL 或 JUHE_HYBRID_SCORING_SHARED_CACHE_REDIS_URL')
}

runtimeConfig.cacheDriver = 'redis'
runtimeConfig.redis.cacheUrl = redisUrl

const key = `hybrid-scoring-shared-cache-regression:${Date.now()}:${Math.random().toString(16).slice(2)}`

try {
  await clearHybridScoringSharedCacheForTest()
  await setHybridScoringSharedCacheEntryForTest(key, {
    level: 7,
    confidence: 0.91,
    factors: ['redis-shared-cache'],
    reason: 'regression'
  })
  const cached = await getHybridScoringSharedCacheEntryForTest(key)
  assert.deepEqual(cached, {
    level: 7,
    confidence: 0.91,
    factors: ['redis-shared-cache'],
    reason: 'regression'
  }, '混合路由评分 Redis shared cache 应能写入并读取评分结果')

  clearHybridScoringCacheForTest({ clearShared: false })
  const stillCached = await getHybridScoringSharedCacheEntryForTest(key)
  assert.deepEqual(stillCached, cached, '清理进程内评分缓存不应误删 Redis shared cache')

  await clearHybridScoringSharedCacheForTest()
  const cleared = await getHybridScoringSharedCacheEntryForTest(key)
  assert.equal(cleared, undefined, '清理 Redis shared cache 后评分结果应不可见')
} finally {
  await closeRedisClients()
}

console.log('混合路由评分 Redis shared cache 回归通过：shared cache set/get/clear 边界正常')

function optionalEnv(name: string): string | undefined {
  const value = process.env[name]?.trim()
  return value ? value : undefined
}
