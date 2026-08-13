import { runtimeConfig } from '../config/runtime.js'
import { logger } from './logger.js'
import {
  buildProcessEventLoopSample,
  currentProcessEventLoopRole,
  processEventLoopRoleFromUnknown,
  type ProcessEventLoopRole,
  type ProcessEventLoopSample
} from './process-event-loop-monitor.js'
import {
  createDedicatedRedisClient,
  isRecoverableRedisClientError,
  type RedisCommandClient
} from './redis-client.js'
import { redisNamespacedKey, sanitizeRedisNamespacePart } from './redis-namespace.js'

const publishIntervalMs = 5_000
const publishPhaseWindowStartMs = 250
const publishPhaseWindowEndMs = 3_000
const minimumNextPublishDelayMs = 10
const registryTtlSeconds = 20
const registryIndexTtlSeconds = 60
const commandTimeoutMs = 800
const registryEntryLimit = 512
const publishFailureBackoffMaxMs = 10_000
const registryKeyPrefix = redisNamespacedKey('juhe-ai:runtime:process-event-loop:')
const registryKeyVersion = 'v2'
const registryIndexKey = redisNamespacedKey('juhe-ai:runtime:process-event-loop-index:v2')
const publishRegistrySampleScript = `
local redis_time = redis.call('TIME')
local observed_at_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = observed_at_ms - tonumber(ARGV[2]) * 1000
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('ZADD', KEYS[2], observed_at_ms, KEYS[1])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', '(' .. minimum_score)
local cardinality = redis.call('ZCARD', KEYS[2])
local cardinality_limit = tonumber(ARGV[3])
if cardinality > cardinality_limit then
  redis.call('ZREMRANGEBYRANK', KEYS[2], 0, cardinality - cardinality_limit - 1)
end
redis.call('EXPIRE', KEYS[2], ARGV[4])
return observed_at_ms
`
const readActiveRegistryKeysScript = `
local redis_time = redis.call('TIME')
local observed_at_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = observed_at_ms - tonumber(ARGV[1]) * 1000
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. minimum_score)
return redis.call('ZRANGEBYSCORE', KEYS[1], minimum_score, observed_at_ms, 'WITHSCORES', 'LIMIT', '0', ARGV[2])
`

export interface PerformanceProcessMetricsTopology {
  controlReplicas: number
  gatewayReplicas: number
  usageWorkerReplicas: number
  logWorkerReplicas: number
  statsWorkerReplicas: number
  opsWorkerReplicas: number
}

let publishTimer: NodeJS.Timeout | undefined
let publisherStarted = false
let publishInFlight = false
let publishFailureCount = 0
let registryClientPromise: Promise<RedisCommandClient> | undefined

export function startPerformanceProcessMetricsPublisher(): void {
  if (!performanceRegistryEnabled() || publisherStarted) return
  publishFailureCount = 0
  publisherStarted = true
  scheduleNextPublish()
}

export function stopPerformanceProcessMetricsPublisher(): void {
  publisherStarted = false
  if (publishTimer) {
    clearTimeout(publishTimer)
    publishTimer = undefined
  }
  dropRegistryClient()
}

export function stablePerformanceProcessMetricsPublishPhaseMs(seed: string): number {
  const windowSize = publishPhaseWindowEndMs - publishPhaseWindowStartMs
  return publishPhaseWindowStartMs + stableHash(seed) % windowSize
}

export function delayUntilPerformanceProcessMetricsPublishPhaseMs(nowMs: number, phaseMs: number): number {
  const normalizedNow = finiteNonNegative(nowMs) % publishIntervalMs
  const normalizedPhase = Math.min(
    publishIntervalMs - 1,
    Math.max(0, Math.floor(finiteNonNegative(phaseMs)))
  )
  let delayMs = (normalizedPhase - normalizedNow + publishIntervalMs) % publishIntervalMs
  if (delayMs < minimumNextPublishDelayMs) delayMs += publishIntervalMs
  return delayMs
}

export function performanceProcessMetricsPublishFailureBackoffMs(failureCount: number): number {
  const normalizedFailureCount = Math.max(0, Math.floor(finiteNonNegative(failureCount)))
  if (normalizedFailureCount === 0) return 0
  return Math.min(publishIntervalMs * 2 ** Math.min(normalizedFailureCount - 1, 4), publishFailureBackoffMaxMs)
}

