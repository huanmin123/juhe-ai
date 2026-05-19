import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupDeletedApiKeyRelatedRecordData, registerDeletedApiKeyRecordCleanupTarget, type DeletedApiKeyRecordCleanupTarget } from '../../storage/repositories.js'
import { enqueueRecordMaintenanceJobWithResult, type RecordMaintenanceEnqueueResult } from '../record-maintenance/record-maintenance-queue.service.js'

export interface ApiKeyRelatedCleanupSubmitResult extends RecordMaintenanceEnqueueResult {
  fallbackExecuted: boolean
}

export function submitApiKeyRelatedCleanup(target: DeletedApiKeyRecordCleanupTarget): ApiKeyRelatedCleanupSubmitResult {
  try {
    registerDeletedApiKeyRecordCleanupTarget(target)
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'api_key_related_cleanup_target_register_failed',
      apiKeyId: target.apiKeyId,
      systemAccountId: target.systemAccountId
    }), 'API Key 删除后的记录库清理目标登记失败，将继续尝试投递 worker')
  }

  const enqueueResult = enqueueRecordMaintenanceJobWithResult({
    type: 'api_key_related_cleanup',
    apiKeyId: target.apiKeyId,
    systemAccountId: target.systemAccountId
  })
  if (enqueueResult.queued) {
    return { ...enqueueResult, fallbackExecuted: false }
  }

  try {
    const cleanupResult = cleanupDeletedApiKeyRelatedRecordData(target)
    logger.warn({
      event: 'api_key_related_cleanup_fallback_executed',
      apiKeyId: target.apiKeyId,
      systemAccountId: target.systemAccountId,
      droppedReason: enqueueResult.droppedReason,
      deletedRows: cleanupResult.deletedRows,
      hasMore: cleanupResult.hasMore,
      blockedReason: cleanupResult.blockedReason
    }, 'API Key 删除后的记录库清理投递失败，已在当前进程同步兜底执行')
    return { ...enqueueResult, fallbackExecuted: true }
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'api_key_related_cleanup_fallback_failed',
      apiKeyId: target.apiKeyId,
      systemAccountId: target.systemAccountId,
      droppedReason: enqueueResult.droppedReason
    }), 'API Key 删除后的记录库清理投递失败，且同步兜底执行失败')
    throw error
  }
}
