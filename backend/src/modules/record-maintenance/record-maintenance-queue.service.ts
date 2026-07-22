import { fork, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import { RedisStreamQueue, type RedisStreamMessage, type RedisStreamQueueRuntime } from '../../shared/redis-stream-queue.js'
import { redisStreamQueueContracts } from '../../shared/redis-stream-drain.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { forwardSupervisorOutput } from '../../shared/supervisor-output.js'
import {
  cleanupNonBusinessDataBeforeWithResult,
  cleanupProcessedUsageRecordsBeforeWithResultAsync,
  type NonBusinessDataHardCleanupResult
} from '../../storage/data-retention.repository.js'
import { cleanupAuditLogsByRetentionAsync } from '../../storage/audit-logs.repository.js'
import { newId, nowIso, usageCatalogDatabasePath } from '../../storage/database.js'
import {
  createBackgroundTaskRun,
  createBackgroundTaskRunAsync,
  cleanupDeletedAccountRelatedRecordDataAsync,
  cleanupDeletedApiKeyRelatedRecordDataAsync,
  type AccountUsageSnapshotUpsertInput
} from '../../storage/repositories.js'
import { requestBackgroundWorkerDbService, sendRecordMaintenanceJobsToWorker } from '../background/background-ipc.js'
import { requestStatsWriter, type BackgroundStatsWriteOperation } from '../background/background-stats-writer.js'
import type { BackgroundWorkerMessage } from '../background/background-ipc.types.js'
import { auditSuccessRetentionCutoffIso } from '../audit-logs/audit-log-retention-policy.js'

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const temporaryMaintenanceWorkerSourcePath = resolve(sourceRoot, 'temporary-maintenance-worker.ts')
const temporaryMaintenanceWorkerDistPath = resolve(sourceRoot, 'temporary-maintenance-worker.js')

export type RecordMaintenanceJob =
  | {
    type: 'api_key_related_cleanup'
    id?: string
    apiKeyId: string
    systemAccountId: string
    createdAt?: string
  }
  | {
    type: 'account_related_cleanup'
    id?: string
    accountId: string
    systemAccountId: string
    relatedAccountIds?: string[]
    authorizationIds?: string[]
    teamScopeIds?: string[]
    createdAt?: string
  }
  | {
    type: 'usage_records_cleanup'
    id?: string
    cutoffAt: string
    batchSize: number
    maxBatches: number
    createdAt?: string
  }
  | {
    type: 'non_business_data_cleanup'
    id?: string
    cutoffAt: string
    batchSize: number
    maxBatches: number
    createdAt?: string
  }
  | {
    type: 'audit_retained_data_cleanup'
    id?: string
    nowAt: string
    successHotRetentionHours: number
    successRetentionDays: number
    failureRetentionDays: number
    errorGroupRetentionDays: number
    successSampleBucketThreshold: number
    batchSize: number
    maxBatches: number
    createdAt?: string
  }
  | {
    type: 'account_usage_snapshot_upsert'
    id?: string
    accountId: string
    kind: 'openai_codex'
    source?: string
    snapshot: Record<string, unknown>
    updatedAt?: string
    createdAt?: string
  }

const recordMaintenanceFlushIntervalMs = 100
const recordMaintenanceRetryPolicy = fixedRetryPolicy('record_maintenance_queue_flush', 1000)
const recordMaintenanceBatchSize = 10
const recordMaintenanceShutdownFlushMaxBatches = 1
const recordMaintenanceQueueMaxItems = 5_000
const recordMaintenanceQueueMaxBytes = 32 * 1024 * 1024
const recordMaintenanceRedisStreamKey = redisStreamQueueContracts.recordMaintenance.streamKey
const recordMaintenanceRedisStreamGroup = redisStreamQueueContracts.recordMaintenance.groupName
const recordMaintenanceRedisConsumerErrorRetryMs = 1000
const recordMaintenanceRedisStopWaitMs = 2000
const auditRetainedDataCleanupBatchPauseMs = 10
const auditRetainedDataCleanupBatchSizeLimit = 100
const auditRetainedDataCleanupMaxBatchesLimit = 3
const minimumUsageRecordCleanupAgeMs = 24 * 60 * 60 * 1000

export interface RecordMaintenanceEnqueueResult {
  job: RecordMaintenanceJob
  queued: boolean
  droppedReason?: string
}

interface QueuedRecordMaintenanceJob {
  job: RecordMaintenanceJob
  bytes: number
}

let pendingJobs: QueuedRecordMaintenanceJob[] = []
let pendingJobBytes = 0
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let completedCount = 0
let flushFailureCount = 0
let retainedOverflowWarningCount = 0
let droppedDispatchCount = 0
let droppedOverflowCount = 0
let droppedOversizeCount = 0
let shutdownHooksInstalled = false
let recordMaintenanceRedisStreamQueueInstance: RedisStreamQueue<RecordMaintenanceJob> | undefined
let recordMaintenanceRedisConsumerStarted = false
let recordMaintenanceRedisConsumerStopping = false
let recordMaintenanceRedisConsumerPromise: Promise<void> | undefined

interface RecordMaintenanceFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueRecordMaintenanceJob(input: RecordMaintenanceJob): RecordMaintenanceJob {
  return enqueueRecordMaintenanceJobWithResult(input).job
}

export async function enqueueRecordMaintenanceJobAsync(input: RecordMaintenanceJob): Promise<RecordMaintenanceJob> {
  const job = normalizeRecordMaintenanceJob(input)
  if (shouldEnqueueRecordMaintenanceJobToRedisStream(job)) {
    await enqueueRecordMaintenanceJobToRedisStream(job)
    return job
  }
  return enqueueRecordMaintenanceJobWithResult(job).job
}

export async function enqueueRecordMaintenanceJobWithResultAsync(input: RecordMaintenanceJob): Promise<RecordMaintenanceEnqueueResult> {
  const job = normalizeRecordMaintenanceJob(input)
  if (shouldEnqueueRecordMaintenanceJobToRedisStream(job)) {
    try {
      await enqueueRecordMaintenanceJobToRedisStream(job)
      return { job, queued: true }
    } catch {
      return { job, queued: false, droppedReason: 'redis_stream_enqueue_failed' }
    }
  }
  return enqueueRecordMaintenanceJobWithResult(job)
}

export function enqueueRecordMaintenanceJobWithResult(input: RecordMaintenanceJob): RecordMaintenanceEnqueueResult {
  const job = normalizeRecordMaintenanceJob(input)
  if (shouldEnqueueRecordMaintenanceJobToRedisStream(job)) {
    return { job, queued: false, droppedReason: 'redis_stream_async_required' }
  }
  if (runtimeConfig.processRole === 'server') {
    const queued = sendRecordMaintenanceJobsToWorker([job])
    if (!queued) {
      recordRecordMaintenanceDispatchFailure(new Error('后台 worker IPC 不可用'), job)
    }
    return {
      job,
      queued,
      droppedReason: queued ? undefined : 'worker_dispatch_failed'
    }
  }

  if (runtimeConfig.processRole === 'db-service') {
    if (process.send && process.connected !== false) {
      try {
        process.send({
          type: 'background_worker_record_maintenance',
          items: [job]
        }, (error) => {
          if (error) {
            recordRecordMaintenanceDispatchFailure(error, job)
          }
        })
        return { job, queued: true }
      } catch (error) {
        recordRecordMaintenanceDispatchFailure(error, job)
        return { job, queued: false, droppedReason: 'worker_dispatch_failed' }
      }
    }
    recordRecordMaintenanceDispatchFailure(new Error('DB service 无父进程 IPC'), job)
    return { job, queued: false, droppedReason: 'worker_ipc_unavailable' }
  }

  if (runtimeConfig.processRole === 'worker'
    && runtimeConfig.workerRole !== 'ingest-worker'
    && !canProcessRecordMaintenanceJobLocally(job)) {
    if (process.send && process.connected !== false) {
      try {
        process.send({
          type: 'background_worker_record_maintenance',
          items: [job]
        }, (error) => {
          if (error) {
            recordRecordMaintenanceDispatchFailure(error, job)
          }
        })
        return { job, queued: true }
      } catch (error) {
        recordRecordMaintenanceDispatchFailure(error, job)
        return { job, queued: false, droppedReason: 'worker_dispatch_failed' }
      }
    }
    recordRecordMaintenanceDispatchFailure(new Error('非 ingest worker 无父进程 IPC'), job)
    return { job, queued: false, droppedReason: 'worker_ipc_unavailable' }
  }

  const queued = enqueueRecordMaintenanceJobLocal(job)
  return {
    job,
    queued,
    droppedReason: queued ? undefined : 'worker_local_queue_full'
  }
}

export function enqueueRecordMaintenanceJobsLocal(inputs: RecordMaintenanceJob[]): void {
  assertLocalRecordMaintenanceJobsAllowed('enqueueRecordMaintenanceJobsLocal', inputs)
  for (const input of inputs) {
    enqueueRecordMaintenanceJobLocal(normalizeRecordMaintenanceJob(input))
  }
}

export function startRecordMaintenanceRedisStreamConsumer(): void {
  if (!shouldUseRedisStreamRecordMaintenanceQueue() || !isRecordMaintenanceIngestWorker() || recordMaintenanceRedisConsumerStarted) {
    return
  }
  recordMaintenanceRedisConsumerStarted = true
  recordMaintenanceRedisConsumerStopping = false
  recordMaintenanceRedisConsumerPromise = runRecordMaintenanceRedisStreamConsumer().catch((error) => {
    logger.error(errorLogFields(error, {
      event: 'record_maintenance_redis_stream_consumer_stopped'
    }), 'Redis Stream 数据维护消费循环异常退出')
  }).finally(() => {
    recordMaintenanceRedisConsumerStarted = false
    recordMaintenanceRedisConsumerPromise = undefined
  })
}

export async function stopRecordMaintenanceRedisStreamConsumer(): Promise<void> {
  recordMaintenanceRedisConsumerStopping = true
  const queue = recordMaintenanceRedisStreamQueueInstance
  if (queue) {
    await queue.closeConsumer().catch(() => undefined)
  }
  if (recordMaintenanceRedisConsumerPromise) {
    await Promise.race([
      recordMaintenanceRedisConsumerPromise.catch(() => undefined),
      delay(recordMaintenanceRedisStopWaitMs)
    ])
  }
}

export async function flushRecordMaintenanceQueue(options: RecordMaintenanceFlushOptions = {}): Promise<void> {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  if (flushing || pendingJobs.length === 0) {
    return
  }

  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }

  flushing = true
  let failed = false
  let shouldRetry = false
  let flushedBatches = 0
  const maxBatches = normalizeMaxBatches(options.maxBatches)
  try {
    do {
      const batch = pendingJobs.slice(0, recordMaintenanceBatchSize)
      if (batch.length === 0) {
        break
      }
      const batchJobs = batch.map((item) => item.job)
      flushedBatches += 1

      for (let index = 0; index < batchJobs.length; index += 1) {
        const job = batchJobs[index]
        const snapshotJobs = collectAccountUsageSnapshotJobs(batchJobs, index)
        try {
          if (snapshotJobs.length > 0) {
            await processAccountUsageSnapshotUpsertJobs(snapshotJobs)
            removeRecordMaintenanceJobsFromHead(snapshotJobs.length)
            completedCount += snapshotJobs.length
            index += snapshotJobs.length - 1
          } else {
            await processRecordMaintenanceJob(job)
            removeRecordMaintenanceJobsFromHead(1)
            completedCount += 1
          }
        } catch (error) {
          failed = true
          flushFailureCount += 1
          logger.error(errorLogFields(error, {
            event: 'record_maintenance_queue_flush_failed',
            jobType: job.type,
            jobId: job.id,
            pendingCount: pendingJobs.length,
            pendingBytes: pendingJobBytes,
            flushFailureCount
          }), '数据维护队列执行失败，已保留任务等待重试')
          shouldRetry = options.retryOnFailure !== false
          return
        }
      }
    } while (options.drain && pendingJobs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
    if (pendingJobs.length > 0 && (!failed || shouldRetry)) {
      scheduleRecordMaintenanceFlush(shouldRetry ? retryDelayMs(recordMaintenanceRetryPolicy) : 0)
    }
  }
}

export async function flushAllRecordMaintenanceQueue(): Promise<void> {
  await flushRecordMaintenanceQueue({ drain: true, retryOnFailure: false })
}

export async function flushRecordMaintenanceQueueForShutdown(): Promise<void> {
  await flushRecordMaintenanceQueue({ drain: true, retryOnFailure: false, maxBatches: recordMaintenanceShutdownFlushMaxBatches })
}

export function getRecordMaintenanceQueueRuntime(): {
  queueLength: number
  queueBytes: number
  droppedCount: number
  completedCount: number
  retainedOverflowWarningCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingJobs.length,
    queueBytes: pendingJobBytes,
    droppedCount: droppedDispatchCount + droppedOverflowCount + droppedOversizeCount,
    completedCount,
    retainedOverflowWarningCount,
    droppedOverflowCount,
    droppedOversizeCount,
    flushFailureCount
  }
}

