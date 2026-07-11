import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sendAccountHealthCheckTriggerToWorker } from '../background/background-ipc.js'
import type { AccountHealthCheckTriggerReason } from './account-health-check-trigger.js'

export function dispatchPendingAccountHealthCheck(
  account: Pick<AccountSummary, 'id' | 'status'>
): boolean {
  return account.status === 'pending_test' && dispatchAccountHealthCheck(account.id, 'activation')
}

export function accountUpdateNeedsImmediateHealthCheck(input: Record<string, unknown>): boolean {
  return ['credentials', 'proxyProfileId', 'supportedModels', 'healthCheckModel', 'modelMappings']
    .some((field) => Object.prototype.hasOwnProperty.call(input, field))
}

export function dispatchAccountHealthCheck(accountId: string, reason: AccountHealthCheckTriggerReason): boolean {
  const normalizedId = accountId.trim()
  if (!normalizedId) return false
  if (runtimeConfig.processRole !== 'db-service') {
    return sendAccountHealthCheckTriggerToWorker(normalizedId, reason)
  }
  if (!process.send || process.connected === false) return false
  try {
    process.send({
      type: 'background_worker_account_health_check_trigger',
      accountId: normalizedId,
      reason
    }, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_health_check_db_service_dispatch_failed',
          accountId: normalizedId
        }), 'DB service 投递账户健康检查触发消息失败')
      }
    })
    return true
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_health_check_db_service_dispatch_failed',
      accountId: normalizedId
    }), 'DB service 投递账户健康检查触发消息失败')
    return false
  }
}
