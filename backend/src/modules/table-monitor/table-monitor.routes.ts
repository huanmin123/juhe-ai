import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { nowIso } from '../../storage/database.js'
import { cleanupProcessedUsageRecordsBeforeWithResult } from '../../storage/repositories.js'
import { getTableStorageOverview, listTableStorageHistory, type MonitoredDatabaseRole } from '../../storage/table-monitor.repository.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'

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
  safetyCursor?: {
    createdAt: string
    id: string
  }
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

  const result = cleanupUsageRecordsBefore({
    cutoffAt: cutoff.iso,
    batchSize: parsed.data.batchSize ?? defaultCleanupBatchSize,
    maxBatches: parsed.data.maxBatches ?? defaultCleanupMaxBatches
  })

  if (result.deletedRows > 0) {
    try {
      recordUsageRecordsCleanupOperation(result, req)
    } catch (error) {
      logger.warn(errorLogFields(error, { event: 'table_monitor_usage_records_cleanup_operation_log_failed' }), '表监控使用记录清理操作日志写入失败')
    }
  }

  res.json(ok(result))
})

function cleanupUsageRecordsBefore(input: { cutoffAt: string; batchSize: number; maxBatches: number }): UsageRecordsCleanupResult {
  let deletedRows = 0
  let batches = 0
  let hasMore = false
  let safetyCursor: UsageRecordsCleanupResult['safetyCursor']
  let blockedReason: string | undefined

  for (let index = 0; index < input.maxBatches; index += 1) {
    const batch = cleanupProcessedUsageRecordsBeforeWithResult(input.cutoffAt, input.batchSize)
    if (batch.safetyCursorCreatedAt && batch.safetyCursorId) {
      safetyCursor = {
        createdAt: batch.safetyCursorCreatedAt,
        id: batch.safetyCursorId
      }
    }
    if (batch.blockedReason) {
      blockedReason = batch.blockedReason
      hasMore = false
      break
    }
    deletedRows += batch.deletedRows
    hasMore = batch.hasMore
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
    safetyCursor,
    blockedReason
  }
}

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
    summary: `清理使用记录：删除 ${result.deletedRows} 条`,
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes: [
      safeChange('cutoffAt', '清理截止时间', undefined, result.cutoffAt),
      safeChange('deletedRows', '删除行数', undefined, result.deletedRows),
      safeChange('batches', '清理批次', undefined, result.batches),
      safeChange('safetyCursorCreatedAt', '安全游标时间', undefined, result.safetyCursor?.createdAt)
    ],
    metadata: {
      cutoffAt: result.cutoffAt,
      deletedRows: result.deletedRows,
      batches: result.batches,
      batchSize: result.batchSize,
      maxBatches: result.maxBatches,
      hasMore: result.hasMore,
      safetyCursor: result.safetyCursor
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
