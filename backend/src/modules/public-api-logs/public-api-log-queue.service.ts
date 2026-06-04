import { errorLogFields, logger } from '../../shared/logger.js'
import { createPublicApiLog, type PublicApiLogInput } from '../../storage/public-api-logs.repository.js'

const publicApiLogQueueMaxSize = 5000
const publicApiLogFlushBatchSize = 50
const publicApiLogDropWarnInterval = 100

const publicApiLogQueue: PublicApiLogInput[] = []
let flushScheduled = false
let droppedPublicApiLogCount = 0

export function enqueuePublicApiLog(input: PublicApiLogInput): boolean {
  if (publicApiLogQueue.length >= publicApiLogQueueMaxSize) {
    droppedPublicApiLogCount += 1
    if (droppedPublicApiLogCount === 1 || droppedPublicApiLogCount % publicApiLogDropWarnInterval === 0) {
      logger.warn({
        event: 'public_api_log_queue_overflow',
        queueSize: publicApiLogQueue.length,
        droppedPublicApiLogCount,
        traceId: input.traceId,
        path: input.path
      }, '公开接口日志队列已满，丢弃日志记录')
    }
    return false
  }

  publicApiLogQueue.push(input)
  schedulePublicApiLogFlush()
  return true
}

export function flushPublicApiLogQueueForTest(): void {
  while (publicApiLogQueue.length > 0) {
    flushPublicApiLogQueueBatch()
  }
  flushScheduled = false
}

function schedulePublicApiLogFlush(): void {
  if (flushScheduled) {
    return
  }
  flushScheduled = true
  setImmediate(flushPublicApiLogQueue)
}

function flushPublicApiLogQueue(): void {
  flushScheduled = false
  flushPublicApiLogQueueBatch()
  if (publicApiLogQueue.length > 0) {
    schedulePublicApiLogFlush()
  }
}

function flushPublicApiLogQueueBatch(): void {
  const batch = publicApiLogQueue.splice(0, publicApiLogFlushBatchSize)
  for (const input of batch) {
    try {
      createPublicApiLog(input)
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'public_api_log_write_failed',
        method: input.method,
        path: input.path,
        statusCode: input.statusCode,
        traceId: input.traceId
      }), '公开接口日志写入失败')
    }
  }
}
