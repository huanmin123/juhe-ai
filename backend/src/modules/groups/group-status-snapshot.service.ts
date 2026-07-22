import type { GroupStatusSnapshotResult } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  listGroupOptionRowsForAccess,
  listGroupOptionRowsForAccessAsync,
  loadGroupAuthorizationUsageSummaries,
  loadGroupAuthorizationUsageSummariesAsync
} from '../../storage/group-read.repository.js'
import {
  loadGroupConcurrencyAccountIdsByGroupIds,
  loadGroupConcurrencyAccountIdsByGroupIdsAsync
} from '../../storage/group-read-loaders.js'
import { runtimeConfig } from '../../config/runtime.js'
import { usageScope } from '../../storage/resource-authorization-helpers.js'
import { loadGroupUsageSummariesForScopes, loadGroupUsageSummariesForScopesAsync } from '../../storage/usage-summary-loaders.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { loadAccountConcurrencyByIds } from '../gateway/runtime/runtime-snapshot.service.js'

const maxSnapshotGroupIds = 100
const maxSnapshotQueryLength = 8192

export function parseGroupStatusSnapshotGroupIds(value: unknown): string[] {
  const raw = typeof value === 'string' ? value : ''
  if (raw.length > maxSnapshotQueryLength) throw new Error('分组状态快照查询参数过长')
  const ids = [...new Set(raw.split(',').map((item) => item.trim()).filter(Boolean))]
  if (ids.length === 0) throw new Error('分组状态快照至少选择 1 个分组')
  if (ids.length > maxSnapshotGroupIds) throw new Error('分组状态快照最多查询 100 个分组')
  return ids
}

export async function getGroupStatusSnapshot(
  access: AccessScope | undefined,
  groupIds: string[]
): Promise<GroupStatusSnapshotResult> {
  const rows = runtimeConfig.databaseDriver === 'postgres'
    ? await listGroupOptionRowsForAccessAsync(access, { ids: groupIds, limit: maxSnapshotGroupIds })
    : listGroupOptionRowsForAccess(access, { ids: groupIds, limit: maxSnapshotGroupIds })
  const timezone = runtimeConfig.databaseDriver === 'postgres' ? await usageStatsTimezoneAsync() : usageStatsTimezone()
  const dateKey = todayDateKey(timezone)
  const ownerScopes = rows
    .filter((row) => row.access_type !== 'authorized')
    .map((row) => usageScope(row.id, row.system_account_id, row.id))
  const authorizationScopes = rows
    .filter((row) => row.access_type === 'authorized' && row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const [concurrencyAccountIdsByGroup, ownerUsage, authorizationUsage] = runtimeConfig.databaseDriver === 'postgres'
    ? await Promise.all([
      loadGroupConcurrencyAccountIdsByGroupIdsAsync(rows.map((row) => row.id)),
      loadGroupUsageSummariesForScopesAsync(ownerScopes, dateKey),
      loadGroupAuthorizationUsageSummariesAsync(authorizationScopes, dateKey)
    ])
    : [
      loadGroupConcurrencyAccountIdsByGroupIds(rows.map((row) => row.id)),
      loadGroupUsageSummariesForScopes(ownerScopes, dateKey),
      loadGroupAuthorizationUsageSummaries(authorizationScopes, dateKey)
    ]
  const concurrency = await loadAccountConcurrencyByIds([...new Set([...concurrencyAccountIdsByGroup.values()].flat())])
  return {
    generatedAt: new Date().toISOString(),
    runtimeSnapshot: {
      accountConcurrencyAvailable: concurrency.available
    },
    items: rows.map((row) => ({
      id: row.id,
      currentConcurrency: sumConcurrency(concurrencyAccountIdsByGroup.get(row.id) ?? [], concurrency.values),
      todayUsage: row.access_type === 'authorized' && row.authorization_id
        ? authorizationUsage.get(row.authorization_id) ?? emptyAccountUsageSummary()
        : ownerUsage.get(row.id) ?? emptyAccountUsageSummary()
    }))
  }
}

function sumConcurrency(accountIds: string[], values: Record<string, number>): number {
  return accountIds.reduce((total, accountId) => total + Math.max(0, Math.trunc(values[accountId] ?? 0)), 0)
}
