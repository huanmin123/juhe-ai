import { errorLogFields, logger } from '../../shared/logger.js'
import { runtimeConfig } from '../../config/runtime.js'
import { registerDeletedApiKeyRecordCleanupTarget, type DeletedApiKeyRecordCleanupTarget } from '../../storage/repositories.js'
import {
  enqueueRecordMaintenanceJobWithResult,
  enqueueRecordMaintenanceJobWithResultAsync,
  type RecordMaintenanceEnqueueResult
} from '../record-maintenance/record-maintenance-queue.service.js'

export type ApiKeyRelatedCleanupSubmitResult = RecordMaintenanceEnqueueResult

export function submitApiKeyRelatedCleanup(target: DeletedApiKeyRecordCleanupTarget): ApiKeyRelatedCleanupSubmitResult {
  const job = {
    type: 'api_key_related_cleanup' as const,
    apiKeyId: target.apiKeyId,
    systemAccountId: target.systemAccountId
  }

  if (runtimeConfig.databaseDriver !== 'postgres') {
    try {
      registerDeletedApiKeyRecordCleanupTarget(target)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'api_key_related_cleanup_target_register_failed',
        apiKeyId: target.apiKeyId,
        systemAccountId: target.systemAccountId
      }), 'API Key 删除后的关联数据清理目标登记失败，将继续尝试投递 worker')
    }
  }

  const enqueueResult = enqueueRecordMaintenanceJobWithResult(job)
  if (enqueueResult.queued) {
    return enqueueResult
  }

  logger.warn({
    event: 'api_key_related_cleanup_enqueue_failed_deferred',
    apiKeyId: target.apiKeyId,
    systemAccountId: target.systemAccountId,
    droppedReason: enqueueResult.droppedReason
  }, 'API Key 删除后的关联数据清理投递失败，已保留清理目标等待后台重试')
  return enqueueResult
}

export async function submitApiKeyRelatedCleanupAsync(target: DeletedApiKeyRecordCleanupTarget): Promise<ApiKeyRelatedCleanupSubmitResult> {
  const job = {
    type: 'api_key_related_cleanup' as const,
    apiKeyId: target.apiKeyId,
    systemAccountId: target.systemAccountId
  }

  if (runtimeConfig.databaseDriver !== 'postgres') {
    try {
      registerDeletedApiKeyRecordCleanupTarget(target)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'api_key_related_cleanup_target_register_failed',
        apiKeyId: target.apiKeyId,
        systemAccountId: target.systemAccountId
      }), 'API Key 删除后的关联数据清理目标登记失败，将继续尝试投递 worker')
    }
  }

  const enqueueResult = await enqueueRecordMaintenanceJobWithResultAsync(job)
  if (enqueueResult.queued) {
    return enqueueResult
  }

  logger.warn({
    event: 'api_key_related_cleanup_enqueue_failed_deferred',
    apiKeyId: target.apiKeyId,
    systemAccountId: target.systemAccountId,
    droppedReason: enqueueResult.droppedReason
  }, 'API Key 删除后的关联数据清理投递失败，已保留清理目标等待后台重试')
  return enqueueResult
}
