import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import {
  redisStreamDrainContracts,
  inspectRedisStreamDrain,
  type RedisStreamDrainSnapshot
} from './redis-stream-drain.js'
import { getRedisClient } from './redis-client.js'
import {
  redisStreamQueueMetricNames,
  setRedisStreamQueueMetricsSnapshot,
  type RedisStreamQueueMetricSample
} from './prometheus-metrics.js'

const refreshIntervalMs = 30_000
const refreshTimeoutMs = 2_000

let refreshTimer: NodeJS.Timeout | undefined
let metricsStarted = false
let refreshInFlight = false
let refreshFailureCount = 0

export function startRedisStreamMetrics(): void {
  if (metricsStarted) return
  metricsStarted = true
  if (!redisStreamMetricsEnabled()) {
    setRedisStreamQueueMetricsSnapshot({ enabled: false, collectionSuccess: false, queues: [] })
    return
  }
  setRedisStreamQueueMetricsSnapshot({ enabled: true, collectionSuccess: false, queues: [] })
  void refreshRedisStreamMetrics().finally(scheduleNextRefresh)
}

export function stopRedisStreamMetrics(): void {
  metricsStarted = false
  if (refreshTimer) {
    clearTimeout(refreshTimer)
    refreshTimer = undefined
  }
}

export function redisStreamMetricSamples(snapshot: RedisStreamDrainSnapshot): RedisStreamQueueMetricSample[] {
  const byName = new Map(snapshot.streams.map((stream) => [stream.name, stream]))
  return redisStreamQueueMetricNames.map((queue) => {
    const contract = redisStreamDrainContracts.find((candidate) => candidate.name === metricQueueContractName(queue))
    if (!contract) throw new Error(`Redis Stream 指标缺少固定队列契约：${queue}`)
    const stream = byName.get(contract.name)
    const group = stream?.groups.find((candidate) => candidate.name === contract.groupName)
    return {
      queue,
      streamLength: nonNegative(stream?.length),
      pendingCount: nonNegative(group?.pending),
      lag: nonNegative(group?.lag),
      lagKnown: group?.lag === null || group?.lag === undefined ? 0 : 1,
      consumerCount: nonNegative(group?.consumers),
      oldestPendingIdleSeconds: nonNegative(group?.oldestPendingIdleMs) / 1_000,
      consumerGroupPresent: group ? 1 : 0
    }
  })
}

function metricQueueContractName(queue: RedisStreamQueueMetricSample['queue']): string {
  if (queue === 'usage_records') return 'usage-records'
  if (queue === 'public_api_logs') return 'public-api-logs'
  return 'record-maintenance'
}

function scheduleNextRefresh(): void {
  if (!metricsStarted || !redisStreamMetricsEnabled() || refreshTimer) return
  refreshTimer = setTimeout(() => {
    refreshTimer = undefined
    void refreshRedisStreamMetrics().finally(scheduleNextRefresh)
  }, refreshIntervalMs)
  refreshTimer.unref()
}

async function refreshRedisStreamMetrics(): Promise<void> {
  if (!metricsStarted || refreshInFlight || !redisStreamMetricsEnabled()) return
  refreshInFlight = true
  try {
    const queueUrl = runtimeConfig.redis.queueUrl
    if (!queueUrl) throw new Error('Redis Stream 指标采集缺少 queue URL')
    const client = await getRedisClient(queueUrl)
    const snapshot = await withTimeout(inspectRedisStreamDrain(client), refreshTimeoutMs)
    setRedisStreamQueueMetricsSnapshot({
      enabled: true,
      collectionSuccess: true,
      lastSuccessTimestampSeconds: Date.now() / 1_000,
      queues: redisStreamMetricSamples(snapshot)
    })
    refreshFailureCount = 0
  } catch (error) {
    refreshFailureCount += 1
    setRedisStreamQueueMetricsSnapshot({ enabled: true, collectionSuccess: false, queues: [] })
    if (refreshFailureCount === 1 || refreshFailureCount % 12 === 0) {
      logger.warn(errorLogFields(error, {
        event: 'redis_stream_metrics_collection_failed',
        refreshFailureCount
      }), 'Redis Stream 指标采集失败，不影响业务队列')
    }
  } finally {
    refreshInFlight = false
  }
}

function redisStreamMetricsEnabled(): boolean {
  return runtimeConfig.queueDriver === 'redis_stream' && Boolean(runtimeConfig.redis.queueUrl)
}

function nonNegative(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error('Redis Stream 指标采集超时')), timeoutMs)
        timer.unref()
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
    promise.catch(() => undefined)
  }
}
