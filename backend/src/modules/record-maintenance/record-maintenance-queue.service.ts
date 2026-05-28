import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { cleanupProcessedUsageRecordsBeforeWithResult } from '../../storage/data-retention.repository.js'
import { newId, nowIso } from '../../storage/database.js'
import {
  cleanupDeletedAccountRelatedRecordData,
  cleanupDeletedApiKeyRelatedRecordData,
  upsertAccountUsageSnapshots,
  type AccountUsageSnapshotUpsertInput
} from '../../storage/repositories.js'
import { sendRecordMaintenanceJobsToWorker } from '../background/background-ipc.js'

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
const recordMaintenanceQueueMaxItems = 5_000
const recordMaintenanceQueueMaxBytes = 32 * 1024 * 1024
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

interface RecordMaintenanceFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueRecordMaintenanceJob(input: RecordMaintenanceJob): RecordMaintenanceJob {
  return enqueueRecordMaintenanceJobWithResult(input).job
}

export function enqueueRecordMaintenanceJobWithResult(input: RecordMaintenanceJob): RecordMaintenanceEnqueueResult {
  const job = normalizeRecordMaintenanceJob(input)
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

  const queued = enqueueRecordMaintenanceJobLocal(job)
  return {
    job,
    queued,
    droppedReason: queued ? undefined : 'worker_local_queue_full'
  }
}

export function enqueueRecordMaintenanceJobsLocal(inputs: RecordMaintenanceJob[]): void {
  assertLocalRecordMaintenanceWriteAllowed('enqueueRecordMaintenanceJobsLocal')
  for (const input of inputs) {
    enqueueRecordMaintenanceJobLocal(normalizeRecordMaintenanceJob(input))
  }
}

export function flushRecordMaintenanceQueue(options: RecordMaintenanceFlushOptions = {}): void {
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
      const batch = pendingJobs.splice(0, recordMaintenanceBatchSize)
      if (batch.length === 0) {
        break
      }
      pendingJobBytes = Math.max(0, pendingJobBytes - sumQueuedRecordMaintenanceJobBytes(batch))
      const batchJobs = batch.map((item) => item.job)
      flushedBatches += 1

      for (let index = 0; index < batchJobs.length; index += 1) {
        const job = batchJobs[index]
        const snapshotJobs = collectAccountUsageSnapshotJobs(batchJobs, index)
        try {
          if (snapshotJobs.length > 0) {
            processAccountUsageSnapshotUpsertJobs(snapshotJobs)
            completedCount += snapshotJobs.length
            index += snapshotJobs.length - 1
          } else {
            processRecordMaintenanceJob(job)
            completedCount += 1
          }
        } catch (error) {
          failed = true
          pendingJobs = [...batch.slice(index), ...pendingJobs]
          pendingJobBytes = sumQueuedRecordMaintenanceJobBytes(pendingJobs)
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

export function flushAllRecordMaintenanceQueue(): void {
  flushRecordMaintenanceQueue({ drain: true, retryOnFailure: false })
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

  process.once('beforeExit', flushAllRecordMaintenanceQueue)
  process.once('exit', flushAllRecordMaintenanceQueue)

  process.once('SIGINT', () => exitAfterRecordMaintenanceFlush(0))
  process.once('SIGTERM', () => exitAfterRecordMaintenanceFlush(0))
}

function enqueueRecordMaintenanceJobLocal(job: RecordMaintenanceJob): boolean {
  assertLocalRecordMaintenanceWriteAllowed('enqueueRecordMaintenanceJobLocal')
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

function processRecordMaintenanceJob(job: RecordMaintenanceJob): void {
  switch (job.type) {
    case 'api_key_related_cleanup': {
      const result = cleanupDeletedApiKeyRelatedRecordData({
        apiKeyId: job.apiKeyId,
        systemAccountId: job.systemAccountId
      })
      const deferred = result.hasMore || Boolean(result.blockedReason)
      logger.info({
        event: deferred ? 'record_maintenance_api_key_cleanup_deferred' : 'record_maintenance_api_key_cleanup_completed',
        jobId: job.id,
        ...result
      }, deferred ? 'API Key 关联数据清理等待统计游标追平' : 'API Key 关联数据清理完成')
      return
    }
    case 'account_related_cleanup': {
      const result = cleanupDeletedAccountRelatedRecordData({
        accountId: job.accountId,
        systemAccountId: job.systemAccountId,
        authorizationIds: job.authorizationIds,
        teamScopeIds: job.teamScopeIds
      })
      const deferred = result.hasMore || Boolean(result.blockedReason)
      logger.info({
        event: deferred ? 'record_maintenance_account_cleanup_deferred' : 'record_maintenance_account_cleanup_completed',
        jobId: job.id,
        ...result
      }, deferred ? 'AI 账户关联数据清理等待统计游标追平' : 'AI 账户关联数据清理完成')
      return
    }
    case 'usage_records_cleanup': {
      const result = cleanupUsageRecordsBefore({
        cutoffAt: job.cutoffAt,
        batchSize: job.batchSize,
        maxBatches: job.maxBatches
      })
      logger.info({
        event: 'record_maintenance_usage_records_cleanup_completed',
        jobId: job.id,
        ...result
      }, '使用记录后台清理完成')
      return
    }
    case 'account_usage_snapshot_upsert':
      processAccountUsageSnapshotUpsertJobs([job])
      return
    default:
      assertNever(job)
  }
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

function processAccountUsageSnapshotUpsertJobs(jobs: AccountUsageSnapshotUpsertJob[]): void {
  const inputs: AccountUsageSnapshotUpsertInput[] = jobs.map((job) => ({
    accountId: job.accountId,
    kind: job.kind,
    source: job.source,
    snapshot: job.snapshot,
    updatedAt: job.updatedAt
  }))
  upsertAccountUsageSnapshots(inputs)
  logger.info({
    event: 'record_maintenance_account_usage_snapshots_upserted',
    jobCount: jobs.length,
    jobIds: jobs.map((job) => job.id),
    accountIds: jobs.map((job) => job.accountId)
  }, '账号用量快照后台批量写入完成')
}

function cleanupUsageRecordsBefore(input: { cutoffAt: string; batchSize: number; maxBatches: number }): {
  cutoffAt: string
  deletedRows: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  blockedReason?: string
} {
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
    const batch = cleanupProcessedUsageRecordsBeforeWithResult(input.cutoffAt, input.batchSize)
    deletedRows += batch.deletedRows
    hasMore = batch.hasMore
    blockedReason = batch.blockedReason ?? blockedReason
    if (batch.deletedRows > 0) {
      batches += 1
    }
    if (batch.deletedRows === 0 || !batch.hasMore) {
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

function exitAfterRecordMaintenanceFlush(exitCode: number): never {
  flushAllRecordMaintenanceQueue()
  process.exit(exitCode)
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
    flushRecordMaintenanceQueue()
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
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string')
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function estimateRecordMaintenanceJobBytes(job: RecordMaintenanceJob): number {
  return estimateJsonLikeBytes(job) + 256
}

function sumQueuedRecordMaintenanceJobBytes(items: QueuedRecordMaintenanceJob[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
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
      && (job.source ?? '') === (snapshotJob.source ?? '')
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

function assertLocalRecordMaintenanceWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole !== 'worker') {
    throw new Error(`${runtimeConfig.processRole} 角色禁止直接执行数据维护：${operation} 必须投递 background worker`)
  }
}

function assertNever(value: never): never {
  throw new Error(`未知数据维护任务：${JSON.stringify(value)}`)
}
