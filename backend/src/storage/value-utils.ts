export function parseJsonArray(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  return Array.isArray(parsed) ? parsed.map(String) : []
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
  const hasTimeZone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(normalizedText)
  const timestamp = hasTimeZone ? Date.parse(normalizedText) : serverLocalDateTimeMs(normalizedText)
  if (!Number.isFinite(timestamp)) return undefined
  return new Date(timestamp).toISOString()
}

export function optionalNullableServerDateTimeIso(value: unknown): string | null {
  if (value === undefined || value === null) return null
  return optionalServerDateTimeIso(value) ?? null
}

export function serverLocalDateTimeMs(value: string): number {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2})(?::(\d{2})(?::(\d{2}))?)?)?$/.exec(value)
  if (!match) return Date.parse(value)
  const [, year, month, day, hour = '0', minute = '0', second = '0'] = match
  return new Date(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), Number(second), 0).getTime()
}

export function parseOptionalJsonObject(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : undefined
  } catch {
    return undefined
  }
}

export function parseJsonRules(value: string): Array<Record<string, unknown>> {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
  } catch {
    return []
  }
}

export function jsonObjectOrNull(value: unknown): string | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  return JSON.stringify(value)
}
