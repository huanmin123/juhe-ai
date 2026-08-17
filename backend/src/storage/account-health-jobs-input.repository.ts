import type { AccountSummary } from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import { findAccountSummary, findAccountSummaryAsync } from './account-summary.repository.js'

const frozenEndpointModes = new Set(['chat_json', 'responses_json', 'images_json'])
const acceptedStatuses = new Set(['active', 'pending_test', 'temporary_unavailable', 'rate_limited'])
const inputPublisherAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

// Read adapter for immutable J1 input production.  This is intentionally not a
// due-candidate scan and has no health state mutation or scheduling side effect.
export function findAccountForAccountHealthJobsInput(accountId: string): AccountSummary | undefined {
  const account = findAccountSummary(accountId, inputPublisherAccess)
  return isEligibleForAccountHealthJobsInput(account) ? account : undefined
}

export async function findAccountForAccountHealthJobsInputAsync(accountId: string): Promise<AccountSummary | undefined> {
  const account = await findAccountSummaryAsync(accountId, inputPublisherAccess)
  return isEligibleForAccountHealthJobsInput(account) ? account : undefined
}

function isEligibleForAccountHealthJobsInput(account: AccountSummary | undefined): account is AccountSummary {
  if (!account) return false
  if (account.providerCode !== 'openai') return false
  if (account.type !== 'api_key' && account.type !== 'oauth') return false
  if (!frozenEndpointModes.has(account.healthCheckEndpointMode)) return false
  if (!acceptedStatuses.has(account.status)) return false
  if (account.status !== 'pending_test' && !account.schedulable) return false
  if (account.accountExpiresAt && Date.parse(account.accountExpiresAt) <= Date.now()) return false
  if (!account.boundGroupId || account.groupBindStatus !== 'bound') return false
  if (account.accessType !== 'authorized') return true
  if (!account.accountAuthorizationId || !account.bindingSystemAccountId) return false
  if (account.authorizationStatus !== 'active' || account.authorizationQuotaExceeded) return false
  if (account.authorizationExpiresAt && Date.parse(account.authorizationExpiresAt) <= Date.now()) return false
  if (account.authorizationInstanceSourceAccountStatus !== 'active') return false
  if (!account.authorizationInstanceSourceAccountSchedulable) return false
  if (account.authorizationInstanceSourceAccountExpiresAt && Date.parse(account.authorizationInstanceSourceAccountExpiresAt) <= Date.now()) return false
  if (account.authorizationInstanceSourceAccountCooldownUntil && Date.parse(account.authorizationInstanceSourceAccountCooldownUntil) > Date.now()) return false
  return true
}
