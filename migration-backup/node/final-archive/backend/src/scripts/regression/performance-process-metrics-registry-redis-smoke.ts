import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import type { ProcessEventLoopRole, ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import {
  performanceProcessMetricsRegistryIndexKey,
  performanceProcessMetricsRegistryKey,
  readPerformanceProcessMetricsRegistrySamples,
  writePerformanceProcessMetricsRegistrySample
} from '../../shared/performance-process-metrics-registry.js'
import { createDedicatedRedisClient } from '../../shared/redis-client.js'

assert.equal(process.env.JUHE_AI_ALLOW_PERFORMANCE_PROCESS_METRICS_REDIS_SMOKE, '1')
const cacheRedisUrl = runtimeConfig.redis.cacheUrl
assert.ok(cacheRedisUrl, 'live smoke 需要隔离 Redis cache URL')
assert.match(runtimeConfig.redis.namespace, /^codex-process-metrics-[a-z0-9-]+$/)

const stateRedisUrl = new URL(cacheRedisUrl)
stateRedisUrl.port = '6380'
const queueRedisUrl = new URL(cacheRedisUrl)
queueRedisUrl.port = '6381'
const client = await createDedicatedRedisClient(cacheRedisUrl, {
  disableOfflineQueue: true,
  connectTimeoutMs: 3_000
})
const indexKey = performanceProcessMetricsRegistryIndexKey()
const sampledAtMs = Date.now()
const roles: ProcessEventLoopRole[] = [
  'control:control-1',
  'gateway:gateway-1',
  'gateway:gateway-2',
  'gateway:gateway-3',
  'db-service:control-1',
  'db-service:gateway-1',
  'db-service:gateway-2',
  'db-service:gateway-3',
  'usage-worker:1',
  'usage-worker:2',
  'log-worker:1',
  'log-worker:2',
  'stats-worker:1',
  'ops-worker:1'
]
const sampleKeys = roles.map((role, index) => performanceProcessMetricsRegistryKey(`redis-smoke-${index + 1}`, role))
const staleKey = performanceProcessMetricsRegistryKey('redis-smoke-stale', 'gateway:stale')
const churnKeys = Array.from({ length: 520 }, (_value, index) => (
  performanceProcessMetricsRegistryKey(`redis-smoke-churn-${index}`, `gateway:churn-${index}`)
))
const cleanupKeys = [...sampleKeys, staleKey, ...churnKeys, indexKey]
const preflightEnvironment = {
  ...process.env,
  NODE_ENV: 'test',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_PERFORMANCE_NODE_ROLE: 'control',
  JUHE_AI_PROCESS_ROLE: 'server',
  JUHE_AI_INSTANCE_ID: 'metrics-registry-preflight',
  JUHE_AI_DATABASE_DRIVER: 'postgres',
  JUHE_AI_POSTGRES_URL: 'postgresql://preflight:preflight@127.0.0.1:1/preflight',
  JUHE_AI_CACHE_DRIVER: 'redis',
  JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
  JUHE_AI_QUEUE_DRIVER: 'redis_stream',
  JUHE_AI_REDIS_STATE_URL: stateRedisUrl.toString(),
  JUHE_AI_REDIS_QUEUE_URL: queueRedisUrl.toString()
}

try {
  await client.sendCommand(['DEL', ...cleanupKeys])
  for (let index = 0; index < roles.length; index += 1) {
    await writePerformanceProcessMetricsRegistrySample(
      client,
      `redis-smoke-${index + 1}`,
      sample(roles[index], index === 0 ? sampledAtMs + 60 * 60 * 1_000 : sampledAtMs, 70_000 + index)
    )
  }

  const readSamples = await readPerformanceProcessMetricsRegistrySamples(client)
  assert.deepEqual(new Set(readSamples.map((item) => item.processRole)), new Set(roles))
  assert.equal(Number(await client.sendCommand(['ZCARD', indexKey])), roles.length)
  const sampleTtl = Number(await client.sendCommand(['TTL', sampleKeys[0]]))
  const indexTtl = Number(await client.sendCommand(['TTL', indexKey]))
  assert.ok(sampleTtl > 0 && sampleTtl <= 20, 'sample key 必须由真实 Redis 设置 20 秒短 TTL')
  assert.ok(indexTtl > 0 && indexTtl <= 60, 'index key 必须由真实 Redis 设置 60 秒短 TTL')
  const firstSampleScore = Number(await client.sendCommand(['ZSCORE', indexKey, sampleKeys[0]]))
  assert.ok(Number.isFinite(firstSampleScore))
  assert.equal(
    readSamples.find((item) => item.processRole === roles[0])?.sampledAt,
    new Date(firstSampleScore).toISOString(),
    '真实 reader 必须用 Redis score 覆盖快一小时的本地 payload 时间'
  )

  const roleArguments = roles.flatMap((role) => ['--role', role])
  const preflightResult = runPreflight(['--timeout-ms', '5000', ...roleArguments])
  assert.equal(
    preflightResult.status,
    0,
    `真实 Redis 部署注册 gate 必须识别完整角色: ${preflightResult.stderr.slice(-2_000)}`
  )

  const redisTimeResult = runPreflight(['--print-redis-time-ms'])
  assert.equal(redisTimeResult.status, 0, `preflight 必须能读取 Redis 服务端时间: ${redisTimeResult.stderr.slice(-2_000)}`)
  const deploymentFenceMs = Number(redisTimeResult.stdout.trim())
  assert.ok(Number.isSafeInteger(deploymentFenceMs) && deploymentFenceMs > 0, 'Redis 时间输出必须是纯正整数毫秒')
  const staleRolePidArguments = roles.flatMap((role, index) => ['--role-pid', `${role}=${70_000 + index}`])
  const staleLeaseResult = runPreflight([
    '--timeout-ms',
    '1000',
    '--observed-after-ms',
    String(deploymentFenceMs),
    ...roleArguments,
    ...staleRolePidArguments
  ])
  assert.notEqual(staleLeaseResult.status, 0, '重启前尚未过期的同角色样本不得通过 freshness gate')
  for (let index = 0; index < roles.length; index += 1) {
    await writePerformanceProcessMetricsRegistrySample(
      client,
      `redis-smoke-${index + 1}`,
      sample(roles[index], Date.now(), 75_000 + index)
    )
  }
  const freshRolePidArguments = roles.flatMap((role, index) => ['--role-pid', `${role}=${75_000 + index}`])
  const freshLeaseResult = runPreflight([
    '--timeout-ms',
    '5000',
    '--observed-after-ms',
    String(deploymentFenceMs),
    ...roleArguments,
    ...freshRolePidArguments
  ])
  assert.equal(freshLeaseResult.status, 0, `本次重启后的新样本必须通过 freshness gate: ${freshLeaseResult.stderr.slice(-2_000)}`)
  const wrongPidResult = runPreflight([
    '--timeout-ms',
    '1000',
    '--observed-after-ms',
    String(deploymentFenceMs),
    ...roleArguments,
    ...freshRolePidArguments.slice(0, -2),
    '--role-pid',
    `${roles.at(-1)}=999999`
  ])
  assert.notEqual(wrongPidResult.status, 0, 'fresh score 但 PID 不属于本次健康拓扑的样本不得通过部署 gate')

  await client.set(staleKey, JSON.stringify(sample('gateway:stale', sampledAtMs - 21_000, 80_001)), { EX: 60 })
  const redisTime = await client.sendCommand(['TIME'])
  assert.ok(Array.isArray(redisTime) && redisTime.length >= 2)
  const redisNowMs = Number(redisTime[0]) * 1_000 + Math.floor(Number(redisTime[1]) / 1_000)
  await client.sendCommand(['ZADD', indexKey, String(redisNowMs - 21_000), staleKey])
  const withoutStale = await readPerformanceProcessMetricsRegistrySamples(client)
  assert.equal(withoutStale.some((item) => item.processRole === 'gateway:stale'), false)
  assert.equal(await client.sendCommand(['ZSCORE', indexKey, staleKey]), null, '真实 reader Lua 必须清理过期索引成员')

  for (let index = 0; index < churnKeys.length; index += 1) {
    await writePerformanceProcessMetricsRegistrySample(
      client,
      `redis-smoke-churn-${index}`,
      sample(`gateway:churn-${index}`, sampledAtMs, 90_000 + index)
    )
  }
  const cappedCardinality = Number(await client.sendCommand(['ZCARD', indexKey]))
  assert.equal(cappedCardinality, 512, '真实 publisher Lua 必须在 reader 缺席和实例 churn 时限制索引基数')
  for (let index = 0; index < roles.length; index += 1) {
    await writePerformanceProcessMetricsRegistrySample(
      client,
      `redis-smoke-${index + 1}`,
      sample(roles[index], Date.now(), 100_000 + index)
    )
  }
  const recoveredSamples = await readPerformanceProcessMetricsRegistrySamples(client)
  const recoveredRoles = new Set(recoveredSamples.map((item) => item.processRole))
  assert.equal(roles.every((role) => recoveredRoles.has(role)), true, '当前 publisher 刷新后必须从 512 成员 shedding 中恢复')

  console.log(JSON.stringify({
    event: 'performance_process_metrics_registry_redis_smoke_passed',
    processCount: readSamples.length,
    sampleTtl,
    indexTtl,
    freshnessAndPidFenceVerified: true,
    staleMemberRemoved: true,
    cappedCardinality
  }))
} finally {
  await client.sendCommand(['DEL', ...cleanupKeys]).catch(() => 0)
  const remainingKeyCount = Number(await client.sendCommand(['EXISTS', ...cleanupKeys]).catch(() => -1))
  assert.equal(remainingKeyCount, 0, '隔离 Redis smoke 必须清理全部测试 key')
  await client.quit?.().catch(() => undefined)
}

function runPreflight(args: string[]) {
  return spawnSync(
    process.execPath,
    [
      '--import',
      'tsx',
      fileURLToPath(new URL('../preflight/check-performance-process-metrics-registry.ts', import.meta.url)),
      ...args
    ],
    {
      encoding: 'utf8',
      env: preflightEnvironment
    }
  )
}

function sample(processRole: ProcessEventLoopRole, atMs: number, processPid: number): ProcessEventLoopSample {
  return {
    processRole,
    processPid,
    sampledAt: new Date(atMs).toISOString(),
    eventLoopLagMs: processPid % 100,
    processRssBytes: 100_000_000 + processPid
  }
}
