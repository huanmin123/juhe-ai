import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceTokenStatus
} from './external-integration-source-types.js'
import { externalIntegrationScopeOptions } from './external-integration-source-constants.js'
import { optionalServerDateTimeIso } from './value-utils.js'

const externalIntegrationRateLimitRuleKeys = ['windowSeconds', 'maxRequests'] as const
const activeExternalIntegrationScopes = new Set<string>(externalIntegrationScopeOptions.map((item) => item.value))

export function normalizeSourceStatusInput(status: ExternalIntegrationSourceStatus | undefined): ExternalIntegrationSourceStatus {
  return status === undefined ? 'active' : normalizeSourceStatus(status)
}

export function normalizeSourceStatus(status: unknown): ExternalIntegrationSourceStatus {
  if (status === 'active' || status === 'disabled') {
    return status
  }
  throw new Error('来源系统状态无效')
}

export function normalizeTokenStatusInput(status: ExternalIntegrationSourceTokenStatus | undefined): ExternalIntegrationSourceTokenStatus {
  return status === undefined ? 'active' : normalizeTokenStatus(status)
}

export function normalizeTokenStatus(status: unknown): ExternalIntegrationSourceTokenStatus {
  if (status === 'active' || status === 'disabled' || status === 'revoked') {
    return status
  }
  throw new Error('来源系统 token 状态无效')
}

export function encodeScopes(scopes: unknown): string {
  return JSON.stringify(normalizeScopes(scopes))
}

export function normalizeScopes(scopes: unknown): string[] {
  if (scopes === undefined) {
    return []
  }
  if (!Array.isArray(scopes)) {
    throw new Error('来源系统 scopes 必须是字符串数组')
  }
  const values = new Set<string>()
  for (const scope of scopes) {
    if (typeof scope !== 'string') {
      throw new Error('来源系统 scopes 必须是字符串数组')
    }
    const value = scope.trim()
    if (!value) {
      throw new Error('来源系统 scopes 不能为空')
    }
    if (!activeExternalIntegrationScopes.has(value)) {
      throw new Error(`来源系统 scope 不受支持：${value}`)
    }
    values.add(value)
  }
  return [...values].sort()
}

export function decodeScopes(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  if (Array.isArray(parsed)) {
    return normalizeScopes(parsed.filter((scope) => typeof scope !== 'string' || activeExternalIntegrationScopes.has(scope.trim())))
  }
  return normalizeScopes(parsed)
}

export function encodeRateLimits(rules: unknown): string {
  return JSON.stringify(normalizeRateLimits(rules))
}

export function decodeRateLimits(value: string | null | undefined): ExternalIntegrationRateLimitRule[] {
  if (!value) {
    return []
  }
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('来源系统 rate_limits_json 必须是数组')
  }
  return normalizeRateLimits(parsed)
}

export function normalizeRateLimits(rules: unknown): ExternalIntegrationRateLimitRule[] {
  if (rules === undefined) {
    return []
  }
  if (!Array.isArray(rules)) {
    throw new Error('来源系统限频规则必须是数组')
  }
  if (rules.length > 8) {
    throw new Error('来源系统限频规则最多 8 条')
  }
  const normalized: ExternalIntegrationRateLimitRule[] = []
  const seen = new Set<number>()
  for (const rule of rules) {
    if (typeof rule !== 'object' || rule === null || Array.isArray(rule)) {
      throw new Error('来源系统限频规则必须是对象')
    }
    const record = rule as Record<string, unknown>
    assertOnlyKeys(record, externalIntegrationRateLimitRuleKeys, '来源系统限频规则')
    const windowSeconds = normalizeRateLimitInteger(record.windowSeconds, 1, 86_400, '来源系统限频窗口')
    const maxRequests = normalizeRateLimitInteger(record.maxRequests, 1, 100_000, '来源系统限频次数')
    if (seen.has(windowSeconds)) {
      throw new Error('来源系统限频窗口不能重复')
    }
    seen.add(windowSeconds)
    normalized.push({ windowSeconds, maxRequests })
  }
  return normalized.sort((a, b) => a.windowSeconds - b.windowSeconds)
}

export function normalizeNullableIso(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('过期时间无效')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('过期时间无效')
  }
  const normalized = optionalServerDateTimeIso(text)
  if (!normalized) {
    throw new Error('过期时间无效')
  }
  return normalized
}

export function normalizeNullableText(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('备注必须是字符串')
  }
  const text = value.trim()
  if (text.length > 500) {
    throw new Error('备注不能超过 500 个字符')
  }
  return text || null
}

function normalizeRateLimitInteger(value: unknown, min: number, max: number, label: string): number {
  if (!Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  const numeric = value as number
  if (numeric < min || numeric > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return numeric
}

function assertOnlyKeys(record: Record<string, unknown>, allowedKeys: readonly string[], label: string): void {
  const unknownKeys = Object.keys(record).filter((key) => !allowedKeys.includes(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}