export function performanceProcessMetricsRegistryKey(
  instanceId: string,
  processRole: ProcessEventLoopRole
): string {
  return `${registryKeyPrefix}${registryKeyVersion}:${sanitizeRedisNamespacePart(instanceId)}:${sanitizeRedisNamespacePart(processRole)}`
}

export function performanceProcessMetricsRegistryIndexKey(): string {
  return registryIndexKey
}

function scheduleNextPublish(): void {
  if (!publisherStarted || publishTimer) return
  const seed = `${stableRuntimeIdentity(runtimeConfig.instanceId)}:${stableRuntimeIdentity(currentProcessEventLoopRole())}`
  const phaseMs = stablePerformanceProcessMetricsPublishPhaseMs(seed)
  const delayMs = Math.max(
    delayUntilPerformanceProcessMetricsPublishPhaseMs(Date.now(), phaseMs),
    performanceProcessMetricsPublishFailureBackoffMs(publishFailureCount)
  )
  publishTimer = setTimeout(() => {
    publishTimer = undefined
    if (!publisherStarted) return
    void publishCurrentProcessMetrics().finally(() => scheduleNextPublish())
  }, delayMs)
  publishTimer.unref()
}

export async function readPerformanceProcessEventLoopSamples(): Promise<ProcessEventLoopSample[]> {
  if (!performanceRegistryEnabled()) return []
  const redisUrl = runtimeConfig.redis.cacheUrl
  if (!redisUrl) return []
  try {
    return await withTimeout(readRegistry(redisUrl), commandTimeoutMs, '高性能进程指标注册表读取超时')
  } catch (error) {
    if (isRecoverableRedisClientError(error) || error instanceof PerformanceRegistryCommandTimeoutError) {
      dropRegistryClient()
    }
    throw error
  }
}

export async function readPerformanceProcessMetricsRegistryTimeMs(): Promise<number> {
  if (!performanceRegistryEnabled()) throw new Error('高性能进程指标注册表未启用')
  const redisUrl = runtimeConfig.redis.cacheUrl
  if (!redisUrl) throw new Error('高性能进程指标注册表缺少 Redis URL')
  try {
    return await withTimeout(readRegistryTimeMs(redisUrl), commandTimeoutMs, '高性能进程指标 Redis 时间读取超时')
  } catch (error) {
    if (isRecoverableRedisClientError(error) || error instanceof PerformanceRegistryCommandTimeoutError) {
      dropRegistryClient()
    }
    throw error
  }
}

async function publishCurrentProcessMetrics(): Promise<void> {
  if (publishInFlight) return
  const redisUrl = runtimeConfig.redis.cacheUrl
  if (!redisUrl) return
  publishInFlight = true
  try {
    const sample = buildProcessEventLoopSample()
    await withTimeout(
      writeRegistrySample(redisUrl, sample),
      commandTimeoutMs,
      '高性能进程指标注册表写入超时'
    )
    publishFailureCount = 0
  } catch (error) {
    publishFailureCount += 1
    if (isRecoverableRedisClientError(error) || error instanceof PerformanceRegistryCommandTimeoutError) {
      dropRegistryClient()
    }
    if (publishFailureCount === 1 || publishFailureCount % 12 === 0) {
      logger.warn({
        event: 'performance_process_metrics_publish_failed',
        publishFailureCount,
        err: error
      }, '高性能进程指标注册失败，不影响业务请求')
    }
  } finally {
    publishInFlight = false
  }
}

async function writeRegistrySample(redisUrl: string, sample: ProcessEventLoopSample): Promise<void> {
  const client = await getRegistryClient(redisUrl)
  await writePerformanceProcessMetricsRegistrySample(client, runtimeConfig.instanceId, sample)
}

async function readRegistry(redisUrl: string): Promise<ProcessEventLoopSample[]> {
  const client = await getRegistryClient(redisUrl)
  return await readPerformanceProcessMetricsRegistrySamples(client)
}

