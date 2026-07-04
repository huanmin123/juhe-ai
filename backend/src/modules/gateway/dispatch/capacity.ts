import { loadAccountCurrentConcurrencyByIds, loadAccountCurrentConcurrencyByIdsAsync } from '../../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { preserveGatewayAccountDispatchPriorityTiers } from '../runtime/account-dispatch-priority-order.js'
import {
  gatewayAccountConcurrencyAccountId,
  gatewayAccountConcurrencyAccountIds
} from './account-concurrency-identity.js'
import type { GatewayAccountModelPriority } from './model-filter.js'

export function refreshGatewayAccountCurrentConcurrency(accounts: UpstreamAccount[]): UpstreamAccount[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(gatewayAccountConcurrencyAccountIds(accounts))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(gatewayAccountConcurrencyAccountId(account)) ?? 0
  }))
}

export async function refreshGatewayAccountCurrentConcurrencyAsync(accounts: UpstreamAccount[]): Promise<UpstreamAccount[]> {
  const concurrency = await loadAccountCurrentConcurrencyByIdsAsync(gatewayAccountConcurrencyAccountIds(accounts))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(gatewayAccountConcurrencyAccountId(account)) ?? 0
  }))
}

export function orderGatewayAccountsByLaneCapacityAvailability(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy,
  modelPriority?: GatewayAccountModelPriority
): UpstreamAccount[] {
  if (accounts.length < 2) {
    return accounts
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const currentConcurrency = loadAccountCurrentConcurrencyByIds(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? loadAccountCurrentConcurrencyByIds(accountIds, 'image')
    : undefined
  return orderAccountsByLaneCapacityBusyState(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy, modelPriority)
}

export async function orderGatewayAccountsByLaneCapacityAvailabilityAsync(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy,
  modelPriority?: GatewayAccountModelPriority
): Promise<UpstreamAccount[]> {
  if (accounts.length < 2) {
    return accounts
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const currentConcurrency = await loadAccountCurrentConcurrencyByIdsAsync(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? await loadAccountCurrentConcurrencyByIdsAsync(accountIds, 'image')
    : undefined
  return orderAccountsByLaneCapacityBusyState(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy, modelPriority)
}

export function areGatewayAccountsCapacityBusyForLane(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy
): boolean {
  if (accounts.length === 0) {
    return false
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const currentConcurrency = loadAccountCurrentConcurrencyByIds(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? loadAccountCurrentConcurrencyByIds(accountIds, 'image')
    : undefined
  return areAccountsCapacityBusyForLane(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy)
}

export async function areGatewayAccountsCapacityBusyForLaneAsync(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy
): Promise<boolean> {
  if (accounts.length === 0) {
    return false
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const currentConcurrency = await loadAccountCurrentConcurrencyByIdsAsync(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? await loadAccountCurrentConcurrencyByIdsAsync(accountIds, 'image')
    : undefined
  return areAccountsCapacityBusyForLane(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy)
}

function orderAccountsByLaneCapacityBusyState(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  currentConcurrency: Map<string, number>,
  imageLaneConcurrency: Map<string, number> | undefined,
  schedulingPolicy?: GroupSchedulingPolicy,
  modelPriority?: GatewayAccountModelPriority
): UpstreamAccount[] {
  const orderedAccounts = accounts
    .map((account, index) => ({
      account,
      index,
      busy: isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy)
    }))
    .sort((left, right) => Number(left.busy) - Number(right.busy) || left.index - right.index)
    .map((item) => item.account)
  return preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccounts, {
    modelRankByAccountId: modelPriority?.rankByAccountId
  })
}

function areAccountsCapacityBusyForLane(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  currentConcurrency: Map<string, number>,
  imageLaneConcurrency: Map<string, number> | undefined,
  schedulingPolicy?: GroupSchedulingPolicy
): boolean {
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
  const concurrencyAccountId = gatewayAccountConcurrencyAccountId(account)
  if ((currentConcurrency.get(concurrencyAccountId) ?? 0) >= hardLimit) {
    return true
  }
  if (requestLane !== 'image') {
    return false
  }
  return (imageLaneConcurrency?.get(concurrencyAccountId) ?? 0) >= effectiveImageLaneConcurrencyLimit({
    accountConcurrencyLimit: hardLimit,
    policy: schedulingPolicy
  })
}
