import type { AccountSummary } from '../../domain/types.js'
import { dispatchAccountHealthCheck } from '../internal-api/account-health-check-dispatch.service.js'

export { dispatchAccountHealthCheck } from '../internal-api/account-health-check-dispatch.service.js'

export function dispatchPendingAccountHealthCheck(
  account: Pick<AccountSummary, 'id' | 'status'>
): boolean {
  return account.status === 'pending_test' && dispatchAccountHealthCheck(account.id, 'activation')
}

export function accountUpdateNeedsImmediateHealthCheck(input: Record<string, unknown>): boolean {
  return ['credentials', 'proxyProfileId', 'supportedModels', 'healthCheckModel', 'healthCheckEndpointMode', 'modelMappings']
    .some((field) => Object.prototype.hasOwnProperty.call(input, field))
}
