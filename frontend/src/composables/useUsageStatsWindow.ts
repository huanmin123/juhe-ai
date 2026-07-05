import type { Dayjs } from 'dayjs'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { parseDateKey, recentDateRange } from '@/shared/dateRange'
import type { UsageStatsWindow } from '@/types/domain'

const fallbackMaxDays = 31
const windowCacheTtlMs = 60_000
const windowState = ref<UsageStatsWindow>()
let windowLoadedAtMs = 0
let windowRequest: Promise<UsageStatsWindow> | undefined

type UsageStatsWindowLoadOptions = {
  force?: boolean
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
  if (!options.force && windowState.value && Date.now() - windowLoadedAtMs < windowCacheTtlMs) {
    return windowState.value
  }
  if (!options.force && windowRequest) return windowRequest
  const request = api.myStats.usageWindow()
    .then((window) => {
      windowState.value = window
      windowLoadedAtMs = Date.now()
      return window
    })
    .catch((error) => {
      console.error(error)
      const fallback = fallbackWindow()
      windowState.value = fallback
      windowLoadedAtMs = Date.now()
      return fallback
    })
    .finally(() => {
      if (windowRequest === request) {
        windowRequest = undefined
      }
    })
  windowRequest = request
  return request
}

export function clearUsageStatsWindowCache() {
  windowState.value = undefined
  windowLoadedAtMs = 0
  windowRequest = undefined
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
