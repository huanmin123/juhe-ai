export function parseJsonArray(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed) || parsed.some((item) => typeof item !== 'string')) {
    throw new Error('JSON 数组字段必须是字符串数组')
  }
  return parsed
}

export function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

export function optionalNullableString(value: unknown): string | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'string') return null
  return value.trim().length > 0 ? value : null
}

export function optionalServerDateTimeIso(value: unknown): string | undefined {
  const text = optionalString(value)?.trim()
  if (!text) return undefined
  const normalizedText = text.includes(' ') ? text.replace(' ', 'T') : text
  const match = serverDateTimePattern.exec(normalizedText)
  if (!match || !isValidServerDateTimeMatch(match)) return undefined
  const hasTimeZone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(normalizedText)
  const timestamp = hasTimeZone ? Date.parse(normalizedText) : serverLocalDateTimeMs(normalizedText)
  if (!Number.isFinite(timestamp)) return undefined
  return new Date(timestamp).toISOString()
}

export function optionalNullableServerDateTimeIso(value: unknown): string | null {
  if (value === undefined || value === null) return null
  return optionalServerDateTimeIso(value) ?? null
}

export function nullableServerDateTimeIso(value: unknown, label = '时间'): string | null {
  if (value === null) return null
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}必须是有效时间字符串`)
  }
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    throw new Error(`${label}必须是有效时间字符串`)
  }
  return normalized
}

export function serverLocalDateTimeMs(value: string): number {
  if (/(?:Z|[+-]\d{2}:?\d{2})$/i.test(value)) return NaN
  const match = serverDateTimePattern.exec(value)
  if (!match || !isValidServerDateTimeMatch(match)) return NaN
  const [, year, month, day, hour = '0', minute = '0', second = '0', millisecond = '0'] = match
  return new Date(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hour),
    Number(minute),
    Number(second),
    Number(millisecond.padEnd(3, '0'))
  ).getTime()
}

const serverDateTimePattern = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2})(?::(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?)?)?(?:Z|[+-]\d{2}:?\d{2})?$/i

function isValidServerDateTimeMatch(match: RegExpExecArray): boolean {
  const [, year, month, day, hour = '0', minute = '0', second = '0', millisecond = '0'] = match
  const yearValue = Number(year)
  const monthValue = Number(month)
  const dayValue = Number(day)
  const hourValue = Number(hour)
  const minuteValue = Number(minute)
  const secondValue = Number(second)
  const millisecondValue = Number(millisecond.padEnd(3, '0'))
  return Number.isInteger(yearValue)
    && monthValue >= 1
    && monthValue <= 12
    && dayValue >= 1
    && dayValue <= new Date(Date.UTC(yearValue, monthValue, 0)).getUTCDate()
    && hourValue >= 0
    && hourValue <= 23
    && minuteValue >= 0
    && minuteValue <= 59
    && secondValue >= 0
    && secondValue <= 59
    && millisecondValue >= 0
    && millisecondValue <= 999
}

export function parseOptionalJsonObject(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  const parsed = JSON.parse(value) as unknown
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('JSON 对象字段必须是对象')
  }
  return parsed as Record<string, unknown>
}

export function parseJsonRules(value: string): Array<Record<string, unknown>> {
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('规则 JSON 必须是数组')
  }
  return parsed.map((item) => {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      throw new Error('规则 JSON 必须是对象数组')
    }
    return item as Record<string, unknown>
  })
}

export function jsonObjectOrNull(value: unknown): string | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  return JSON.stringify(value)
}
