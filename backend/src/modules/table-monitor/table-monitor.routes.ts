import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { nowIso } from '../../storage/database.js'
import { getTableStorageOverviewAsync, listDatabaseStorageHistoryAsync, listTableStorageHistoryAsync, type MonitoredDatabaseRole } from '../../storage/table-monitor.repository.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'
import { enqueueRecordMaintenanceJobWithResult } from '../record-maintenance/record-maintenance-queue.service.js'

export const tableMonitorRouter = Router()

const defaultCleanupBatchSize = 10000
const defaultCleanupMaxBatches = 100

const overviewQuerySchema = z.object({
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(1000).optional()
})

const historyQuerySchema = z.object({
  databaseRole: z.enum(['business', 'dataset', 'usage-catalog', 'stats']),
  tableName: z.string().trim().min(1),
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(10000).optional()
})

const databaseHistoryQuerySchema = z.object({
  startAt: z.string().trim().optional(),
  endAt: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(10000).optional()
})

const nonBusinessDataCleanupSchema = z.object({
  cutoffAt: z.string().trim().min(1, '请选择清理截止时间'),
  batchSize: z.number().int().min(100).max(10000).optional(),
  maxBatches: z.number().int().min(1).max(100).optional()
}).strict()

interface NonBusinessDataCleanupResult {
  cutoffAt: string
  deletedRows: number
  deletedFiles: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  queued: boolean
  jobId?: string
  submittedAt?: string
  blockedReason?: string
}

tableMonitorRouter.get('/overview', async (req, res, next) => {
  const parsed = overviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控参数无效')))
    return
  }
  try {
    res.json(ok(await getTableStorageOverviewAsync({
      startAt: parsed.data.startAt,
      endAt: parsed.data.endAt,
      limit: parsed.data.limit
    })))
  } catch (error) {
    next(error)
  }
})

tableMonitorRouter.post('/non-business-data/cleanup', mutationGuard({
  operationKey: 'table_monitor.cleanup_non_business_data',
  fingerprint: (req) => ({
    cutoffAt: bodyField(req, 'cutoffAt'),
    batchSize: bodyField(req, 'batchSize'),
    maxBatches: bodyField(req, 'maxBatches')
  })
}), async (req, res) => {
  const parsed = nonBusinessDataCleanupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '非业务数据清理参数无效')))
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

  const batchSize = parsed.data.batchSize ?? defaultCleanupBatchSize
  const maxBatches = parsed.data.maxBatches ?? defaultCleanupMaxBatches
  const baseResult: NonBusinessDataCleanupResult = {
    cutoffAt: cutoff.iso,
    deletedRows: 0,
    deletedFiles: 0,
    batches: 0,
    batchSize,
    maxBatches,
    hasMore: false,
    queued: false
  }

  const enqueueResult = enqueueRecordMaintenanceJobWithResult({
    type: 'non_business_data_cleanup',
    cutoffAt: cutoff.iso,
    batchSize,
    maxBatches
  })
  const result: NonBusinessDataCleanupResult = {
    ...baseResult,
    queued: enqueueResult.queued,
    jobId: enqueueResult.job.id,
    submittedAt: enqueueResult.job.createdAt ?? nowIso(),
    blockedReason: enqueueResult.queued
      ? undefined
      : enqueueResult.droppedReason === 'worker_ipc_unavailable'
        ? '后台 worker 投递通道不可用，非业务数据清理任务未提交；请确认后端主进程、DB service 和 background worker 都由同一个 supervisor 启动'
        : '后台 worker 投递失败，非业务数据清理任务未提交；请稍后重试或查看后台日志'
  }

  try {
    await recordNonBusinessDataCleanupOperation(result, req)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'table_monitor_non_business_data_cleanup_operation_log_failed' }), '表监控非业务数据清理操作日志写入失败')
  }

  res.json(ok(result))
})

function normalizeCleanupCutoff(value: string): { iso: string; time: number } | undefined {
  const time = Date.parse(value)
  return Number.isNaN(time) ? undefined : { iso: new Date(time).toISOString(), time }
}

async function recordNonBusinessDataCleanupOperation(result: NonBusinessDataCleanupResult, req: Parameters<typeof recordOperationLogAsync>[1]): Promise<void> {
  await recordOperationLogAsync({
    module: 'table_monitor',
    action: 'cleanup_non_business_data',
    operationKey: 'table_monitor.cleanup_non_business_data',
    resourceType: 'non_business_data',
    resourceId: 'dataset_stats_usage_shards',
    resourceName: '非业务数据',
    summary: result.queued
      ? `提交非业务数据硬清理任务：${result.jobId ?? result.cutoffAt}`
      : result.blockedReason
        ? `非业务数据清理未提交：${result.blockedReason}`
        : '非业务数据清理未提交',
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes: [
      safeChange('cutoffAt', '清理截止时间', undefined, result.cutoffAt),
      safeChange('batchSize', '单批数量', undefined, result.batchSize),
      safeChange('maxBatches', '最大批次', undefined, result.maxBatches),
      safeChange('jobId', '后台任务', undefined, result.jobId),
      safeChange('blockedReason', '未提交原因', undefined, result.blockedReason)
    ],
    metadata: {
      cutoffAt: result.cutoffAt,
      batchSize: result.batchSize,
      maxBatches: result.maxBatches,
      queued: result.queued,
      jobId: result.jobId,
      submittedAt: result.submittedAt,
      blockedReason: result.blockedReason
    }
  }, req)
}

tableMonitorRouter.get('/history', async (req, res, next) => {
  const parsed = historyQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控历史参数无效')))
    return
  }
  try {
    res.json(ok(await listTableStorageHistoryAsync({
      databaseRole: parsed.data.databaseRole as MonitoredDatabaseRole,
      tableName: parsed.data.tableName,
      startAt: parsed.data.startAt,
      endAt: parsed.data.endAt,
      limit: parsed.data.limit
    })))
  } catch (error) {
    next(error)
  }
})

tableMonitorRouter.get('/database-history', async (req, res, next) => {
  const parsed = databaseHistoryQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '数据库增长历史参数无效')))
    return
  }
  try {
    res.json(ok(await listDatabaseStorageHistoryAsync({
      startAt: parsed.data.startAt,
      endAt: parsed.data.endAt,
      limit: parsed.data.limit
    })))
  } catch (error) {
    next(error)
  }
})