export function installRecordMaintenanceQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', () => {
    void flushRecordMaintenanceQueueForShutdown()
  })
}

function enqueueRecordMaintenanceJobLocal(job: RecordMaintenanceJob): boolean {
  assertLocalRecordMaintenanceJobAllowed('enqueueRecordMaintenanceJobLocal', job)
  const queued = {
    job,
    bytes: estimateRecordMaintenanceJobBytes(job)
  }
  if (queued.bytes > recordMaintenanceQueueMaxBytes) {
    recordRecordMaintenanceLocalDrop(queued, 'oversize')
    return false
  }
  const mergeResult = mergeAccountUsageSnapshotJob(queued)
  if (mergeResult !== 'not_found') {
    if (mergeResult === 'queued') {
      scheduleRecordMaintenanceFlush(pendingJobs.length >= recordMaintenanceBatchSize ? 0 : recordMaintenanceFlushIntervalMs)
      return true
    }
    return false
  }
  if (pendingJobs.length >= recordMaintenanceQueueMaxItems || pendingJobBytes + queued.bytes > recordMaintenanceQueueMaxBytes) {
    recordRecordMaintenanceLocalDrop(queued, 'overflow')
    return false
  }
  pendingJobs.push(queued)
  pendingJobBytes += queued.bytes
  scheduleRecordMaintenanceFlush(pendingJobs.length >= recordMaintenanceBatchSize ? 0 : recordMaintenanceFlushIntervalMs)
  return true
}

