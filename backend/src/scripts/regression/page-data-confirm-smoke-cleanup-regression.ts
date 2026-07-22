import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import type { RedisCommandClient } from '../../shared/redis-client.js'
import {
  cleanupDedicatedRedisResources,
  shutdownServerAndRuntimeRedis
} from '../smoke/page-data-confirm-http-smoke.js'

const source = await readFile(new URL('../smoke/page-data-confirm-http-smoke.ts', import.meta.url), 'utf8')

assert.doesNotMatch(
  source,
  /must_change_password\s*,\s*image_generation_enabled/,
  'PG fixture 必须依赖 schema 默认值，兼容 integer/boolean 两类历史 schema'
)
assert.match(
  source,
  /await clearRateLimitKeys\([\s\S]*?readBuckets[\s\S]*?writeKeys/,
  '限流实测前必须精确清空本次 namespace 的 read/write bucket'
)

const events: string[] = []
const errors: string[] = []
const clients = new Map<string, RedisCommandClient>([
  ['cache', redisClient('cache', events, { scanFailure: true })],
  ['state', redisClient('state', events, { hangingDel: true })],
  ['queue', redisClient('queue', events, { hangingQuit: true })]
])

await cleanupDedicatedRedisResources({
  clients,
  prefix: 'juhe-ai:test:',
  errors,
  operationTimeoutMs: 5
})

assert.ok(errors.includes('redis-cache-namespace'), '单个 Redis namespace 失败必须被汇总')
assert.ok(errors.includes('redis-state-namespace'), 'DEL 超时必须被汇总')
assert.ok(errors.includes('redis-queue-client'), 'QUIT 超时必须被汇总')
assert.ok(events.includes('state:del:juhe-ai:test:state'), 'cache 失败后仍必须尝试清理 state Redis')
assert.ok(events.includes('queue:del:juhe-ai:test:queue'), 'cache 失败后仍必须清理 queue Redis')
assert.ok(events.includes('cache:quit'), 'namespace 失败后仍必须尝试关闭 cache Redis')
assert.ok(events.includes('state:quit'), '必须尝试关闭 state Redis')
assert.ok(events.includes('queue:destroy'), 'QUIT 超时必须 destroy Redis client')

const shutdownEvents: string[] = []
const shutdownErrors: string[] = []
const server = {
  listening: true,
  close(_callback: (error?: Error) => void) {
    shutdownEvents.push('server:close')
    return this
  },
  closeAllConnections() {
    shutdownEvents.push('server:closeAllConnections')
  }
}

await shutdownServerAndRuntimeRedis({
  server,
  closeRuntimeRedis: async () => {
    shutdownEvents.push('runtime-redis:close')
    await new Promise<never>(() => undefined)
  },
  errors: shutdownErrors,
  operationTimeoutMs: 5,
  forceCloseGraceMs: 5
})

assert.deepEqual(
  shutdownEvents.slice(0, 2),
  ['server:close', 'runtime-redis:close'],
  '必须先停止接收 HTTP，再关闭 runtime Redis 释放在途请求'
)
assert.ok(shutdownEvents.includes('server:closeAllConnections'), 'server deadline 后必须强制关闭连接')
assert.ok(shutdownErrors.includes('runtime-redis-clients'), 'runtime Redis 超时必须被汇总')
assert.ok(shutdownErrors.includes('server'), 'server 强制关闭后仍未回调必须被汇总且返回')

process.stdout.write('page data confirm smoke cleanup regression passed\n')

function redisClient(
  role: string,
  events: string[],
  options: { scanFailure?: boolean; hangingDel?: boolean; hangingQuit?: boolean } = {}
): RedisCommandClient {
  return {
    connect: async () => undefined,
    get: async () => null,
    set: async () => 'OK',
    del: async (key) => {
      events.push(`${role}:del:${key}`)
      if (options.hangingDel) await new Promise<never>(() => undefined)
      return 1
    },
    eval: async () => undefined,
    sendCommand: async (command) => {
      events.push(`${role}:${command[0]?.toLowerCase()}`)
      if (options.scanFailure) throw new Error('scan failed')
      return ['0', [`juhe-ai:test:${role}`]]
    },
    quit: async () => {
      events.push(`${role}:quit`)
      if (options.hangingQuit) await new Promise<never>(() => undefined)
      return undefined
    },
    destroy: () => {
      events.push(`${role}:destroy`)
    },
    on: () => undefined
  }
}
