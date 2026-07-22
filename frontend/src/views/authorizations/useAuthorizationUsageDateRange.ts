import type { Dayjs } from 'dayjs'
import { computed, ref } from 'vue'

import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { formatDateKey, formatDateLabel, isRecentWindowDateDisabled, normalizeDateRangeKeys, parseDateKey, parseDateRangeKeys, todayDateRange, type DateRangeKeys } from '@/shared/dateRange'

const defaultMaxRangeDays = 31

export function useAuthorizationUsageDateRange(options: {
  maxRangeDays?: number
  onChange?: () => void | Promise<void>
} = {}) {
  const maxRangeDays = options.maxRangeDays ?? defaultMaxRangeDays
  const { usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow } = useUsageStatsWindow()
  const dateRange = ref<[Dayjs, Dayjs]>(defaultDateRange())
  const dateRangeExplicit = ref(false)
  const calendarRange = ref<[Dayjs | null, Dayjs | null]>([null, null])
  const selectedRange = computed(() => normalizedDateRange(dateRange.value))
  const displayRange = computed(() => [formatDateKey(dateRange.value[0]), formatDateKey(dateRange.value[1])] as const)
  const rangeLabel = computed(() => `${formatDateLabel(displayRange.value[0])} 至 ${formatDateLabel(displayRange.value[1])}`)

  function handleDateRangeChange() {
    dateRange.value = parseDateRange({
      startDate: formatDateKey(dateRange.value[0]),
      endDate: formatDateKey(dateRange.value[1])
    })
    dateRangeExplicit.value = true
    void options.onChange?.()
  }

  function selectedRangeParams(): DateRangeKeys {
    if (!dateRangeExplicit.value) return {}
    const [startDate, endDate] = selectedRange.value
    return { startDate, endDate }
  }

  function handleCalendarChange(value: Array<Dayjs | null> | null) {
    calendarRange.value = [value?.[0] ?? null, value?.[1] ?? null]
  }

  function handleDateRangeOpenChange(open: boolean) {
    if (!open) {
      calendarRange.value = [null, null]
    }
  }

  function disabledDate(current: Dayjs) {
    return isRecentWindowDateDisabled(current, calendarRange.value, usageStatsWindowMaxDays.value || maxRangeDays, usageStatsWindowEndDate.value)
  }

  function resetDateRange() {
    dateRange.value = defaultDateRange()
    dateRangeExplicit.value = false
    calendarRange.value = [null, null]
  }

  function setExplicitDateRange(value?: DateRangeKeys) {
    dateRange.value = parseDateRange(value)
    dateRangeExplicit.value = true
  }

  function syncDateRangeFromResponse(value?: DateRangeKeys) {
    const start = parseDateKey(value?.startDate)
    const end = parseDateKey(value?.endDate)
    if (!start || !end || start.isAfter(end, 'day')) return
    dateRange.value = [start.startOf('day'), end.startOf('day')]
  }

  function defaultDateRange(): [Dayjs, Dayjs] {
    return todayDateRange()
  }

  function parseDateRange(value?: DateRangeKeys): [Dayjs, Dayjs] {
    return parseDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: maxRangeDays })
  }

  function normalizedDateRange(value: [Dayjs, Dayjs]): [string, string] {
    return normalizeDateRangeKeys(value, { defaultRange: defaultDateRange, maxDays: maxRangeDays })
  }

  void loadUsageStatsWindow()

  return {
    dateRange,
    dateRangeExplicit,
    calendarRange,
    selectedRange,
    displayRange,
    rangeLabel,
    handleDateRangeChange,
    selectedRangeParams,
    handleCalendarChange,
    handleDateRangeOpenChange,
    disabledDate,
    resetDateRange,
    setExplicitDateRange,
    syncDateRangeFromResponse
  }
}
