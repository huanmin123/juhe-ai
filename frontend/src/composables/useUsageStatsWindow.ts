import type { Dayjs } from 'dayjs'
import { computed, ref } from 'vue'

import { api } from '@/api/client'
import { parseDateKey, recentDateRange } from '@/shared/dateRange'
import type { UsageStatsWindow } from '@/types/domain'

const fallbackMaxDays = 31
const windowState = ref<UsageStatsWindow>()
let windowRequest: Promise<UsageStatsWindow> | undefined

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

async function loadUsageStatsWindow(): Promise<UsageStatsWindow> {
  if (windowState.value) return windowState.value
  if (!windowRequest) {
    windowRequest = api.myStats.usageWindow()
      .then((window) => {
        windowState.value = window
        return window
      })
      .catch((error) => {
        console.error(error)
        const fallback = fallbackWindow()
        windowState.value = fallback
        return fallback
      })
      .finally(() => {
        windowRequest = undefined
      })
  }
  return windowRequest
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
