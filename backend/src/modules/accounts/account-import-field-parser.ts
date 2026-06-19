import type { AccountAvailabilitySchedule, AccountModelMapping } from '../../domain/types.js'
import { accountAvailabilityScheduleFromRequest } from '../../storage/account-availability-schedule.js'
import { optionalServerDateTimeIso } from '../../storage/value-utils.js'

export type AccountImportStatus = 'active' | 'pending_test' | 'disabled'
export type AccountImportProxyType = 'http' | 'https' | 'socks5' | 'socks5h'

export const importRootKeys: ReadonlySet<string> = new Set(['type', 'version', 'proxies', 'accounts'])
export const importProxyKeys: ReadonlySet<string> = new Set([
  'ref',
  'name',
  'type',
  'host',
  'port',
  'username',
  'password',
  'description',
  'enabled'
])
export const importAccountKeys: ReadonlySet<string> = new Set([
  'ref',
  'name',
  'providerCode',
  'providerProtocolProfileId',
  'type',
  'status',
  'credentials',
  'groupId',
  'groupName',
  'proxyRef',
  'proxyProfileId',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'supportedModels',
  'modelMappings',
  'tags',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

export function importAvailabilityScheduleInput(record: Record<string, unknown>): { present: boolean; value: unknown } {
  if (!hasOwnField(record, 'availabilitySchedule')) return { present: false, value: undefined }
  return { present: true, value: record.availabilitySchedule }
}

export function normalizeImportAvailabilitySchedule(value: unknown, messages: string[]): AccountAvailabilitySchedule | undefined {
  try {
    return accountAvailabilityScheduleFromRequest({ availabilitySchedule: value })
  } catch (error) {
    messages.push(errorMessage(error))
    return undefined
  }
}

export function normalizeStatus(value: unknown): AccountImportStatus | undefined {
  const input = text(value)
  if (input === 'active' || input === 'pending_test' || input === 'disabled') return input
  return undefined
}

export function normalizeProxyType(value: unknown): AccountImportProxyType {
  const input = text(value)
  if (!input) {
    throw new Error('代理 type 不能为空')
  }
  if (!isProxyType(input)) {
    throw new Error(`代理 type 不支持：${input}`)
  }
  return input
}

export function isProxyType(value: string): value is AccountImportProxyType {
  return value === 'http' || value === 'https' || value === 'socks5' || value === 'socks5h'
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

export function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function hasOwnField(record: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key)
}

export function appendUnknownFieldMessages(
  record: Record<string, unknown>,
  allowedKeys: ReadonlySet<string>,
  label: string,
  messages: string[]
): void {
  const unknownKeys = Object.keys(record).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    messages.push(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

export function optionalTextField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'string') {
    messages.push(`${label}必须是字符串`)
    return undefined
  }
  const input = value.trim()
  return input || undefined
}

export function optionalBooleanField(record: Record<string, unknown>, key: string, label: string, messages: string[]): boolean | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'boolean') {
    messages.push(`${label}必须是布尔值`)
    return undefined
  }
  return value
}

export function optionalIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    messages.push(`${label}必须是整数`)
    return undefined
  }
  return value
}

export function optionalPositiveIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  const value = optionalIntegerField(record, key, label, messages)
  if (value === undefined) return undefined
  if (value <= 0) {
    messages.push(`${label}必须是大于 0 的整数`)
    return undefined
  }
  return value
}

export function optionalNonNegativeIntegerField(record: Record<string, unknown>, key: string, label: string, messages: string[]): number | undefined {
  const value = optionalIntegerField(record, key, label, messages)
  if (value === undefined) return undefined
  if (value < 0) {
    messages.push(`${label}必须是大于等于 0 的整数`)
    return undefined
  }
  return value
}

export function optionalStringArrayField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string[] | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (!Array.isArray(value)) {
    messages.push(`${label}必须是非空字符串数组`)
    return undefined
  }
  const items: string[] = []
  for (const item of value) {
    if (typeof item !== 'string' || !item.trim()) {
      messages.push(`${label}必须是非空字符串数组`)
      return undefined
    }
    items.push(item.trim())
  }
  return items
}

export function optionalAccountTagsField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string[] | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (!Array.isArray(value)) {
    messages.push(`${label}必须是字符串数组`)
    return undefined
  }
  const items: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') {
      messages.push(`${label}必须是字符串数组`)
      return undefined
    }
    const tagName = item.replace(/\s+/g, ' ').trim()
    if (!tagName) continue
    if (tagName.length > 40) {
      messages.push(`${label}单个标签不能超过 40 个字符`)
      return undefined
    }
    const tagKey = tagName.toLocaleLowerCase()
    if (seen.has(tagKey)) continue
    seen.add(tagKey)
    items.push(tagName)
  }
  if (items.length > 24) {
    messages.push(`${label}单个账户最多配置 24 个标签`)
    return undefined
  }
  return items
}

export function optionalModelMappingsField(record: Record<string, unknown>, key: string, label: string, messages: string[]): AccountModelMapping[] | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (!Array.isArray(value)) {
    messages.push(`${label}必须是模型映射数组`)
    return undefined
  }
  const output: AccountModelMapping[] = []
  const seenSources = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      messages.push(`${label}条目必须是对象`)
      return undefined
    }
    const itemRecord = item as Record<string, unknown>
    const sourceModel = optionalModelMappingText(itemRecord.sourceModel)
    const upstreamModel = optionalModelMappingText(itemRecord.upstreamModel)
    if (!sourceModel || !upstreamModel) {
      messages.push(`${label}条目必须包含 sourceModel 和 upstreamModel`)
      return undefined
    }
    if (sourceModel === upstreamModel) {
      continue
    }
    if (seenSources.has(sourceModel)) {
      messages.push(`${label}不能重复配置同一个 sourceModel：${sourceModel}`)
      return undefined
    }
    seenSources.add(sourceModel)
    output.push({
      sourceModel,
      upstreamModel,
      enabled: itemRecord.enabled !== false
    })
  }
  return output
}

export function optionalModelMappingText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

export function optionalDateTimeField(record: Record<string, unknown>, key: string, label: string, messages: string[]): string | undefined {
  if (!hasOwnField(record, key)) return undefined
  const value = record[key]
  if (typeof value !== 'string' || !value.trim()) {
    messages.push(`${label}必须是有效时间字符串`)
    return undefined
  }
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    messages.push(`${label}必须是有效时间字符串`)
    return undefined
  }
  return normalized
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