async function enqueueRecordMaintenanceJobToRedisStream(job: RecordMaintenanceJob): Promise<void> {
  try {
    await recordMaintenanceRedisStreamQueue().enqueue(job)
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'record_maintenance_redis_stream_enqueue_failed',
      jobId: job.id,
      jobType: job.type
    }), '数据维护任务写入 Redis Stream 失败，高性能模式禁止回退 IPC 或本地队列')
    throw error
  }
}

function sendRecordMaintenanceJobToParent(job: RecordMaintenanceJob): boolean {
  if (!process.send || process.connected === false) {
    return false
  }
  try {
    process.send({
      type: 'background_worker_record_maintenance',
      items: [job]
    }, (error) => {
      if (error) {
        recordRecordMaintenanceDispatchFailure(error, job)
      }
    })
    return true
  } catch (error) {
    recordRecordMaintenanceDispatchFailure(error, job)
    return false
  }
}

async function runRecordMaintenanceRedisStreamConsumer(): Promise<void> {
  const queue = recordMaintenanceRedisStreamQueue()
  while (!recordMaintenanceRedisConsumerStopping) {
    try {
      const claimed = await queue.claimPending()
      const messages = claimed.length > 0 ? claimed : await queue.readNew()
      if (messages.length === 0) {
        continue
      }
      await flushRecordMaintenanceRedisStreamMessages(queue, messages)
    } catch (error) {
      if (recordMaintenanceRedisConsumerStopping) {
        break
      }
      flushFailureCount += 1
      logger.error(errorLogFields(error, {
        event: 'record_maintenance_redis_stream_consume_failed',
        flushFailureCount
      }), 'Redis Stream 数据维护消费失败，稍后重试')
      await delay(recordMaintenanceRedisConsumerErrorRetryMs)
    }
  }
}

async function flushRecordMaintenanceRedisStreamMessages(
  queue: RedisStreamQueue<RecordMaintenanceJob>,
  messages: Array<RedisStreamMessage<RecordMaintenanceJob>>
): Promise<void> {
  for (let index = 0; index < messages.length; index += 1) {
    const message = messages[index]
    const job = normalizeRecordMaintenanceStreamJob(message.payload)
    const snapshotMessages = collectAccountUsageSnapshotStreamMessages(messages, index)
    try {
      if (snapshotMessages.length > 0) {
        await processAccountUsageSnapshotUpsertJobs(snapshotMessages.map((item) => normalizeRecordMaintenanceStreamJob(item.payload) as AccountUsageSnapshotUpsertJob))
        completedCount += snapshotMessages.length
        await queue.ack(snapshotMessages.map((item) => item.id))
        index += snapshotMessages.length - 1
      } else {
        await processRecordMaintenanceJob(job)
        completedCount += 1
        await queue.ack([message.id])
      }
    } catch (error) {
      flushFailureCount += 1
      logger.error(errorLogFields(error, {
        event: 'record_maintenance_redis_stream_flush_failed',
        jobType: job.type,
        jobId: job.id,
        messageId: message.id,
        flushFailureCount
      }), 'Redis Stream 数据维护任务执行失败，消息保持 pending 等待重投')
      break
    }
  }
}

