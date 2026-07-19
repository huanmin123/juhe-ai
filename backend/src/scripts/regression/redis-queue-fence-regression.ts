import assert from 'node:assert/strict'

import {
  acquireRedisQueueFenceWithClient,
  releaseRedisQueueFenceIdempotentlyWithClient,
  redisQueueFenceKey,
  releaseRedisQueueFenceWithClient
} from '../../shared/redis-queue-fence.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'

let fenceToken: string | null = null
let nextId = 0
const evalCalls: Array<{ script: string; keys: string[]; arguments: string[] }> = []

const client: RedisCommandClient = {
  connect: async () => undefined,
  get: async () => fenceToken,
  set: async (_key, value, options) => {
    if (options?.NX === true && fenceToken !== null) return null
    fenceToken = value
    return 'OK'
  },
  del: async () => {
    const existed = fenceToken === null ? 0 : 1
    fenceToken = null
    return existed
  },
  eval: async (script, options) => {
    evalCalls.push({ script, keys: options.keys, arguments: options.arguments })
    if (script.includes("redis.call('XADD'")) {
      if (fenceToken !== null) throw new Error('QUEUE_QUIESCED')
      nextId += 1
      return `${nextId}-0`
    }
    if (script.includes("redis.call('GET'")) {
      if (script.includes('if not current') && fenceToken === null) return 1
      if (fenceToken !== options.arguments[0]) return 0
      fenceToken = null
      return 1
    }
    throw new Error('unexpected script')
  },
  sendCommand: async () => null,
  on: () => undefined
}

assert.equal(await acquireRedisQueueFenceWithClient(client, 'release-a'), true, '首个 owner 必须获取 fence')
assert.equal(await acquireRedisQueueFenceWithClient(client, 'release-b'), false, '已有 owner 时第二个 token 不得覆盖 fence')
assert.equal(await releaseRedisQueueFenceWithClient(client, 'release-b'), false, '错误 token 不得释放 fence')

const queue = new RedisStreamQueue<{ id: string }>({
  streamKey: 'queue:fence-regression',
  groupName: 'queue:fence-regression-writers',
  redisUrl: 'redis://127.0.0.1:6381/0',
  producerClient: async () => client
})

await assert.rejects(queue.enqueue({ id: 'blocked' }), /QUEUE_QUIESCED/, 'fence 存在时 XADD 必须原子拒绝')
assert.equal(await releaseRedisQueueFenceWithClient(client, 'release-a'), true, '正确 token 必须释放 fence')
assert.equal(await queue.enqueue({ id: 'accepted' }), '1-0', '解除 fence 后必须恢复 XADD')

assert.equal(await acquireRedisQueueFenceWithClient(client, 'release-c'), true)
assert.equal(await releaseRedisQueueFenceWithClient(client, 'release-a'), false, '旧 owner token 不得释放新 fence')
assert.equal(await releaseRedisQueueFenceWithClient(client, 'release-c'), true)
assert.equal(await releaseRedisQueueFenceIdempotentlyWithClient(client, 'release-c'), true, '同一 owner 在 fence 已不存在时重复恢复释放应成功')
assert.equal(await acquireRedisQueueFenceWithClient(client, 'release-d'), true)
assert.equal(await releaseRedisQueueFenceIdempotentlyWithClient(client, 'release-c'), false, '幂等恢复释放仍不得删除其他 owner fence')
assert.equal(await releaseRedisQueueFenceIdempotentlyWithClient(client, 'release-d'), true)

assert.match(redisQueueFenceKey(), /^juhe-ai:[^:]+:queue:fence$/, 'fence key 必须使用部署 namespace')
assert.ok(evalCalls.some((call) => call.script.includes("redis.call('XADD'") && call.keys.length === 2), 'XADD 必须与 fence 检查处于同一 Lua')
assert.ok(evalCalls.some((call) => call.script.includes("redis.call('GET'") && call.script.includes("redis.call('DEL'")), '释放必须使用 compare-and-delete Lua')

console.log('redis-queue-fence-regression passed')
