import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupPendingDeletedAccountRecordTargetsAsync } from '../../storage/account-record-cleanup.js'
import { cleanupPendingDeletedApiKeyRecordTargetsAsync } from '../../storage/api-key-record-cleanup.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { cleanupExpiredAuditHotRetentionData } from './audit-hot-retention-cleanup.service.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'

export async function runApiKeyRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedApiKeyRecordTargetsAsync(1)
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
    const summary = await cleanupPendingDeletedAccountRecordTargetsAsync(1)
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
  await cleanupExpiredRetainedData()
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
