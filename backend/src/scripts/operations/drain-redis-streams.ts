import { runtimeConfig } from '../../config/runtime.js'
import {
  startUsageRecordRedisStreamConsumer,
  stopUsageRecordRedisStreamConsumer
} from '../../modules/gateway/usage/record-queue.service.js'
import {
  startAuditLogRedisStreamConsumer,
  stopAuditLogRedisStreamConsumer
} from '../../modules/audit-logs/audit-log-queue.service.js'
import {
  startOperationLogRedisStreamConsumer,
  stopOperationLogRedisStreamConsumer
} from '../../modules/operation-logs/operation-log-queue.service.js'
import {
  startPublicApiLogRedisStreamConsumer,
  stopPublicApiLogRedisStreamConsumer
} from '../../modules/public-api-logs/public-api-log-queue.service.js'
import {
  startRuntimeLogRedisStreamConsumer,
  stopRuntimeLogRedisStreamConsumer
} from '../../modules/runtime-logs/runtime-log-index-queue.service.js'
import {
  startRecordMaintenanceRedisStreamConsumer,
  stopRecordMaintenanceRedisStreamConsumer
} from '../../modules/record-maintenance/record-maintenance-queue.service.js'
import { closeRedisClients, getRedisClient } from '../../shared/redis-client.js'
import { redisQueueFenceKey } from '../../shared/redis-queue-fence.js'
import {
  inspectRedisStreamDrain,
  redisStreamDrainContracts,
  RedisStreamDrainStabilityTracker
} from '../../shared/redis-stream-drain.js'

const pollIntervalMs = boundedIntegerEnv('JUHE_AI_QUEUE_DRAIN_POLL_INTERVAL_MS', 2000, 250, 30000)
const timeoutMs = boundedIntegerEnv('JUHE_AI_QUEUE_DRAIN_TIMEOUT_MS', 900000, 10000, 3600000)
const requiredStableWindows = boundedIntegerEnv('JUHE_AI_QUEUE_DRAIN_STABLE_WINDOWS', 2, 2, 10)
let interrupted = false

process.once('SIGINT', () => { interrupted = true })
process.once('SIGTERM', () => { interrupted = true })

try {
  await drainRedisStreams()
} catch (error) {
  process.exitCode = 1
  console.error(JSON.stringify({
    event: 'redis_stream_drain_failed',
    error: error instanceof Error ? error.message : String(error)
  }))
}

async function drainRedisStreams(): Promise<void> {
  assertDrainRuntime()
  const fenceToken = requiredEnv('JUHE_AI_QUEUE_FENCE_TOKEN')
  const queueUrl = runtimeConfig.redis.queueUrl as string
  const client = await getRedisClient(queueUrl)
  const currentFenceToken = await client.get(redisQueueFenceKey())
  if (currentFenceToken !== fenceToken) {
    throw new Error('Redis queue fence token 不匹配，拒绝启动排空消费者')
  }

  const preflightSnapshot = await inspectRedisStreamDrain(client)
  assertRequiredConsumerGroupsPresent(preflightSnapshot)

  const tracker = new RedisStreamDrainStabilityTracker(requiredStableWindows)
  const deadline = Date.now() + timeoutMs
  startConsumers()
  try {
    while (!interrupted && Date.now() < deadline) {
      const snapshot = await inspectRedisStreamDrain(client)
      console.log(JSON.stringify({ event: 'redis_stream_drain_snapshot', ...snapshot }))
      if (tracker.observe(snapshot)) {
        console.log(JSON.stringify({
          event: 'redis_stream_drain_completed',
          checkedAt: snapshot.checkedAt,
          stableWindows: requiredStableWindows,
          xaddCalls: snapshot.xaddCalls
        }))
        return
      }
      await delay(pollIntervalMs)
    }
    if (interrupted) {
      throw new Error('Redis Stream 排空被信号中断')
    }
    throw new Error(`Redis Stream 排空超时（${timeoutMs}ms）`)
  } finally {
    await stopConsumers()
    await closeRedisClients()
  }
}

function assertRequiredConsumerGroupsPresent(
  snapshot: Awaited<ReturnType<typeof inspectRedisStreamDrain>>
): void {
  const missing = redisStreamDrainContracts.filter((contract) => {
    const stream = snapshot.streams.find((candidate) => candidate.streamKey === contract.streamKey)
    return !stream?.groups.some((group) => group.name === contract.groupName)
  })
  if (missing.length > 0) {
    throw new Error(`Redis Stream 排空缺少既有 consumer group：${missing.map((contract) => `${contract.name}:${contract.groupName}`).join(', ')}`)
  }
}

function assertDrainRuntime(): void {
  if (runtimeConfig.runtimeMode !== 'performance') {
    throw new Error('Redis Stream 排空仅允许 performance 模式')
  }
  if (runtimeConfig.processRole !== 'worker') {
    throw new Error('Redis Stream 排空必须使用 JUHE_AI_PROCESS_ROLE=worker')
  }
  if (runtimeConfig.workerRole !== 'ingest-worker') {
    throw new Error('Redis Stream 排空必须使用 JUHE_AI_WORKER_ROLE=ingest-worker')
  }
  if (runtimeConfig.queueDriver !== 'redis_stream') {
    throw new Error('Redis Stream 排空必须使用 JUHE_AI_QUEUE_DRIVER=redis_stream')
  }
  if (!runtimeConfig.redis.queueUrl) {
    throw new Error('Redis Stream 排空缺少 JUHE_AI_REDIS_QUEUE_URL')
  }
}

function startConsumers(): void {
  startUsageRecordRedisStreamConsumer()
  startAuditLogRedisStreamConsumer()
  startOperationLogRedisStreamConsumer()
  startPublicApiLogRedisStreamConsumer()
  startRuntimeLogRedisStreamConsumer()
  startRecordMaintenanceRedisStreamConsumer()
}

async function stopConsumers(): Promise<void> {
  await Promise.allSettled([
    stopUsageRecordRedisStreamConsumer(),
    stopAuditLogRedisStreamConsumer(),
    stopOperationLogRedisStreamConsumer(),
    stopPublicApiLogRedisStreamConsumer(),
    stopRuntimeLogRedisStreamConsumer(),
    stopRecordMaintenanceRedisStreamConsumer()
  ])
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 不能为空`)
  return value
}

function boundedIntegerEnv(name: string, fallback: number, min: number, max: number): number {
  const rawValue = process.env[name]?.trim()
  if (!rawValue) return fallback
  const value = Number(rawValue)
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`${name} 必须是 ${min}-${max} 的整数`)
  }
  return value
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref?.()
  })
}
