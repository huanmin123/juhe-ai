import type { GroupListItem, GroupListPageResult, GroupStatusSnapshotResult } from '../../domain/types.js'
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

export async function hydrateGroupListPage(
  access: AccessScope | undefined,
  page: GroupListPageResult
): Promise<Omit<GroupListPageResult, 'runtimeSnapshot'> & { generatedAt: string }> {
  const { runtimeSnapshot: _runtimeSnapshot, ...listPage } = page
  if (page.items.length === 0) {
    return { ...listPage, generatedAt: new Date().toISOString() }
  }
  const snapshot = await getGroupStatusSnapshotForListItems(page.items)
  const snapshotById = new Map(snapshot.items.map((item) => [item.id, item]))
  return {
    ...listPage,
    generatedAt: snapshot.generatedAt,
    items: page.items.map((item) => {
      const dynamic = snapshotById.get(item.id)
      return {
        ...item,
        accountStats: {
          ...item.accountStats,
          currentConcurrency: dynamic?.currentConcurrency ?? 0,
          todayUsage: dynamic?.todayUsage ?? emptyAccountUsageSummary()
        }
      }
    })
  }
}

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
  return loadGroupStatusSnapshot(rows.map((row) => ({
    id: row.id,
    ownerSystemAccountId: row.system_account_id,
    accessType: row.access_type === 'authorized' ? 'authorized' : 'owner',
    groupAuthorizationId: row.authorization_id ?? undefined
  })))
}

async function getGroupStatusSnapshotForListItems(items: GroupListItem[]): Promise<GroupStatusSnapshotResult> {
  return loadGroupStatusSnapshot(items.map((item) => {
    if (!item.ownerSystemAccountId) throw new Error(`分组 ${item.id} 缺少所有者上下文`)
    return {
      id: item.id,
      ownerSystemAccountId: item.ownerSystemAccountId,
      accessType: item.accessType === 'authorized' ? 'authorized' : 'owner',
      groupAuthorizationId: item.groupAuthorizationId
    }
  }))
}

interface GroupStatusSnapshotSubject {
  id: string
  ownerSystemAccountId: string
  accessType: 'owner' | 'authorized'
  groupAuthorizationId?: string
}

async function loadGroupStatusSnapshot(subjects: GroupStatusSnapshotSubject[]): Promise<GroupStatusSnapshotResult> {
  const timezone = runtimeConfig.databaseDriver === 'postgres' ? await usageStatsTimezoneAsync() : usageStatsTimezone()
  const dateKey = todayDateKey(timezone)
  const ownerScopes = subjects
    .filter((subject) => subject.accessType !== 'authorized')
    .map((subject) => usageScope(subject.id, subject.ownerSystemAccountId, subject.id))
  const authorizationScopes = subjects
    .filter((subject) => subject.accessType === 'authorized' && subject.groupAuthorizationId)
    .map((subject) => usageScope(subject.groupAuthorizationId ?? '', subject.ownerSystemAccountId, subject.groupAuthorizationId ?? ''))
  const [concurrencyAccountIdsByGroup, ownerUsage, authorizationUsage] = runtimeConfig.databaseDriver === 'postgres'
    ? await Promise.all([
      loadGroupConcurrencyAccountIdsByGroupIdsAsync(subjects.map((subject) => subject.id)),
      loadGroupUsageSummariesForScopesAsync(ownerScopes, dateKey),
      loadGroupAuthorizationUsageSummariesAsync(authorizationScopes, dateKey)
    ])
    : [
      loadGroupConcurrencyAccountIdsByGroupIds(subjects.map((subject) => subject.id)),
      loadGroupUsageSummariesForScopes(ownerScopes, dateKey),
      loadGroupAuthorizationUsageSummaries(authorizationScopes, dateKey)
    ]
  const concurrency = await loadAccountConcurrencyByIds([...new Set([...concurrencyAccountIdsByGroup.values()].flat())])
  return {
    generatedAt: new Date().toISOString(),
    runtimeSnapshot: {
      accountConcurrencyAvailable: concurrency.available
    },
    items: subjects.map((subject) => ({
      id: subject.id,
      currentConcurrency: sumConcurrency(concurrencyAccountIdsByGroup.get(subject.id) ?? [], concurrency.values),
      todayUsage: subject.accessType === 'authorized' && subject.groupAuthorizationId
        ? authorizationUsage.get(subject.groupAuthorizationId) ?? emptyAccountUsageSummary()
        : ownerUsage.get(subject.id) ?? emptyAccountUsageSummary()
    }))
  }
}

function sumConcurrency(accountIds: string[], values: Record<string, number>): number {
  return accountIds.reduce((total, accountId) => total + Math.max(0, Math.trunc(values[accountId] ?? 0)), 0)
}
