import assert from 'node:assert/strict'

import type { RedisCommandClient, RedisOperationDeadlineOptions } from '../../shared/redis-client.js'

const { runtimeConfig } = await import('../../config/runtime.js')
const {
  internalGatewayRegistryEntryKey,
  setInternalGatewayRegistryOperationRunnerForTest,
  startInternalGatewayRegistry,
  stopInternalGatewayRegistry
} = await import('../../modules/gateway/runtime/internal-gateway-registry.js')

const previous = {
  runtimeMode: runtimeConfig.runtimeMode,
  performanceNodeRole: runtimeConfig.performanceNodeRole,
  processRole: runtimeConfig.processRole,
  runtimeStateDriver: runtimeConfig.runtimeStateDriver,
  stateUrl: runtimeConfig.redis.stateUrl,
  instanceId: runtimeConfig.instanceId,
  port: runtimeConfig.port
}
const entries = new Map<string, string>()
let heldUnregister: Promise<void> | undefined
let releaseUnregister: (() => void) | undefined
let unregisterStarted = false

const fakeRedisClient: RedisCommandClient = {
  async connect() {},
  async get(key) { return entries.get(key) ?? null },
  async set(key, value) {
    entries.set(key, value)
    return 'OK'
  },
  async del(key) {
    return entries.delete(key) ? 1 : 0
  },
  async eval(_script, options) {
    if (options.arguments.length === 4) {
      entries.set(options.keys[0]!, options.arguments[0]!)
      return Date.now()
    }
    if (options.arguments.length === 1) {
      unregisterStarted = true
      await heldUnregister
      const key = options.keys[0]!
      const current = entries.get(key)
      const bootId = current ? JSON.parse(current).bootId : undefined
      if (bootId !== options.arguments[0]) return 0
      entries.delete(key)
      return 1
    }
    return []
  },
  async sendCommand() { return [] },
  on() { return undefined }
}

try {
  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.performanceNodeRole = 'gateway'
  runtimeConfig.processRole = 'server'
  runtimeConfig.runtimeStateDriver = 'redis'
  runtimeConfig.redis.stateUrl = 'redis://127.0.0.1:6380/0'
  runtimeConfig.instanceId = 'internal-gateway-registry-regression'
  runtimeConfig.port = 39124
  setInternalGatewayRegistryOperationRunnerForTest(async <T>(
    _url: string,
    _options: RedisOperationDeadlineOptions,
    operation: (client: RedisCommandClient) => Promise<T>
  ) => await operation(fakeRedisClient))

  const entryKey = internalGatewayRegistryEntryKey(runtimeConfig.instanceId)
  startInternalGatewayRegistry()
  await waitUntil(() => entries.has(entryKey), 'Gateway 启动后必须登记自身 loopback endpoint')
  const firstEntry = JSON.parse(entries.get(entryKey) ?? '{}') as { bootId?: string }
  assert.ok(firstEntry.bootId, 'Gateway 登记必须包含本次启动 bootId')

  heldUnregister = new Promise<void>((resolve) => {
    releaseUnregister = resolve
  })
  const firstStop = stopInternalGatewayRegistry()
  await waitUntil(() => unregisterStarted, '停止 Gateway 时必须先进入原子注销')
  startInternalGatewayRegistry()
  releaseUnregister?.()
  await firstStop
  await waitUntil(() => entries.has(entryKey), 'DB service 恢复后 Gateway 必须以新 bootId 重新登记')
  const secondEntry = JSON.parse(entries.get(entryKey) ?? '{}') as { bootId?: string }
  assert.notEqual(secondEntry.bootId, firstEntry.bootId, '旧 bootId 注销后不得删除恢复实例的新登记')

  heldUnregister = undefined
  unregisterStarted = false
  await stopInternalGatewayRegistry()
  assert.equal(entries.has(entryKey), false, 'DB service 不可用或服务退出时必须立即注销 Gateway 登记')
} finally {
  await stopInternalGatewayRegistry()
  setInternalGatewayRegistryOperationRunnerForTest()
  runtimeConfig.runtimeMode = previous.runtimeMode
  runtimeConfig.performanceNodeRole = previous.performanceNodeRole
  runtimeConfig.processRole = previous.processRole
  runtimeConfig.runtimeStateDriver = previous.runtimeStateDriver
  runtimeConfig.redis.stateUrl = previous.stateUrl
  runtimeConfig.instanceId = previous.instanceId
  runtimeConfig.port = previous.port
}

console.log('internal gateway registry regression passed: bounded stop, bootId atomic unregister, re-registration')

async function waitUntil(predicate: () => boolean, message: string): Promise<void> {
  const deadlineAtMs = Date.now() + 1_000
  while (!predicate()) {
    if (Date.now() >= deadlineAtMs) throw new Error(message)
    await new Promise<void>((resolve) => setTimeout(resolve, 5))
  }
}
