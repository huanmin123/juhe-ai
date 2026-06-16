import {
  listClientIpStats as listClientIpStatsFromWindow,
  type ClientIpStatsListOptions,
  type ClientIpStatsListResult
} from './client-ip-stats-list.repository.js'

export { normalizeClientIpForStats, type NormalizedClientIp } from './client-ip-normalization.js'
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
  disableClientIpPolicies,
  findActiveClientIpPolicyByHash,
  listActiveClientIpPolicies,
  recordClientIpPolicyHits,
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
  refreshClientIpUsageRangeWindows
} from './client-ip-usage-range-windows.repository.js'
export { aggregateClientIpStatsBatch, latestClientIpStatsLagSeconds } from './client-ip-stats-aggregation.repository.js'

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  return listClientIpStatsFromWindow(options)
}