function collectAccountUsageSnapshotStreamMessages(
  messages: Array<RedisStreamMessage<RecordMaintenanceJob>>,
  startIndex: number
): Array<RedisStreamMessage<RecordMaintenanceJob>> {
  const output: Array<RedisStreamMessage<RecordMaintenanceJob>> = []
  for (let index = startIndex; index < messages.length; index += 1) {
    const message = messages[index]
    const job = normalizeRecordMaintenanceStreamJob(message.payload)
    if (job.type !== 'account_usage_snapshot_upsert') break
    output.push(message)
  }
  return output
}

function normalizeRecordMaintenanceStreamJob(payload: RecordMaintenanceJob): RecordMaintenanceJob {
  if (!isRecordMaintenanceJob(payload)) {
    throw new Error('Redis Stream 数据维护消息格式无效')
  }
  return normalizeRecordMaintenanceJob(payload)
}

function recordMaintenanceRedisStreamQueue(): RedisStreamQueue<RecordMaintenanceJob> {
  if (!recordMaintenanceRedisStreamQueueInstance) {
    recordMaintenanceRedisStreamQueueInstance = new RedisStreamQueue<RecordMaintenanceJob>({
      streamKey: recordMaintenanceRedisStreamKey,
      groupName: recordMaintenanceRedisStreamGroup,
      readCount: recordMaintenanceBatchSize
    })
  }
  return recordMaintenanceRedisStreamQueueInstance
}

export async function getRecordMaintenanceRedisStreamRuntime(): Promise<RedisStreamQueueRuntime | undefined> {
  if (!shouldUseRedisStreamRecordMaintenanceQueue()) return undefined
  return await recordMaintenanceRedisStreamQueue().inspectRuntime()
}

async function processRecordMaintenanceJob(job: RecordMaintenanceJob): Promise<void> {
  if (isTemporaryRecordMaintenanceJob(job)) {
    const input = {
      jobName: `record-maintenance:${job.type}`,
      jobType: job.type,
      workerRole: 'temporary-maintenance-worker',
      leaseKey: `record-maintenance:${job.type}`,
      params: { job }
    }
    const run = runtimeConfig.databaseDriver === 'postgres'
      ? await createBackgroundTaskRunAsync(input)
      : createBackgroundTaskRun(input)
    await spawnTemporaryMaintenanceWorker(run.runId, job)
    logger.info({
      event: 'record_maintenance_temporary_worker_completed',
      jobType: job.type,
      jobId: job.id,
      runId: run.runId
    }, '数据维护任务临时 worker 已执行完成')
    return
  }
  await runRecordMaintenanceJobOnce(job)
}

export async function runRecordMaintenanceJobOnce(job: RecordMaintenanceJob): Promise<Record<string, unknown>> {
  switch (job.type) {
    case 'api_key_related_cleanup': {
      const result = await cleanupDeletedApiKeyRelatedRecordDataAsync({
        apiKeyId: job.apiKeyId,
        systemAccountId: job.systemAccountId
      }, runtimeConfig.databaseDriver === 'postgres' ? undefined : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_api_key_record_stats', input })
        })
      const deferred = result.hasMore || Boolean(result.blockedReason)
      logger.info({
        event: deferred ? 'record_maintenance_api_key_cleanup_deferred' : 'record_maintenance_api_key_cleanup_completed',
        jobId: job.id,
        ...result
      }, deferred ? 'API Key 关联数据清理等待统计游标追平' : 'API Key 关联数据清理完成')
      return result as unknown as Record<string, unknown>
    }
    case 'account_related_cleanup': {
      const result = await cleanupDeletedAccountRelatedRecordDataAsync({
        accountId: job.accountId,
        systemAccountId: job.systemAccountId,
        relatedAccountIds: job.relatedAccountIds,
        authorizationIds: job.authorizationIds,
        teamScopeIds: job.teamScopeIds
      }, runtimeConfig.databaseDriver === 'postgres' ? undefined : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_account_record_stats', input })
        })
      const deferred = result.hasMore || Boolean(result.blockedReason)
      logger.info({
        event: deferred ? 'record_maintenance_account_cleanup_deferred' : 'record_maintenance_account_cleanup_completed',
        jobId: job.id,
        ...result
      }, deferred ? 'AI 账户关联数据清理等待统计游标追平' : 'AI 账户关联数据清理完成')
      return result as unknown as Record<string, unknown>
    }
    case 'usage_records_cleanup': {
      const result = await cleanupUsageRecordsBefore({
        cutoffAt: job.cutoffAt,
        batchSize: job.batchSize,
        maxBatches: job.maxBatches
      })
      logger.info({
        event: 'record_maintenance_usage_records_cleanup_completed',
        jobId: job.id,
        ...result
      }, '使用记录后台清理完成')
      return result
    }
    case 'non_business_data_cleanup': {
      const result = await cleanupNonBusinessDataBefore({
        cutoffAt: job.cutoffAt,
        batchSize: job.batchSize,
        maxBatches: job.maxBatches
      })
      logger.info({
        event: 'record_maintenance_non_business_data_cleanup_completed',
        jobId: job.id,
        ...result
      }, '非业务数据后台硬清理完成')
      return result as unknown as Record<string, unknown>
    }
    case 'audit_retained_data_cleanup': {
      const result = await cleanupAuditRetainedData(job)
      logger.info({
        event: 'record_maintenance_audit_retained_data_cleanup_completed',
        jobId: job.id,
        ...result
      }, '审计日志保留后台清理完成')
      return result
    }
    case 'account_usage_snapshot_upsert':
      await processAccountUsageSnapshotUpsertJobs([job])
      return { upsertedCount: 1 }
    default:
      assertNever(job)
  }
}

