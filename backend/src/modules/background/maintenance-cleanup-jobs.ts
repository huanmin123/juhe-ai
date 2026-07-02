import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupPendingDeletedAccountRecordTargetsAsync } from '../../storage/account-record-cleanup.js'
import { cleanupPendingDeletedApiKeyRecordTargetsAsync } from '../../storage/api-key-record-cleanup.js'
import { getSettingsAsync } from '../../storage/settings.repository.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { cleanupExpiredAuditHotRetentionData } from './audit-hot-retention-cleanup.service.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'
import { enqueueRecordMaintenanceJobAsync, enqueueRecordMaintenanceJobWithResult } from '../record-maintenance/record-maintenance-queue.service.js'

const dayMs = 24 * 60 * 60 * 1000
const usageRecordRetentionMaxDays = 180
const retentionCleanupBatchSizeMax = 5_000
const retentionCleanupMaxBatchesMax = 100
let postgresDataRetentionDispatchRunning = false

export async function runApiKeyRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedApiKeyRecordTargetsAsync(1, runtimeConfig.databaseDriver === 'postgres'
      ? undefined
      : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_api_key_record_stats', input })
        })
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_api_key_record_cleanup_retry_completed',
        ...summary
      }, '已删除 API Key 关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_api_key_record_cleanup_retry_failed' }), '已删除 API Key 关联数据清理重试失败')
    throw error
  }
}

export async function runAccountRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedAccountRecordTargetsAsync(1, runtimeConfig.databaseDriver === 'postgres'
      ? undefined
      : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_account_record_stats', input })
        })
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_account_record_cleanup_retry_completed',
        ...summary
      }, '已删除 AI 账户关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_record_cleanup_retry_failed' }), '已删除 AI 账户关联数据清理重试失败')
    throw error
  }
}

export async function runDataRetentionCleanup(): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    await enqueuePostgresDataRetentionMaintenanceJobs()
    return
  }
  await cleanupExpiredRetainedData()
}

async function enqueuePostgresDataRetentionMaintenanceJobs(): Promise<void> {
  if (runtimeConfig.processRole !== 'worker') return
  if (postgresDataRetentionDispatchRunning) return
  postgresDataRetentionDispatchRunning = true
  try {
    const settings = await getSettingsAsync()
    const batchSize = settingNumber(settings, 'dataRetentionCleanupBatchSize', 100, retentionCleanupBatchSizeMax)
    const maxBatches = settingNumber(settings, 'dataRetentionCleanupMaxBatchesPerRun', 1, retentionCleanupMaxBatchesMax)
    const usageRecordRetentionDays = settingNumber(settings, 'usageRecordRetentionDays', 1, usageRecordRetentionMaxDays)
    const cutoffAt = new Date(Date.now() - usageRecordRetentionDays * dayMs).toISOString()
    await enqueueRecordMaintenanceJobAsync({
      type: 'usage_records_cleanup',
      cutoffAt,
      batchSize,
      maxBatches
    })
    await enqueueRecordMaintenanceJobAsync({
      type: 'non_business_data_cleanup',
      cutoffAt,
      batchSize,
      maxBatches
    })
    logger.info({
      event: 'postgres_data_retention_maintenance_jobs_enqueued',
      cutoffAt,
      usageRecordRetentionDays,
      batchSize,
      maxBatches
    }, 'PostgreSQL 高性能数据保留维护任务已投递')
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'postgres_data_retention_maintenance_jobs_enqueue_failed'
    }), 'PostgreSQL 高性能数据保留维护任务投递失败')
    throw error
  } finally {
    postgresDataRetentionDispatchRunning = false
  }
}

export async function runAuditHotRetentionCleanup(): Promise<void> {
  await cleanupExpiredAuditHotRetentionData()
}

export async function runExpiredDeletedAccountCleanup(): Promise<void> {
  try {
    const summary = await requestBackgroundWorkerDbService({ type: 'cleanup_expired_deleted_accounts' })
    if (!summary) {
      throw new Error('DB service 未返回逻辑删除 AI 账户清理结果')
    }
    for (const target of summary.recordCleanupTargets ?? []) {
      const enqueueResult = enqueueRecordMaintenanceJobWithResult({
        type: 'account_related_cleanup',
        accountId: target.accountId,
        systemAccountId: target.systemAccountId,
        relatedAccountIds: target.relatedAccountIds,
        authorizationIds: target.authorizationIds,
        teamScopeIds: target.teamScopeIds
      })
      if (!enqueueResult.queued) {
        logger.warn({
          event: 'background_expired_deleted_account_record_cleanup_enqueue_failed',
          accountId: target.accountId,
          systemAccountId: target.systemAccountId,
          droppedReason: enqueueResult.droppedReason
        }, '逻辑删除 AI 账户物理清理发现关联记录未清空，投递记录清理失败')
      }
    }
    if (summary.attempted > 0 || summary.orphanedAuthorizationInstances > 0) {
      logger.info({
        event: 'background_expired_deleted_account_cleanup_completed',
        ...summary
      }, '逻辑删除 AI 账户过期物理清理与孤儿授权实例扫尾完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_expired_deleted_account_cleanup_failed' }), '超过一个月的逻辑删除 AI 账户物理清理失败')
    throw error
  }
}

function settingNumber(settings: Record<string, unknown>, key: string, min: number, max: number): number {
  const value = settings[key]
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`系统设置 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`系统设置 ${key} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}
