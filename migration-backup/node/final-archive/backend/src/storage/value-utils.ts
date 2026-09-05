import { canonicalizeRfc3339Instant, requiredRfc3339Instant } from '../shared/rfc3339.js'

export function parseJsonArray(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed) || parsed.some((item) => typeof item !== 'string')) {
    throw new Error('JSON 数组字段必须是字符串数组')
  }
  return parsed
}

export function optionalString(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') throw new Error('文本字段必须是字符串')
  return value.length > 0 ? value : undefined
}

export function optionalNullableString(value: unknown): string | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'string') throw new Error('文本字段必须是字符串')
  return value.trim().length > 0 ? value : null
}

export function optionalServerDateTimeIso(value: unknown): string | undefined {
  const text = optionalString(value)?.trim()
  if (!text) return undefined
  return canonicalizeRfc3339Instant(text)
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
  return requiredRfc3339Instant(value, label)
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