function spawnTemporaryMaintenanceWorker(runId: string, job: RecordMaintenanceJob): Promise<void> {
  const entry = resolveTemporaryMaintenanceWorkerEntry()
  const child = fork(entry.modulePath, [runId], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'worker',
      JUHE_AI_WORKER_ROLE: 'temporary-maintenance-worker',
      JUHE_AI_RUNTIME_MODE: runtimeConfig.runtimeMode,
      JUHE_AI_DATABASE_DRIVER: runtimeConfig.databaseDriver,
      JUHE_AI_CACHE_DRIVER: runtimeConfig.cacheDriver,
      JUHE_AI_RUNTIME_STATE_DRIVER: runtimeConfig.runtimeStateDriver,
      JUHE_AI_QUEUE_DRIVER: runtimeConfig.queueDriver,
      JUHE_AI_POSTGRES_URL: runtimeConfig.postgres.url,
      JUHE_AI_REDIS_CACHE_URL: runtimeConfig.redis.cacheUrl,
      JUHE_AI_REDIS_STATE_URL: runtimeConfig.redis.stateUrl,
      JUHE_AI_REDIS_QUEUE_URL: runtimeConfig.redis.queueUrl,
      JUHE_AI_REDIS_NAMESPACE: runtimeConfig.redis.namespace,
      JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
      JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
      JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath(),
      JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot
    },
    execArgv: entry.execArgv,
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })
  child.stdout?.on('data', (chunk: Buffer) => forwardSupervisorOutput(process.stdout, chunk))
  child.stderr?.on('data', (chunk: Buffer) => forwardSupervisorOutput(process.stderr, chunk))
  child.on('message', (message: unknown) => {
    void handleTemporaryMaintenanceWorkerMessage(child, message, runId, job)
  })
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false
    const settle = (error?: Error): void => {
      if (settled) return
      settled = true
      if (error) rejectPromise(error)
      else resolvePromise()
    }
    child.once('error', (error) => {
      logger.error(errorLogFields(error, {
        event: 'temporary_maintenance_worker_spawn_failed',
        runId,
        jobType: job.type,
        jobId: job.id
      }), '临时维护 worker 启动失败')
      settle(error instanceof Error ? error : new Error(String(error)))
    })
    child.once('exit', (code, signal) => {
      logger.info({
        event: 'temporary_maintenance_worker_exited',
        runId,
        jobType: job.type,
        jobId: job.id,
        pid: child.pid,
        code,
        signal
      }, '临时维护 worker 已退出')
      if (code === 0) {
        settle()
        return
      }
      settle(new Error(`临时维护 worker 执行失败：runId=${runId}, code=${code ?? 'null'}, signal=${signal ?? 'null'}`))
    })
  })
}

async function handleTemporaryMaintenanceWorkerMessage(
  child: ChildProcess,
  message: unknown,
  runId: string,
  job: RecordMaintenanceJob
): Promise<void> {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }
  const record = message as Partial<BackgroundWorkerMessage> & Record<string, unknown>
  if (record.type === 'background_worker_stats_write_request' && typeof record.requestId === 'string' && isIpcOperation(record.operation)) {
    await respondToTemporaryMaintenanceStatsWriteRequest(child, record.requestId, record.operation as BackgroundStatsWriteOperation, runId, job)
    return
  }
  if (record.type === 'background_worker_db_service_request' && typeof record.requestId === 'string' && isIpcOperation(record.operation)) {
    await respondToTemporaryMaintenanceDbServiceRequest(child, record.requestId, record.operation as Parameters<typeof requestBackgroundWorkerDbService>[0], runId, job)
  }
}

async function respondToTemporaryMaintenanceStatsWriteRequest(
  child: ChildProcess,
  requestId: string,
  operation: BackgroundStatsWriteOperation,
  runId: string,
  job: RecordMaintenanceJob
): Promise<void> {
  try {
    const result = await requestStatsWriter(operation)
    sendTemporaryMaintenanceWorkerMessage(child, {
      type: 'background_worker_stats_write_response',
      requestId,
      ok: true,
      result
    } as BackgroundWorkerMessage, runId, job, operation.type)
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'temporary_maintenance_worker_stats_write_failed',
      runId,
      jobType: job.type,
      jobId: job.id,
      operationType: operation.type
    }), '临时维护 worker stats-writer 请求失败')
    sendTemporaryMaintenanceWorkerMessage(child, {
      type: 'background_worker_stats_write_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, runId, job, operation.type)
  }
}

async function respondToTemporaryMaintenanceDbServiceRequest(
  child: ChildProcess,
  requestId: string,
  operation: Parameters<typeof requestBackgroundWorkerDbService>[0],
  runId: string,
  job: RecordMaintenanceJob
): Promise<void> {
  try {
    const result = await requestBackgroundWorkerDbService(operation)
    if (result === undefined) {
      throw new Error(`DB service 不可用，无法执行临时维护操作：${operation.type}`)
    }
    sendTemporaryMaintenanceWorkerMessage(child, {
      type: 'background_worker_db_service_response',
      requestId,
      ok: true,
      result
    }, runId, job, operation.type)
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'temporary_maintenance_worker_db_service_failed',
      runId,
      jobType: job.type,
      jobId: job.id,
      operationType: operation.type
    }), '临时维护 worker DB service 请求失败')
    sendTemporaryMaintenanceWorkerMessage(child, {
      type: 'background_worker_db_service_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, runId, job, operation.type)
  }
}

function sendTemporaryMaintenanceWorkerMessage(
  child: ChildProcess,
  message: BackgroundWorkerMessage,
  runId: string,
  job: RecordMaintenanceJob,
  operationType: string
): void {
  if (!child.connected) {
    logger.warn({
      event: 'temporary_maintenance_worker_ipc_disconnected',
      runId,
      jobType: job.type,
      jobId: job.id,
      operationType,
      responseType: message.type
    }, '临时维护 worker IPC 已断开，无法返回请求结果')
    return
  }
  try {
    child.send(message, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'temporary_maintenance_worker_ipc_send_failed',
          runId,
          jobType: job.type,
          jobId: job.id,
          operationType,
          responseType: message.type
        }), '临时维护 worker IPC 响应发送失败')
      }
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'temporary_maintenance_worker_ipc_send_failed',
      runId,
      jobType: job.type,
      jobId: job.id,
      operationType,
      responseType: message.type
    }), '临时维护 worker IPC 响应发送失败')
  }
}

