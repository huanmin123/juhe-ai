import assert from 'node:assert/strict'

import type { ProcessEventLoopRole, ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import {
  performanceProcessMetricsTopologyComplete,
  readPerformanceProcessMetricsRegistrySamples,
  writePerformanceProcessMetricsRegistrySample
} from '../../shared/performance-process-metrics-registry.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'

interface StoredValue {
  value: string
  expiresAtMs: number
}

class FakeRegistryRedisClient implements RedisCommandClient {
  readonly strings = new Map<string, StoredValue>()
  readonly sortedSets = new Map<string, Map<string, number>>()
  readonly commandNames: string[] = []
  nowMs: number
  indexExpiresAtMs = 0

  constructor(nowMs: number, unrelatedKeyCount = 0) {
    this.nowMs = nowMs
    for (let index = 0; index < unrelatedKeyCount; index += 1) {
      this.strings.set(`unrelated:${index}`, {
        value: String(index),
        expiresAtMs: Number.POSITIVE_INFINITY
      })
    }
  }

  async connect(): Promise<void> {}

  async get(key: string): Promise<string | null> {
    return this.liveValue(key)
  }

  async set(): Promise<never> {
    throw new Error('publisher 必须使用原子 Lua，不能拆成独立 SET')
  }

  async del(key: string): Promise<number> {
    return this.strings.delete(key) ? 1 : 0
  }

  async eval(_script: string, options: { keys: string[]; arguments: string[] }): Promise<unknown> {
    this.commandNames.push(options.keys.length === 2 ? 'PUBLISH_EVAL' : 'READ_EVAL')
    if (options.keys.length === 2) {
      const [sampleKey, indexKey] = options.keys
      const [payload, sampleTtlSeconds, cardinalityLimit, indexTtlSeconds] = options.arguments
      this.strings.set(sampleKey, {
        value: payload,
        expiresAtMs: this.nowMs + Number(sampleTtlSeconds) * 1_000
      })
      const index = this.sortedSets.get(indexKey) ?? new Map<string, number>()
      index.set(sampleKey, this.nowMs)
      this.removeExpiredMembers(index, Number(sampleTtlSeconds))
      const excessMembers = index.size - Number(cardinalityLimit)
      if (excessMembers > 0) {
        for (const [member] of this.sortedEntries(index).slice(0, excessMembers)) index.delete(member)
      }
      this.sortedSets.set(indexKey, index)
      this.indexExpiresAtMs = this.nowMs + Number(indexTtlSeconds) * 1_000
      return this.nowMs
    }

    assert.equal(options.keys.length, 1, '读索引 Lua 只能访问一个索引 key')
    const [indexKey] = options.keys
    const [sampleTtlSeconds, limit] = options.arguments.map(Number)
    const index = this.sortedSets.get(indexKey) ?? new Map<string, number>()
    this.removeExpiredMembers(index, sampleTtlSeconds)
    return this.sortedEntries(index)
      .filter(([, score]) => score <= this.nowMs)
      .slice(0, limit)
      .flatMap(([member, score]) => [member, String(score)])
  }

  async sendCommand(command: string[]): Promise<unknown> {
    const [name, ...arguments_] = command
    this.commandNames.push(name)
    assert.equal(name, 'MGET', `registry reader 不得发送 ${name}`)
    return arguments_.map((key) => this.liveValue(key))
  }

  on(): void {}

  indexCardinality(): number {
    return Math.max(0, ...[...this.sortedSets.values()].map((index) => index.size))
  }

  private liveValue(key: string): string | null {
    const stored = this.strings.get(key)
    if (!stored) return null
    if (stored.expiresAtMs <= this.nowMs) {
      this.strings.delete(key)
      return null
    }
    return stored.value
  }

  private removeExpiredMembers(index: Map<string, number>, sampleTtlSeconds: number): void {
    const minimumScore = this.nowMs - sampleTtlSeconds * 1_000
    for (const [member, score] of index) {
      if (score < minimumScore) index.delete(member)
    }
  }

  private sortedEntries(index: Map<string, number>): Array<[string, number]> {
    return [...index.entries()].sort((left, right) => left[1] - right[1] || left[0].localeCompare(right[0]))
  }
}

const sampledAtMs = Date.parse('2026-07-27T05:30:00.000Z')
const defaultTopology = topology(3, 2, 2)
const client = new FakeRegistryRedisClient(sampledAtMs, 10_000)
const roles = performanceRoles(defaultTopology)

for (let index = 0; index < roles.length; index += 1) {
  const processRole = roles[index]
  const sample = buildSample(processRole, sampledAtMs, 10_000 + index)
  await writePerformanceProcessMetricsRegistrySample(client, `instance-${index + 1}`, sample)
}
await writePerformanceProcessMetricsRegistrySample(
  client,
  'malformed-role',
  { ...buildSample('gateway:malformed-role', sampledAtMs, 19_001), processRole: 'malformed-role' as ProcessEventLoopRole }
)

const samples = await readPerformanceProcessMetricsRegistrySamples(client)
assert.equal(samples.length, roles.length, '大键空间中必须完整发现默认 performance 拓扑的 14 个进程')
assert.deepEqual(
  new Set(samples.map((sample) => sample.processRole)),
  new Set(roles),
  '读回的动态进程角色必须与 publisher 一致'
)
assert.equal(
  client.commandNames.filter((name) => name === 'PUBLISH_EVAL').length,
  roles.length + 1,
  '每次发布必须只通过一个原子 Lua 更新样本和索引'
)
assert.equal(client.commandNames.includes('SCAN'), false, 'reader 不得扫描 10000 个无关业务 key')
assert.equal(client.indexExpiresAtMs, sampledAtMs + 60_000, '活跃索引必须使用独立短 TTL')
assert.equal(performanceProcessMetricsTopologyComplete(samples, defaultTopology), true, '默认拓扑的角色集合必须判定完整')

const dualControlTopology = topology(3, 2, 2, 2)
const dualControlRoles = performanceRoles(dualControlTopology)
const dualControlSamples = dualControlRoles.map((processRole, index) => buildSample(processRole, sampledAtMs, 18_000 + index))
assert.equal(performanceProcessMetricsTopologyComplete(dualControlSamples, dualControlTopology), true, '双 Control 拓扑必须要求管理副本及其 DB service')
assert.equal(
  performanceProcessMetricsTopologyComplete(
    dualControlSamples.filter((sample) => sample.processRole !== 'control-replica:control-2'),
    dualControlTopology
  ),
  false,
  '缺失管理副本时不得把双 Control 拓扑误判为完整'
)
const tripleControlTopology = topology(5, 2, 2, 3)
const tripleControlRoles = performanceRoles(tripleControlTopology)
const tripleControlSamples = tripleControlRoles.map((processRole, index) => buildSample(processRole, sampledAtMs, 18_500 + index))
assert.equal(performanceProcessMetricsTopologyComplete(tripleControlSamples, tripleControlTopology), true, '三 Control 拓扑必须同时要求两个管理副本及其 DB service')
assert.equal(
  performanceProcessMetricsTopologyComplete(
    tripleControlSamples.filter((sample) => sample.processRole !== 'control-replica:control-3'),
    tripleControlTopology
  ),
  false,
  '缺失第二个管理副本时不得把三 Control 拓扑误判为完整'
)
assert.equal(
  performanceProcessMetricsTopologyComplete(
    [...samples.filter((sample) => sample.processRole !== 'gateway:gateway-3'), samples[1]],
    defaultTopology
  ),
  false,
  '重复旧实例角色不得用条目数量掩盖 Gateway 角色缺失'
)
assert.equal(
  performanceProcessMetricsTopologyComplete(
    [
      ...samples.filter((sample) => sample.processRole !== 'db-service:gateway-3'),
      buildSample('db-service:old-gateway', sampledAtMs, 19_100)
    ],
    defaultTopology
  ),
  false,
  '旧 DB service 不得按类别数量掩盖当前 Gateway 的同 ID DB service 缺失'
)

client.nowMs = sampledAtMs + 20_001
const refreshedRole = roles[2]
await writePerformanceProcessMetricsRegistrySample(
  client,
  'instance-3',
  buildSample(refreshedRole, client.nowMs, 20_003)
)
const expiredSamples = await readPerformanceProcessMetricsRegistrySamples(client)
assert.deepEqual(
  expiredSamples.map((sample) => sample.processRole),
  [refreshedRole],
  '超过 20 秒的进程必须从索引和读结果中淘汰'
)

const skewedClockClient = new FakeRegistryRedisClient(sampledAtMs)
await writePerformanceProcessMetricsRegistrySample(
  skewedClockClient,
  'skewed-clock',
  buildSample('gateway:skewed-clock', sampledAtMs + 60 * 60 * 1_000, 25_001)
)
const skewedClockSamples = await readPerformanceProcessMetricsRegistrySamples(skewedClockClient)
assert.equal(skewedClockSamples.length, 1, 'publisher 本地时钟偏差不得让健康进程从 Redis 活跃索引消失')
assert.equal(
  skewedClockSamples[0].sampledAt,
  new Date(sampledAtMs).toISOString(),
  '持久化采样时间必须使用 Redis 统一观测时间'
)

const maximumTopology = topology(32, 32, 32)
const maximumRoles = performanceRoles(maximumTopology)
assert.equal(maximumRoles.length, 132, '合法最大 performance 拓扑应包含 132 个进程')
const maximumClient = new FakeRegistryRedisClient(sampledAtMs)
for (let index = 0; index < maximumRoles.length; index += 1) {
  await writePerformanceProcessMetricsRegistrySample(
    maximumClient,
    `maximum-${index + 1}`,
    buildSample(maximumRoles[index], sampledAtMs, 40_000 + index)
  )
}
const maximumSamples = await readPerformanceProcessMetricsRegistrySamples(maximumClient)
assert.equal(maximumSamples.length, 132, 'reader 必须完整返回合法最大拓扑，不能在 128 条截断')
assert.equal(performanceProcessMetricsTopologyComplete(maximumSamples, maximumTopology), true)
for (let index = 0; index < maximumRoles.length; index += 1) {
  await writePerformanceProcessMetricsRegistrySample(
    maximumClient,
    `maximum-rolling-${index + 1}`,
    buildSample(maximumRoles[index], sampledAtMs, 45_000 + index)
  )
}
const rollingSamples = await readPerformanceProcessMetricsRegistrySamples(maximumClient)
assert.equal(rollingSamples.length, 264, '索引必须容纳合法最大拓扑的新旧双版本滚动重叠')
assert.equal(performanceProcessMetricsTopologyComplete(rollingSamples, maximumTopology), true)

const churnClient = new FakeRegistryRedisClient(sampledAtMs)
for (let index = 0; index < 600; index += 1) {
  await writePerformanceProcessMetricsRegistrySample(
    churnClient,
    `churn-${index}`,
    buildSample(`gateway:churn-${index}`, sampledAtMs, 50_000 + index)
  )
}
assert.equal(churnClient.indexCardinality(), 512, 'reader 缺席并发生实例 churn 时 publisher 必须限制索引基数')
churnClient.nowMs += 1
for (let index = 0; index < maximumRoles.length; index += 1) {
  await writePerformanceProcessMetricsRegistrySample(
    churnClient,
    `current-${index}`,
    buildSample(maximumRoles[index], churnClient.nowMs, 60_000 + index)
  )
}
const recoveredFromChurn = await readPerformanceProcessMetricsRegistrySamples(churnClient)
assert.equal(
  performanceProcessMetricsTopologyComplete(recoveredFromChurn, maximumTopology),
  true,
  '超过 512 时允许淘汰最旧成员，但当前 132 个 publisher 刷新后必须恢复完整拓扑'
)

await assert.rejects(
  writePerformanceProcessMetricsRegistrySample(
    client,
    'invalid-time',
    { ...buildSample('gateway:invalid-time', client.nowMs, 30_001), sampledAt: 'invalid' }
  ),
  /采样时间无效/,
  'publisher 不得把无效时间写成不可清理的索引 score'
)

console.log('performance 进程指标注册表回归通过：Redis 时钟、132 角色容量、拓扑完整性、短 TTL 与 churn 上限正确')

function topology(gatewayReplicas: number, usageWorkerReplicas: number, logWorkerReplicas: number, controlReplicas = 1) {
  return {
    controlReplicas,
    gatewayReplicas,
    usageWorkerReplicas,
    logWorkerReplicas,
    statsWorkerReplicas: 1,
    opsWorkerReplicas: 1
  }
}

function performanceRoles(value: ReturnType<typeof topology>): ProcessEventLoopRole[] {
  const roles: ProcessEventLoopRole[] = ['control:control-1', 'db-service:control-1']
  for (let replica = 2; replica <= value.controlReplicas; replica += 1) {
    roles.push(`control-replica:control-${replica}`, `db-service:control-${replica}`)
  }
  for (let replica = 1; replica <= value.gatewayReplicas; replica += 1) {
    roles.push(`gateway:gateway-${replica}`, `db-service:gateway-${replica}`)
  }
  for (let replica = 1; replica <= value.usageWorkerReplicas; replica += 1) roles.push(`usage-worker:${replica}`)
  for (let replica = 1; replica <= value.logWorkerReplicas; replica += 1) roles.push(`log-worker:${replica}`)
  for (let replica = 1; replica <= value.statsWorkerReplicas; replica += 1) roles.push(`stats-worker:${replica}`)
  for (let replica = 1; replica <= value.opsWorkerReplicas; replica += 1) roles.push(`ops-worker:${replica}`)
  return roles
}

function buildSample(processRole: ProcessEventLoopRole, atMs: number, processPid: number): ProcessEventLoopSample {
  return {
    processRole,
    processPid,
    sampledAt: new Date(atMs).toISOString(),
    eventLoopLagMs: processPid % 100,
    processRssBytes: 100_000_000 + processPid,
    processHeapUsedBytes: 50_000_000 + processPid,
    processHeapTotalBytes: 75_000_000 + processPid,
    processExternalBytes: 1_000_000 + processPid,
    processArrayBuffersBytes: 500_000 + processPid
  }
}
