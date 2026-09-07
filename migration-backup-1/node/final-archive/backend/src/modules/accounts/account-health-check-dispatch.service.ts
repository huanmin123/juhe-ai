import type { AccountSummary } from '../../domain/types.js'
import type { AccountHealthCheckTriggerReason } from './account-health-check-trigger.js'
import { effectiveAccountApiKeyCount } from './account-balance-config.js'
import { dispatchAccountHealthCheck } from '../internal-api/account-health-check-dispatch.service.js'

export {
  dispatchAccountHealthCheck,
  dispatchAccountHealthCheckWithOutcome,
  type AccountHealthCheckDispatchOutcome
} from '../internal-api/account-health-check-dispatch.service.js'

export function dispatchPendingAccountHealthCheck(
  account: Pick<AccountSummary, 'id' | 'status'>
): boolean {
  return account.status === 'pending_test' && dispatchAccountHealthCheck(account.id, 'activation')
}

/**
 * A newly saved active API Key account still needs one successful health check
 * before the balance detector can determine whether its upstream is supported.
 */
export function dispatchInitialAccountHealthCheck(
  account: Pick<AccountSummary,
    'id' | 'status' | 'type' | 'credentials' | 'balanceQueryEnabled' | 'balanceQueryConfig'
  >
): boolean {
  return (
    account.status === 'pending_test'
    || accountNeedsInitialBalanceDetection(account)
  ) && dispatchAccountHealthCheck(account.id, 'activation')
}

function accountNeedsInitialBalanceDetection(
  account: Pick<AccountSummary,
    'status' | 'type' | 'credentials' | 'balanceQueryEnabled' | 'balanceQueryConfig'
  >
): boolean {
  return account.status === 'active'
    && account.type === 'api_key'
    && effectiveAccountApiKeyCount(account.credentials) === 1
    && account.balanceQueryEnabled !== true
    && Object.keys(account.balanceQueryConfig ?? {}).length === 0
}

export function accountUpdateNeedsImmediateHealthCheck(input: Record<string, unknown>): boolean {
  return ['credentials', 'proxyProfileId', 'supportedModels', 'healthCheckModel', 'healthCheckEndpointMode', 'modelMappings']
    .some((field) => Object.prototype.hasOwnProperty.call(input, field))
}