function isIpcOperation(value: unknown): value is { type: string } & Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  return typeof (value as Record<string, unknown>).type === 'string'
}

function resolveTemporaryMaintenanceWorkerEntry(): { modulePath: string; execArgv: string[] } {
  if (existsSync(temporaryMaintenanceWorkerDistPath)) {
    return {
      modulePath: temporaryMaintenanceWorkerDistPath,
      execArgv: []
    }
  }
  return {
    modulePath: temporaryMaintenanceWorkerSourcePath,
    execArgv: process.execArgv.some((arg) => arg.includes('tsx'))
      ? process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
      : ['--import', 'tsx']
  }
}

function isTemporaryRecordMaintenanceJob(job: RecordMaintenanceJob): boolean {
  return job.type === 'usage_records_cleanup' || job.type === 'non_business_data_cleanup' || job.type === 'audit_retained_data_cleanup'
}

type AccountUsageSnapshotUpsertJob = Extract<RecordMaintenanceJob, { type: 'account_usage_snapshot_upsert' }>

function collectAccountUsageSnapshotJobs(batch: RecordMaintenanceJob[], startIndex: number): AccountUsageSnapshotUpsertJob[] {
  const output: AccountUsageSnapshotUpsertJob[] = []
  for (let index = startIndex; index < batch.length; index += 1) {
    const job = batch[index]
    if (job.type !== 'account_usage_snapshot_upsert') break
    output.push(job)
  }
  return output
}

async function processAccountUsageSnapshotUpsertJobs(jobs: AccountUsageSnapshotUpsertJob[]): Promise<void> {
  const inputs: AccountUsageSnapshotUpsertInput[] = jobs.map((job) => ({
    accountId: job.accountId,
    kind: job.kind,
    source: job.source,
    snapshot: job.snapshot,
    updatedAt: job.updatedAt
  }))
  await requestStatsWriter({ type: 'upsert_account_usage_snapshots', inputs })
  logger.info({
    event: 'record_maintenance_account_usage_snapshots_upserted',
    jobCount: jobs.length,
    jobIds: jobs.map((job) => job.id),
    accountIds: jobs.map((job) => job.accountId)
  }, '账号用量快照后台批量写入完成')
}

async function cleanupUsageRecordsBefore(input: { cutoffAt: string; batchSize: number; maxBatches: number }): Promise<{
  cutoffAt: string
  deletedRows: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  blockedReason?: string
}> {
  let deletedRows = 0
  let batches = 0
  let hasMore = false
  let blockedReason: string | undefined
  const cutoffTime = Date.parse(input.cutoffAt)
  if (Number.isNaN(cutoffTime)) {
    return {
      cutoffAt: input.cutoffAt,
      deletedRows: 0,
      batches: 0,
      batchSize: input.batchSize,
      maxBatches: input.maxBatches,
      hasMore: false,
      blockedReason: '使用记录清理截止时间无效'
    }
  }
  if (cutoffTime > Date.now() - minimumUsageRecordCleanupAgeMs) {
    return {
      cutoffAt: input.cutoffAt,
      deletedRows: 0,
      batches: 0,
      batchSize: input.batchSize,
      maxBatches: input.maxBatches,
      hasMore: false,
      blockedReason: '不能清理最近 1 天内的使用记录'
    }
  }

  for (let index = 0; index < input.maxBatches; index += 1) {
    const batch = await cleanupProcessedUsageRecordsBeforeWithResultAsync(input.cutoffAt, input.batchSize)
    deletedRows += batch.deletedRows
    hasMore = batch.hasMore
    blockedReason = batch.blockedReason ?? blockedReason
    const changed = batch.deletedRows > 0 || Number(batch.droppedPartitions ?? 0) > 0
    if (changed) {
      batches += 1
    }
    if (!changed || !batch.hasMore) {
      break
    }
  }


  return {
    cutoffAt: input.cutoffAt,
    deletedRows,
    batches,
    batchSize: input.batchSize,
    maxBatches: input.maxBatches,
    hasMore,
    blockedReason
  }
}

async function cleanupNonBusinessDataBefore(input: { cutoffAt: string; batchSize: number; maxBatches: number }): Promise<NonBusinessDataHardCleanupResult & {
  batches: number
  batchSize: number
  maxBatches: number
}> {
  const cutoffTime = Date.parse(input.cutoffAt)
  const base = {
    cutoffAt: input.cutoffAt,
    deletedRows: 0,
    deletedFiles: 0,
    hasMore: false,
    tableRows: {} as Record<string, number>,
    fileDeletes: {} as Record<string, number>
  }
  if (Number.isNaN(cutoffTime)) {
    return {
      ...base,
      batches: 0,
      batchSize: input.batchSize,
      maxBatches: input.maxBatches
    }
  }

  let batches = 0
  let result: NonBusinessDataHardCleanupResult = base
  for (let index = 0; index < input.maxBatches; index += 1) {
    const batch = await cleanupNonBusinessDataBeforeWithResult({
      cutoffAt: input.cutoffAt,
      limit: input.batchSize,
      scope: 'dataset'
    })
    const statsBatch = await requestStatsWriter({
      type: 'cleanup_non_business_stats_data',
      cutoffAt: input.cutoffAt,
      limit: input.batchSize
    })
    const mergedBatch = mergeNonBusinessCleanupResult(batch, statsBatch)
    result = mergeNonBusinessCleanupResult(result, mergedBatch)
    if (mergedBatch.deletedRows > 0 || mergedBatch.deletedFiles > 0) {
      batches += 1
    }
    if (!mergedBatch.hasMore || (mergedBatch.deletedRows === 0 && mergedBatch.deletedFiles === 0)) {
      break
    }
  }

  return {
    ...result,
    batches,
    batchSize: input.batchSize,
    maxBatches: input.maxBatches
  }
}

