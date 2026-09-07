import { optionalNullableString } from './value-utils.js'

export function hasOwnInput(input: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}

export function normalizeOptionalBooleanInput(input: Record<string, unknown>, key: string, fallback: boolean, label: string): boolean {
  if (!hasOwnInput(input, key)) return fallback
  const value = input[key]
  if (typeof value === 'boolean') return value
  throw new Error(`${label}必须是布尔值`)
}

export function normalizeOptionalRequiredTextInput(input: Record<string, unknown>, key: string, fallback: string, label: string): string {
  if (!hasOwnInput(input, key)) return fallback
  return requiredTextInput(input[key], label)
}

export function normalizeNullableTextInput(value: unknown, label: string): string | undefined {
  try {
    return optionalNullableString(value) ?? undefined
  } catch {
    throw new Error(`${label}必须是字符串`)
  }
}

export function normalizeNullableIdInput(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}无效`)
  }
  return value.trim()
}

export function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

export function requiredTextInput(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
