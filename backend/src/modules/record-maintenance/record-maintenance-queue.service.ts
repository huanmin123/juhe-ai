import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupProcessedUsageRecordsBeforeWithResult } from '../../storage/data-retention.repository.js'
import { newId, nowIso } from '../../storage/database.js'
import {
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
const recordMaintenanceRetryDelayMs = 1000
const recordMaintenanceBatchSize = 50
const recordMaintenanceMaxPending = 5000
const minimumUsageRecordCleanupAgeMs = 24 * 60 * 60 * 1000

export interface RecordMaintenanceEnqueueResult {
  job: RecordMaintenanceJob
  queued: boolean
  droppedReason?: string
}

let pendingJobs: RecordMaintenanceJob[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let completedCount = 0
let flushFailureCount = 0
let retainedOverflowWarningCount = 0
let droppedDispatchCount = 0
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
    return {
      job,
      queued: sendRecordMaintenanceJobsToWorker([job])
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

  enqueueRecordMaintenanceJobLocal(job)
  return { job, queued: true }
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
      flushedBatches += 1

      for (let index = 0; index < batch.length; index += 1) {
        const job = batch[index]
        const snapshotJobs = collectAccountUsageSnapshotJobs(batch, index)
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
          pendingJobs = [job, ...batch.slice(index + 1), ...pendingJobs]
          flushFailureCount += 1
          logger.error(errorLogFields(error, {
            event: 'record_maintenance_queue_flush_failed',
            jobType: job.type,
            jobId: job.id,
            pendingCount: pendingJobs.length,
            flushFailureCount
          }), '记录库维护队列执行失败，已保留任务等待重试')
          shouldRetry = options.retryOnFailure !== false
          return
        }
      }
    } while (options.drain && pendingJobs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
    if (pendingJobs.length > 0 && (!failed || shouldRetry)) {
      scheduleRecordMaintenanceFlush(shouldRetry ? recordMaintenanceRetryDelayMs : 0)
    }
  }
}

export function flushAllRecordMaintenanceQueue(): void {
  flushRecordMaintenanceQueue({ drain: true, retryOnFailure: false })
}

export function getRecordMaintenanceQueueRuntime(): {
  queueLength: number
  droppedCount: number
  completedCount: number
  retainedOverflowWarningCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingJobs.length,
    droppedCount: droppedDispatchCount,
    completedCount,
    retainedOverflowWarningCount,
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

function enqueueRecordMaintenanceJobLocal(job: RecordMaintenanceJob): void {
  assertLocalRecordMaintenanceWriteAllowed('enqueueRecordMaintenanceJobLocal')
  pendingJobs.push(job)
  if (pendingJobs.length > recordMaintenanceMaxPending) {
    const overflowCount = pendingJobs.length - recordMaintenanceMaxPending
    retainedOverflowWarningCount += 1
    logger.warn({
      event: 'record_maintenance_queue_soft_limit_exceeded',
      overflowCount,
      retainedOverflowWarningCount,
      pendingCount: pendingJobs.length
    }, '记录库维护队列超过软上限，已保留待执行任务并触发立即处理')
    flushRecordMaintenanceQueue({ drain: true, maxBatches: 5 })
  }
  scheduleRecordMaintenanceFlush(pendingJobs.length >= recordMaintenanceBatchSize ? 0 : recordMaintenanceFlushIntervalMs)
}

function processRecordMaintenanceJob(job: RecordMaintenanceJob): void {
  switch (job.type) {
    case 'api_key_related_cleanup':
      cleanupDeletedApiKeyRelatedRecordData({
        apiKeyId: job.apiKeyId,
        systemAccountId: job.systemAccountId
      })
      logger.info({
        event: 'record_maintenance_api_key_cleanup_completed',
        jobId: job.id,
        apiKeyId: job.apiKeyId,
        systemAccountId: job.systemAccountId
      }, 'API Key 关联记录库数据清理完成')
      return
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
  }), 'DB service 记录库维护任务投递失败，已跳过投递')
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function assertLocalRecordMaintenanceWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole !== 'worker') {
    throw new Error(`${runtimeConfig.processRole} 角色禁止直接执行记录库维护：${operation} 必须投递 background worker`)
  }
}

function assertNever(value: never): never {
  throw new Error(`未知记录库维护任务：${JSON.stringify(value)}`)
}
