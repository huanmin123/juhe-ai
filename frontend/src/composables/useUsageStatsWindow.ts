import type { Dayjs } from 'dayjs'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { parseDateKey, recentDateRange } from '@/shared/dateRange'
import type { UsageStatsWindow } from '@/types/domain'

const fallbackMaxDays = 31
const windowCacheTtlMs = 60_000
const windowState = ref<UsageStatsWindow>()

type UsageStatsWindowScope = 'admin' | 'self'

type UsageStatsWindowScopeState = {
  value?: UsageStatsWindow
  loadedAtMs: number
  request?: Promise<UsageStatsWindow>
  generation: number
  lastLoadFailed: boolean
}

const scopeStates: Record<UsageStatsWindowScope, UsageStatsWindowScopeState> = {
  admin: { loadedAtMs: 0, generation: 0, lastLoadFailed: false },
  self: { loadedAtMs: 0, generation: 0, lastLoadFailed: false }
}
let displayGeneration = 0
let activeIdentitySignature = usageStatsWindowIdentitySignature()

type UsageStatsWindowLoadOptions = {
  force?: boolean
  viewScope?: UsageStatsWindowScope
}

function fallbackWindow(): UsageStatsWindow {
  const [start, end] = recentDateRange(fallbackMaxDays)
  return {
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    startDate: start.format('YYYY-MM-DD'),
    endDate: end.format('YYYY-MM-DD'),
    days: fallbackMaxDays,
    maxDays: fallbackMaxDays
  }
}

async function loadUsageStatsWindow(options: UsageStatsWindowLoadOptions = {}): Promise<UsageStatsWindow> {
  ensureUsageStatsWindowIdentity()
  const scope = options.viewScope ?? 'self'
  const scopeState = scopeStates[scope]

  if (!options.force && scopeState.request) return scopeState.request
  if (!options.force && scopeState.value && Date.now() - scopeState.loadedAtMs < windowCacheTtlMs) {
    displayGeneration += 1
    windowState.value = scopeState.value
    return scopeState.value
  }

  const requestGeneration = ++scopeState.generation
  const requestDisplayGeneration = ++displayGeneration
  const request = (scope === 'admin' ? api.stats : api.myStats).usageWindow()
    .then((window) => {
      if (scopeState.generation === requestGeneration) {
        scopeState.value = window
        scopeState.loadedAtMs = Date.now()
        scopeState.lastLoadFailed = false
      }
      if (displayGeneration === requestDisplayGeneration) {
        windowState.value = window
      }
      return window
    })
    .catch((error) => {
      console.error(error)
      const fallback = fallbackWindow()
      if (scopeState.generation === requestGeneration) {
        scopeState.value = undefined
        scopeState.loadedAtMs = 0
        scopeState.lastLoadFailed = true
      }
      if (displayGeneration === requestDisplayGeneration) {
        windowState.value = fallback
      }
      return fallback
    })
    .finally(() => {
      if (scopeState.request === request) {
        scopeState.request = undefined
      }
    })
  scopeState.request = request
  return request
}

export function didUsageStatsWindowLoadFail(viewScope: UsageStatsWindowScope): boolean {
  ensureUsageStatsWindowIdentity()
  return scopeStates[viewScope].lastLoadFailed
}

export function clearUsageStatsWindowCache() {
  activeIdentitySignature = usageStatsWindowIdentitySignature()
  resetUsageStatsWindowScopeStates()
}

function ensureUsageStatsWindowIdentity(): void {
  const nextIdentitySignature = usageStatsWindowIdentitySignature()
  if (nextIdentitySignature === activeIdentitySignature) return
  activeIdentitySignature = nextIdentitySignature
  resetUsageStatsWindowScopeStates()
}

function resetUsageStatsWindowScopeStates(): void {
  displayGeneration += 1
  windowState.value = undefined
  for (const scope of Object.keys(scopeStates) as UsageStatsWindowScope[]) {
    const nextGeneration = scopeStates[scope].generation + 1
    scopeStates[scope] = { loadedAtMs: 0, generation: nextGeneration, lastLoadFailed: false }
  }
}

function usageStatsWindowIdentitySignature(): string {
  const user = authState.currentUser.value
  return JSON.stringify([user?.id ?? '', user?.role ?? '', authState.revision.value])
}

export function useUsageStatsWindow() {
  const windowEndDate = computed<Dayjs | undefined>(() => parseDateKey(windowState.value?.endDate))
  const maxRangeDays = computed(() => windowState.value?.maxDays ?? fallbackMaxDays)

  return {
    usageStatsWindow: windowState,
    usageStatsWindowEndDate: windowEndDate,
    usageStatsWindowMaxDays: maxRangeDays,
    loadUsageStatsWindow
  }
}
