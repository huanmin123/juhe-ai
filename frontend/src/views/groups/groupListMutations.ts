import type { GroupStatusSnapshotResult, GroupSummary } from '@/types/domain'

export function groupListItemHasDynamicSnapshot(group: GroupSummary, listConcurrencyAvailable: boolean): boolean {
  return listConcurrencyAvailable
    && typeof group.accountStats?.currentConcurrency === 'number'
    && group.accountStats.currentConcurrencyAvailable === true
    && group.accountStats.todayUsage !== undefined
}

export function mergeGroupListDynamicSnapshot(
  current: GroupSummary[],
  incoming: GroupSummary[],
  sameScope: boolean
): GroupSummary[] {
  if (!sameScope || current.length === 0) return incoming
  const currentById = new Map(current.map((group) => [group.id, group]))
  return incoming.map((group) => {
    const previous = currentById.get(group.id)
    if (!previous) return group
    const preserveConcurrency = group.accountStats.currentConcurrencyAvailable !== true
      && previous.accountStats.currentConcurrencyAvailable === true
    return {
      ...group,
      accountStats: {
        ...group.accountStats,
        currentConcurrency: preserveConcurrency
          ? previous.accountStats.currentConcurrency
          : group.accountStats.currentConcurrency,
        currentConcurrencyAvailable: preserveConcurrency
          ? true
          : group.accountStats.currentConcurrencyAvailable,
        todayUsage: group.accountStats.todayUsage ?? previous.accountStats.todayUsage
      }
    }
  })
}

export function mergeGroupStatusSnapshot(
  groups: GroupSummary[],
  snapshot: GroupStatusSnapshotResult
): GroupSummary[] {
  const itemsById = new Map(snapshot.items.map((item) => [item.id, item]))
  const concurrencyAvailable = snapshot.runtimeSnapshot.accountConcurrencyAvailable === true
  let changed = false
  const next = groups.map((group) => {
    const item = itemsById.get(group.id)
    if (!item) return group
    changed = true
    const preserveConcurrency = !concurrencyAvailable
      && group.accountStats.currentConcurrencyAvailable === true
    return {
      ...group,
      accountStats: {
        ...group.accountStats,
        currentConcurrency: preserveConcurrency
          ? group.accountStats.currentConcurrency
          : item.currentConcurrency,
        currentConcurrencyAvailable: preserveConcurrency
          ? true
          : concurrencyAvailable,
        todayUsage: item.todayUsage
      }
    }
  })
  return changed ? next : groups
}