async function readRegistryTimeMs(redisUrl: string): Promise<number> {
  const client = await getRegistryClient(redisUrl)
  const value = await client.sendCommand(['TIME'])
  if (!Array.isArray(value) || value.length < 2) throw new Error('Redis TIME 返回格式无效')
  const seconds = Number(value[0])
  const microseconds = Number(value[1])
  const observedAtMs = seconds * 1_000 + Math.floor(microseconds / 1_000)
  if (!Number.isSafeInteger(observedAtMs) || observedAtMs <= 0) throw new Error('Redis TIME 返回时间无效')
  return observedAtMs
}

export async function writePerformanceProcessMetricsRegistrySample(
  client: RedisCommandClient,
  instanceId: string,
  sample: ProcessEventLoopSample
): Promise<void> {
  const key = performanceProcessMetricsRegistryKey(instanceId, sample.processRole)
  const sampledAtMs = Date.parse(sample.sampledAt)
  if (!Number.isFinite(sampledAtMs)) {
    throw new Error('高性能进程指标采样时间无效')
  }
  await client.eval(publishRegistrySampleScript, {
    keys: [key, registryIndexKey],
    arguments: [
      JSON.stringify(sample),
      String(registryTtlSeconds),
      String(registryEntryLimit),
      String(registryIndexTtlSeconds)
    ]
  })
}

export async function readPerformanceProcessMetricsRegistrySamples(
  client: RedisCommandClient
): Promise<ProcessEventLoopSample[]> {
  const indexedEntries = await client.eval(readActiveRegistryKeysScript, {
    keys: [registryIndexKey],
    arguments: [
      String(registryTtlSeconds),
      String(registryEntryLimit)
    ]
  })
  const entries = uniqueRegistryEntries(indexedEntries)
  if (entries.length === 0) return []
  const values = await client.sendCommand(['MGET', ...entries.map((entry) => entry.key)])
  if (!Array.isArray(values)) return []
  const samples: ProcessEventLoopSample[] = []
  for (let index = 0; index < Math.min(values.length, entries.length); index += 1) {
    const sample = parseRegistrySample(values[index], entries[index].observedAtMs)
    if (sample) samples.push(sample)
  }
  return samples
}

export function performanceProcessMetricsTopologyComplete(
  samples: readonly Pick<ProcessEventLoopSample, 'processRole'>[],
  topology: PerformanceProcessMetricsTopology
): boolean {
  const roles = new Set(samples.map((sample) => sample.processRole))
  const controlInstanceIds = roleInstanceIds(roles, 'control:')
  const controlReplicaInstanceIds = roleInstanceIds(roles, 'control-replica:')
  const gatewayInstanceIds = roleInstanceIds(roles, 'gateway:')
  const dbServiceInstanceIds = roleInstanceIds(roles, 'db-service:')
  if (matchingInstanceCount(controlInstanceIds, dbServiceInstanceIds) < 1) return false
  if (matchingInstanceCount(controlReplicaInstanceIds, dbServiceInstanceIds) < topology.controlReplicas - 1) return false
  if (matchingInstanceCount(gatewayInstanceIds, dbServiceInstanceIds) < topology.gatewayReplicas) return false
  return numberedWorkerRolesComplete(roles, 'usage-worker', topology.usageWorkerReplicas)
    && numberedWorkerRolesComplete(roles, 'log-worker', topology.logWorkerReplicas)
    && numberedWorkerRolesComplete(roles, 'stats-worker', topology.statsWorkerReplicas)
    && numberedWorkerRolesComplete(roles, 'ops-worker', topology.opsWorkerReplicas)
}

function uniqueRegistryEntries(value: unknown): Array<{ key: string; observedAtMs: number }> {
  if (!Array.isArray(value)) return []
  const entries = new Map<string, number>()
  for (let index = 0; index + 1 < value.length; index += 2) {
    const key = value[index]
    const observedAtMs = Number(value[index + 1])
    if (
      typeof key !== 'string'
      || !key.startsWith(`${registryKeyPrefix}${registryKeyVersion}:`)
      || !Number.isFinite(observedAtMs)
      || observedAtMs <= 0
    ) continue
    const existing = entries.get(key)
    if (existing === undefined || observedAtMs > existing) entries.set(key, observedAtMs)
    if (entries.size >= registryEntryLimit) break
  }
  return [...entries].map(([key, observedAtMs]) => ({ key, observedAtMs }))
}

