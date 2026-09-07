import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { canonicalizeRfc3339Instant, requiredRfc3339Instant } from '../../shared/rfc3339.js'
import { nowIso } from '../../storage/database.js'
import { getTableStorageOverviewAsync, listDatabaseStorageHistoryAsync, listTableStorageHistoryAsync, type MonitoredDatabaseRole } from '../../storage/table-monitor.repository.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'
import { enqueueRecordMaintenanceJobWithResultAsync } from '../record-maintenance/record-maintenance-queue.service.js'

export const tableMonitorRouter = Router()

const defaultCleanupBatchSize = 5000
const defaultCleanupMaxBatches = 100
const maxHistoryPointsPerSeries = 2000
const absoluteDateTimeQuerySchema = z.string()
  .trim()
  .min(1, '时间不能为空')
  .refine((value) => canonicalizeRfc3339Instant(value) !== undefined, '时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  .transform((value) => canonicalizeRfc3339Instant(value)!)

export const tableMonitorOverviewQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().max(200).optional(),
  refresh: z.preprocess((value) => booleanQueryValue(value), z.boolean().optional())
})

const historyQuerySchema = z.object({
  databaseRole: z.enum(['business', 'dataset', 'usage-catalog', 'stats', 'codex-context-state']),
  tableName: z.string().trim().min(1),
  startAt: absoluteDateTimeQuerySchema.optional(),
  endAt: absoluteDateTimeQuerySchema.optional(),
  limit: z.coerce.number().int().min(1).max(maxHistoryPointsPerSeries).optional()
})

const databaseHistoryQuerySchema = z.object({
  startAt: absoluteDateTimeQuerySchema.optional(),
  endAt: absoluteDateTimeQuerySchema.optional(),
  limit: z.coerce.number().int().min(1).max(maxHistoryPointsPerSeries).optional()
})

const nonBusinessDataCleanupSchema = z.object({
  cutoffAt: absoluteDateTimeQuerySchema
}).strict()

interface NonBusinessDataCleanupReceipt {
  cutoffAt: string
  queued: boolean
  jobId?: string
  submittedAt?: string
  blockedReason?: string
}

tableMonitorRouter.get('/overview', async (req, res, next) => {
  const parsed = tableMonitorOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '表监控参数无效')))
    return
  }
  try {
    const startedAt = Date.now()
    const cacheableOverview = !parsed.data.keyword
      && (parsed.data.page ?? 1) === 1
      && (parsed.data.pageSize ?? 10) === 10
    res.setHeader(
      'X-Table-Monitor-Cache',
      cacheableOverview ? (parsed.data.refresh === true ? 'bypass' : 'bounded-swr') : 'none'
    )
    const result = await getTableStorageOverviewAsync({
      page: parsed.data.page,
      pageSize: parsed.data.pageSize,
      keyword: parsed.data.keyword
    }, {
      bypassCache: parsed.data.refresh === true
    })
    res.setHeader('X-Table-Monitor-Duration-Ms', String(Date.now() - startedAt))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

function booleanQueryValue(value: unknown): unknown {
  const text = Array.isArray(value) && value.length === 1 ? value[0] : value
  if (typeof text === 'boolean') return text
  if (text === undefined) return undefined
  if (typeof text !== 'string') return value
  const normalized = text.trim().toLowerCase()
  if (['1', 'true', 'yes'].includes(normalized)) return true
  if (['0', 'false', 'no'].includes(normalized)) return false
  return value
}

tableMonitorRouter.post('/non-business-data/cleanup', mutationGuard({
  operationKey: 'table_monitor.cleanup_non_business_data',
  fingerprint: (req) => ({
    cutoffAt: bodyField(req, 'cutoffAt')
  })
}), async (req, res) => {
  const parsed = nonBusinessDataCleanupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '非业务数据清理参数无效')))
    return
  }

  const cutoff = normalizeCleanupCutoff(parsed.data.cutoffAt)
  if (cutoff.iso > nowIso()) {
    res.status(400).json(badRequest('清理截止时间不能晚于当前时间'))
    return
  }

  const batchSize = defaultCleanupBatchSize
  const maxBatches = defaultCleanupMaxBatches
  const enqueueResult = await enqueueRecordMaintenanceJobWithResultAsync({
    type: 'non_business_data_cleanup',
    cutoffAt: cutoff.iso,
    batchSize,
    maxBatches
  })
  const result: NonBusinessDataCleanupReceipt = {
    cutoffAt: cutoff.iso,
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
    await recordNonBusinessDataCleanupOperation({ ...result, batchSize, maxBatches }, req)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'table_monitor_non_business_data_cleanup_operation_log_failed' }), '表监控非业务数据清理操作日志写入失败')
  }

  res.json(ok(result))
})

function normalizeCleanupCutoff(value: string): { iso: string; time: number } {
  const iso = requiredRfc3339Instant(value, '清理截止时间')
  return { iso, time: new Date(iso).getTime() }
}

async function recordNonBusinessDataCleanupOperation(
  result: NonBusinessDataCleanupReceipt & { batchSize: number; maxBatches: number },
  req: Parameters<typeof recordOperationLogAsync>[1]
): Promise<void> {
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
