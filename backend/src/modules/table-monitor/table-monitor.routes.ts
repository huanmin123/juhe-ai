import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { inspectProcessedUsageRecordsCleanupBefore } from '../../storage/data-retention.repository.js'
import { nowIso } from '../../storage/database.js'
import { getTableStorageOverview, listTableStorageHistory, type MonitoredDatabaseRole } from '../../storage/table-monitor.repository.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'
import { enqueueRecordMaintenanceJobWithResult } from '../record-maintenance/record-maintenance-queue.service.js'

export const tableMonitorRouter = Router()

const defaultCleanupBatchSize = 10000
const defaultCleanupMaxBatches = 100
const minimumUsageRecordCleanupAgeMs = 24 * 60 * 60 * 1000

const overviewQuerySchema = z.object({
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(1000).optional()
})

const historyQuerySchema = z.object({
  databaseRole: z.enum(['business', 'records']),
  tableName: z.string().trim().min(1),
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(10000).optional()
})

const usageRecordsCleanupSchema = z.object({
  cutoffAt: z.string().trim().min(1, '请选择清理截止时间'),
  batchSize: z.coerce.number().int().min(100).max(10000).optional(),
  maxBatches: z.coerce.number().int().min(1).max(100).optional()
})

interface UsageRecordsCleanupResult {
  cutoffAt: string
  deletedRows: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  queued: boolean
  eligibleRows?: number
  jobId?: string
  submittedAt?: string
  blockedReason?: string
}

tableMonitorRouter.get('/overview', (req, res) => {
  const parsed = overviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控参数无效')))
    return
  }
  res.json(ok(getTableStorageOverview({
    startAt: parsed.data.startAt,
    endAt: parsed.data.endAt,
    limit: parsed.data.limit
  })))
})

tableMonitorRouter.post('/usage-records/cleanup', mutationGuard({
  operationKey: 'table_monitor.cleanup_usage_records',
  fingerprint: (req) => ({
    cutoffAt: bodyField(req, 'cutoffAt'),
    batchSize: bodyField(req, 'batchSize'),
    maxBatches: bodyField(req, 'maxBatches')
  })
}), (req, res) => {
  const parsed = usageRecordsCleanupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '使用记录清理参数无效')))
    return
  }

  const cutoff = normalizeCleanupCutoff(parsed.data.cutoffAt)
  if (!cutoff) {
    res.status(400).json(badRequest('清理截止时间无效'))
    return
  }
  if (cutoff.iso > nowIso()) {
    res.status(400).json(badRequest('清理截止时间不能晚于当前时间'))
    return
  }
  if (cutoff.time > Date.now() - minimumUsageRecordCleanupAgeMs) {
    res.status(400).json(badRequest('不能清理最近 1 天内的使用记录'))
    return
  }

  const batchSize = parsed.data.batchSize ?? defaultCleanupBatchSize
  const maxBatches = parsed.data.maxBatches ?? defaultCleanupMaxBatches
  const preview = inspectProcessedUsageRecordsCleanupBefore(cutoff.iso, batchSize)
  const baseResult: UsageRecordsCleanupResult = {
    cutoffAt: cutoff.iso,
    deletedRows: 0,
    batches: 0,
    batchSize,
    maxBatches,
    hasMore: preview.hasMore,
    queued: false,
    eligibleRows: preview.eligibleRows,
    blockedReason: preview.blockedReason
  }

  if (preview.eligibleRows <= 0) {
    try {
      recordUsageRecordsCleanupOperation(baseResult, req)
    } catch (error) {
      logger.warn(errorLogFields(error, { event: 'table_monitor_usage_records_cleanup_operation_log_failed' }), '表监控使用记录清理操作日志写入失败')
    }
    res.json(ok(baseResult))
    return
  }

  const enqueueResult = enqueueRecordMaintenanceJobWithResult({
    type: 'usage_records_cleanup',
    cutoffAt: cutoff.iso,
    batchSize,
    maxBatches
  })
  const result: UsageRecordsCleanupResult = {
    ...baseResult,
    queued: enqueueResult.queued,
    jobId: enqueueResult.job.id,
    submittedAt: enqueueResult.job.createdAt ?? nowIso(),
    blockedReason: enqueueResult.queued
      ? undefined
      : enqueueResult.droppedReason === 'worker_ipc_unavailable'
        ? '后台 worker 投递通道不可用，使用记录清理任务未提交；请确认后端主进程、DB service 和 background worker 都由同一个 supervisor 启动'
        : '后台 worker 投递失败，使用记录清理任务未提交；请稍后重试或查看后台日志'
  }

  try {
    recordUsageRecordsCleanupOperation(result, req)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'table_monitor_usage_records_cleanup_operation_log_failed' }), '表监控使用记录清理操作日志写入失败')
  }

  res.json(ok(result))
})

function normalizeCleanupCutoff(value: string): { iso: string; time: number } | undefined {
  const time = Date.parse(value)
  return Number.isNaN(time) ? undefined : { iso: new Date(time).toISOString(), time }
}

function recordUsageRecordsCleanupOperation(result: UsageRecordsCleanupResult, req: Parameters<typeof recordOperationLog>[1]): void {
  recordOperationLog({
    module: 'table_monitor',
    action: 'cleanup_usage_records',
    operationKey: 'table_monitor.cleanup_usage_records',
    resourceType: 'usage_records',
    resourceId: 'usage_records',
    resourceName: 'usage_records',
    summary: result.queued
      ? `提交使用记录清理任务：${result.jobId ?? result.cutoffAt}`
      : result.blockedReason
        ? `使用记录清理未提交：${result.blockedReason}`
        : '使用记录清理未提交：没有可清理记录',
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes: [
      safeChange('cutoffAt', '清理截止时间', undefined, result.cutoffAt),
      safeChange('batchSize', '单批数量', undefined, result.batchSize),
      safeChange('maxBatches', '最大批次', undefined, result.maxBatches),
      safeChange('eligibleRows', '首批可清理记录', undefined, result.eligibleRows),
      safeChange('jobId', '后台任务', undefined, result.jobId),
      safeChange('blockedReason', '未提交原因', undefined, result.blockedReason)
    ],
    metadata: {
      cutoffAt: result.cutoffAt,
      batchSize: result.batchSize,
      maxBatches: result.maxBatches,
      eligibleRows: result.eligibleRows,
      queued: result.queued,
      jobId: result.jobId,
      submittedAt: result.submittedAt,
      blockedReason: result.blockedReason
    }
  }, req)
}

tableMonitorRouter.get('/history', (req, res) => {
  const parsed = historyQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控历史参数无效')))
    return
  }
  res.json(ok(listTableStorageHistory({
    databaseRole: parsed.data.databaseRole as MonitoredDatabaseRole,
    tableName: parsed.data.tableName,
    startAt: parsed.data.startAt,
    endAt: parsed.data.endAt,
    limit: parsed.data.limit
  })))
})
