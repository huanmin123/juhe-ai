import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients, createDedicatedRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'
import {
  acquireRedisQueueFence,
  redisQueueFenceKey,
  releaseRedisQueueFence
} from '../../shared/redis-queue-fence.js'
import { redisNamespacedGroup, redisNamespacedKey } from '../../shared/redis-namespace.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'

if (process.env.JUHE_AI_ALLOW_REDIS_ROLE_LIVE_SMOKE !== '1') {
  throw new Error('真实 Redis 角色 smoke 会写入并清理当前 namespace，必须显式设置 JUHE_AI_ALLOW_REDIS_ROLE_LIVE_SMOKE=1')
}
if (!runtimeConfig.redis.cacheUrl || !runtimeConfig.redis.stateUrl || !runtimeConfig.redis.queueUrl) {
  throw new Error('Redis 三角色 URL 不完整')
}

const streamKey = redisNamespacedKey('juhe-ai:queue:role-live-smoke')
const groupName = redisNamespacedGroup('juhe-ai:role-live-smoke-writers')
const fenceToken = `role-live-smoke-${process.pid}-${Date.now()}`
const clients: RedisCommandClient[] = []

try {
  const cache = await roleClient(runtimeConfig.redis.cacheUrl, 'cache', 'no', 'allkeys-lru')
  const state = await roleClient(runtimeConfig.redis.stateUrl, 'state', 'no', 'noeviction')
  const queueClient = await roleClient(runtimeConfig.redis.queueUrl, 'queue', 'yes', 'noeviction')
  const stateXaddBefore = await commandCalls(state, 'xadd')
  const queue = new RedisStreamQueue<{ id: string }>({
    streamKey: 'juhe-ai:queue:role-live-smoke',
    groupName: 'juhe-ai:role-live-smoke-writers',
    consumerName: 'role-live-smoke-consumer',
    redisUrl: runtimeConfig.redis.queueUrl,
    blockMs: 50
  })

  assert.equal(await acquireRedisQueueFence(runtimeConfig.redis.queueUrl, fenceToken), true)
  await assert.rejects(queue.enqueue({ id: 'blocked' }), /QUEUE_QUIESCED/)
  assert.equal(await releaseRedisQueueFence(runtimeConfig.redis.queueUrl, fenceToken), true)

  const id = await queue.enqueue({ id: 'accepted' })
  assert.match(id, /^\d+-\d+$/)
  const messages = await queue.readNew()
  assert.deepEqual(messages.map((message) => message.payload.id), ['accepted'])
  assert.equal(await queue.ack(messages.map((message) => message.id)), 1)
  const runtime = await queue.inspectRuntime()
  assert.equal(runtime.streamLength, 0)
  assert.equal(runtime.pendingCount, 0)
  assert.equal(await commandCalls(state, 'xadd'), stateXaddBefore, 'state Redis 不得收到 queue XADD')
  await queue.closeConsumer()

  await Promise.all([cache, state, queueClient].map((client) => client.sendCommand(['PING'])))
  console.log(JSON.stringify({ event: 'redis_role_live_smoke_passed', namespace: runtimeConfig.redis.namespace }))
} finally {
  await releaseRedisQueueFence(runtimeConfig.redis.queueUrl, fenceToken).catch(() => false)
  for (const client of clients) {
    await client.del(streamKey).catch(() => 0)
    await client.del(redisQueueFenceKey()).catch(() => 0)
    await client.quit?.().catch(() => undefined)
  }
  await closeRedisClients()
}

async function roleClient(url: string, role: string, expectedAof: string, expectedPolicy: string): Promise<RedisCommandClient> {
  const client = await createDedicatedRedisClient(url, { disableOfflineQueue: true, connectTimeoutMs: 3000 })
  clients.push(client)
  assert.equal(await client.sendCommand(['PING']), 'PONG', `${role} PING`)
  const raw = await client.sendCommand(['CONFIG', 'GET', 'appendonly', 'save', 'maxmemory-policy'])
  const config = redisConfigMap(raw)
  assert.equal(config.get('appendonly'), expectedAof, `${role} appendonly`)
  assert.equal(config.get('save'), '', `${role} save`)
  assert.equal(config.get('maxmemory-policy'), expectedPolicy, `${role} maxmemory-policy`)
  return client
}

function redisConfigMap(raw: unknown): Map<string, string> {
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
    return new Map(Object.entries(raw).map(([key, value]) => [key, String(value)]))
  }
  assert(Array.isArray(raw), 'CONFIG GET 必须返回对象或键值数组')
  const config = new Map<string, string>()
  for (let index = 0; index + 1 < raw.length; index += 2) config.set(String(raw[index]), String(raw[index + 1]))
  return config
}

async function commandCalls(client: RedisCommandClient, command: string): Promise<number> {
  const info = String(await client.sendCommand(['INFO', 'commandstats']))
  return Number(new RegExp(`^cmdstat_${command}:calls=(\\d+)`, 'm').exec(info)?.[1] ?? 0)
}
