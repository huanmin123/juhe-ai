import { canonicalizeRfc3339Instant } from './rfc3339.js'

export function firstQueryValue(value: unknown): unknown {
  return Array.isArray(value) ? value[0] : value
}

export function optionalQueryText(value: unknown): string | undefined {
  const text = firstQueryValue(value)
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

export function queryTextList(value: unknown, maxItems = 500): string[] {
  const values = Array.isArray(value) ? value : [value]
  return [...new Set(values
    .flatMap((item) => typeof item === 'string' ? item.split(',') : [])
    .map((item) => item.trim())
    .filter(Boolean))]
    .slice(0, Math.max(1, maxItems))
}

export function integerQueryValue(value: unknown): number | undefined {
  const text = firstQueryValue(value)
  if (typeof text === 'string') {
    const trimmed = text.trim()
    if (!trimmed) return undefined
    const number = Number(trimmed)
    return Number.isInteger(number) ? number : undefined
  }
  return typeof text === 'number' && Number.isInteger(text) ? text : undefined
}

export function finiteNumberQueryValue(value: unknown): number | undefined {
  const text = firstQueryValue(value)
  if (typeof text === 'string') {
    const trimmed = text.trim()
    if (!trimmed) return undefined
    const number = Number(trimmed)
    return Number.isFinite(number) ? number : undefined
  }
  return typeof text === 'number' && Number.isFinite(text) ? text : undefined
}

export function strictDateTimeQueryValue(value: unknown, label = '时间'): string | undefined {
  const text = optionalQueryText(value)
  if (!text) return undefined
  const normalized = canonicalizeRfc3339Instant(text)
  if (!normalized) {
    const error = new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`) as Error & { statusCode: number }
    error.statusCode = 400
    throw error
  }
  return normalized
}

export function strictDateTimeRangeQueryValue(startValue: unknown, endValue: unknown): { startAt?: string; endAt?: string } {
  const startAt = strictDateTimeQueryValue(startValue, '开始时间')
  const endAt = strictDateTimeQueryValue(endValue, '结束时间')
  if (startAt && endAt && startAt > endAt) return { startAt: endAt, endAt: startAt }
  return { startAt, endAt }
}