async function cleanupAuditRetainedData(input: Extract<RecordMaintenanceJob, { type: 'audit_retained_data_cleanup' }>): Promise<{
  nowAt: string
  auditLogs: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  blockedReason?: string
}> {
  const nowMs = Date.parse(input.nowAt)
  if (Number.isNaN(nowMs)) {
    return {
      nowAt: input.nowAt,
      auditLogs: 0,
      batches: 0,
      batchSize: input.batchSize,
      maxBatches: input.maxBatches,
      hasMore: false,
      blockedReason: '审计清理基准时间无效'
    }
  }
  let auditLogs = 0
  let batches = 0
  let hasMore = false
  const batchSize = Math.min(positiveBatchSize(input.batchSize), auditRetainedDataCleanupBatchSizeLimit)
  const maxBatches = Math.min(normalizeMaxBatches(input.maxBatches), auditRetainedDataCleanupMaxBatchesLimit)
  for (let index = 0; index < maxBatches; index += 1) {
    const deleted = await cleanupAuditLogsByRetentionAsync({
      successHotCutoffCreatedAt: cutoffHoursIso(nowMs, input.successHotRetentionHours),
      successCutoffCreatedAt: auditSuccessRetentionCutoffIso(nowMs, input.successHotRetentionHours, input.successRetentionDays),
      failureCutoffCreatedAt: cutoffDaysIso(nowMs, input.failureRetentionDays),
      errorGroupCutoffUpdatedAt: cutoffDaysIso(nowMs, input.errorGroupRetentionDays),
      successSampleBucketThreshold: input.successSampleBucketThreshold,
      limit: batchSize
    })
    auditLogs += deleted
    hasMore = deleted >= batchSize
    if (deleted > 0) {
      batches += 1
    }
    if (deleted < batchSize) {
      break
    }
    await delay(auditRetainedDataCleanupBatchPauseMs)
  }
  return {
    nowAt: input.nowAt,
    auditLogs,
    batches,
    batchSize,
    maxBatches,
    hasMore
  }
}

function mergeNonBusinessCleanupResult(
  current: NonBusinessDataHardCleanupResult,
  batch: NonBusinessDataHardCleanupResult
): NonBusinessDataHardCleanupResult {
  const tableRows = { ...current.tableRows }
  for (const [key, value] of Object.entries(batch.tableRows)) {
    tableRows[key] = (tableRows[key] ?? 0) + value
  }
  const fileDeletes = { ...current.fileDeletes }
  for (const [key, value] of Object.entries(batch.fileDeletes)) {
    fileDeletes[key] = (fileDeletes[key] ?? 0) + value
  }
  return {
    cutoffAt: batch.cutoffAt,
    deletedRows: current.deletedRows + batch.deletedRows,
    deletedFiles: current.deletedFiles + batch.deletedFiles,
    hasMore: current.hasMore || batch.hasMore,
    tableRows,
    fileDeletes
  }
}

function normalizeRecordMaintenanceJob(input: RecordMaintenanceJob): RecordMaintenanceJob {
  return {
    ...input,
    id: input.id ?? newId('recmaint'),
    createdAt: input.createdAt ?? nowIso()
  }
}

