import {
  listClientIpStatsAsync as listClientIpStatsFromWindowAsync,
  listClientIpStats as listClientIpStatsFromWindow,
  type ClientIpStatsListOptions,
  type ClientIpStatsListResult
} from './client-ip-stats-list.repository.js'
import {
  getClientIpStatsDetailAsync as getClientIpStatsDetailFromWindowAsync,
  getClientIpStatsDetail as getClientIpStatsDetailFromWindow,
  type ClientIpStatsDetailOptions,
  type ClientIpStatsDetailResult
} from './client-ip-stats-detail.repository.js'

export { normalizeClientIpForStats, type NormalizedClientIp } from './client-ip-normalization.js'
export type {
  ClientIpAccountUsageRow,
  ClientIpStatsDetailOptions,
  ClientIpStatsDetailResult
} from './client-ip-stats-detail.repository.js'
export type {
  ClientIpLastUsedSortScope,
  ClientIpPolicyFilter,
  ClientIpStatsListOptions,
  ClientIpStatsListResult,
  ClientIpStatsRow,
  ClientIpStatsSortField,
  ClientIpUsageSummary
} from './client-ip-stats-list.repository.js'
export {
  createClientIpPolicy,
  createClientIpPolicyAsync,
  disableClientIpPolicies,
  disableClientIpPoliciesAsync,
  findActiveClientIpPolicyByHash,
  findActiveClientIpPolicyByHashAsync,
  listActiveClientIpPolicies,
  listActiveClientIpPoliciesAsync,
  recordClientIpPolicyHits,
  recordClientIpPolicyHitsAsync,
  type ActiveClientIpPolicy,
  type ClientIpPolicyDisableInput,
  type ClientIpPolicyHitInput,
  type ClientIpPolicyMutationInput,
  type ClientIpPolicyStatus,
  type ClientIpPolicySummary
} from './client-ip-policy.repository.js'
export {
  clearClientIpRangeWindowDirtyMemoryForTest,
  pendingClientIpRangeWindowDirtyCountForTest,
  rebuildClientIpUsageRangeWindows,
  refreshClientIpUsageRangeWindowsAsync,
  refreshClientIpUsageRangeWindows
} from './client-ip-usage-range-windows.repository.js'
export {
  aggregateClientIpStatsBatch,
  aggregateClientIpStatsBatchAsync,
  latestClientIpStatsLagSeconds,
  latestClientIpStatsLagSecondsAsync
} from './client-ip-stats-aggregation.repository.js'

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  return listClientIpStatsFromWindow(options)
}

export async function listClientIpStatsAsync(options: ClientIpStatsListOptions = {}): Promise<ClientIpStatsListResult> {
  return await listClientIpStatsFromWindowAsync(options)
}

export function getClientIpStatsDetail(options: ClientIpStatsDetailOptions): ClientIpStatsDetailResult | undefined {
  return getClientIpStatsDetailFromWindow(options)
}

export async function getClientIpStatsDetailAsync(options: ClientIpStatsDetailOptions): Promise<ClientIpStatsDetailResult | undefined> {
  return await getClientIpStatsDetailFromWindowAsync(options)
}
