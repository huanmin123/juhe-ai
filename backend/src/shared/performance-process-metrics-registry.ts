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
const commandTimeoutMs = 800
const scanPageLimit = 4
const registryEntryLimit = 128
const registryKeyPrefix = redisNamespacedKey('juhe-ai:runtime:process-event-loop:')
const registryKeyVersion = 'v2'

let publishTimer: NodeJS.Timeout | undefined
let publisherStarted = false
let publishInFlight = false
let publishFailureCount = 0
let registryClientPromise: Promise<RedisCommandClient> | undefined

export function startPerformanceProcessMetricsPublisher(): void {
  if (!performanceRegistryEnabled() || publisherStarted) return
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

export function performanceProcessMetricsRegistryKey(
  instanceId: string,
  processRole: ProcessEventLoopRole
): string {
  return `${registryKeyPrefix}${registryKeyVersion}:${sanitizeRedisNamespacePart(instanceId)}:${sanitizeRedisNamespacePart(processRole)}`
}

function scheduleNextPublish(): void {
  if (!publisherStarted || publishTimer) return
  const seed = `${stableRuntimeIdentity(runtimeConfig.instanceId)}:${stableRuntimeIdentity(currentProcessEventLoopRole())}`
  const phaseMs = stablePerformanceProcessMetricsPublishPhaseMs(seed)
  const delayMs = delayUntilPerformanceProcessMetricsPublishPhaseMs(Date.now(), phaseMs)
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
  const key = performanceProcessMetricsRegistryKey(runtimeConfig.instanceId, sample.processRole)
  await client.set(key, JSON.stringify(sample), { EX: registryTtlSeconds })
}

async function readRegistry(redisUrl: string): Promise<ProcessEventLoopSample[]> {
  const client = await getRegistryClient(redisUrl)
  const keys = await scanRegistryKeys(client)
  if (keys.length === 0) return []
  const values = await client.sendCommand(['MGET', ...keys])
  if (!Array.isArray(values)) return []
  const now = Date.now()
  const samples: ProcessEventLoopSample[] = []
  for (const value of values.slice(0, registryEntryLimit)) {
    const sample = parseRegistrySample(value, now)
    if (sample) samples.push(sample)
  }
  return samples
}

async function scanRegistryKeys(client: RedisCommandClient): Promise<string[]> {
  const keys: string[] = []
  let cursor = '0'
  for (let page = 0; page < scanPageLimit; page += 1) {
    const result = await client.sendCommand(['SCAN', cursor, 'MATCH', `${registryKeyPrefix}*`, 'COUNT', '64'])
    const parsed = parseScanResult(result)
    if (!parsed) break
    cursor = parsed.cursor
    for (const key of parsed.keys) {
      if (keys.length >= registryEntryLimit) return keys
      keys.push(key)
    }
    if (cursor === '0') break
  }
  return keys
}

function parseScanResult(value: unknown): { cursor: string; keys: string[] } | undefined {
  if (!Array.isArray(value) || value.length < 2 || !Array.isArray(value[1])) return undefined
  return {
    cursor: String(value[0] ?? '0'),
    keys: value[1].filter((key): key is string => typeof key === 'string')
  }
}

function parseRegistrySample(value: unknown, now: number): ProcessEventLoopSample | undefined {
  if (typeof value !== 'string') return undefined
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>
    const processRole = processEventLoopRoleFromUnknown(parsed.processRole)
    const processPid = finitePositiveInteger(parsed.processPid)
    const sampledAt = typeof parsed.sampledAt === 'string' ? parsed.sampledAt : ''
    const sampledAtMs = Date.parse(sampledAt)
    if (!processRole || !processPid || !Number.isFinite(sampledAtMs)) return undefined
    if (sampledAtMs < now - registryTtlSeconds * 1_000 || sampledAtMs > now + 5_000) return undefined
    return {
      processRole,
      processPid,
      sampledAt,
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
