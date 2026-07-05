import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupAuditHotSearchFilesBefore } from '../../storage/audit-log-hot-search-files.js'
import { cleanupAuditSuccessHotRetentionAsync } from '../../storage/audit-logs.repository.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'

export interface AuditHotRetentionCleanupResult {
  auditLogs: number
  auditPayloadBlobs: number
  auditHotSearchFiles: number
}

const hourMs = 60 * 60 * 1000
const auditHotRetentionCleanupBatchSize = 2000
const auditHotRetentionCleanupMaxBatches = 20
const auditHotRetentionCleanupMaxRunMs = 5000
let auditHotRetentionCleanupRunning = false

export async function cleanupExpiredAuditHotRetentionData(nowMs = Date.now()): Promise<AuditHotRetentionCleanupResult> {
  if (runtimeConfig.processRole !== 'worker') {
    return emptyAuditHotRetentionCleanupResult()
  }
  if (auditHotRetentionCleanupRunning) {
    return emptyAuditHotRetentionCleanupResult()
  }

  auditHotRetentionCleanupRunning = true
  const startedAt = Date.now()
  try {
    const auditSettings = readAuditLogSettings()
    const successHotCutoffCreatedAt = new Date(nowMs - auditSettings.successHotRetentionHours * hourMs).toISOString()
    const successSampleBucketThreshold = Math.round(auditSettings.successSampleRate * 10000)
    const result = emptyAuditHotRetentionCleanupResult()

    for (let index = 0; index < auditHotRetentionCleanupMaxBatches; index += 1) {
      const batch = await cleanupAuditSuccessHotRetentionAsync({
        successHotCutoffCreatedAt,
        successSampleBucketThreshold,
        limit: auditHotRetentionCleanupBatchSize
      })
      result.auditLogs += batch.auditLogs
      result.auditPayloadBlobs += batch.auditPayloadBlobs
      await yieldToEventLoop()
      if (batch.auditLogs < auditHotRetentionCleanupBatchSize) {
        break
      }
      if (Date.now() - startedAt >= auditHotRetentionCleanupMaxRunMs) {
        break
      }
    }

    const remainingFileCleanupMs = auditHotRetentionCleanupMaxRunMs - (Date.now() - startedAt)
    if (remainingFileCleanupMs > 0) {
      result.auditHotSearchFiles = await cleanupAuditHotSearchFilesBefore(successHotCutoffCreatedAt, {
        maxFiles: auditHotRetentionCleanupBatchSize,
        maxRunMs: remainingFileCleanupMs
      })
    }
    if (result.auditLogs > 0 || result.auditPayloadBlobs > 0 || result.auditHotSearchFiles > 0) {
      logger.info({
        event: 'audit_hot_retention_cleanup_completed',
        deleted: result,
        successHotCutoffCreatedAt,
        batchSize: auditHotRetentionCleanupBatchSize,
        maxBatches: auditHotRetentionCleanupMaxBatches,
        maxRunMs: auditHotRetentionCleanupMaxRunMs,
        durationMs: Date.now() - startedAt
      }, '审计成功热保留清理完成')
    }

    return result
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'audit_hot_retention_cleanup_failed' }), '审计成功热保留清理失败')
    throw error
  } finally {
    auditHotRetentionCleanupRunning = false
  }
}

function emptyAuditHotRetentionCleanupResult(): AuditHotRetentionCleanupResult {
  return {
    auditLogs: 0,
    auditPayloadBlobs: 0,
    auditHotSearchFiles: 0
  }
}

async function yieldToEventLoop(): Promise<void> {
  await new Promise((resolve) => setImmediate(resolve))
}
