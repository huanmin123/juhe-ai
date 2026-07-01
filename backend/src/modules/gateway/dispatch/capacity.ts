import { loadAccountCurrentConcurrencyByIds } from '../../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'

export function refreshGatewayAccountCurrentConcurrency(accounts: UpstreamAccount[]): UpstreamAccount[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(account.id) ?? 0
  }))
}

export function orderGatewayAccountsByLaneCapacityAvailability(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy
): UpstreamAccount[] {
  if (accounts.length < 2) {
    return accounts
  }
  const accountIds = accounts.map((account) => account.id)
  const currentConcurrency = loadAccountCurrentConcurrencyByIds(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? loadAccountCurrentConcurrencyByIds(accountIds, 'image')
    : undefined
  return accounts
    .map((account, index) => ({
      account,
      index,
      busy: isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy)
    }))
    .sort((left, right) => Number(left.busy) - Number(right.busy) || left.index - right.index)
    .map((item) => item.account)
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
  return accounts.every((account) => isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy))
}

function accountHardConcurrencyLimit(account: UpstreamAccount): number {
  return Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
}

function isAccountCapacityBusyForLane(
  account: UpstreamAccount,
  requestLane: OpenAIGatewayRequestLane,
  currentConcurrency: Map<string, number>,
  imageLaneConcurrency: Map<string, number> | undefined,
  schedulingPolicy?: GroupSchedulingPolicy
): boolean {
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
}
