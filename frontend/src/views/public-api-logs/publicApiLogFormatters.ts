import dayjs, { type Dayjs } from 'dayjs'

import { formatMillisecondsAsSeconds } from '@/shared/formatters'

export type PublicApiLogTimeRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined

export function parseStoredPublicApiLogTimeRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizePublicApiLogTimeRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

export function normalizePublicApiLogTimeRange(value: PublicApiLogTimeRangeValue): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end) ? [end, start] : [start, end]
}

export function normalizePublicApiLogStatusCode(value: string): number | undefined {
  const number = Number(value.trim())
  return Number.isInteger(number) && number >= 100 && number <= 599 ? number : undefined
}

export function formatPublicApiLogDuration(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

export function getPublicApiLogStatusColor(value?: number): string {
  if (!value) return 'default'
  if (value >= 200 && value < 300) return 'green'
  if (value >= 400 && value < 500) return 'orange'
  if (value >= 500) return 'red'
  return 'blue'
}

export function prettyPublicApiLogJson(value: unknown): string {
  try {
    return JSON.stringify(value ?? {}, null, 2)
  } catch {
    return '{}'
  }
}
