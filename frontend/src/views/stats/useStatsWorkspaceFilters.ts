import { onScopeDispose } from 'vue'
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router'

import { authState } from '@/composables/useAuth'
import { parseDateKey } from '@/shared/dateRange'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

interface StatsWorkspaceFilters {
  startDate: string
  endDate: string
  systemAccountId: string
  systemAccount?: PrincipalSelection
}

export type StatsOverviewRangeMode = 'auto' | 'today' | 'recent7d' | 'recent1m' | 'custom'

const filterReaders = new Map<string, () => StatsWorkspaceFilters | undefined>()
const workspaceStateKeys = [
  'statsWorkspaceUser',
  'statsWorkspaceScope',
  'statsStartDate',
  'statsEndDate',
  'statsOverviewRangeMode',
  'statsSystemAccountId',
  'statsSystemAccountName'
] as const

export function useStatsWorkspaceFilters() {
  const route = useRoute()
  const router = useRouter()
  const pagePath = route.path

  function incoming(): (StatsWorkspaceFilters & { overviewRangeMode?: StatsOverviewRangeMode }) | undefined {
    const state = router.options.history.state
    const userId = authState.currentUser.value?.id
    if (!userId || state.statsWorkspaceUser !== userId || state.statsWorkspaceScope !== route.meta.viewScope) return
    const { statsStartDate: startDate, statsEndDate: endDate, statsSystemAccountId, statsSystemAccountName } = state
    if (typeof startDate !== 'string' || typeof endDate !== 'string' || !parseDateKey(startDate) || !parseDateKey(endDate) || startDate > endDate) return
    const systemAccountId = route.meta.viewScope === 'admin' && typeof statsSystemAccountId === 'string'
      ? statsSystemAccountId
      : allSystemAccountsValue
    const systemAccount = systemAccountId !== allSystemAccountsValue && typeof statsSystemAccountName === 'string'
      ? { id: systemAccountId, name: statsSystemAccountName, kind: 'system_account' as const }
      : undefined
    const mode = state.statsOverviewRangeMode
    const overviewRangeMode = mode === 'auto' || mode === 'today' || mode === 'recent7d' || mode === 'recent1m' || mode === 'custom'
      ? mode
      : undefined
    return overviewRangeMode
      ? { startDate, endDate, systemAccountId, systemAccount, overviewRangeMode }
      : { startDate, endDate, systemAccountId, systemAccount }
  }

  function writeHistoryState(filters?: StatsWorkspaceFilters, overviewRangeMode?: StatsOverviewRangeMode): void {
    const history = router.options.history
    if (route.path !== pagePath || history.location !== route.fullPath) return
    const state = { ...history.state }
    // Web history 的 replace 会合并旧 state，必须显式覆盖才能清除旧值。
    for (const key of workspaceStateKeys) state[key] = null
    if (filters) {
      const userId = authState.currentUser.value?.id
      if (!userId) return
      Object.assign(state, {
        statsWorkspaceUser: userId,
        statsWorkspaceScope: route.meta.viewScope,
        statsStartDate: filters.startDate,
        statsEndDate: filters.endDate,
        statsOverviewRangeMode: overviewRangeMode ?? null,
        statsSystemAccountId: filters.systemAccountId,
        statsSystemAccountName: filters.systemAccount?.name ?? null
      })
    }
    history.replace(history.location, state)
  }

  function persist(overviewRangeMode?: StatsOverviewRangeMode): void {
    const filters = filterReaders.get(pagePath)?.()
    if (!filters) {
      clear()
      return
    }
    writeHistoryState(filters, overviewRangeMode)
  }

  function clear(): void {
    writeHistoryState()
  }

  function link(path: string): RouteLocationRaw {
    const saved = incoming()
    const filters = filterReaders.get(route.path)?.() ?? saved
    const userId = authState.currentUser.value?.id
    if (!filters || !userId || router.resolve(path).meta.viewScope !== route.meta.viewScope) return path
    return {
      path,
      state: {
        statsWorkspaceUser: userId,
        statsWorkspaceScope: route.meta.viewScope,
        statsStartDate: filters.startDate,
        statsEndDate: filters.endDate,
        statsOverviewRangeMode: saved?.startDate === filters.startDate && saved.endDate === filters.endDate
          ? saved.overviewRangeMode ?? null
          : null,
        statsSystemAccountId: filters.systemAccountId,
        statsSystemAccountName: filters.systemAccount?.name ?? null
      }
    }
  }

  function register(read: () => StatsWorkspaceFilters) {
    const owner = authState.currentUser.value?.id
    const readCurrent = () => authState.currentUser.value?.id === owner ? read() : undefined
    filterReaders.set(pagePath, readCurrent)
    onScopeDispose(() => {
      if (filterReaders.get(pagePath) === readCurrent) filterReaders.delete(pagePath)
    })
  }

  return { incoming, link, persist, clear, register }
}

export function useStatsWorkspaceLink() {
  return useStatsWorkspaceFilters().link
}
