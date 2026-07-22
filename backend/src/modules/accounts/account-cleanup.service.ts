import { errorLogFields, logger } from '../../shared/logger.js'
import { runtimeConfig } from '../../config/runtime.js'
import { registerDeletedAccountRecordCleanupTarget, type DeletedAccountRecordCleanupTarget } from '../../storage/repositories.js'
import {
  enqueueRecordMaintenanceJobWithResult,
  enqueueRecordMaintenanceJobWithResultAsync,
  type RecordMaintenanceEnqueueResult
} from '../record-maintenance/record-maintenance-queue.service.js'

export type AccountRelatedCleanupSubmitResult = RecordMaintenanceEnqueueResult

export function submitAccountRelatedCleanup(target: DeletedAccountRecordCleanupTarget): AccountRelatedCleanupSubmitResult {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    try {
      registerDeletedAccountRecordCleanupTarget(target)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'account_related_cleanup_target_register_failed',
        accountId: target.accountId,
        systemAccountId: target.systemAccountId
      }), 'AI 账户删除后的关联数据清理目标登记失败，将继续尝试投递 worker')
    }
  }

  const enqueueResult = enqueueRecordMaintenanceJobWithResult({
    type: 'account_related_cleanup',
    accountId: target.accountId,
    systemAccountId: target.systemAccountId,
    relatedAccountIds: target.relatedAccountIds,
    authorizationIds: target.authorizationIds,
    teamScopeIds: target.teamScopeIds
  })
  if (enqueueResult.queued) {
    return enqueueResult
  }

  logger.warn({
    event: 'account_related_cleanup_enqueue_failed_deferred',
    accountId: target.accountId,
    systemAccountId: target.systemAccountId,
    droppedReason: enqueueResult.droppedReason
  }, 'AI 账户删除后的关联数据清理投递失败，已保留清理目标等待后台重试')
  return enqueueResult
}

export async function submitAccountRelatedCleanupAsync(target: DeletedAccountRecordCleanupTarget): Promise<AccountRelatedCleanupSubmitResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    try {
      registerDeletedAccountRecordCleanupTarget(target)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'account_related_cleanup_target_register_failed',
        accountId: target.accountId,
        systemAccountId: target.systemAccountId
      }), 'AI 账户删除后的关联数据清理目标登记失败，将继续尝试投递 worker')
    }
  }

  const enqueueResult = await enqueueRecordMaintenanceJobWithResultAsync({
    type: 'account_related_cleanup',
    accountId: target.accountId,
    systemAccountId: target.systemAccountId,
    relatedAccountIds: target.relatedAccountIds,
    authorizationIds: target.authorizationIds,
    teamScopeIds: target.teamScopeIds
  })
  if (enqueueResult.queued) {
    return enqueueResult
  }

  logger.warn({
    event: 'account_related_cleanup_enqueue_failed_deferred',
    accountId: target.accountId,
    systemAccountId: target.systemAccountId,
    droppedReason: enqueueResult.droppedReason
  }, 'AI 账户删除后的关联数据清理投递失败，已保留清理目标等待后台重试')
  return enqueueResult
}
