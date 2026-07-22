import { message } from '@/lib/antd'
import { computed, ref, type Ref } from 'vue'
import dayjs, { type Dayjs } from 'dayjs'

import { api } from '@/api/client'
import type { RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogGrepRuntime } from '@/types/domain'
import { splitGrepKeywords } from './runtimeLogFormatters'
import {
  defaultGrepRange as defaultRuntimeLogGrepRange,
  isDefaultGrepRange as isDefaultRuntimeLogGrepRange,
  isGrepDateDisabled,
  normalizeGrepRange as normalizeRuntimeLogGrepRange,
  parseStoredGrepRangeWithoutRuntime
} from './runtimeLogTimeRanges'

type UseRuntimeLogGrepSearchStateOptions = {
  clearRouteTraceIdForManualState: () => void
  grepRuntime: Ref<RuntimeLogGrepRuntime | undefined>
  initialKeywordFilter: string
  initialTimeRange?: [string, string]
  isRouteTraceActive: () => boolean
  loading: Ref<boolean>
  schedulePageStateWrite: () => void
}

type RuntimeLogStoredGrepSearchState = {
  grepKeywordFilter: string
  grepTimeRange?: [string, string]
}

export function useRuntimeLogGrepSearchState(options: UseRuntimeLogGrepSearchStateOptions) {
  const grepRecords = ref<RuntimeLogGrepItem[]>([])
  const grepResult = ref<RuntimeLogGrepResult>()
  const grepTimeRange = ref<[Dayjs, Dayjs] | undefined>(parseStoredGrepRangeWithoutRuntime(options.initialTimeRange))
  const grepKeywordFilter = ref(options.initialKeywordFilter)
  let grepSearchRequestId = 0

  const grepActiveFilterCount = computed(() => isDefaultGrepRange() ? 0 : 1)

  function defaultGrepRange(): [Dayjs, Dayjs] {
    return defaultRuntimeLogGrepRange(options.grepRuntime.value)
  }

  function normalizeGrepRange(value?: [Dayjs, Dayjs]): [Dayjs, Dayjs] {
    return normalizeRuntimeLogGrepRange(value, options.grepRuntime.value)
  }

  function ensureGrepTimeRange(): [Dayjs, Dayjs] {
    const normalized = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
    grepTimeRange.value = normalized
    return normalized
  }

  function isDefaultGrepRange(): boolean {
    return isDefaultRuntimeLogGrepRange(grepTimeRange.value, options.grepRuntime.value)
  }

  function disabledGrepDate(current: Dayjs): boolean {
    return isGrepDateDisabled(current, options.grepRuntime.value)
  }

  function handleGrepRangeChange(): void {
    grepTimeRange.value = ensureGrepTimeRange()
  }

  function applyGrepSearchState(state: RuntimeLogStoredGrepSearchState): void {
    grepKeywordFilter.value = state.grepKeywordFilter
    grepTimeRange.value = parseStoredGrepRangeWithoutRuntime(state.grepTimeRange)
  }

  function resetGrepSearch(): void {
    options.clearRouteTraceIdForManualState()
    grepSearchRequestId += 1
    options.loading.value = false
    grepKeywordFilter.value = ''
    grepTimeRange.value = defaultGrepRange()
    grepRecords.value = []
    grepResult.value = undefined
    if (!options.isRouteTraceActive()) {
      options.schedulePageStateWrite()
    }
  }

  async function searchGrepLogs(): Promise<void> {
    options.clearRouteTraceIdForManualState()
    const requestId = ++grepSearchRequestId
    const keywords = splitGrepKeywords(grepKeywordFilter.value)
    if (!keywords.length) {
      options.loading.value = false
      grepRecords.value = []
      grepResult.value = undefined
      message.warning('请输入要搜索的关键字')
      return
    }

    const range = ensureGrepTimeRange()
    options.loading.value = true
    try {
      const result = await api.runtimeLogs.grep({
        keywords: keywords.join(' '),
        startAt: range[0].toISOString(),
        endAt: range[1].toISOString(),
        limit: 100
      })
      if (requestId !== grepSearchRequestId) return
      grepResult.value = result
      grepTimeRange.value = normalizeGrepRange([dayjs(result.startAt), dayjs(result.endAt)])
      grepRecords.value = result.items
      if (!result.available) {
        message.warning(result.message || 'grep 模式不可用')
      }
    } catch (error) {
      if (requestId !== grepSearchRequestId) return
      console.error(error)
      message.error('grep 搜索失败')
    } finally {
      if (requestId === grepSearchRequestId) {
        options.loading.value = false
      }
    }
  }

  return {
    applyGrepSearchState,
    defaultGrepRange,
    disabledGrepDate,
    ensureGrepTimeRange,
    grepActiveFilterCount,
    grepKeywordFilter,
    grepRecords,
    grepResult,
    grepTimeRange,
    handleGrepRangeChange,
    normalizeGrepRange,
    resetGrepSearch,
    searchGrepLogs
  }
}