function parseRegistrySample(value: unknown, observedAtMs: number): ProcessEventLoopSample | undefined {
  if (typeof value !== 'string') return undefined
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>
    const processRole = processEventLoopRoleFromUnknown(parsed.processRole)
    const processPid = finitePositiveInteger(parsed.processPid)
    const sampledAt = typeof parsed.sampledAt === 'string' ? parsed.sampledAt : ''
    const sampledAtMs = Date.parse(sampledAt)
    if (!processRole || !processPid || !Number.isFinite(sampledAtMs)) return undefined
    return {
      processRole,
      processPid,
      sampledAt: new Date(observedAtMs).toISOString(),
      eventLoopLagMs: finiteMetric(parsed.eventLoopLagMs),
      processRssBytes: finiteMetric(parsed.processRssBytes),
      processHeapUsedBytes: finiteMetric(parsed.processHeapUsedBytes),
      processHeapTotalBytes: finiteMetric(parsed.processHeapTotalBytes),
      processExternalBytes: finiteMetric(parsed.processExternalBytes),
      processArrayBuffersBytes: finiteMetric(parsed.processArrayBuffersBytes)
    }
  } catch {
    return undefined
  }
}

function roleInstanceIds(roles: ReadonlySet<ProcessEventLoopRole>, prefix: string): Set<string> {
  const instanceIds = new Set<string>()
  for (const role of roles) {
    if (!role.startsWith(prefix)) continue
    const instanceId = role.slice(prefix.length).trim()
    if (instanceId) instanceIds.add(instanceId)
  }
  return instanceIds
}

function matchingInstanceCount(left: ReadonlySet<string>, right: ReadonlySet<string>): number {
  let count = 0
  for (const value of left) {
    if (right.has(value)) count += 1
  }
  return count
}

function numberedWorkerRolesComplete(
  roles: ReadonlySet<ProcessEventLoopRole>,
  role: 'usage-worker' | 'log-worker' | 'stats-worker' | 'ops-worker',
  replicas: number
): boolean {
  for (let replica = 1; replica <= replicas; replica += 1) {
    if (!roles.has(`${role}:${replica}`)) return false
  }
  return true
}

function finitePositiveInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

function finiteMetric(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function performanceRegistryEnabled(): boolean {
  return runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.cacheDriver === 'redis'
    && Boolean(runtimeConfig.redis.cacheUrl)
}

async function getRegistryClient(redisUrl: string): Promise<RedisCommandClient> {
  const existingPromise = registryClientPromise
  if (existingPromise) {
    const existing = await existingPromise
    if (existing.isOpen !== false && existing.isReady !== false) return existing
    dropRegistryClient(existingPromise)
    const replacementPromise = registryClientPromise
    if (replacementPromise) return await replacementPromise
  }
  const clientPromise = createDedicatedRedisClient(redisUrl, {
    disableOfflineQueue: true,
    commandsQueueMaxLength: 16,
    connectTimeoutMs: commandTimeoutMs
  }).catch((error) => {
    if (registryClientPromise === clientPromise) registryClientPromise = undefined
    throw error
  })
  clientPromise.catch(() => undefined)
  registryClientPromise = clientPromise
  return await clientPromise
}

function dropRegistryClient(expectedPromise?: Promise<RedisCommandClient>): void {
  const clientPromise = registryClientPromise
  if (!clientPromise || (expectedPromise && clientPromise !== expectedPromise)) return
  registryClientPromise = undefined
  void clientPromise.then((client) => client.destroy?.(), () => undefined)
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new PerformanceRegistryCommandTimeoutError(message)), timeoutMs)
        timeout.unref()
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
    promise.catch(() => undefined)
  }
}

class PerformanceRegistryCommandTimeoutError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PerformanceRegistryCommandTimeoutError'
  }
}

function stableHash(value: string): number {
  let hash = 0x811c9dc5
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 0x01000193)
  }
  return hash >>> 0
}

function finiteNonNegative(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0
}

function stableRuntimeIdentity(value: string): string {
  return value.replace(/(^|:)process-\d+(?=$|-)/g, '$1default')
}
