import dayjs, { type Dayjs } from 'dayjs'

import type { RuntimeLogFacets, RuntimeLogGrepRuntime } from '@/types/domain'

export type RuntimeLogTimeRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined
export type RuntimeLogDayjsRange = [Dayjs, Dayjs]

export function parseStoredGrepRangeWithoutRuntime(value?: [string, string]): RuntimeLogDayjsRange | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return start.isValid() && end.isValid() ? [start, end] : undefined
}

export function defaultGrepRange(runtime?: RuntimeLogGrepRuntime): RuntimeLogDayjsRange {
  const end = dayjs(runtime?.defaultEndAt ?? new Date())
  const start = dayjs(runtime?.defaultStartAt ?? end.subtract(3, 'day'))
  return normalizeGrepRange([start, end], runtime)
}

export function normalizeGrepRange(value: RuntimeLogDayjsRange | undefined, runtime?: RuntimeLogGrepRuntime): RuntimeLogDayjsRange {
  const now = dayjs()
  const earliest = runtime?.earliestFileTime ? dayjs(runtime.earliestFileTime) : now.subtract(runtime?.fileRetentionDays ?? 30, 'day')
  const maxRangeDays = runtime?.maxRangeDays ?? 7
  let end = value?.[1]?.isValid() ? value[1] : now
  if (end.isAfter(now)) end = now
  if (end.isBefore(earliest)) end = earliest

  let start = value?.[0]?.isValid() ? value[0] : end.subtract(runtime?.defaultRangeDays ?? 3, 'day')
  if (start.isBefore(earliest)) start = earliest
  if (start.isAfter(end)) start = end.subtract(runtime?.defaultRangeDays ?? 3, 'day')
  if (end.diff(start, 'millisecond') > maxRangeDays * 24 * 60 * 60 * 1000) {
    start = end.subtract(maxRangeDays, 'day')
  }
  if (start.isBefore(earliest)) start = earliest
  return [start, end]
}

export function isDefaultGrepRange(range: RuntimeLogDayjsRange | undefined, runtime?: RuntimeLogGrepRuntime): boolean {
  if (!range) return true
  const defaults = defaultGrepRange(runtime)
  return Math.abs(range[0].diff(defaults[0], 'minute')) <= 1
    && Math.abs(range[1].diff(defaults[1], 'minute')) <= 1
}

export function isGrepDateDisabled(current: Dayjs, runtime?: RuntimeLogGrepRuntime): boolean {
  const earliest = runtime?.earliestFileTime ? dayjs(runtime.earliestFileTime).startOf('day') : dayjs().subtract(runtime?.fileRetentionDays ?? 30, 'day').startOf('day')
  return current.isBefore(earliest, 'day') || current.isAfter(dayjs(), 'day')
}

export function isIndexDateDisabled(current: Dayjs, facets?: RuntimeLogFacets): boolean {
  const earliest = facets?.earliestIndexedAt ? dayjs(facets.earliestIndexedAt).startOf('day') : dayjs().subtract(facets?.retentionDays ?? 3, 'day').startOf('day')
  return current.isBefore(earliest, 'day') || current.isAfter(dayjs(), 'day')
}

export function parseOptionalTimeRange(value?: [string, string]): RuntimeLogDayjsRange | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizeOptionalTimeRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

export function normalizeOptionalTimeRange(value: RuntimeLogTimeRangeValue): RuntimeLogDayjsRange | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end) ? [end, start] : [start, end]
}
