import { optionalServerDateTimeIso, optionalString } from './value-utils.js'

export const accountCreateInputKeys = new Set([
  'providerCode',
  'providerProtocolProfileId',
  'name',
  'type',
  'credentials',
  'supportedModels',
  'defaultTestModel',
  'modelMappings',
  'tags',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'proxyProfileId',
  'schedulable',
  'groupId',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

export const accountUpdateInputKeys = new Set([
  'name',
  'credentials',
  'supportedModels',
  'defaultTestModel',
  'modelMappings',
  'tags',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'proxyProfileId',
  'schedulable',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

export function normalizedAccountType(value: unknown): string {
  if (typeof value !== 'string') {
    throw new Error('账户类型不能为空')
  }
  const accountType = value.trim()
  if (!accountType) {
    throw new Error('账户类型不能为空')
  }
  return accountType
}

export function normalizedDispatchPriority(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) {
    throw new Error('优先级必须是大于等于 0 的整数')
  }
  return value
}

export function normalizedOptionalDispatchPriority(value: unknown, fallback: number): number {
  return value === undefined ? fallback : normalizedDispatchPriority(value)
}

export function normalizedPositiveIntegerInput(value: unknown, fallback: number, label: string): number {
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) {
    throw new Error(`${label}必须是大于 0 的整数`)
  }
  return value
}

export function normalizeSuperPriorityInput(value: unknown, fallback: boolean): boolean {
  return normalizeBooleanDispatchInput(value, fallback, '超级优先')
}

export function normalizeFallbackInput(value: unknown, fallback: boolean): boolean {
  return normalizeBooleanDispatchInput(value, fallback, '降级备用')
}

export function openAIOAuthRefreshMetadata(accountType: string, credentials: Record<string, unknown>): {
  accessTokenExpiresAt: string | null
  refreshTokenPresent: boolean
} {
  if (accountType !== 'oauth') {
    return { accessTokenExpiresAt: null, refreshTokenPresent: false }
  }
  const refreshToken = optionalString(credentials.refresh_token)
  const accessToken = optionalString(credentials.access_token)
  const expiresAt = accessToken ? optionalServerDateTimeIso(credentials.expires_at) : undefined
  return {
    accessTokenExpiresAt: expiresAt ?? null,
    refreshTokenPresent: Boolean(refreshToken)
  }
}

function normalizeBooleanDispatchInput(value: unknown, fallback: boolean, label: string): boolean {
  if (value === undefined) return fallback
  if (typeof value === 'boolean') return value
  throw new Error(`${label}必须是布尔值`)
}
