import type { AccountUsageSummary, GroupAccountStats } from '../domain/types.js'
import type { GroupAccountStatsRow } from './group-read-loaders.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'

export function emptyGroupAccountStats(): GroupAccountStats {
  return {
    total: 0,
    available: 0,
    active: 0,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    currentConcurrency: 0,
    concurrencyLimit: 0,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary()
  }
}

export function groupAccountStatsFromRow(row: GroupAccountStatsRow | undefined, todayUsage?: AccountUsageSummary, totalUsage?: AccountUsageSummary): GroupAccountStats {
  return {
    total: Number(row?.total ?? 0),
    available: Number(row?.available ?? 0),
    active: Number(row?.active ?? 0),
    disabled: Number(row?.disabled ?? 0),
    error: Number(row?.error ?? 0),
    rateLimited: Number(row?.rate_limited ?? 0),
    currentConcurrency: Number(row?.current_concurrency ?? 0),
    concurrencyLimit: Number(row?.concurrency_limit ?? 0),
    todayUsage: todayUsage ?? emptyAccountUsageSummary(),
    usage: totalUsage ?? emptyAccountUsageSummary()
  }
}
