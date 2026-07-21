import assert from 'node:assert/strict'

import {
  closeRedisClients,
  getRedisClient
} from '../../shared/redis-client.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  consumePenaltyWindowRateLimitGroupsAsync,
  createPenaltyWindowRateLimitStore
} from '../../modules/rate-limit/penalty-window-rate-limit.js'

const stateUrl = runtimeConfig.redis.stateUrl
assert(stateUrl, '真实 Redis 回归必须配置 JUHE_AI_REDIS_STATE_URL')

const namespace = `plan145-rate-limit-${Date.now().toString(36)}`
runtimeConfig.redis.namespace = namespace

const client = await getRedisClient(stateUrl)
try {
  const originalEval = client.eval.bind(client)
  let evalCount = 0
  client.eval = async (script, options) => {
    evalCount += 1
    return originalEval(script, options)
  }

  const runSequence = async (driver: 'memory' | 'redis') => {
    runtimeConfig.runtimeStateDriver = driver
    const globalStore = createPenaltyWindowRateLimitStore({
      name: 'authenticated-models-global',
      penaltyMode: 'fixed_window'
    })
    const ipStore = createPenaltyWindowRateLimitStore({
      name: 'authenticated-models-ip',
      penaltyMode: 'fixed_window'
    })
    const groupsForIp = (ip: string) => [
      {
        scope: 'api_key' as const,
        store: globalStore,
        scopeKey: 'redis-regression-key',
        rules: [
          { windowSeconds: 10, maxRequests: 2 },
          { windowSeconds: 60, maxRequests: 4 }
        ]
      },
      {
        scope: 'api_key_ip' as const,
        store: ipStore,
        scopeKey: `redis-regression-key:ip:${ip}`,
        rules: [
          { windowSeconds: 10, maxRequests: 1 },
          { windowSeconds: 60, maxRequests: 2 }
        ]
      }
    ] as const

    return [
      await consumePenaltyWindowRateLimitGroupsAsync({
        groups: groupsForIp('203.0.113.55'),
        nowMs: 2_000_000
      }),
      await consumePenaltyWindowRateLimitGroupsAsync({
        groups: groupsForIp('203.0.113.55'),
        nowMs: 2_000_001
      }),
      await consumePenaltyWindowRateLimitGroupsAsync({
        groups: groupsForIp('203.0.113.56'),
        nowMs: 2_000_002
      })
    ]
  }

  const memoryDecisions = await runSequence('memory')
  const redisDecisions = await runSequence('redis')
  assert.deepEqual(redisDecisions, memoryDecisions, '相同双窗口序列的 memory/Redis 决策必须一致')
  assert.deepEqual(redisDecisions.map((decision) => [decision.allowed, decision.scope, decision.limit]), [
    [true, undefined, undefined],
    [false, 'api_key_ip', 1],
    [false, 'api_key', 2]
  ])
  assert.equal(evalCount, 3, '每个请求的两个 scope 必须只执行一次 Redis EVAL')

  const keysAfterGlobalBlock = await scanAllTestKeys(client)
  assert.equal(
    keysAfterGlobalBlock.length,
    4,
    '全局桶拒绝后不得为尚未检查的第二个 IP 创建或消费任何窗口 key'
  )
} finally {
  try {
    const keys = await scanAllTestKeys(client)
    if (keys.length) await client.sendCommand(['DEL', ...keys])
    assert.deepEqual(await scanAllTestKeys(client), [], 'Redis 回归必须清理本次 namespace 的全部 key')
  } finally {
    await closeRedisClients()
  }
}

console.log('authenticated models Redis grouped limiter regression passed')

async function scanAllTestKeys(client: Awaited<ReturnType<typeof getRedisClient>>): Promise<string[]> {
  const keys: string[] = []
  let cursor = '0'
  do {
    const scanResult = await client.sendCommand([
      'SCAN',
      cursor,
      'MATCH',
      `juhe-ai:${namespace}:rate-limit:penalty:*`,
      'COUNT',
      '1000'
    ]) as unknown[]
    cursor = String(scanResult?.[0] ?? '0')
    if (Array.isArray(scanResult?.[1])) keys.push(...scanResult[1].map(String))
  } while (cursor !== '0')
  return keys
}