export function isRecordMaintenanceJob(value: unknown): value is RecordMaintenanceJob {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  if (record.type === 'api_key_related_cleanup') {
    return typeof record.apiKeyId === 'string'
      && typeof record.systemAccountId === 'string'
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  if (record.type === 'account_related_cleanup') {
    return typeof record.accountId === 'string'
      && typeof record.systemAccountId === 'string'
      && (record.relatedAccountIds === undefined || isStringArray(record.relatedAccountIds))
      && (record.authorizationIds === undefined || isStringArray(record.authorizationIds))
      && (record.teamScopeIds === undefined || isStringArray(record.teamScopeIds))
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  if (record.type === 'usage_records_cleanup') {
    return typeof record.cutoffAt === 'string'
      && typeof record.batchSize === 'number'
      && Number.isFinite(record.batchSize)
      && typeof record.maxBatches === 'number'
      && Number.isFinite(record.maxBatches)
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  if (record.type === 'non_business_data_cleanup') {
    return typeof record.cutoffAt === 'string'
      && typeof record.batchSize === 'number'
      && Number.isFinite(record.batchSize)
      && typeof record.maxBatches === 'number'
      && Number.isFinite(record.maxBatches)
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  if (record.type === 'audit_retained_data_cleanup') {
    return typeof record.nowAt === 'string'
      && typeof record.successHotRetentionHours === 'number'
      && Number.isFinite(record.successHotRetentionHours)
      && typeof record.successRetentionDays === 'number'
      && Number.isFinite(record.successRetentionDays)
      && typeof record.failureRetentionDays === 'number'
      && Number.isFinite(record.failureRetentionDays)
      && typeof record.errorGroupRetentionDays === 'number'
      && Number.isFinite(record.errorGroupRetentionDays)
      && typeof record.successSampleBucketThreshold === 'number'
      && Number.isFinite(record.successSampleBucketThreshold)
      && typeof record.batchSize === 'number'
      && Number.isFinite(record.batchSize)
      && typeof record.maxBatches === 'number'
      && Number.isFinite(record.maxBatches)
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  if (record.type === 'account_usage_snapshot_upsert') {
    return typeof record.accountId === 'string'
      && record.kind === 'openai_codex'
      && (record.source === undefined || typeof record.source === 'string')
      && typeof record.snapshot === 'object'
      && record.snapshot !== null
      && !Array.isArray(record.snapshot)
      && (record.updatedAt === undefined || typeof record.updatedAt === 'string')
      && (record.id === undefined || typeof record.id === 'string')
      && (record.createdAt === undefined || typeof record.createdAt === 'string')
  }
  return false
}

function scheduleRecordMaintenanceFlush(delayMs: number): void {
  if (runtimeConfig.processRole !== 'worker') {
    return
  }
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    void flushRecordMaintenanceQueue()
  }, delayMs)
  flushTimer.unref()
}

function recordRecordMaintenanceDispatchFailure(error: unknown, job: RecordMaintenanceJob): void {
  droppedDispatchCount += 1
  logger.warn(errorLogFields(error, {
    event: 'record_maintenance_queue_dispatch_failed',
    jobType: job.type,
    jobId: job.id,
    droppedDispatchCount
  }), 'DB service 数据维护任务投递失败，已跳过投递')
}

function isRecordMaintenanceIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

function shouldUseRedisStreamRecordMaintenanceQueue(): boolean {
  return runtimeConfig.queueDriver === 'redis_stream'
}

function shouldEnqueueRecordMaintenanceJobToRedisStream(_job: RecordMaintenanceJob): boolean {
  return shouldUseRedisStreamRecordMaintenanceQueue()
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref()
  })
}

export function clearRecordMaintenanceQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  pendingJobs = []
  pendingJobBytes = 0
  flushing = false
  completedCount = 0
  flushFailureCount = 0
  retainedOverflowWarningCount = 0
  droppedDispatchCount = 0
  droppedOverflowCount = 0
  droppedOversizeCount = 0
  shutdownHooksInstalled = false
  recordMaintenanceRedisConsumerStopping = true
  recordMaintenanceRedisConsumerStarted = false
  recordMaintenanceRedisConsumerPromise = undefined
  void recordMaintenanceRedisStreamQueueInstance?.closeConsumer().catch(() => undefined)
  recordMaintenanceRedisStreamQueueInstance = undefined
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string')
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function positiveBatchSize(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1000
}

function cutoffDaysIso(nowMs: number, days: number): string {
  return new Date(nowMs - Math.max(0, days) * 24 * 60 * 60 * 1000).toISOString()
}

function cutoffHoursIso(nowMs: number, hours: number): string {
  return new Date(nowMs - Math.max(0, hours) * 60 * 60 * 1000).toISOString()
}

function estimateRecordMaintenanceJobBytes(job: RecordMaintenanceJob): number {
  return estimateJsonLikeBytes(job) + 256
}

function sumQueuedRecordMaintenanceJobBytes(items: QueuedRecordMaintenanceJob[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function removeRecordMaintenanceJobsFromHead(count: number): void {
  const removed = pendingJobs.splice(0, count)
  pendingJobBytes = Math.max(0, pendingJobBytes - sumQueuedRecordMaintenanceJobBytes(removed))
}

function mergeAccountUsageSnapshotJob(queued: QueuedRecordMaintenanceJob): 'not_found' | 'queued' | 'dropped' {
  if (queued.job.type !== 'account_usage_snapshot_upsert') {
    return 'not_found'
  }
  const snapshotJob = queued.job
  const index = pendingJobs.findIndex((item) => {
    const job = item.job
    return job.type === 'account_usage_snapshot_upsert'
      && job.accountId === snapshotJob.accountId
      && job.kind === snapshotJob.kind
  })
  if (index < 0) {
    return 'not_found'
  }
  const previous = pendingJobs[index]
  const nextBytes = Math.max(0, pendingJobBytes - previous.bytes + queued.bytes)
  if (nextBytes > recordMaintenanceQueueMaxBytes) {
    recordRecordMaintenanceLocalDrop(queued, 'overflow')
    return 'dropped'
  }
  pendingJobs[index] = queued
  pendingJobBytes = nextBytes
  return 'queued'
}

function recordRecordMaintenanceLocalDrop(item: QueuedRecordMaintenanceJob, reason: 'overflow' | 'oversize'): void {
  if (reason === 'overflow') {
    droppedOverflowCount += 1
    retainedOverflowWarningCount += 1
  } else {
    droppedOversizeCount += 1
  }
  const droppedCount = droppedDispatchCount + droppedOverflowCount + droppedOversizeCount
  if (droppedCount > 10 && droppedCount % 100 !== 0) {
    return
  }
  logger.warn({
    event: 'record_maintenance_queue_dropped',
    reason,
    jobType: item.job.type,
    jobId: item.job.id,
    bytes: item.bytes,
    pendingCount: pendingJobs.length,
    pendingBytes: pendingJobBytes,
    droppedOverflowCount,
    droppedOversizeCount
  }, '数据维护队列达到保护上限，已丢弃新任务')
}

function assertLocalRecordMaintenanceJobsAllowed(operation: string, jobs: RecordMaintenanceJob[]): void {
  for (const job of jobs) {
    assertLocalRecordMaintenanceJobAllowed(operation, normalizeRecordMaintenanceJob(job))
  }
}

function assertLocalRecordMaintenanceJobAllowed(operation: string, job: RecordMaintenanceJob): void {
  if (shouldUseRedisStreamRecordMaintenanceQueue()) {
    throw new Error(`Redis Stream queue driver 下禁止写入数据维护本地队列：${operation}`)
  }
  if (!canProcessRecordMaintenanceJobLocally(job)) {
    throw new Error(`${runtimeConfig.processRole}/${runtimeConfig.workerRole} 角色禁止直接执行数据维护：${operation} 必须投递对应 writer`)
  }
}

function canProcessRecordMaintenanceJobLocally(job: RecordMaintenanceJob): boolean {
  return runtimeConfig.processRole === 'worker'
    && (
      runtimeConfig.workerRole === 'ingest-worker'
      || (runtimeConfig.workerRole === 'stats-worker' && job.type === 'account_usage_snapshot_upsert')
    )
}

function assertNever(value: never): never {
  throw new Error(`未知数据维护任务：${JSON.stringify(value)}`)
}
