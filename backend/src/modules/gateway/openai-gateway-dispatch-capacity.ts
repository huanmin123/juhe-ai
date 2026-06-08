import { loadAccountCurrentConcurrencyByIds } from '../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import type { OpenAIGatewayRequestLane } from './openai-gateway-request-lane.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export function refreshGatewayAccountCurrentConcurrency(accounts: UpstreamAccount[]): UpstreamAccount[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(account.id) ?? 0
  }))
}

export function areGatewayAccountsCapacityBusyForLane(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy
): boolean {
  if (accounts.length === 0) {
    return false
  }
  const accountIds = accounts.map((account) => account.id)
  const currentConcurrency = loadAccountCurrentConcurrencyByIds(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? loadAccountCurrentConcurrencyByIds(accountIds, 'image')
    : undefined
  return accounts.every((account) => {
    const hardLimit = accountHardConcurrencyLimit(account)
    if ((currentConcurrency.get(account.id) ?? 0) >= hardLimit) {
      return true
    }
    if (requestLane !== 'image') {
      return false
    }
    return (imageLaneConcurrency?.get(account.id) ?? 0) >= effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy: schedulingPolicy
    })
  })
}

function accountHardConcurrencyLimit(account: UpstreamAccount): number {
  return Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
}
