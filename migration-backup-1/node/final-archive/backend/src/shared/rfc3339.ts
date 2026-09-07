const rfc3339InstantPattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/

/**
 * 解析绝对时间输入。RFC3339 的 offset 是必需的；裸日期时间不会按本地时区猜测。
 */
export function parseRfc3339Instant(value: unknown): Date | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  const match = rfc3339InstantPattern.exec(text)
  if (!match) return undefined
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, , offset] = match
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const hour = Number(hourText)
  const minute = Number(minuteText)
  const second = Number(secondText)
  if (
    month < 1 || month > 12
    || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate()
    || hour > 23 || minute > 59 || second > 59
    || (offset !== 'Z' && (Number(offset.slice(1, 3)) > 23 || Number(offset.slice(4, 6)) > 59))
  ) return undefined
  const date = new Date(text)
  return Number.isFinite(date.getTime()) ? date : undefined
}

export function canonicalizeRfc3339Instant(value: unknown): string | undefined {
  return parseRfc3339Instant(value)?.toISOString()
}

export function requiredRfc3339Instant(value: unknown, label = '时间'): string {
  const normalized = canonicalizeRfc3339Instant(value)
  if (!normalized) throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  return normalized
}

export function rfc3339InstantMilliseconds(value: unknown): number | undefined {
  return parseRfc3339Instant(value)?.getTime()
}
